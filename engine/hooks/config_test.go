package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Config Loading Tests
// ---------------------------------------------------------------------------

func TestLoadHooksConfig_FileNotExist(t *testing.T) {
	cfg, err := LoadHooksConfig("/nonexistent/path/hooks.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
		return
	}
	if len(cfg.Events) != 0 {
		t.Fatalf("expected empty events, got %d", len(cfg.Events))
	}
}

func TestLoadHooksConfig_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hooks.json")

	configJSON := `{
		"PreToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{
						"type": "command",
						"command": "echo pre-tool",
						"timeout": 30
					}
				]
			},
			{
				"matcher": "",
				"hooks": [
					{
						"type": "http",
						"url": "https://example.com/hook",
						"headers": {"Authorization": "Bearer $MY_TOKEN"},
						"allowedEnvVars": ["MY_TOKEN"],
						"timeout": 10
					}
				]
			}
		],
		"Stop": [
			{
				"hooks": [
					{
						"type": "command",
						"command": "echo stop"
					}
				]
			}
		],
		"SessionStart": [
			{
				"matcher": "startup|resume",
				"hooks": [
					{
						"type": "command",
						"command": "echo session",
						"statusMessage": "Starting session..."
					}
				]
			}
		]
	}`

	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
		return
	}

	cfg, err := LoadHooksConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	// Check PreToolUse event.
	preToolMatchers := cfg.Events[HookEventPreToolUse]
	if len(preToolMatchers) != 2 {
		t.Fatalf("expected 2 PreToolUse matchers, got %d", len(preToolMatchers))
	}
	if preToolMatchers[0].Matcher != "Bash" {
		t.Fatalf("expected matcher 'Bash', got %q", preToolMatchers[0].Matcher)
	}
	if preToolMatchers[0].Hooks[0].Type != HookCommandTypeCommand {
		t.Fatalf("expected command type, got %q", preToolMatchers[0].Hooks[0].Type)
	}
	if preToolMatchers[0].Hooks[0].Command != "echo pre-tool" {
		t.Fatalf("expected 'echo pre-tool', got %q", preToolMatchers[0].Hooks[0].Command)
	}
	if preToolMatchers[0].Hooks[0].Timeout != 30 {
		t.Fatalf("expected timeout 30, got %d", preToolMatchers[0].Hooks[0].Timeout)
	}

	// Check HTTP hook.
	httpHook := preToolMatchers[1].Hooks[0]
	if httpHook.Type != HookCommandTypeHTTP {
		t.Fatalf("expected http type, got %q", httpHook.Type)
	}
	if httpHook.URL != "https://example.com/hook" {
		t.Fatalf("expected URL, got %q", httpHook.URL)
	}
	if httpHook.Headers["Authorization"] != "Bearer $MY_TOKEN" {
		t.Fatalf("expected header template, got %q", httpHook.Headers["Authorization"])
	}
	if len(httpHook.AllowedEnvVars) != 1 || httpHook.AllowedEnvVars[0] != "MY_TOKEN" {
		t.Fatalf("expected allowedEnvVars [MY_TOKEN], got %v", httpHook.AllowedEnvVars)
	}

	// Check Stop event.
	stopMatchers := cfg.Events[HookEventStop]
	if len(stopMatchers) != 1 {
		t.Fatalf("expected 1 Stop matcher, got %d", len(stopMatchers))
	}
	if stopMatchers[0].Matcher != "" {
		t.Fatalf("expected empty matcher for Stop, got %q", stopMatchers[0].Matcher)
	}

	// Check SessionStart event.
	sessionMatchers := cfg.Events[HookEventSessionStart]
	if len(sessionMatchers) != 1 {
		t.Fatalf("expected 1 SessionStart matcher, got %d", len(sessionMatchers))
	}
	if sessionMatchers[0].Matcher != "startup|resume" {
		t.Fatalf("expected matcher 'startup|resume', got %q", sessionMatchers[0].Matcher)
	}
	if sessionMatchers[0].Hooks[0].StatusMessage != "Starting session..." {
		t.Fatalf("expected statusMessage, got %q", sessionMatchers[0].Hooks[0].StatusMessage)
	}
}

func TestLoadHooksConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hooks.json")

	if err := os.WriteFile(configPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
		return
	}

	_, err := LoadHooksConfig(configPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
		return
	}
}

