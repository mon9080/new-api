package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPrepareBillingModelNameUsesMappedTieredModel(t *testing.T) {
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

	require.NoError(t, PrepareBillingModelName(ctx, info))
	require.Equal(t, "claude-opus-4-8", info.OriginModelName)
	require.Equal(t, "glm-5", info.BillingModelName)

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.NoError(t, err)
	require.Equal(t, 660, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "glm-5", info.TieredBillingSnapshot.ModelName)
	require.Equal(t, "standard", info.TieredBillingSnapshot.EstimatedTier)
}

func TestPrepareBillingModelNameKeepsOriginForNonTieredMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("model_mapping", `{"claude-opus-4-8":"glm-5"}`)

	info := &relaycommon.RelayInfo{OriginModelName: "claude-opus-4-8"}

	require.NoError(t, PrepareBillingModelName(ctx, info))
	require.Equal(t, "claude-opus-4-8", info.BillingModelName)
}

func TestModelPriceHelperClearsStaleTieredSnapshotForRegularBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"regular-test-model":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
	})

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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName:  "regular-test-model",
		BillingModelName: "glm-5",
		UserGroup:        "default",
		UsingGroup:       "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.NoError(t, err)
	require.NotNil(t, info.TieredBillingSnapshot)

	info.BillingModelName = info.OriginModelName
	_, err = ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.NoError(t, err)
	require.Nil(t, info.TieredBillingSnapshot)
}

func TestResolveChannelModelMappingPreservesChainAndCycleRules(t *testing.T) {
	resolved, err := ResolveChannelModelMapping(0, "claude-opus-4-8", `{"claude-opus-4-8":"glm-5","glm-5":"glm-5.2"}`)
	require.NoError(t, err)
	require.True(t, resolved.IsModelMapped)
	require.Equal(t, "glm-5.2", resolved.UpstreamModelName)

	_, err = ResolveChannelModelMapping(0, "claude-opus-4-8", `{"claude-opus-4-8":"glm-5","glm-5":"claude-opus-4-8"}`)
	require.EqualError(t, err, "model_mapping_contains_cycle")
}
