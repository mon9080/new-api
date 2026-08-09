package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var glmModelNames = []string{
	"glm-5",
	"glm-5.1",
	"glm-5.2",
	"glm-5-turbo",
}

var glmBillingExpressions = map[string]string{
	"glm-5":       `tier("standard", p * 1 + cr * 0.2 + c * 3.2)`,
	"glm-5.1":     `tier("standard", p * 1.4 + cr * 0.26 + c * 4.4)`,
	"glm-5.2":     `tier("standard", p * 1.4 + cr * 0.26 + c * 4.4)`,
	"glm-5-turbo": `tier("standard", p * 1.2 + cr * 0.24 + c * 4)`,
}

var managedOptionKeys = []string{
	"ModelRatio",
	"CompletionRatio",
	"CacheRatio",
	"billing_setting.billing_mode",
	"billing_setting.billing_expr",
}

type optionRecord struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (optionRecord) TableName() string {
	return "options"
}

type optionChange struct {
	Key         string   `json:"key"`
	OldValue    string   `json:"old_value"`
	NewValue    string   `json:"new_value"`
	AddedModels []string `json:"added_models,omitempty"`
}

type mergeReport struct {
	Database string         `json:"database"`
	Applied  bool           `json:"applied"`
	Changed  bool           `json:"changed"`
	Changes  []optionChange `json:"changes"`
}

type optionBackup struct {
	Database  string            `json:"database"`
	CreatedAt time.Time         `json:"created_at"`
	Options   map[string]string `json:"options"`
}