func TestLoadHooksConfig_UnknownEventsIgnored(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hooks.json")

	configJSON := `{
		"UnknownEvent": [{"hooks": [{"type": "command", "command": "echo unknown"}]}],
		"Stop": [{"hooks": [{"type": "command", "command": "echo stop"}]}]
	}`

	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
		return
	}

	cfg, err := LoadHooksConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	if _, ok := cfg.Events["UnknownEvent"]; ok {
		t.Fatal("expected unknown event to be ignored")
	}
	if len(cfg.Events[HookEventStop]) != 1 {
		t.Fatal("expected Stop event to be loaded")
	}
}

func TestLoadHooksConfigFromDir(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
		return
	}

	hooksJSON := `{
		"PreToolUse": [{"matcher": "Read", "hooks": [{"type": "command", "command": "echo read"}]}]
	}`
	if err := os.WriteFile(filepath.Join(claudeDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
		return
	}

	settingsJSON := `{
		"hooks": {
			"PostToolUse": [{"matcher": "Write", "hooks": [{"type": "command", "command": "echo write"}]}]
		}
	}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
		return
	}

	cfg, err := LoadHooksConfigFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	if len(cfg.Events[HookEventPreToolUse]) != 1 {
		t.Fatal("expected PreToolUse from hooks.json")
	}
	if len(cfg.Events[HookEventPostToolUse]) != 1 {
		t.Fatal("expected PostToolUse from settings.json")
	}
}

// ---------------------------------------------------------------------------
// Config Manager Tests
// ---------------------------------------------------------------------------

func TestHooksConfigManager_LoadAndResolve(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hooks.json")

	configJSON := `{
		"PreToolUse": [
			{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo bash"}]},
			{"matcher": "Read|Write", "hooks": [{"type": "http", "url": "http://example.com/hook"}]}
		],
		"Stop": [
			{"hooks": [{"type": "command", "command": "echo stop"}]}
		]
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write: %v", err)
		return
	}

	mgr := NewHooksConfigManager()
	if err := mgr.LoadFromPath(HookSourceUser, configPath); err != nil {
		t.Fatalf("load: %v", err)
		return
	}

	snapshot := mgr.GetSnapshot()
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 resolved hooks, got %d", len(snapshot))
	}

	preToolHooks := mgr.GetHooksForEvent(HookEventPreToolUse)
	if len(preToolHooks) != 2 {
		t.Fatalf("expected 2 PreToolUse hooks, got %d", len(preToolHooks))
	}

	stopHooks := mgr.GetHooksForEvent(HookEventStop)
	if len(stopHooks) != 1 {
		t.Fatalf("expected 1 Stop hook, got %d", len(stopHooks))
	}

	// Verify source is set correctly.
	for _, h := range snapshot {
		if h.Source != HookSourceUser {
			t.Fatalf("expected source %q, got %q", HookSourceUser, h.Source)
		}
	}
}

func TestHooksConfigManager_MultipleSources(t *testing.T) {
	mgr := NewHooksConfigManager()

	userCfg := &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventPreToolUse: {{Matcher: "Bash", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo user"}}}},
	}}
	projectCfg := &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventPreToolUse: {{Matcher: "Write", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo project"}}}},
	}}

	mgr.SetConfig(HookSourceUser, userCfg)
	mgr.SetConfig(HookSourceProject, projectCfg)

	hooks := mgr.GetHooksForEvent(HookEventPreToolUse)
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks from 2 sources, got %d", len(hooks))
	}

	// Verify both sources present.
	sources := map[HookSource]bool{}
	for _, h := range hooks {
		sources[h.Source] = true
	}
	if !sources[HookSourceUser] || !sources[HookSourceProject] {
		t.Fatalf("expected both user and project sources, got %v", sources)
	}
}

func TestHooksConfigManager_SetConfigNilRemoves(t *testing.T) {
	mgr := NewHooksConfigManager()

	cfg := &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventStop: {{Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo"}}}},
	}}
	mgr.SetConfig(HookSourceUser, cfg)

	if len(mgr.GetSnapshot()) != 1 {
		t.Fatal("expected 1 hook after set")
	}

	mgr.SetConfig(HookSourceUser, nil)
	if len(mgr.GetSnapshot()) != 0 {
		t.Fatal("expected 0 hooks after nil set")
	}
}

