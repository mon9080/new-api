package service

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// RequestCarriesUpstreamBoundState reports whether the request replays state
// that only the originating upstream can use — Responses API encrypted
// reasoning items (encrypted_content). Such a request cannot be replayed on a
// different channel: the upstream rejects it with "item was created under a
// different resource". Callers must fail fast instead of switching channels.
func RequestCarriesUpstreamBoundState(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// 本次请求的绑定状态已被剥离：后续基于请求体的判定（是否钉死同渠道）
	// 必须按“已无绑定”处理，否则会把请求锁死在刚失败的渠道上。
	if BoundStateAlreadyStripped(c) {
		return false
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage == nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return false
	}
	// Inspect the structured payload: a bare `encrypted_content` substring also
	// appears in `include: ["reasoning.encrypted_content"]`, which carries no
	// state at all. Only an actual non-empty encrypted item (or a
	// previous_response_id reference) binds the request to an upstream resource.
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return false
	}
	for _, key := range []string{"previous_response_id", "previous_response"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return containsNonEmptyEncryptedItem(payload["input"])
}

// containsNonEmptyEncryptedItem walks the request input looking for an item that
// actually carries encrypted reasoning state.
func containsNonEmptyEncryptedItem(v any) bool {
	switch typed := v.(type) {
	case []any:
		for _, item := range typed {
			if containsNonEmptyEncryptedItem(item) {
				return true
			}
		}
	case map[string]any:
		if s, ok := typed["encrypted_content"].(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
		for _, item := range typed {
			if containsNonEmptyEncryptedItem(item) {
				return true
			}
		}
	}
	return false
}

// isInvalidUpstreamResponseError reports whether the attempt produced no usable
// upstream result even though transport-level delivery succeeded: the response
// guard flagged a fake-success payload, or the stream ended without a valid
// terminal event. These are channel-attributable failures worth another channel.
func isInvalidUpstreamResponseError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeCodexResponseGuard, types.ErrorCodeBadResponseBody:
		return true
	}
	return false
}

// isEncryptedContentMismatchError reports the upstream answer when a request
// replays encrypted reasoning items (Responses API) that were minted by a
// different upstream resource: "The requested item was created under a
// different OpenAI resource" / "Encrypted content could not be decrypted".
//
// It arrives as a 400, but it is not a client error and it is not permanent:
// the channel that minted the items can serve the request, so retrying on
// another channel is worthwhile. Without this, strict session affinity turns
// the case into an instant, unexplained failure.
func isEncryptedContentMismatchError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Prefer the upstream's structured code when it provides one; clients may
	// already handle it, and we must not rewrite it into a generic error.
	if strings.Contains(msg, "invalid_encrypted_content") {
		return true
	}
	// Otherwise require an explicit verification/decryption failure, not the bare
	// words "encrypted content" (which also appear in success-path metadata).
	if strings.Contains(msg, "could not be verified") ||
		strings.Contains(msg, "could not be decrypted") ||
		strings.Contains(msg, "under a different openai resource") ||
		strings.Contains(msg, "encrypted content could not be") {
		return true
	}
	return false
}

// ShouldExcludeFailedChannel reports whether a failed attempt should take this
// channel out of the running for the remainder of the request.
//
// It is deliberately not derived from the log-only error source string: caller
// mistakes and client cancellations must never penalise a channel, and a guard
// hit reported as a local error must still be excluded. Request-scoped only —
// cross-request health (cooldown / auto_ban) stays with its own mechanism.
func ShouldExcludeFailedChannel(c *gin.Context, err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	// The caller is gone; there is nothing left to retry and the channel is not
	// at fault.
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return false
	}
	if isEncryptedContentMismatchError(err) {
		// This channel cannot decrypt the session's items; another channel may
		// be the one that minted them.
		return true
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeCodexResponseGuard, types.ErrorCodeBadResponseBody:
		// Bound sessions retry on the SAME channel via a pin; excluding the
		// channel here would make that pin unsatisfiable.
		if RequestCarriesUpstreamBoundState(c) {
			return false
		}
		// Upstream delivered no usable result for a successful transport.
		return true
	case types.ErrorCodeDoRequestFailed:
		// Transport-level failure attributed to the channel.
		return true
	}
	// Channel-prefixed error codes are channel-attributable by construction.
	if types.IsChannelError(err) {
		return true
	}
	// Explicitly non-retryable rejects are caller-side decisions (quota, auth,
	// bad parameters); do not burn the channel for them.
	if types.IsSkipRetryError(err) {
		return false
	}
	// Remaining server-side failures (5xx) are worth avoiding for this request.
	return err.StatusCode >= 500
}

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	// Invalid upstream responses (guard hits / empty terminal payloads) are not
	// the client's fault: a strict session binding must not pin the request to
	// a channel that keeps answering with garbage. Only the session gate is
	// bypassed here — the operator's status-code retry configuration still
	// decides. Requests carrying encrypted Responses state stay bound, because
	// that state only the originating upstream can use.
	invalidUpstream := isInvalidUpstreamResponseError(err) && !RequestCarriesUpstreamBoundState(c)
	// Encrypted/signed state only the current upstream can use, so switching
	// channels cannot work — but a capacity failure is transient and the state
	// stays valid on the SAME channel. Pin it and retry there instead of
	// failing the request outright.
	if isInvalidUpstreamResponseError(err) && RequestCarriesUpstreamBoundState(c) {
		if retryTimes > 0 {
			if failedChannel := c.GetInt("channel_id"); failedChannel > 0 {
				GetChannelConstraints(c).AddPin(dto.ChannelPin{
					ChannelId: failedChannel,
					Source:    dto.PinSourceOriginTask,
					Rank:      dto.PinRankOriginTask,
					RetryMode: dto.PinRetrySameChannel,
				})
			}
			return PolicyDecision{Action: "retry", Reason: "bound_state_same_channel_retry", Source: "system"}
		}
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	// Encrypted/signed conversation state is bound to the upstream resource that
	// minted it. Once the upstream explicitly reports that state cannot be
	// verified, another channel is a blind lottery: production showed the same
	// item rejected by three channels in a row (~60s wasted). Stop immediately
	// and return the real failure instead of an attempt-budget reason.
	if isEncryptedContentMismatchError(err) {
		return PolicyDecision{Action: "stop", Reason: "encrypted_state_mismatch", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) && !invalidUpstream {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	upstreamURLs := common.UpstreamAddresses(c)
	logger.LogError(c, common.LogText("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(upstreamURLs...), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
