package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryCodexResponseGuardRespectsStatusCodeConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	guardErr := types.NewOpenAIError(
		errors.New("codex response guard triggered"),
		types.ErrorCodeCodexResponseGuard,
		http.StatusBadGateway,
		types.ErrOptionWithRetryLimit(5),
	)

	originalRanges := operation_setting.AutomaticRetryStatusCodeRanges
	t.Cleanup(func() {
		operation_setting.AutomaticRetryStatusCodeRanges = originalRanges
	})

	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusBadGateway, End: http.StatusBadGateway}}
	require.True(t, shouldRetry(ctx, guardErr, 1, 0))

	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusInternalServerError, End: http.StatusInternalServerError}}
	require.False(t, shouldRetry(ctx, guardErr, 1, 0))
}

func TestShouldRetryCodexResponseGuardRespectsGuardLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	guardErr := types.NewOpenAIError(
		errors.New("codex response guard triggered"),
		types.ErrorCodeCodexResponseGuard,
		http.StatusBadGateway,
		types.ErrOptionWithRetryLimit(5),
	)

	originalRanges := operation_setting.AutomaticRetryStatusCodeRanges
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = originalRanges })
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusBadGateway, End: http.StatusBadGateway}}

	require.True(t, shouldRetry(ctx, guardErr, 4, 4))
	require.False(t, shouldRetry(ctx, guardErr, 3, 5))
}
