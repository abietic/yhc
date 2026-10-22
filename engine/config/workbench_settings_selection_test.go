package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func workbenchSelectionMetadata(t *testing.T, field WorkbenchSettingField, name string) []string {
	t.Helper()
	data, err := json.Marshal(field)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	var values []string
	if raw, exists := wire[name]; exists {
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
	}
	return values
}

func TestWorkbenchSelectionMetadataUsesEditableSameScopeConflicts(t *testing.T) {
	expected := map[string][]string{
		"model_profile": {"provider", "model", "api_base_url"},
		"provider":      {"model_profile"}, "model": {"model_profile"}, "api_base_url": {"model_profile"},
		"theme": nil, "max_turns": nil,
	}
	for key, values := range expected {
		if got := workbenchSelectionMetadata(t, SettingsSchema()[key], "conflicts_with"); !reflect.DeepEqual(got, values) {
			t.Errorf("%s: conflicts = %v, want %v", key, got, values)
		}
	}
	projectDir := workbenchSettingsFixture(t, `{"fallback_model":"PRIVATE_ROUTING_SENTINEL","api_key":"PRIVATE_KEY_SENTINEL"}`, `{"model":"project-model"}`)
	for _, scope := range []string{"user", "project"} {
		snapshot, err := SettingsSnapshot(projectDir, scope)
		if err != nil {
			t.Fatal(err)
		}
		var blockers []string
		if scope == "user" {
			blockers = []string{"fallback_model"}
		}
		if got := workbenchSelectionMetadata(t, snapshot.Fields["model_profile"], "blocked_by"); !reflect.DeepEqual(got, blockers) {
			t.Errorf("%s: blockers = %v, want %v", scope, got, blockers)
		}
		data, _ := json.Marshal(snapshot)
		if strings.Contains(string(data), "PRIVATE_") {
			t.Fatal("constraint metadata exposed private routing or credential values")
		}
	}
}

func TestWorkbenchSelectionBlockersMatchNullAndEmptyValidation(t *testing.T) {
	for _, raw := range []string{"null", `""`, `"openai:fixture-model"`} {
		t.Run(raw, func(t *testing.T) {
			projectDir := workbenchSettingsFixture(t, "", `{"fallback_model":`+raw+`}`)
			snapshot, err := SettingsSnapshot(projectDir, "project")
			if err != nil {
				t.Fatal(err)
			}
			got := workbenchSelectionMetadata(t, snapshot.Fields["model_profile"], "blocked_by")
			if raw == `"openai:fixture-model"` {
				if !reflect.DeepEqual(got, []string{"fallback_model"}) {
					t.Fatal("non-editable model route was not a blocker")
				}
			} else if len(got) != 0 {
				t.Fatal("null or empty route became a blocker")
			}
		})
	}
}

func TestWorkbenchSelectionConfirmedAtomicPatchKeepsPortfolioAndOtherFields(t *testing.T) {
	user := validPortfolioSources().User
	user.ModelProfile = ""
	user.Provider, user.Model, user.APIBaseURL = "openai", "before-model", "https://api.example/v1"
	data, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["private_extension"] = json.RawMessage(`{"sentinel":"keep"}`)
	data, _ = json.Marshal(document)
	projectDir := workbenchSettingsFixture(t, string(data), "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SettingsPatch(projectDir, "user", snapshot.Revision, map[string]json.RawMessage{"model_profile": json.RawMessage(`"primary"`)}); !errors.Is(err, ErrSettingsInvalid) {
		t.Fatalf("unconfirmed conflicting selection: %v", err)
	}
	unchanged, _ := os.ReadFile(UserConfigPath())
	if !bytes.Equal(unchanged, data) {
		t.Fatal("rejected profile selection changed configuration")
	}
	result, err := SettingsPatch(projectDir, "user", snapshot.Revision, map[string]json.RawMessage{
		"model_profile": json.RawMessage(`"primary"`), "provider": json.RawMessage("null"),
		"model": json.RawMessage("null"), "api_base_url": json.RawMessage("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Fields["model_profile"].StoredValue) != `"primary"` {
		t.Fatal("atomic selection was not saved")
	}
	saved, _ := os.ReadFile(UserConfigPath())
	// Decode into a fresh map: omitted keys must not survive from the old fixture.
	var committed map[string]json.RawMessage
	if err := json.Unmarshal(saved, &committed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"provider", "model", "api_base_url"} {
		if _, present := committed[key]; present {
			t.Fatalf("%s override was not deleted", key)
		}
	}
	for _, key := range []string{"provider_accounts", "model_profiles", "private_extension"} {
		if _, present := committed[key]; !present {
			t.Fatalf("%s was lost", key)
		}
	}
}

func TestWorkbenchSelectionDoesNotDeleteInheritedUserOverrides(t *testing.T) {
	user := validPortfolioSources().User
	user.ModelProfile = ""
	user.Provider, user.Model = "openai", "user-model"
	data, _ := json.Marshal(user)
	projectDir := workbenchSettingsFixture(t, string(data), "")
	snapshot, err := SettingsSnapshot(projectDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Fields["provider"].StoredValue) != "null" || string(snapshot.Fields["provider"].EffectiveValue) != `"openai"` {
		t.Fatal("fixture did not distinguish inherited and stored values")
	}
	if _, err := SettingsPatch(projectDir, "project", snapshot.Revision, map[string]json.RawMessage{"model_profile": json.RawMessage(`"primary"`)}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(UserConfigPath())
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatal("project selection changed user configuration")
	}
}

func TestWorkbenchSelectionNoneditableBlockerCannotBeBypassedByResetPatch(t *testing.T) {
	data := `{"fallback_model":"openai:fixture-model","provider":"openai","model":"before-model"}`
	projectDir := workbenchSettingsFixture(t, data, "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, changes := range []map[string]json.RawMessage{
		{"model_profile": json.RawMessage(`"primary"`), "provider": json.RawMessage("null"), "model": json.RawMessage("null")},
		{"fallback_model": json.RawMessage("null")},
	} {
		if _, err := SettingsPatch(projectDir, "user", snapshot.Revision, changes); !errors.Is(err, ErrSettingsInvalid) {
			t.Fatalf("noneditable blocker was bypassed: %v", err)
		}
		unchanged, err := os.ReadFile(UserConfigPath())
		if err != nil || string(unchanged) != data {
			t.Fatal("rejected patch changed settings")
		}
	}
}