// ---------------------------------------------------------------------------
// Event Matcher Tests
// ---------------------------------------------------------------------------

func TestEventMatcher_ExactMatch(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventPreToolUse: {
			{Matcher: "Bash", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo bash"}}},
			{Matcher: "Write", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo write"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	// Exact match for Bash.
	matched := matcher.MatchHooks(HookEventPreToolUse, "Bash")
	if len(matched) != 1 || matched[0].Command.Command != "echo bash" {
		t.Fatalf("expected Bash hook, got %v", matched)
	}

	// Exact match for Write.
	matched = matcher.MatchHooks(HookEventPreToolUse, "Write")
	if len(matched) != 1 || matched[0].Command.Command != "echo write" {
		t.Fatalf("expected Write hook, got %v", matched)
	}

	// No match for Read.
	matched = matcher.MatchHooks(HookEventPreToolUse, "Read")
	if len(matched) != 0 {
		t.Fatalf("expected no match for Read, got %d", len(matched))
	}
}

func TestEventMatcher_EmptyMatcherMatchesAll(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventStop: {
			{Matcher: "", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo stop"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	// Empty matcher should match any field value.
	matched := matcher.MatchHooks(HookEventStop, "anything")
	if len(matched) != 1 {
		t.Fatalf("expected empty matcher to match, got %d", len(matched))
	}

	matched = matcher.MatchHooks(HookEventStop, "")
	if len(matched) != 1 {
		t.Fatalf("expected empty matcher to match empty value, got %d", len(matched))
	}
}

func TestEventMatcher_PipeSeparatedPattern(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventPreToolUse: {
			{Matcher: "Read|Write|Edit", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo file"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	for _, tool := range []string{"Read", "Write", "Edit"} {
		matched := matcher.MatchHooks(HookEventPreToolUse, tool)
		if len(matched) != 1 {
			t.Fatalf("expected match for %s, got %d", tool, len(matched))
		}
	}

	matched := matcher.MatchHooks(HookEventPreToolUse, "Bash")
	if len(matched) != 0 {
		t.Fatalf("expected no match for Bash with pipe pattern, got %d", len(matched))
	}
}

func TestEventMatcher_GlobPattern(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventPreToolUse: {
			{Matcher: "Bash*", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo bash-glob"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	matched := matcher.MatchHooks(HookEventPreToolUse, "Bash")
	if len(matched) != 1 {
		t.Fatalf("expected Bash* to match 'Bash', got %d", len(matched))
	}

	matched = matcher.MatchHooks(HookEventPreToolUse, "BashOutput")
	if len(matched) != 1 {
		t.Fatalf("expected Bash* to match 'BashOutput', got %d", len(matched))
	}

	matched = matcher.MatchHooks(HookEventPreToolUse, "Read")
	if len(matched) != 0 {
		t.Fatalf("expected Bash* to NOT match 'Read', got %d", len(matched))
	}
}

func TestEventMatcher_SessionStartMatcher(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventSessionStart: {
			{Matcher: "startup|resume", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo session"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	matched := matcher.MatchHooks(HookEventSessionStart, "startup")
	if len(matched) != 1 {
		t.Fatalf("expected match for 'startup', got %d", len(matched))
	}

	matched = matcher.MatchHooks(HookEventSessionStart, "resume")
	if len(matched) != 1 {
		t.Fatalf("expected match for 'resume', got %d", len(matched))
	}

	matched = matcher.MatchHooks(HookEventSessionStart, "compact")
	if len(matched) != 0 {
		t.Fatalf("expected no match for 'compact', got %d", len(matched))
	}
}

func TestEventMatcher_MatchHooksMulti(t *testing.T) {
	mgr := NewHooksConfigManager()
	mgr.SetConfig(HookSourceUser, &HooksConfig{Events: map[HookEvent][]HookMatcher{
		HookEventNotification: {
			{Matcher: "permission_prompt", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo perm"}}},
			{Matcher: "", Hooks: []HookCommand{{Type: HookCommandTypeCommand, Command: "echo all"}}},
		},
	}})

	matcher := NewEventMatcher(mgr)

	// MatchHooksMulti with one matching value.
	matched := matcher.MatchHooksMulti(HookEventNotification, []string{"permission_prompt", "idle_prompt"})
	if len(matched) != 2 {
		// Should match both: "permission_prompt" exact match AND "" catch-all.
		t.Fatalf("expected 2 matches, got %d", len(matched))
	}
}

// ---------------------------------------------------------------------------
// matchEventPattern Tests
// ---------------------------------------------------------------------------

func TestMatchEventPattern(t *testing.T) {
	tests := []struct {
		pattern    string
		fieldValue string
		want       bool
	}{
		// Empty pattern matches all.
		{"", "anything", true},
		{"", "", true},

		// Exact match.
		{"Bash", "Bash", true},
		{"Bash", "Read", false},

		// Pipe-separated.
		{"Read|Write|Edit", "Read", true},
		{"Read|Write|Edit", "Write", true},
		{"Read|Write|Edit", "Edit", true},
		{"Read|Write|Edit", "Bash", false},

		// Glob prefix.
		{"Bash*", "Bash", true},
		{"Bash*", "BashOutput", true},
		{"Bash*", "Read", false},

		// Glob suffix.
		{"*Tool", "BashTool", true},
		{"*Tool", "ReadTool", true},
		{"*Tool", "Bash", false},

		// Glob with contains.
		{"*oo*", "Foo", true},
		{"*oo*", "FooBar", true},
		{"*oo*", "Bar", false},
	}

	for _, tt := range tests {
		got := matchEventPattern(tt.pattern, tt.fieldValue)
		if got != tt.want {
			t.Errorf("matchEventPattern(%q, %q) = %v, want %v", tt.pattern, tt.fieldValue, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP Hook Executor Tests
// ---------------------------------------------------------------------------

func TestHTTPHookExecutor_Success(t *testing.T) {
	var receivedBody string
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  server.URL,
	}

	result := executor.Execute(context.Background(), hook, `{"tool_name":"Bash","command":"pwd"}`)

	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}
	if result.Body != `{"ok": true}` {
		t.Fatalf("unexpected body: %s", result.Body)
	}
	if receivedBody != `{"tool_name":"Bash","command":"pwd"}` {
		t.Fatalf("unexpected received body: %s", receivedBody)
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", receivedHeaders.Get("Content-Type"))
	}
}

func TestHTTPHookExecutor_WithHeaders(t *testing.T) {
	// Set env var for interpolation test.
	t.Setenv("TEST_HOOK_TOKEN", "secret123")

	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type:           HookCommandTypeHTTP,
		URL:            server.URL,
		Headers:        map[string]string{"Authorization": "Bearer $TEST_HOOK_TOKEN"},
		AllowedEnvVars: []string{"TEST_HOOK_TOKEN"},
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if receivedAuth != "Bearer secret123" {
		t.Fatalf("expected interpolated auth header, got %q", receivedAuth)
	}
}

func TestHTTPHookExecutor_HeaderEnvVarNotAllowed(t *testing.T) {
	t.Setenv("SECRET_VAR", "should_not_appear")

	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type:           HookCommandTypeHTTP,
		URL:            server.URL,
		Headers:        map[string]string{"Authorization": "Bearer $SECRET_VAR"},
		AllowedEnvVars: []string{}, // SECRET_VAR not in allowlist
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	// Secret should NOT be interpolated (replaced with empty string).
	// Note: net/http trims trailing whitespace from header values, so "Bearer " becomes "Bearer".
	if receivedAuth != "Bearer" {
		t.Fatalf("expected non-interpolated header 'Bearer' (trailing space trimmed by net/http), got %q", receivedAuth)
	}
}

func TestHTTPHookExecutor_NonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  server.URL,
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for 403 response")
	}
	if result.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", result.StatusCode)
	}
	if result.Body != "forbidden" {
		t.Fatalf("expected body 'forbidden', got %q", result.Body)
	}
}

func TestHTTPHookExecutor_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type:    HookCommandTypeHTTP,
		URL:     server.URL,
		Timeout: 1, // 1 second timeout
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for timeout")
	}
	// Should be marked as aborted or have an error.
	if !result.Aborted && result.Error == "" {
		t.Fatal("expected aborted or error for timeout")
	}
}

func TestHTTPHookExecutor_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait long enough that the client cancels first, but not so long
		// that the test is slow (httptest waits for handler completion).
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  server.URL,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result := executor.Execute(ctx, hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for cancelled context")
	}
	if !result.Aborted {
		t.Fatal("expected aborted for cancelled context")
	}
}

func TestHTTPHookExecutor_InvalidURL(t *testing.T) {
	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  "://invalid-url",
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for invalid URL")
	}
	if result.Error == "" {
		t.Fatal("expected error message for invalid URL")
	}
}

func TestHTTPHookExecutor_NilHook(t *testing.T) {
	executor := NewHTTPHookExecutor()
	result := executor.Execute(context.Background(), nil, `{}`)

	if result.OK {
		t.Fatal("expected not OK for nil hook")
	}
	if result.Error == "" {
		t.Fatal("expected error for nil hook")
	}
}

func TestHTTPHookExecutor_WrongType(t *testing.T) {
	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type:    HookCommandTypeCommand,
		Command: "echo hello",
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for wrong hook type")
	}
}

func TestHTTPHookExecutor_EmptyURL(t *testing.T) {
	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  "",
	}

	result := executor.Execute(context.Background(), hook, `{}`)

	if result.OK {
		t.Fatal("expected not OK for empty URL")
	}
}

func TestHTTPHookExecutor_ExecuteWithPayload(t *testing.T) {
	var receivedBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  server.URL,
	}

	payload := map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}

	result := executor.ExecuteWithPayload(context.Background(), hook, payload)
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}

	// Verify the payload was correctly serialized.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(receivedBody), &parsed); err != nil {
		t.Fatalf("parse received body: %v", err)
		return
	}
	if parsed["tool_name"] != "Bash" {
		t.Fatalf("expected tool_name=Bash, got %v", parsed["tool_name"])
	}
}

