package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
)

func sharedConfigFixture(t *testing.T) (string, string) {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The old implementation must remain inside a test-owned path in red runs.
	t.Setenv("YHC_CONFIG_DIR", filepath.Join(t.TempDir(), "legacy-override"))
	t.Setenv("EINO_AGENT_CONFIG_DIR", "")
	return project, home
}

func runSharedConfig(ctx context.Context, t *testing.T, input string) (string, error) {
	t.Helper()
	tool := ConfigTool()
	if tool.ExecuteCtx != nil {
		return tool.ExecuteCtx(ctx, input)
	}
	return tool.Execute(input)
}

func TestSharedConfigSchemaAndValidation(t *testing.T) {
	tool := ConfigTool()
	if tool.Info.Name != "Config" || tool.ExecuteCtx == nil || tool.IsReadOnly {
		t.Fatal("Config lost its context-aware write-tool contract")
	}
	if !reflect.DeepEqual(SupportedConfigSettings, sharedConfigDefinitions()) {
		t.Fatal("Config schema differs from the shared settings owner")
	}
	if slices.Contains(SupportedConfigSettings["permission_mode"].Options, "bypassPermissions") {
		t.Fatal("Config admits persisted permission bypass")
	}
	for _, input := range []map[string]any{
		{"setting": "model"},
		{"action": "list"},
		{"action": "list", "setting": "_"},
		{"action": "set", "key": "max_turns", "value": "0"},
		{"setting": "permissions.defaultMode", "key": "permission_mode", "value": "acceptEdits"},
	} {
		if err := tool.ValidateInput(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []map[string]any{
		{},
		{"setting": ""},
		{"setting": "api_key"},
		{"setting": "model", "path": "/untrusted"},
		{"action": "get", "setting": "model", "value": "unexpected"},
	} {
		if err := tool.ValidateInput(input); err == nil {
			t.Fatal("invalid input passed validation")
		}
	}
}

func TestSharedConfigWritesTheSameProjectSettingsAsDesktopAndTUI(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	ctx := WithExecutionCWD(t.Context(), project)
	for _, input := range []string{
		`{"setting":"max_turns","value":"0"}`,
		`{"setting":"reduced_motion","value":"false"}`,
		`{"setting":"permissions.defaultMode","value":"acceptEdits"}`,
	} {
		if _, err := runSharedConfig(ctx, t, input); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := engineconfig.SettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"max_turns": "0", "reduced_motion": "false", "permission_mode": `"acceptEdits"`} {
		field := snapshot.Fields[key]
		if string(field.StoredValue) != value || string(field.EffectiveValue) != value || field.Source != "project" {
			t.Fatalf("%s did not use the shared settings service", key)
		}
	}
	assertPrivateMode(t, engineconfig.ProjectConfigPath(project), 0o600)
	assertPrivateMode(t, filepath.Dir(engineconfig.ProjectConfigPath(project)), 0o700)
	effective, err := engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 0 || effective.ReducedMotion || effective.PermissionMode != "acceptEdits" {
		t.Fatal("startup loader does not consume Config writes")
	}
	if _, err := os.Stat(filepath.Join(project, ".yhc")); !os.IsNotExist(err) {
		t.Fatal("Config created an independent legacy store")
	}
}

func TestSharedConfigNeverProjectsOrEditsSecretsAndDedicatedSettings(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	path := engineconfig.ProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"shared-model","api_key":"private-fixture-sentinel","unknown":{"private":"private-fixture-sentinel"},"large":9007199254740993}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionCWD(t.Context(), project)
	for _, key := range []string{"provider.apiKey", "api_key", "hooks.preToolUse", "permissions.allow", "compact.threshold", "memory.enabled", "unknown"} {
		for _, action := range []string{"get", "set"} {
			input, _ := json.Marshal(map[string]any{"setting": key, "action": action, "value": "private-fixture-sentinel"})
			if action == "get" {
				input, _ = json.Marshal(map[string]any{"setting": key})
			}
			output, err := runSharedConfig(ctx, t, string(input))
			if err == nil || strings.Contains(output+err.Error(), "private-fixture-sentinel") {
				t.Fatalf("%s %s did not fail closed without exposing values", action, key)
			}
		}
	}
	output, err := runSharedConfig(ctx, t, `{"action":"list"}`)
	if err != nil || strings.Contains(output, "private-fixture-sentinel") {
		t.Fatal("list is not a safe shared projection")
	}
	if _, err := runSharedConfig(ctx, t, `{"setting":"theme","value":"polar-night"}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(`9007199254740993`)) || !bytes.Contains(data, []byte(`private-fixture-sentinel`)) {
		t.Fatal("shared patch failed to preserve private unknown fields")
	}
}

func TestSharedConfigScopesFollowTheEngineCWDAndConflictsAreNotRetried(t *testing.T) {
	first, home := sharedConfigFixture(t)
	second := t.TempDir()
	firstCtx, secondCtx := WithExecutionCWD(t.Context(), first), WithExecutionCWD(t.Context(), second)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		ctx   context.Context
		input string
	}{
		{firstCtx, `{"scope":"user","setting":"model","value":"user-default"}`},
		{firstCtx, `{"setting":"model","value":"first-default"}`},
		{secondCtx, `{"setting":"model","value":"second-default"}`},
	} {
		if _, err := runSharedConfig(item.ctx, t, item.input); err != nil {
			t.Fatal(err)
		}
	}
	firstSnapshot, err := engineconfig.SettingsSnapshot(first, "project")
	if err != nil {
		t.Fatal(err)
	}
	if string(firstSnapshot.Fields["model"].EffectiveValue) != `"first-default"` {
		t.Fatal("first scope was lost")
	}
	secondSnapshot, err := engineconfig.SettingsSnapshot(second, "project")
	if err != nil || string(secondSnapshot.Fields["model"].EffectiveValue) != `"second-default"` {
		t.Fatal("second scope was lost")
	}
	if _, err := engineconfig.SettingsPatch(first, "project", firstSnapshot.Revision, map[string]json.RawMessage{"theme": json.RawMessage(`"daybreak"`)}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"setting": "model", "value": "stale-default", "revision": firstSnapshot.Revision})
	if _, err := runSharedConfig(firstCtx, t, string(input)); !errors.Is(err, engineconfig.ErrSettingsConflict) {
		t.Fatalf("stale revision = %v", err)
	}
	if _, err := runSharedConfig(firstCtx, t, `{"setting":"model","value":null}`); err != nil {
		t.Fatal(err)
	}
	reset, err := engineconfig.SettingsSnapshot(first, "project")
	if err != nil || string(reset.Fields["model"].EffectiveValue) != `"user-default"` || reset.Fields["model"].Source != "user" {
		t.Fatal("reset did not reveal inherited default")
	}
	if after, _ := os.Getwd(); after != cwd {
		t.Fatal("Config changed process cwd")
	}
	if _, err := os.Stat(filepath.Join(home, ".yhc")); !os.IsNotExist(err) {
		t.Fatal("Config wrote the old user store")
	}
}

func TestSharedConfigRejectsMalformedOrCancelledRequestsBeforeWriting(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	ctx := WithExecutionCWD(t.Context(), project)
	for _, input := range []string{
		`{"setting":"theme","setting":"model","value":"dark"}`,
		`{"setting":"theme","value":"dark"} {}`,
		`{"setting":"theme","value":"dark","cwd":"/untrusted"}`,
		`{"setting":"theme","key":"model","value":"dark"}`,
		`{"scope":"outside","setting":"theme","value":"dark"}`,
		`{"action":"set","setting":"theme"}`,
		`{"action":"get","setting":"theme","value":"dark"}`,
		`{"setting":"permission_mode","value":"bypassPermissions"}`,
		`{"setting":"theme","value":null,"action":"get"}`,
		`{"setting":"theme","scope":null}`,
		`{"setting":"theme","action":false}`,
		`{"setting":"theme","value":"daybreak","revision":"bad"}`,
		`{"setting":"max_turns","value":"-1"}`,
		`{"setting":"max_turns","value":"1.5"}`,
		`{"setting":"reduced_motion","value":"yes"}`,
		`{"setting":"theme","value":"auto"}`,
		`{"setting":"model_profile","value":"missing-profile"}`,
		`{"setting":"api_base_url","value":"https://private:secret@example.invalid/?key=secret"}`,
	} {
		if _, err := runSharedConfig(ctx, t, input); err == nil {
			t.Fatal("invalid Config input was admitted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := runSharedConfig(cancelled, t, `{"setting":"theme","value":"dark"}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Config = %v", err)
	}
	if _, err := os.Stat(engineconfig.ProjectConfigPath(project)); !os.IsNotExist(err) {
		t.Fatal("rejected input created settings")
	}
}

func TestSharedConfigLegacyFormsAndSafeEndpointProjection(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	ctx := WithExecutionCWD(t.Context(), project)
	path := engineconfig.ProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"api_base_url":"https://private-fixture-sentinel@example.invalid/v1?token=private-fixture-sentinel#private-fixture-sentinel"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"action":"get","key":"provider.baseURL"}`, `{"action":"list","setting":"_"}`} {
		output, err := runSharedConfig(ctx, t, input)
		if err != nil || strings.Contains(output, "private-fixture-sentinel") || !strings.Contains(output, "https://example.invalid/v1") {
			t.Fatal("Config did not use the shared endpoint projection")
		}
	}
	if _, err := runSharedConfig(ctx, t, `{"action":"set","key":"provider.baseURL","value":"https://example.invalid/v2"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runSharedConfig(ctx, t, `{"action":"set","key":"prompt_suggestions","value":false}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runSharedConfig(ctx, t, `{"action":"set","key":"max_turns","value":12}`); err != nil {
		t.Fatal(err)
	}
	output, err := runSharedConfig(ctx, t, `{"setting":"max_turns"}`)
	var result struct {
		Scope, Revision, Apply string
		Field                  engineconfig.WorkbenchSettingField
	}
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.Scope != "project" || len(result.Revision) != 64 || result.Apply != "new_session" || string(result.Field.StoredValue) != "12" {
		t.Fatal("Config read omitted scope/revision/provenance")
	}
}

func TestSharedConfigNeverReadsOrChangesLegacyStores(t *testing.T) {
	project, home := sharedConfigFixture(t)
	paths := []string{filepath.Join(project, ".yhc", "settings.json"), filepath.Join(home, ".yhc", "settings.json"), filepath.Join(os.Getenv("YHC_CONFIG_DIR"), "settings.json")}
	sentinel := []byte(`{"model":"legacy-private-fixture-sentinel","provider":{"apiKey":"legacy-private-fixture-sentinel"}}`)
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, sentinel, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithExecutionCWD(t.Context(), project)
	output, err := runSharedConfig(ctx, t, `{"action":"list"}`)
	if err != nil || strings.Contains(output, "legacy-private-fixture-sentinel") {
		t.Fatal("legacy configuration was read")
	}
	if _, err := runSharedConfig(ctx, t, `{"setting":"theme","value":"daybreak"}`); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, sentinel) {
			t.Fatal("legacy configuration was changed")
		}
	}
}

func TestSharedConfigCorruptAndUnsafeSettingsRemainUntouched(t *testing.T) {
	for _, mode := range []string{"corrupt", "directory-symlink", "file-symlink"} {
		t.Run(mode, func(t *testing.T) {
			project, _ := sharedConfigFixture(t)
			path := engineconfig.ProjectConfigPath(project)
			outside := filepath.Join(t.TempDir(), "settings.json")
			sentinel := []byte(`{"api_key":"private-fixture-sentinel"}`)
			if err := os.WriteFile(outside, sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "directory-symlink" {
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(path)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if mode == "file-symlink" {
					if err := os.Symlink(outside, path); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else if err := os.WriteFile(path, []byte(`{"broken":"private-fixture-sentinel"`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			output, err := runSharedConfig(WithExecutionCWD(t.Context(), project), t, `{"setting":"model","value":"next-model"}`)
			if err == nil || strings.Contains(output+err.Error(), "private-fixture-sentinel") {
				t.Fatal("unsafe settings were admitted or disclosed")
			}
			after, err := os.ReadFile(path)
			outsideAfter, outsideErr := os.ReadFile(outside)
			if err != nil || outsideErr != nil || !bytes.Equal(before, after) || !bytes.Equal(outsideAfter, sentinel) {
				t.Fatal("unsafe settings changed")
			}
		})
	}
}

func TestSharedConfigLeavesHistoricalImporterSchemaUnchanged(t *testing.T) {
	if _, err := parseSettingsMigrationPayload([]byte(`{"theme":"auto","permissions":{"defaultMode":"bypassPermissions"},"compact":{"strategy":"manual"}}`)); err != nil {
		t.Fatal("historical import choices followed the live editor schema")
	}
	if _, err := parseSettingsMigrationPayload([]byte(`{"theme":"polar-night"}`)); err == nil {
		t.Fatal("historical format silently expanded")
	}
}

func TestSharedConfigRejectsUnsafeNativeIntegerTransport(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	ctx := WithExecutionCWD(t.Context(), project)
	for _, value := range []string{"9007199254740992", "9007199254740993", "9223372036854775807"} {
		if _, err := runSharedConfig(ctx, t, `{"setting":"max_turns","value":`+value+`}`); !errors.Is(err, engineconfig.ErrSettingsInvalid) {
			t.Fatal("unsafe native integer was admitted")
		}
	}
	if _, err := os.Stat(engineconfig.ProjectConfigPath(project)); !os.IsNotExist(err) {
		t.Fatal("unsafe native integer created settings")
	}
}
