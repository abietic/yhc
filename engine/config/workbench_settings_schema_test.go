package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkbenchSettingsSchemaIsStaticAndDetached(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserConfigPath(), []byte(`not-valid-json-private-fixture`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := SettingsSchema()
	if len(original) != 9 {
		t.Fatal("schema includes non-editable settings")
	}
	mutated := SettingsSchema()
	mutated["provider"].Options[0] = "untrusted-provider"
	mutated["model_profile"].ConflictsWith[0] = "untrusted-field"
	mutated["provider"].ConflictsWith[0] = "untrusted-profile"
	mutated["theme"].Aliases["dark"] = "untrusted-theme"
	mutated["max_turns"].Default[0] = '9'
	mutated["max_turns"].EffectiveValue[0] = '8'
	delete(mutated, "model")
	if !reflect.DeepEqual(SettingsSchema(), original) {
		t.Fatal("schema returns shared mutable data")
	}
	for _, field := range original {
		if field.Source != "default" || string(field.StoredValue) != "null" || field.Apply != "new_session" || !json.Valid(field.Default) {
			t.Fatal("static schema consumed user settings or has invalid metadata")
		}
	}
}

func TestWorkbenchSettingsSchemaDeclaresChoiceOnlyFieldsWithoutCatalog(t *testing.T) {
	expected := map[string]bool{
		"provider": true, "model_profile": true, "permission_mode": true, "theme": true,
		"model": false, "api_base_url": false, "max_turns": false,
		"reduced_motion": false, "prompt_suggestions": false,
	}
	fields := SettingsSchema()
	if len(fields) != len(expected) || len(fields["model_profile"].Options) != 0 {
		t.Fatal("static schema must not consume a model profile catalog")
	}
	for key, choiceOnly := range expected {
		field, present := fields[key]
		if !present {
			t.Fatalf("%s: missing setting metadata", key)
		}
		data, err := json.Marshal(field)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if choiceOnly {
			if string(wire["choice_only"]) != "true" {
				t.Errorf("%s: missing choice-only metadata", key)
			}
		} else if _, present := wire["choice_only"]; present {
			t.Errorf("%s: optional choice-only metadata must be omitted", key)
		}
	}
}

func TestWorkbenchSettingsEmptyProfileCatalogKeepsChoiceOnlyMetadata(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, "", "")
	for _, scope := range []string{"user", "project"} {
		snapshot, err := SettingsSnapshot(projectDir, scope)
		if err != nil {
			t.Fatal(err)
		}
		field := snapshot.Fields["model_profile"]
		data, err := json.Marshal(field)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if len(field.Options) != 0 || string(wire["choice_only"]) != "true" {
			t.Errorf("%s: empty catalog lost selection metadata", scope)
		}
	}
}
