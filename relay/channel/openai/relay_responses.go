package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	// Response guard runs for non-streaming responses too: the upstream can
	// answer HTTP 200 with a terminal failure payload or a fake-success body,
	// and that must become a retryable channel error before anything is
	// written to the client. (Restored after the upstream merge dropped it.)
	var upstreamError *types.NewAPIError
	protocolErrorMessage := ""
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil {
		// Give the guard every error object: Codex overload failures carry only
		//  (server_is_overloaded / slow_down) with an empty , and the
		// guard maps those to the configured client-visible message.
		protocolErrorMessage = codexResponseGuardProtocolErrorText(oaiError)
		if oaiError.Type != "" {
			upstreamError = types.WithOpenAIError(*oaiError, resp.StatusCode)
		}
	}
	if guardErr := codexResponseGuardError(c, info, resp.StatusCode, protocolErrorMessage, &responsesResponse, false, upstreamError); guardErr != nil {
		return nil, guardErr
	}
	if upstreamError != nil {
		return nil, upstreamError
	}

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	// No prefix buffering: events are forwarded as they arrive, so the client
	// always sees progress. Buffering left slow upstreams silent for 14+
	// seconds and the client cancelled (production 499s on ch50/51/52).
	//
	// The guard keeps only the value it can deliver safely: a failure detected
	// *before* anything is written is converted into a channel failover. Once
	// the stream is committed, retrying would splice two upstream responses
	// into one client stream, so the failure is reported as non-retryable and
	// the outer layer writes the SSE error.
	guardEnabled := codexResponseGuardEnabled(info)
	var guardErr *types.NewAPIError

	accumulator := service.NewResponsesUsageAccumulator(info)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}

		if guardEnabled && guardErr == nil {
			var protocolErrorMessage string
			var upstreamError *types.NewAPIError
			switch streamResponse.Type {
			case "error":
				if streamError := dto.GetOpenAIError(streamResponse.Error); streamError != nil {
					protocolErrorMessage = codexResponseGuardProtocolErrorText(streamError)
					upstreamError = types.WithOpenAIError(*streamError, resp.StatusCode)
				}
				if protocolErrorMessage == "" {
					protocolErrorMessage = streamResponse.Message
				}
				if upstreamError == nil && protocolErrorMessage != "" {
					upstreamError = types.NewOpenAIError(errors.New(protocolErrorMessage), types.ErrorCodeBadResponse, resp.StatusCode)
				}
			case "response.failed", "response.error":
				if streamResponse.Response != nil {
					if streamError := streamResponse.Response.GetOpenAIError(); streamError != nil {
						protocolErrorMessage = codexResponseGuardProtocolErrorText(streamError)
						upstreamError = types.WithOpenAIError(*streamError, resp.StatusCode)
					}
				}
			}
			if reasoningTokens, observed := helper.ObserveCodexResponse(streamResponse.Response); observed {
				logger.LogInfo(c, fmt.Sprintf("codex response guard observation: reasoning_tokens=%d (not blocking)", reasoningTokens))
			}
			if matched := codexResponseGuardError(c, info, resp.StatusCode, protocolErrorMessage, streamResponse.Response, true, upstreamError); matched != nil {
				if !c.Writer.Written() {
					// Nothing reached the client yet: fail over to another channel.
					guardErr = matched
					sr.Stop(matched)
					return
				}
				// Committed: keep the failure visible but non-retryable.
				logger.LogError(c, common.LogText("codex response guard hit after the stream was committed: %s", matched.Error()))
				guardErr = types.NewErrorWithStatusCode(
					errors.New(matched.Error()),
					types.ErrorCodeCodexResponseGuard,
					http.StatusBadGateway,
					types.ErrOptionWithSkipRetry(),
				)
				sr.Stop(guardErr)
				return
			}
		}

		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			sr.Stop(err)
			return
		}
		if (streamResponse.Type == "response.completed" || streamResponse.Type == "response.done") &&
			streamResponse.Response != nil && info.StreamStatus.ResponseOutcome() == string(relaycommon.ResponseOutcomeCompleted) {
			sr.Done()
		}
	})

	if guardErr != nil {
		if !c.Writer.Written() {
			c.Writer.Header().Del("Content-Type")
		}
		return nil, guardErr
	}

	// Terminal check: a stream that ended without a successful terminal event
	// must not be reported as success. Production showed `response.failed`
	// events recorded as successful requests (type=2, no usage) while the client
	// saw a truncated answer, so no failover or alert ever fired.
	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	// Only convert to an error while nothing reached the client: an upstream
	// response.failed before the first write can still fail over. Once bytes
	// are out, the relay keeps upstream's contract (return nil so usage settles)
	// and the failure stays visible through the stream-status log fields.
	if info.StreamStatus.ResponseFailed() && (c == nil || !c.Writer.Written()) {
		return nil, types.NewOpenAIError(errors.New("upstream reported a failed response"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}

	info.StreamStatus.RequireTerminal()
	return accumulator.Finish(), nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !ojson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := ojson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}