// ---------------------------------------------------------------------------
// Env Var Interpolation Tests
// ---------------------------------------------------------------------------

func TestInterpolateEnvVars(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc123")
	t.Setenv("OTHER_VAR", "xyz")

	allowed := map[string]bool{"MY_TOKEN": true}

	tests := []struct {
		input string
		want  string
	}{
		// Basic $VAR interpolation.
		{"Bearer $MY_TOKEN", "Bearer abc123"},
		// ${VAR} syntax.
		{"Bearer ${MY_TOKEN}", "Bearer abc123"},
		// Disallowed var → empty string.
		{"$OTHER_VAR", ""},
		// Mixed allowed and disallowed.
		{"$MY_TOKEN:$OTHER_VAR", "abc123:"},
		// No vars → unchanged.
		{"plain text", "plain text"},
		// Multiple occurrences.
		{"$MY_TOKEN/$MY_TOKEN", "abc123/abc123"},
	}

	for _, tt := range tests {
		got := interpolateEnvVars(tt.input, allowed)
		if got != tt.want {
			t.Errorf("interpolateEnvVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeHeaderValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal value", "normal value"},
		{"value\r\nX-Evil: 1", "valueX-Evil: 1"},
		{"value\x00null", "valuenull"},
		{"clean", "clean"},
	}

	for _, tt := range tests {
		got := sanitizeHeaderValue(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeHeaderValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsValidHookEvent Tests
// ---------------------------------------------------------------------------

func TestIsValidHookEvent(t *testing.T) {
	if !IsValidHookEvent("PreToolUse") {
		t.Fatal("expected PreToolUse to be valid")
	}
	if !IsValidHookEvent("Stop") {
		t.Fatal("expected Stop to be valid")
	}
	if !IsValidHookEvent("SessionStart") {
		t.Fatal("expected SessionStart to be valid")
	}
	if IsValidHookEvent("NotARealEvent") {
		t.Fatal("expected NotARealEvent to be invalid")
	}
	if IsValidHookEvent("") {
		t.Fatal("expected empty string to be invalid")
	}
}

// ---------------------------------------------------------------------------
// HookCommand.TimeoutDuration Tests
// ---------------------------------------------------------------------------

func TestHookCommand_TimeoutDuration(t *testing.T) {
	h := HookCommand{Timeout: 0}
	if h.TimeoutDuration() != DefaultShellHookTimeout {
		t.Fatalf("expected default timeout, got %v", h.TimeoutDuration())
	}

	h.Timeout = 30
	if h.TimeoutDuration() != 30*time.Second {
		t.Fatalf("expected 30s, got %v", h.TimeoutDuration())
	}
}
