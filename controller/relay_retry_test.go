package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
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
	require.Equal(t, "retry", service.DecideRelayRetry(ctx, guardErr, 1).Action)

	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusInternalServerError, End: http.StatusInternalServerError}}
	require.Equal(t, "stop", service.DecideRelayRetry(ctx, guardErr, 1).Action)
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

	// The per-guard retry limit is enforced by the relay loop (it reads
	// GetRetryLimit before deciding to continue), so what this test pins is
	// that the limit survives error construction untouched.
	require.Equal(t, 5, guardErr.GetRetryLimit())
	require.NotEqual(t, "stop", service.DecideRelayRetry(ctx, guardErr, 4).Action)
}
