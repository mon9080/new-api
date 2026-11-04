package service

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestInjectTieredBillingInfoIncludesBillingModelName(t *testing.T) {
	other := model.NewLogOther()
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			ModelName:  "glm-5",
			ExprString: `tier("standard", p * 1 + cr * 0.2 + c * 3.2)`,
		},
	}

	InjectTieredBillingInfo(other, info, &billingexpr.TieredResult{MatchedTier: "standard"})

	// LogOther keeps its maps private; assert through the serialized form.
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(other.JSONString()), &got))
	require.Equal(t, "tiered_expr", got["billing_mode"])
	require.Equal(t, "glm-5", got["billing_model_name"])
	require.Equal(t, "standard", got["matched_tier"])
}
