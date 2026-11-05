package openai

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func codexResponseGuardProtocolErrorText(openAIError *types.OpenAIError) string {
	if openAIError == nil {
		return ""
	}
	fields := []string{openAIError.Message, openAIError.Type}
	if code, ok := openAIError.Code.(string); ok {
		fields = append(fields, code)
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "server_is_overloaded", "slow_down":
			fields = append(fields, helper.DefaultCodexResponseGuardErrorMessage)
		}
	}
	return strings.Join(fields, "\n")
}

func codexResponseGuardError(c *gin.Context, info *relaycommon.RelayInfo, upstreamStatus int, protocolErrorMessage string, response *dto.OpenAIResponsesResponse, stream bool, originalErr *types.NewAPIError) *types.NewAPIError {
	if !helper.CodexResponseGuardEnabled(info) {
		return nil
	}
	match, err := helper.MatchCodexResponseGuard(info.ChannelOtherSettings, upstreamStatus, protocolErrorMessage, response)
	if err != nil {
		logger.LogError(c, "invalid codex response guard configuration: "+err.Error())
		return nil
	}
	if match == nil {
		return nil
	}
	// 会话状态归属错误必须原样交给重试决策：守卫会把原文替换成
	// "codex response guard triggered"，降级逻辑将再也识别不出来。
	if originalErr != nil && service.IsUpstreamBoundStateMismatchError(originalErr) {
		// 返回原始错误而不是 nil：调用方据此 sr.Stop 并交给重试决策；
		// 若返回 nil，流内错误事件会被原样转发给客户端并被记为成功。
		//
		// 非流式路径的体内错误沿用上游 200，直接上抛会让客户端收到
		// “HTTP 200 + error body”；这里归一化为非 2xx。
		// 归一化为 400 + bad_request_body：这是“请求里的会话状态不被接受”的
		// 4xx 语义。用 502/bad_response_body 会被当成渠道 5xx 故障，从而触发
		// 渠道排除、同渠道钉死甚至自动禁用健康渠道。
		ret := originalErr
		if ret.StatusCode >= 200 && ret.StatusCode < 300 {
			ret = types.NewOpenAIError(errors.New(ret.Error()), types.ErrorCodeBadRequestBody, http.StatusBadRequest)
		}
		logger.LogInfo(c, "codex response guard skipped: bound-state mismatch returned to retry policy")
		return ret
	}
	if helper.ShouldDeferCodexResponseGuardToAutoDisable(c, originalErr) {
		helper.LogCodexResponseGuardConflict(c, info, match, upstreamStatus)
		return originalErr
	}
	return helper.NewCodexResponseGuardError(c, info, match, upstreamStatus, stream)
}

func codexResponseGuardEnabled(info *relaycommon.RelayInfo) bool {
	return helper.CodexResponseGuardEnabled(info)
}
