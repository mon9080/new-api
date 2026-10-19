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

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
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

	guardEnabled := codexResponseGuardEnabled(info)
	if guardEnabled {
		originalDisablePing := info.DisablePing
		info.DisablePing = true
		defer func() {
			info.DisablePing = originalDisablePing
		}()
	}

	type bufferedStreamEvent struct {
		response dto.ResponsesStreamResponse
		data     string
	}
	var bufferedEvents []bufferedStreamEvent
	var guardErr *types.NewAPIError

	accumulator := service.NewResponsesUsageAccumulator(info)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		if guardEnabled {
			bufferedEvents = append(bufferedEvents, bufferedStreamEvent{response: streamResponse, data: data})
		} else {
			sendResponsesStreamData(c, streamResponse, data)
		}
		if guardErr == nil {
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
			guardErr = codexResponseGuardError(c, info, resp.StatusCode, protocolErrorMessage, streamResponse.Response, true, upstreamError)
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
	})

	if guardErr != nil {
		c.Writer.Header().Del("Content-Type")
		return nil, guardErr
	}
	if guardEnabled {
		for _, event := range bufferedEvents {
			sendResponsesStreamData(c, event.response, event.data)
		}
	}

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	return accumulator.Finish(), nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !gjson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := sjson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}
