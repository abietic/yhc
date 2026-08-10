package commands

import (
	"context"
	"strings"
	"testing"
)

func TestSessionsCommandProducesTypedServiceIntents(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	ctx := &CommandContext{SessionID: "current-id"}
	tests := []struct {
		input     string
		action    CommandAction
		operation string
		sessionID string
		name      string
		filename  string
		search    string
		limit     string
	}{
		{input: "/sessions", action: ActionSessions, operation: "list", limit: "10"},
		{input: "/sessions list 25", action: ActionSessions, operation: "list", limit: "25"},
		{input: `/sessions search "plan mode" 5`, action: ActionSessions, operation: "list", search: "plan mode", limit: "5"},
		{input: "/sessions resume saved", action: ActionResume, sessionID: "saved"},
		{input: `/sessions rename saved "release plan"`, action: ActionRename, sessionID: "saved", name: "release plan"},
		{input: `/sessions export current "report final.md"`, action: ActionExport, filename: "report final.md"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result, err := registry.Dispatch(context.Background(), EntrypointPlain, ctx, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Action != test.action {
				t.Fatalf("action = %q, want %q", result.Action, test.action)
			}
			assertOptionalResultString(t, result, "operation", test.operation)
			assertOptionalResultString(t, result, "session_id", test.sessionID)
			assertOptionalResultString(t, result, "name", test.name)
			assertOptionalResultString(t, result, "filename", test.filename)
			assertOptionalResultString(t, result, "search", test.search)
			assertOptionalResultString(t, result, "limit", test.limit)
		})
	}
}

func TestSessionsCompatibilityShortcutsAreRemovedAtP167bBoundary(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	for _, name := range []string{"history", "rename", "export"} {
		if cmd := registry.Get(name); cmd != nil {
			t.Fatalf("/%s remained registered: %#v", name, cmd)
		}
		result, err := registry.Dispatch(
			context.Background(),
			EntrypointPlain,
			&CommandContext{},
			"/"+name,
		)
		if err == nil || result != nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("/%s result = %#v, err=%v", name, result, err)
		}
	}
}

func TestSessionsRejectsMalformedAndACPSlashOperationsBeforeAction(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	for _, input := range []string{
		"/sessions list nope",
		"/sessions search",
		"/sessions resume",
		"/sessions rename saved",
		"/sessions export",
		"/sessions delete saved",
	} {
		result, err := registry.Dispatch(context.Background(), EntrypointPlain, &CommandContext{}, input)
		if err == nil || result != nil {
			t.Fatalf("%q result = %#v, err=%v", input, result, err)
		}
	}
	result, err := registry.Dispatch(
		context.Background(),
		EntrypointACP,
		&CommandContext{},
		"/sessions resume saved",
	)
	if err == nil || result != nil || !strings.Contains(err.Error(), "unavailable on acp") {
		t.Fatalf("ACP sessions result = %#v, err=%v", result, err)
	}
}

func assertOptionalResultString(
	t *testing.T,
	result *CommandResult,
	key string,
	want string,
) {
	t.Helper()
	got, err := result.OptionalString(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
