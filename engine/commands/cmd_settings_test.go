package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
)

func TestSettingsCommandSharesPersistenceAndPreservesJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	registry := NewRegistry()
	RegisterDefaults(registry)
	cmdCtx := &CommandContext{CWD: project}
	read, err := registry.Dispatch(t.Context(), EntrypointTUI, cmdCtx, "/settings")
	if err != nil || !strings.Contains(read.Output, "Shared settings (user scope)") {
		t.Fatalf("default read = %+v, %v", read, err)
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("settings read created file: %v", err)
	}
	for _, input := range []string{
		"/settings user max_turns 10",
		"/settings project max_turns 0",
		"/settings user reduced_motion true",
		"/settings project reduced_motion false",
		`/settings user model "model with  two spaces"`,
	} {
		result, err := registry.Dispatch(t.Context(), EntrypointTUI, cmdCtx, input)
		if err != nil || result.Action != ActionNone || !strings.Contains(result.Output, "Changes apply to new sessions") {
			t.Fatalf("%s = %+v, %v", input, result, err)
		}
	}
	effective, err := engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 0 || effective.ReducedMotion || effective.Model != "model with  two spaces" {
		t.Fatalf("runtime config = %+v, %v", effective, err)
	}
	result, err := registry.Dispatch(t.Context(), EntrypointTUI, cmdCtx, "/settings project max_turns null")
	if err != nil || !strings.Contains(result.Output, "max_turns = 10 (source: user)") {
		t.Fatalf("reset = %+v, %v", result, err)
	}
	snapshot, err := engineconfig.SettingsSnapshot(project, "project")
	if err != nil || string(snapshot.Fields["max_turns"].StoredValue) != "null" {
		t.Fatalf("shared reset snapshot = %+v, %v", snapshot, err)
	}
}

func TestSettingsCommandIsAnExplicitIdleTUIAdministrationOperation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	registry := NewRegistry()
	RegisterDefaults(registry)
	cmd := registry.GetFor(EntrypointTUI, "settings")
	if cmd == nil || cmd.Kind != CommandKindAdministration || cmd.SideEffect != SideEffectWorkspace ||
		cmd.ExecutionOwner != ExecutionOwnerEntrypoint || cmd.ResultKind != ResultKindText || cmd.PhaseScope != PhaseScopeIdleOnly {
		t.Fatalf("settings command contract = %+v", cmd)
	}
	for _, entrypoint := range []Entrypoint{EntrypointPlain, EntrypointHeadless, EntrypointHeadlessGoal, EntrypointACP, EntrypointAppServer, EntrypointAdministration} {
		if registry.GetFor(entrypoint, "settings") != nil {
			t.Fatalf("settings leaked into %s discovery", entrypoint)
		}
		_, err := registry.Dispatch(t.Context(), entrypoint, &CommandContext{CWD: project}, "/settings user max_turns 8")
		if err == nil {
			t.Fatalf("settings dispatched through %s", entrypoint)
		}
	}
	blocked, err := registry.Dispatch(t.Context(), EntrypointTUI, &CommandContext{
		CWD: project, Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn},
	}, "/settings user max_turns 8")
	if err != nil || blocked.Availability != AvailabilityUnavailable {
		t.Fatalf("active-turn mutation = %+v, %v", blocked, err)
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unavailable command wrote configuration: %v", err)
	}
}

func TestSettingsCommandRejectsInvalidWritesAndCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	registry := NewRegistry()
	RegisterDefaults(registry)
	cmdCtx := &CommandContext{CWD: project}
	for _, input := range []string{
		"/settings other",
		"/settings project max_turns",
		"/settings user model unquoted",
		"/settings project max_turns -1",
		`/settings user permission_mode "bypassPermissions"`,
		`/settings user api_key "must-not-save"`,
		`/settings user model "one" "two"`,
	} {
		if _, err := registry.Dispatch(t.Context(), EntrypointTUI, cmdCtx, input); err == nil {
			t.Fatalf("invalid settings accepted: %s", input)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := registry.Dispatch(ctx, EntrypointTUI, cmdCtx, "/settings user max_turns 8"); err == nil {
		t.Fatal("canceled settings command was accepted")
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("invalid command wrote configuration: %v", err)
	}
}

func TestSettingsCommandProjectsOnlySharedSafeFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	if err := os.MkdirAll(engineconfig.UserConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "private-settings-test-marker"
	if err := os.WriteFile(engineconfig.UserConfigPath(), []byte(`{"private_extension":"`+secret+`","max_turns":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	RegisterDefaults(registry)
	result, err := registry.Dispatch(t.Context(), EntrypointTUI, &CommandContext{CWD: project}, "/settings user max_turns 3")
	if err != nil || strings.Contains(result.Output, secret) || strings.Contains(result.Output, "private_extension") {
		t.Fatalf("settings leaked unrelated fields: result=%+v err=%v", result, err)
	}
	contents, err := os.ReadFile(engineconfig.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(contents, &saved); err != nil || string(saved["private_extension"]) != `"`+secret+`"` {
		t.Fatalf("settings changed unrelated data: %v", err)
	}
}
