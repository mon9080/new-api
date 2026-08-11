package openai

import (
	"strings"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

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
	if helper.ShouldDeferCodexResponseGuardToAutoDisable(c, originalErr) {
		helper.LogCodexResponseGuardConflict(c, info, match, upstreamStatus)
		return originalErr
	}
	return helper.NewCodexResponseGuardError(c, info, match, upstreamStatus, stream)
}

func codexResponseGuardEnabled(info *relaycommon.RelayInfo) bool {
	return helper.CodexResponseGuardEnabled(info)
}
