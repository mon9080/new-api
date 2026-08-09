package main

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMergeDatabaseDryRunAndApplyAreIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "one-api.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&optionRecord{}))
	require.NoError(t, db.Create(&optionRecord{Key: "ModelRatio", Value: `{"gpt-5":999}`}).Error)
	require.NoError(t, db.Create(&optionRecord{Key: "CompletionRatio", Value: `{}`}).Error)
	require.NoError(t, db.Create(&optionRecord{Key: "CacheRatio", Value: `{}`}).Error)
	require.NoError(t, db.Create(&optionRecord{Key: "billing_setting.billing_mode", Value: `{"other":"ratio"}`}).Error)
	require.NoError(t, db.Create(&optionRecord{Key: "billing_setting.billing_expr", Value: `{"other":"tier(\"base\", p)"}`}).Error)
	require.NoError(t, db.Create(&optionRecord{Key: "UnrelatedOption", Value: "unchanged"}).Error)

	dryRun, err := mergeDatabase(dbPath, false, "")
	require.NoError(t, err)
	require.True(t, dryRun.Changed)

	var afterDryRun optionRecord
	require.NoError(t, db.Where("key = ?", "ModelRatio").First(&afterDryRun).Error)
	require.Equal(t, `{"gpt-5":999}`, afterDryRun.Value)

	backupPath := filepath.Join(t.TempDir(), "billing-options-backup.json")
	applied, err := mergeDatabase(dbPath, true, backupPath)
	require.NoError(t, err)
	require.True(t, applied.Changed)

	options := readOptionValues(t, db)
	require.Equal(t, "unchanged", options["UnrelatedOption"])
	modelRatios := map[string]float64{}
	require.NoError(t, common.UnmarshalJsonStr(options["ModelRatio"], &modelRatios))
	require.Equal(t, 999.0, modelRatios["gpt-5"])
	require.Equal(t, 2.5, modelRatios["gpt-5.6-sol"])

	completionRatios := map[string]float64{}
	require.NoError(t, common.UnmarshalJsonStr(options["CompletionRatio"], &completionRatios))
	require.Equal(t, 2.0, completionRatios["gpt-4-gizmo-*"])

	cacheRatios := map[string]float64{}
	require.NoError(t, common.UnmarshalJsonStr(options["CacheRatio"], &cacheRatios))
	require.Equal(t, 0.5, cacheRatios["gpt-4o"])

	billingModes := map[string]string{}
	require.NoError(t, common.UnmarshalJsonStr(options["billing_setting.billing_mode"], &billingModes))
	require.Equal(t, "ratio", billingModes["other"])
	for _, modelName := range glmModelNames {
		require.Equal(t, "tiered_expr", billingModes[modelName])
	}

	billingExprs := map[string]string{}
	require.NoError(t, common.UnmarshalJsonStr(options["billing_setting.billing_expr"], &billingExprs))
	expectedCosts := map[string]float64{
		"glm-5":       1420,
		"glm-5.1":     1970,
		"glm-5.2":     1970,
		"glm-5-turbo": 1720,
	}
	for _, modelName := range glmModelNames {
		cost, trace, runErr := billingexpr.RunExpr(billingExprs[modelName], billingexpr.TokenParams{P: 1000, CR: 500, C: 100})
		require.NoError(t, runErr)
		require.Equal(t, "standard", trace.MatchedTier)
		require.Equal(t, expectedCosts[modelName], cost)
	}

	again, err := mergeDatabase(dbPath, true, backupPath)
	require.NoError(t, err)
	require.False(t, again.Changed)
}

func TestGLMBillingExpressionsUseOfficialPricesWithAndWithoutCache(t *testing.T) {
	cases := []struct {
		modelName    string
		withoutCache float64
		withCache    float64
	}{
		{modelName: "glm-5", withoutCache: 1320, withCache: 1420},
		{modelName: "glm-5.1", withoutCache: 1840, withCache: 1970},
		{modelName: "glm-5.2", withoutCache: 1840, withCache: 1970},
		{modelName: "glm-5-turbo", withoutCache: 1600, withCache: 1720},
	}

	for _, tc := range cases {
		t.Run(tc.modelName, func(t *testing.T) {
			withoutCache, trace, err := billingexpr.RunExpr(glmBillingExpressions[tc.modelName], billingexpr.TokenParams{P: 1000, C: 100})
			require.NoError(t, err)
			require.Equal(t, "standard", trace.MatchedTier)
			require.Equal(t, tc.withoutCache, withoutCache)

			withCache, trace, err := billingexpr.RunExpr(glmBillingExpressions[tc.modelName], billingexpr.TokenParams{P: 1000, CR: 500, C: 100})
			require.NoError(t, err)
			require.Equal(t, "standard", trace.MatchedTier)
			require.Equal(t, tc.withCache, withCache)
		})
	}
}

func readOptionValues(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var records []optionRecord
	require.NoError(t, db.Find(&records).Error)
	values := make(map[string]string, len(records))
	for _, record := range records {
		values[record.Key] = record.Value
	}
	return values
}
