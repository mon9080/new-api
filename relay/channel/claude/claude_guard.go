package claude

import (

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// claudeResponseGuardProtocolErrorText normalizes an upstream Claude error into
// the text fragment matched against the configured guard error messages.
func claudeResponseGuardProtocolErrorText(err *types.ClaudeError) string {
	if err == nil {
		return ""
	}
	if err.Message != "" {
		return err.Message
	}
	return err.Type
}

// claudeResponseGuardError evaluates one decoded Claude stream event against the
// channel's response guard settings. It returns nil when the event does not
// match, so callers can keep scanning the stream.
func claudeResponseGuardError(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	upstreamStatus int,
	event *dto.ClaudeResponse,
) *types.NewAPIError {
	if event == nil {
		return nil
	}

	var protocolErrorMessage string
	if claudeError := event.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		protocolErrorMessage = claudeResponseGuardProtocolErrorText(claudeError)
	}

	match, matchErr := helper.MatchCodexResponseGuard(
		info.ChannelOtherSettings,
		upstreamStatus,
		protocolErrorMessage,
		nil, // Claude streams carry no Responses payload.
	)
	if matchErr != nil {
		logger.LogError(c, "invalid response guard configuration: "+matchErr.Error())
		return nil
	}
	if match == nil {
		return nil
	}
	return helper.NewCodexResponseGuardError(c, info, match, upstreamStatus, true)
}

// claudeStreamGuardEvent is one buffered Claude stream event retained while the
// guard is deciding whether the attempt is usable.
type claudeStreamGuardEvent struct {
	response dto.ClaudeResponse
	data     string
}

