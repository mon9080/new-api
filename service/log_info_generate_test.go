package service

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestInjectTieredBillingInfoIncludesBillingModelName(t *testing.T) {
	other := map[string]interface{}{}
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			ModelName:  "glm-5",
			ExprString: `tier("standard", p * 1 + cr * 0.2 + c * 3.2)`,
		},
	}

	InjectTieredBillingInfo(other, info, &billingexpr.TieredResult{MatchedTier: "standard"})

	require.Equal(t, "tiered_expr", other["billing_mode"])
	require.Equal(t, "glm-5", other["billing_model_name"])
	require.Equal(t, "standard", other["matched_tier"])
}
