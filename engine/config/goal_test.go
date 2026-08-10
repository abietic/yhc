package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP49GoalCapabilityDefaultsEnabled(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.Goal == nil ||
		defaults.Goal.Enabled == nil ||
		!*defaults.Goal.Enabled ||
		defaults.Goal.DefaultTokenBudget != nil {
		t.Fatalf("default Goal config = %#v", defaults.Goal)
	}
}

func TestP244GoalConfigDefaultsAndFieldwiseMerge(t *testing.T) {
	enabled := true
	budget := uint64(250_000)
	merged := MergeConfigs(
		&Config{Goal: &GoalConfig{Enabled: &enabled}},
		&Config{Goal: &GoalConfig{DefaultTokenBudget: &budget}},
	)
	if merged.Goal == nil ||
		merged.Goal.Enabled == nil ||
		!*merged.Goal.Enabled ||
		merged.Goal.DefaultTokenBudget == nil ||
		*merged.Goal.DefaultTokenBudget != budget {
		t.Fatalf("fieldwise merged Goal config = %#v", merged.Goal)
	}

	enabled = false
	budget = 1
	if merged.Goal == nil ||
		!*merged.Goal.Enabled ||
		*merged.Goal.DefaultTokenBudget != 250_000 {
		t.Fatal("merged Goal config retained caller-owned pointers")
	}

	disabled := false
	merged = MergeConfigs(&Config{Goal: &GoalConfig{Enabled: &disabled}}, nil)
	if merged.Goal == nil || merged.Goal.Enabled == nil || *merged.Goal.Enabled {
		t.Fatalf("explicitly disabled Goal config = %#v", merged.Goal)
	}
}

func TestP244GoalConfigRejectsZeroDefaultBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"goal":{"enabled":true,"default_token_budget":0}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigFromPath(path); err == nil ||
		!strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("zero Goal budget error = %v", err)
	}
}
