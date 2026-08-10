package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestP290EffectiveConfigMergeRetainsLegacyLayerOrder(t *testing.T) {
	user := &Config{
		Provider:      "openai",
		Model:         "user-model",
		APIBaseURL:    "https://user.example/v1",
		FallbackModel: "user-fallback",
		ModelAliases: map[string]string{
			"fast":  "user-fast",
			"cheap": "user-cheap",
		},
	}
	project := &Config{
		Model:      "project-model",
		APIBaseURL: "https://project.example/v1",
		ModelAliases: map[string]string{
			"fast": "project-fast",
		},
	}

	effective := MergeConfigs(user, project)
	if effective.Provider != user.Provider ||
		effective.Model != project.Model ||
		effective.APIBaseURL != project.APIBaseURL ||
		effective.FallbackModel != user.FallbackModel {
		t.Fatalf("effective legacy routing fields = %#v", effective)
	}
	if effective.ModelAliases["fast"] != "project-fast" ||
		effective.ModelAliases["cheap"] != "user-cheap" {
		t.Fatalf("effective aliases = %#v", effective.ModelAliases)
	}
}

func TestP290LoadEffectiveConfigMergesUserThenProject(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(UserConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		UserConfigPath(),
		[]byte(`{"provider":"openai","model":"user-model","api_base_url":"https://user.example/v1","model_aliases":{"fast":"user-fast","cheap":"user-cheap"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ProjectConfigPath(projectDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		ProjectConfigPath(projectDir),
		[]byte(`{"model":"project-model","api_base_url":"https://project.example/v1","model_aliases":{"fast":"project-fast"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	effective, err := LoadEffectiveConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Provider != "openai" ||
		effective.Model != "project-model" ||
		effective.APIBaseURL != "https://project.example/v1" {
		t.Fatalf("loaded effective config = %#v", effective)
	}
	if effective.ModelAliases["fast"] != "project-fast" ||
		effective.ModelAliases["cheap"] != "user-cheap" {
		t.Fatalf("loaded aliases = %#v", effective.ModelAliases)
	}
}
