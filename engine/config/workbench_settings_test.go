package config

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestWorkbenchSettingsSupportsGLMWithoutCredentials(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, "", "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snapshot.Fields["provider"].Options, "glm") {
		t.Error("shared settings omit the runtime-supported GLM provider")
	}
	result, err := SettingsPatch(projectDir, "user", snapshot.Revision, map[string]json.RawMessage{
		"provider": json.RawMessage(`"glm"`),
		"model":    json.RawMessage(`"glm-5.3-flash"`),
	})
	if err != nil {
		t.Fatalf("setting GLM defaults without credentials: %v", err)
	}
	if string(result.Fields["provider"].StoredValue) != `"glm"` || result.Fields["provider"].Source != "user" {
		t.Fatal("shared settings did not persist GLM with user provenance")
	}
	effective, err := LoadEffectiveConfig(projectDir)
	if err != nil || effective.Provider != "glm" || effective.Model != "glm-5.3-flash" {
		t.Fatalf("startup config does not consume the same GLM defaults: %v", err)
	}
}

func workbenchSettingsFixture(t *testing.T, user, project string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	for path, data := range map[string]string{UserConfigPath(): user, ProjectConfigPath(projectDir): project} {
		if data == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir
}

func TestWorkbenchSettingsSharesEffectiveConfigAndPreservesExplicitValues(t *testing.T) {
	projectDir := workbenchSettingsFixture(t,
		`{"model":"user-model","auto_compact":true,"reduced_motion":true,"max_turns":12,"prompt_suggestions":true}`,
		`{"auto_compact":false,"reduced_motion":false,"max_turns":0,"prompt_suggestions":false}`)
	snapshot, err := SettingsSnapshot(projectDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"reduced_motion": "false", "max_turns": "0", "prompt_suggestions": "false"} {
		field := snapshot.Fields[key]
		if string(field.StoredValue) != want || string(field.EffectiveValue) != want || field.Source != "project" {
			t.Fatalf("%s = %#v", key, field)
		}
	}
	effective, err := LoadEffectiveConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if effective.AutoCompact || effective.ReducedMotion || effective.MaxTurns != 0 || *effective.PromptSuggestions {
		t.Fatal("runtime merge disagrees with shared settings")
	}
	if snapshot.Fields["model"].Source != "user" {
		t.Fatal("inherited model source lost")
	}
	if snapshot.Fields["model"].Overridden {
		t.Fatal("inherited user default was labelled as a workspace override")
	}
	userSnapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	if !userSnapshot.Fields["reduced_motion"].Overridden {
		t.Fatal("workspace override was not labelled in user settings")
	}
	result, err := SettingsPatch(projectDir, "project", snapshot.Revision, map[string]json.RawMessage{"reduced_motion": json.RawMessage("null")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reduced_motion"} {
		field := result.Fields[key]
		if string(field.StoredValue) != "null" || string(field.EffectiveValue) != "true" || field.Source != "user" {
			t.Fatalf("reset %s = %#v", key, field)
		}
	}
}

func TestWorkbenchSettingsBooleanPresenceDoesNotOverrideWhenAbsent(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, `{"auto_compact":true,"reduced_motion":true}`, `{"model":"project-model"}`)
	effective, err := LoadEffectiveConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if !effective.AutoCompact || !effective.ReducedMotion {
		t.Fatal("absent project booleans changed inherited settings")
	}
}

func TestWorkbenchSettingsExistingNullProvenanceMatchesRuntime(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, `{"max_turns":10,"reduced_motion":true}`, `{"max_turns":null,"reduced_motion":null}`)
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	effective, err := LoadEffectiveConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if effective.MaxTurns != 0 || effective.ReducedMotion {
		t.Fatal("unexpected runtime null decode")
	}
	for key, want := range map[string]string{"max_turns": "0", "reduced_motion": "false"} {
		field := snapshot.Fields[key]
		if string(field.EffectiveValue) != want || field.Source != "project" || !field.Overridden {
			t.Fatalf("%s null provenance = %#v", key, field)
		}
	}
}

