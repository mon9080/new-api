package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestShouldDisableChannelSkipsSyntheticCodexGuardError(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
	})
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusBadGateway, End: http.StatusBadGateway}}

	err := types.NewOpenAIError(
		errors.New("codex response guard triggered"),
		types.ErrorCodeCodexResponseGuard,
		http.StatusBadGateway,
		types.ErrOptionWithSkipAutoDisable(),
	)

	require.False(t, ShouldDisableChannel(err))

	originalErr := types.NewOpenAIError(
		errors.New("upstream failed"),
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
	)
	require.True(t, ShouldDisableChannel(originalErr))
}