func main() {
	databasePath := flag.String("db", "", "path to the New API SQLite database")
	dryRun := flag.Bool("dry-run", false, "print the merge result without writing the database")
	apply := flag.Bool("apply", false, "apply the merge in one SQLite transaction")
	backupPath := flag.String("backup", "", "path for the pre-apply JSON backup; required with --apply")
	flag.Parse()

	if *databasePath == "" || (*dryRun == *apply) {
		fatalf("usage: billing-config-merge --db /path/to/one-api.db (--dry-run | --apply --backup /path/to/backup.json)")
	}
	if *apply && *backupPath == "" {
		fatalf("--backup is required with --apply")
	}

	report, err := mergeDatabase(*databasePath, *apply, *backupPath)
	if err != nil {
		fatalf("billing configuration merge failed: %v", err)
	}
	output, err := common.Marshal(report)
	if err != nil {
		fatalf("marshal merge report: %v", err)
	}
	fmt.Println(string(output))
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func mergeDatabase(databasePath string, apply bool, backupPath string) (mergeReport, error) {
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	if err != nil {
		return mergeReport{}, err
	}

	current, err := loadManagedOptions(db)
	if err != nil {
		return mergeReport{}, err
	}
	updated, addedModels, err := buildMergedOptions(current)
	if err != nil {
		return mergeReport{}, err
	}

	report := mergeReport{Database: databasePath, Applied: apply}
	for _, key := range managedOptionKeys {
		if current[key] == updated[key] {
			continue
		}
		report.Changed = true
		report.Changes = append(report.Changes, optionChange{
			Key:         key,
			OldValue:    current[key],
			NewValue:    updated[key],
			AddedModels: addedModels[key],
		})
	}

	if !apply || !report.Changed {
		return report, nil
	}
	if err := writeBackup(backupPath, databasePath, current); err != nil {
		return mergeReport{}, err
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, change := range report.Changes {
			if err := tx.Save(&optionRecord{Key: change.Key, Value: change.NewValue}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return mergeReport{}, err
	}
	return report, nil
}

func loadManagedOptions(db *gorm.DB) (map[string]string, error) {
	var records []optionRecord
	if err := db.Where("key IN ?", managedOptionKeys).Find(&records).Error; err != nil {
		return nil, err
	}
	options := make(map[string]string, len(managedOptionKeys))
	for _, key := range managedOptionKeys {
		options[key] = ""
	}
	for _, record := range records {
		options[record.Key] = record.Value
	}
	return options, nil
}

func buildMergedOptions(current map[string]string) (map[string]string, map[string][]string, error) {
	ratio_setting.InitRatioSettings()
	updated := make(map[string]string, len(current))
	for key, value := range current {
		updated[key] = value
	}
	addedModels := make(map[string][]string)

	modelRatios, err := parseFloatOption(current["ModelRatio"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse ModelRatio: %w", err)
	}
	if added := mergeMissingGPTDefaults(modelRatios, ratio_setting.GetDefaultModelRatioMap()); len(added) > 0 {
		updated["ModelRatio"], err = marshalOption(modelRatios)
		if err != nil {
			return nil, nil, err
		}
		addedModels["ModelRatio"] = added
	}

	completionRatios, err := parseFloatOption(current["CompletionRatio"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse CompletionRatio: %w", err)
	}
	if added := mergeMissingGPTDefaults(completionRatios, ratio_setting.GetCompletionRatioCopy()); len(added) > 0 {
		updated["CompletionRatio"], err = marshalOption(completionRatios)
		if err != nil {
			return nil, nil, err
		}
		addedModels["CompletionRatio"] = added
	}

	cacheRatios, err := parseFloatOption(current["CacheRatio"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse CacheRatio: %w", err)
	}
	if added := mergeMissingGPTDefaults(cacheRatios, ratio_setting.GetCacheRatioCopy()); len(added) > 0 {
		updated["CacheRatio"], err = marshalOption(cacheRatios)
		if err != nil {
			return nil, nil, err
		}
		addedModels["CacheRatio"] = added
	}

	billingModes, err := parseStringOption(current["billing_setting.billing_mode"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse billing_setting.billing_mode: %w", err)
	}
	if setGLMBillingModes(billingModes) {
		updated["billing_setting.billing_mode"], err = marshalOption(billingModes)
		if err != nil {
			return nil, nil, err
		}
		addedModels["billing_setting.billing_mode"] = append([]string(nil), glmModelNames...)
	}

	billingExprs, err := parseStringOption(current["billing_setting.billing_expr"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse billing_setting.billing_expr: %w", err)
	}
	if setGLMBillingExpressions(billingExprs) {
		updated["billing_setting.billing_expr"], err = marshalOption(billingExprs)
		if err != nil {
			return nil, nil, err
		}
		addedModels["billing_setting.billing_expr"] = append([]string(nil), glmModelNames...)
	}

	return updated, addedModels, nil
}

func parseFloatOption(value string) (map[string]float64, error) {
	parsed := make(map[string]float64)
	if strings.TrimSpace(value) == "" {
		return parsed, nil
	}
	if err := common.UnmarshalJsonStr(value, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseStringOption(value string) (map[string]string, error) {
	parsed := make(map[string]string)
	if strings.TrimSpace(value) == "" {
		return parsed, nil
	}
	if err := common.UnmarshalJsonStr(value, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func marshalOption(value interface{}) (string, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func mergeMissingGPTDefaults(target, defaults map[string]float64) []string {
	added := make([]string, 0)
	for modelName, ratio := range defaults {
		if !strings.HasPrefix(modelName, "gpt-") {
			continue
		}
		if _, exists := target[modelName]; exists {
			continue
		}
		target[modelName] = ratio
		added = append(added, modelName)
	}
	sort.Strings(added)
	return added
}

func setGLMBillingModes(modes map[string]string) bool {
	changed := false
	for _, modelName := range glmModelNames {
		if modes[modelName] == billing_setting.BillingModeTieredExpr {
			continue
		}
		modes[modelName] = billing_setting.BillingModeTieredExpr
		changed = true
	}
	return changed
}

func setGLMBillingExpressions(expressions map[string]string) bool {
	changed := false
	for _, modelName := range glmModelNames {
		if expressions[modelName] == glmBillingExpressions[modelName] {
			continue
		}
		expressions[modelName] = glmBillingExpressions[modelName]
		changed = true
	}
	return changed
}

func writeBackup(path, databasePath string, options map[string]string) error {
	if path == "" {
		return errors.New("backup path is empty")
	}
	backup, err := common.Marshal(optionBackup{
		Database:  databasePath,
		CreatedAt: time.Now().UTC(),
		Options:   options,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(backup, '\n'), 0600)
}