func TestWorkbenchSettingsPatchPreservesUnknownDataAndRedactsProjection(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, `{"model":"before","opaque":{"token":"SENTINEL_SECRET"},"mcp_servers":{"private":{"env":{"API_KEY":"SENTINEL_SECRET"}}},"api_base_url":"https://user:SENTINEL_SECRET@example.test/v1?key=SENTINEL_SECRET#SENTINEL_SECRET"}`, "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	result, err := SettingsPatch(projectDir, "user", snapshot.Revision, map[string]json.RawMessage{"model": json.RawMessage(`"after"`), "max_turns": json.RawMessage("0"), "reduced_motion": json.RawMessage("false")})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "SENTINEL_SECRET") || strings.Contains(string(encoded), "mcp_servers") || strings.Contains(string(encoded), "opaque") {
		t.Fatal("private settings escaped projection")
	}
	if string(result.Fields["api_base_url"].StoredValue) != `"https://example.test/v1"` {
		t.Fatal("endpoint was not sanitized")
	}
	data, err := os.ReadFile(UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored["opaque"]), "SENTINEL_SECRET") || !strings.Contains(string(stored["mcp_servers"]), "SENTINEL_SECRET") || !strings.Contains(string(stored["api_base_url"]), "SENTINEL_SECRET") {
		t.Fatal("unmodified private fields changed")
	}
	if string(stored["max_turns"]) != "0" || string(stored["reduced_motion"]) != "false" {
		t.Fatal("explicit false or zero was omitted")
	}
	info, err := os.Stat(UserConfigPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, %v", info, err)
	}
}

func TestWorkbenchSettingsRevisionCoversUnknownFields(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, `{"model":"before","opaque":1}`, "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserConfigPath(), []byte(`{"model":"before","opaque":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = SettingsPatch(projectDir, "user", snapshot.Revision, map[string]json.RawMessage{"model": json.RawMessage(`"after"`)})
	if !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("conflict = %v", err)
	}
	data, _ := os.ReadFile(UserConfigPath())
	if string(data) != `{"model":"before","opaque":2}` {
		t.Fatal("stale patch modified document")
	}
}

