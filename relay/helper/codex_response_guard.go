package helper

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

const (
	codexResponseGuardFirstReasoningTokens = 516
	codexResponseGuardReasoningStep        = 518
	DefaultCodexResponseGuardErrorMessage  = "Selected model is at capacity. Please try a different model."
	DefaultCodexResponseGuardRetryTimes    = 5
	MaxCodexResponseGuardRetryTimes        = 20

	CodexResponseGuardMatchReasoningTokens = "reasoning_tokens"
	CodexResponseGuardMatchStatusCode      = "status_code"
	CodexResponseGuardMatchErrorMessage    = "error_message"
)

type CodexResponseGuardMatch struct {
	Kind            string
	ReasoningTokens int
}

func IsCodexResponseGuardReasoningTokens(reasoningTokens int) bool {
	return reasoningTokens >= codexResponseGuardFirstReasoningTokens &&
		(reasoningTokens+2)%codexResponseGuardReasoningStep == 0
}

func ShouldBlockCodexResponse(response *dto.OpenAIResponsesResponse) bool {
	reasoningTokens, ok := CodexResponseGuardReasoningTokens(response)
	return ok && IsCodexResponseGuardReasoningTokens(reasoningTokens)
}

func CodexResponseGuardReasoningTokens(response *dto.OpenAIResponsesResponse) (int, bool) {
	if response == nil || response.Usage == nil {
		return 0, false
	}
	if response.Usage.OutputTokensDetails != nil {
		return response.Usage.OutputTokensDetails.ReasoningTokens, true
	}
	if response.Usage.CompletionTokenDetails.ReasoningTokens == 0 {
		return 0, false
	}
	return response.Usage.CompletionTokenDetails.ReasoningTokens, true
}

func MatchCodexResponseGuardStatusCode(settings dto.ChannelOtherSettings, statusCode int) (bool, error) {
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges(settings.CodexResponseGuardStatusCodes)
	if err != nil {
		return false, err
	}
	for _, statusRange := range ranges {
		if statusRange.Start < http.StatusBadRequest || statusRange.End > 599 {
			return false, fmt.Errorf("codex response guard status code range must be within 400-599: %d-%d", statusRange.Start, statusRange.End)
		}
		if statusCode >= statusRange.Start && statusCode <= statusRange.End {
			return true, nil
		}
	}
	return false, nil
}

func MatchCodexResponseGuardErrorMessage(settings dto.ChannelOtherSettings, message string) bool {
	configuredMessages := DefaultCodexResponseGuardErrorMessage
	if settings.CodexResponseGuardErrorMessages != nil {
		configuredMessages = *settings.CodexResponseGuardErrorMessages
	}
	normalizedMessage := normalizeCodexResponseGuardErrorMessage(message)
	if normalizedMessage == "" {
		return false
	}
	for _, configured := range strings.Split(configuredMessages, "\n") {
		normalizedConfigured := normalizeCodexResponseGuardErrorMessage(configured)
		if normalizedConfigured != "" && strings.Contains(normalizedMessage, normalizedConfigured) {
			return true
		}
	}
	return false
}

func normalizeCodexResponseGuardErrorMessage(message string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(message), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}), " ")
}

func ShouldDeferCodexResponseGuardToAutoDisable(c *gin.Context, originalErr *types.NewAPIError) bool {
	if !service.ShouldDisableChannel(originalErr) {
		return false
	}
	autoBan, exists := common.GetContextKeyType[bool](c, constant.ContextKeyChannelAutoBan)
	return !exists || autoBan
}

func CodexResponseGuardRetryTimes(settings dto.ChannelOtherSettings) int {
	retryTimes := settings.CodexResponseGuardRetryTimes
	if retryTimes <= 0 {
		return DefaultCodexResponseGuardRetryTimes
	}
	if retryTimes > MaxCodexResponseGuardRetryTimes {
		return MaxCodexResponseGuardRetryTimes
	}
	return retryTimes
}

func MatchCodexResponseGuard(settings dto.ChannelOtherSettings, upstreamStatus int, protocolErrorMessage string, response *dto.OpenAIResponsesResponse) (*CodexResponseGuardMatch, error) {
	if ShouldBlockCodexResponse(response) {
		reasoningTokens, _ := CodexResponseGuardReasoningTokens(response)
		return &CodexResponseGuardMatch{Kind: CodexResponseGuardMatchReasoningTokens, ReasoningTokens: reasoningTokens}, nil
	}
	statusMatched, err := MatchCodexResponseGuardStatusCode(settings, upstreamStatus)
	if err != nil {
		return nil, err
	}
	if statusMatched {
		return &CodexResponseGuardMatch{Kind: CodexResponseGuardMatchStatusCode}, nil
	}
	if MatchCodexResponseGuardErrorMessage(settings, protocolErrorMessage) {
		return &CodexResponseGuardMatch{Kind: CodexResponseGuardMatchErrorMessage}, nil
	}
	return nil, nil
}

func CodexResponseGuardEnabled(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.ChannelOtherSettings.CodexResponseGuardEnabled
}

func NewCodexResponseGuardError(c *gin.Context, info *relaycommon.RelayInfo, match *CodexResponseGuardMatch, upstreamStatus int, stream bool) *types.NewAPIError {
	retryLimit := CodexResponseGuardRetryTimes(info.ChannelOtherSettings)
	logger.LogInfo(c, fmt.Sprintf(
		"codex_response_guard_hit channel_id=%d model=%s match=%s upstream_status=%d stream=%t retry_limit=%d action=return_502_for_retry",
		info.ChannelId,
		info.UpstreamModelName,
		match.Kind,
		upstreamStatus,
		stream,
		retryLimit,
	))
	return types.NewOpenAIError(
		fmt.Errorf("codex response guard triggered: match=%s", match.Kind),
		types.ErrorCodeCodexResponseGuard,
		http.StatusBadGateway,
		types.ErrOptionWithSkipAutoDisable(),
		types.ErrOptionWithRetryLimit(retryLimit),
	)
}

func LogCodexResponseGuardConflict(c *gin.Context, info *relaycommon.RelayInfo, match *CodexResponseGuardMatch, upstreamStatus int) {
	logger.LogInfo(c, fmt.Sprintf(
		"codex_response_guard_conflict channel_id=%d match=%s upstream_status=%d action=defer_to_auto_disable",
		info.ChannelId,
		match.Kind,
		upstreamStatus,
	))
}
