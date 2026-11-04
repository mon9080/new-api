package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay/helper"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRefreshChannelBillingUsesSelectedChannelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"glm-5":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"glm-5":"tier(\"standard\", p * 1 + cr * 0.2 + c * 3.2)"}`,
	}))

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx.Set("model_mapping", `{"claude-opus-4-8":"glm-5"}`)
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-opus-4-8",
		RelayMode:       1,
		UserGroup:       "default",
		UsingGroup:      "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	// The relay loop now reprices inline via helper.ModelPriceHelper after
	// channel selection; the old controller helper is gone. Pre-consume covers
	// the prompt side only (C=0); settlement uses the real output tokens.
	require.NoError(t, helper.PrepareBillingModelName(ctx, info))
	priceData, apiErr := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.Nil(t, apiErr)
	require.Equal(t, 500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "glm-5", info.TieredBillingSnapshot.ModelName)
}

func TestPreConsumeBillingReservesHigherRetryEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	billing := &recordingBillingSettler{preConsumedQuota: 660}
	info := &relaycommon.RelayInfo{Billing: billing}

	// The controller helper was folded into service.PreConsumeBilling during
	// the upstream merge; the contract is unchanged: raise the reservation to
	// the retry estimate before submitting upstream.
	apiErr := service.PreConsumeBilling(ctx, 1970, info)
	require.Nil(t, apiErr)
	require.Equal(t, 1970, billing.reservedQuota)
}

func TestPreConsumeBillingPreservesReserveAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	expected := types.NewError(errors.New("quota is insufficient"), types.ErrorCodeInsufficientUserQuota, types.ErrOptionWithStatusCode(http.StatusForbidden), types.ErrOptionWithSkipRetry())
	billing := &recordingBillingSettler{preConsumedQuota: 660, reserveErr: expected}
	info := &relaycommon.RelayInfo{Billing: billing}

	apiErr := service.PreConsumeBilling(ctx, 1970, info)
	require.Same(t, expected, apiErr)
}

type recordingBillingSettler struct {
	preConsumedQuota int
	reservedQuota    int
	reserveErr       error
}

func (s *recordingBillingSettler) Settle(int) error {
	return nil
}

func (s *recordingBillingSettler) Refund(*gin.Context) {}

func (s *recordingBillingSettler) NeedsRefund() bool {
	return false
}

func (s *recordingBillingSettler) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

func (s *recordingBillingSettler) Reserve(targetQuota int) error {
	s.reservedQuota = targetQuota
	return s.reserveErr
}

// ReserveBeforeSubmit mirrors Reserve; upstream added it to BillingSettler for
// pre-submit top-ups on task retries.
func (s *recordingBillingSettler) ReserveBeforeSubmit(targetQuota int) error {
	s.reservedQuota = targetQuota
	return s.reserveErr
}