func TestWorkbenchSettingsConcurrentWritersShareRevisionAdmission(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, "", "")
	snapshot, err := SettingsSnapshot(projectDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{`"one"`, `"two"`} {
		wg.Go(func() {
			<-start
			_, err := SettingsPatch(projectDir, "project", snapshot.Revision, map[string]json.RawMessage{"model": json.RawMessage(value)})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("success=%d conflicts=%d", succeeded, conflicted)
	}
}

func TestWorkbenchSettingsProcessWriterHelper(t *testing.T) {
	if os.Getenv("YHC_SETTINGS_TEST_HELPER") != "1" {
		return
	}
	_, err := SettingsPatch(os.Getenv("YHC_SETTINGS_TEST_PROJECT"), "project", os.Getenv("YHC_SETTINGS_TEST_REVISION"), map[string]json.RawMessage{"model": json.RawMessage(`"child"`)})
	if errors.Is(err, ErrSettingsBusy) {
		return
	}
	t.Fatalf("another process did not observe the lock: %v", err)
}

func TestWorkbenchSettingsLockIsVisibleAcrossProcesses(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, "", `{"model":"before"}`)
	snapshot, err := SettingsSnapshot(projectDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openWorkbenchSettingsStore(ProjectConfigPath(projectDir), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.lock(); err != nil {
		t.Fatal(err)
	}
	defer store.unlock()
	command := exec.Command(os.Args[0], "-test.run=^TestWorkbenchSettingsProcessWriterHelper$")
	command.Env = append(os.Environ(), "YHC_SETTINGS_TEST_HELPER=1", "YHC_SETTINGS_TEST_PROJECT="+projectDir, "YHC_SETTINGS_TEST_REVISION="+snapshot.Revision)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, output)
	}
}

func TestWorkbenchSettingsRejectsInvalidFieldsWithoutWrites(t *testing.T) {
	projectDir := workbenchSettingsFixture(t, `{"model":"before"}`, "")
	snapshot, err := SettingsSnapshot(projectDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, changes := range []map[string]json.RawMessage{
		{"auto_compact": json.RawMessage("false")},
		{"api_key": json.RawMessage(`"SENTINEL_SECRET"`)},
		{"provider.apiKey": json.RawMessage(`"SENTINEL_SECRET"`)},
		{"permission_mode": json.RawMessage(`"bypassPermissions"`)},
		{"permission_mode": json.RawMessage(`"strict"`)},
		{"max_turns": json.RawMessage("-1")},
		{"max_turns": json.RawMessage("0.5")},
		{"reduced_motion": json.RawMessage(`"false"`)},
		{"api_base_url": json.RawMessage(`"https://user:SENTINEL_SECRET@example.test"`)},
		{"api_base_url": json.RawMessage(`"https://example.test?key=SENTINEL_SECRET"`)},
		{"model": json.RawMessage(`"SENTINEL_SECRET\n"`)},
		{"model_profile": json.RawMessage(`"new-profile"`)},
	} {
		_, err := SettingsPatch(projectDir, "user", snapshot.Revision, changes)
		if !errors.Is(err, ErrSettingsInvalid) || strings.Contains(err.Error(), "SENTINEL_SECRET") {
			t.Fatalf("invalid patch error = %v", err)
		}
	}
	data, _ := os.ReadFile(UserConfigPath())
	if string(data) != `{"model":"before"}` {
		t.Fatal("rejected patch changed settings")
	}
}

func TestWorkbenchSettingsRejectsSymlinkRootsAndTargets(t *testing.T) {
	for _, target := range []string{"directory", "file", "lock"} {
		t.Run(target, func(t *testing.T) {
			projectDir := workbenchSettingsFixture(t, "", "")
			outside := t.TempDir()
			path := filepath.Dir(ProjectConfigPath(projectDir))
			if target != "directory" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if target == "file" {
					path = filepath.Join(path, "settings.json")
					outside = filepath.Join(outside, "settings.json")
					if err := os.WriteFile(outside, []byte(`{"model":"outside"}`), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					path = filepath.Join(path, ".settings-write.lock")
				}
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if target != "lock" {
				if _, err := SettingsSnapshot(projectDir, "project"); !errors.Is(err, ErrSettingsUnsafePath) {
					t.Fatalf("snapshot unsafe = %v", err)
				}
			}
			revision := (workbenchDocument{}).revision()
			if _, err := SettingsPatch(projectDir, "project", revision, map[string]json.RawMessage{"model": json.RawMessage(`"changed"`)}); !errors.Is(err, ErrSettingsUnsafePath) {
				t.Fatalf("patch unsafe = %v", err)
			}
		})
	}
}

func TestWorkbenchSettingsGlobalSnapshotDoesNotRequireWorkspace(t *testing.T) {
	_ = workbenchSettingsFixture(t, `{"max_turns":7}`, "")
	snapshot, err := SettingsSnapshot("", "user")
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Fields["max_turns"].EffectiveValue) != "7" {
		t.Fatal("user settings not loaded")
	}
	if _, err := SettingsPatch("", "user", snapshot.Revision, map[string]json.RawMessage{"max_turns": json.RawMessage("0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := SettingsSnapshot("", "project"); !errors.Is(err, ErrSettingsInvalid) {
		t.Fatalf("empty project = %v", err)
	}
}

func TestWorkbenchSettingsProfileSelectionUsesPortfolioAuthority(t *testing.T) {
	user := validPortfolioSources().User
	private := user.ModelProfiles["primary"]
	private.ProjectSelectable = false
	user.ModelProfiles["private"] = private
	data, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	projectDir := workbenchSettingsFixture(t, string(data), "")
	snapshot, err := SettingsSnapshot(projectDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Fields["model_profile"].Options; len(got) != 1 || got[0] != "primary" {
		t.Fatalf("project profiles = %v", got)
	}
	for _, profile := range []string{`"missing"`, `"private"`} {
		if _, err := SettingsPatch(projectDir, "project", snapshot.Revision, map[string]json.RawMessage{"model_profile": json.RawMessage(profile)}); !errors.Is(err, ErrSettingsInvalid) {
			t.Fatalf("unauthorized selection = %v", err)
		}
	}
	result, err := SettingsPatch(projectDir, "project", snapshot.Revision, map[string]json.RawMessage{"model_profile": json.RawMessage(`"primary"`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Fields["model_profile"].EffectiveValue) != `"primary"` || result.Fields["model_profile"].Source != "project" {
		t.Fatal("validated project selection was not applied")
	}
}
