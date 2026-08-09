package openai

import (
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

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
	if originalErr != nil && service.ShouldDisableChannel(originalErr) {
		helper.LogCodexResponseGuardConflict(c, info, match, upstreamStatus)
		return originalErr
	}
	return helper.NewCodexResponseGuardError(c, info, match, upstreamStatus, stream)
}

func codexResponseGuardEnabled(info *relaycommon.RelayInfo) bool {
	return helper.CodexResponseGuardEnabled(info)
}
