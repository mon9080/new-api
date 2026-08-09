package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
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

	priceData, apiErr := refreshChannelPrice(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.Nil(t, apiErr)
	require.Equal(t, 660, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "glm-5", info.TieredBillingSnapshot.ModelName)
}

func TestEnsureChannelPreConsumeReservesHigherRetryEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	billing := &recordingBillingSettler{preConsumedQuota: 660}
	info := &relaycommon.RelayInfo{Billing: billing}

	apiErr := ensureChannelPreConsume(ctx, info, 1970)
	require.Nil(t, apiErr)
	require.Equal(t, 1970, billing.reservedQuota)
}

func TestEnsureChannelPreConsumePreservesReserveAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	expected := types.NewError(errors.New("quota is insufficient"), types.ErrorCodeInsufficientUserQuota, types.ErrOptionWithStatusCode(http.StatusForbidden), types.ErrOptionWithSkipRetry())
	billing := &recordingBillingSettler{preConsumedQuota: 660, reserveErr: expected}
	info := &relaycommon.RelayInfo{Billing: billing}

	apiErr := ensureChannelPreConsume(ctx, info, 1970)
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
