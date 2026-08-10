package keybindings

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNormalizeKeyPatternAndChord(t *testing.T) {
	got, err := NormalizeKeyPattern("Control+Shift+K  meta+Right")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ctrl+shift+k alt+right" {
		t.Fatalf("normalized chord = %q", got)
	}
	if _, err := NormalizeKeyPattern("ctrl++k"); err == nil {
		t.Fatal("invalid empty key segment accepted")
	}
}

func TestDefaultBindingsOnlyAdvertiseSupportedActions(t *testing.T) {
	for _, block := range DefaultBindings() {
		for key, action := range block.Bindings {
			if !SupportsAction(block.Context, action) {
				t.Fatalf("default %s %q advertises unsupported action %q", block.Context, key, action)
			}
			if _, err := ParseChord(key); err != nil {
				t.Fatalf("default %s %q is invalid: %v", block.Context, key, err)
			}
		}
	}
}

func TestResolverContextPriorityAndGlobalFallback(t *testing.T) {
	resolver := NewResolver()
	resolver.SetBindings([]Block{
		{Context: ContextGlobal, Bindings: map[string]Action{"ctrl+t": ActionAppToggleTodos}},
		{Context: ContextChat, Bindings: map[string]Action{"ctrl+t": ActionChatNextAgent}},
	})

	action, ok := resolver.Resolve(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, ContextChat)
	if !ok || action != ActionChatNextAgent {
		t.Fatalf("specific action = %q ok=%v", action, ok)
	}
	action, ok = resolver.Resolve(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, ContextTranscript)
	if !ok || action != ActionAppToggleTodos {
		t.Fatalf("global fallback = %q ok=%v", action, ok)
	}
}

func TestResolverChordLifecycle(t *testing.T) {
	resolver := NewResolver()
	resolver.SetBindings([]Block{{
		Context: ContextChat,
		Bindings: map[string]Action{
			"ctrl+x ctrl+t": ActionChatNextAgent,
		},
	}})

	first := resolver.ResolveEvent(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, ContextChat)
	if first.Kind != ResolutionChordStarted || first.Pending != "ctrl+x" {
		t.Fatalf("first chord result = %#v", first)
	}
	second := resolver.ResolveEvent(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, ContextChat)
	if second.Kind != ResolutionMatch || second.Action != ActionChatNextAgent {
		t.Fatalf("second chord result = %#v", second)
	}

	resolver.ResolveEvent(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, ContextChat)
	cancel := resolver.ResolveEvent(tea.KeyPressMsg{Code: tea.KeyEscape}, ContextChat)
	if cancel.Kind != ResolutionChordCancelled {
		t.Fatalf("escape chord result = %#v", cancel)
	}
}

func TestResolverResetPendingPreventsEditorChordLeak(t *testing.T) {
	resolver := NewResolver()
	resolver.SetBindings([]Block{{
		Context: ContextChat,
		Bindings: map[string]Action{
			"ctrl+x ctrl+s": ActionChatSubmit,
		},
	}})
	started := resolver.ResolveEvent(
		tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl},
		ContextChat,
	)
	if started.Kind != ResolutionChordStarted {
		t.Fatalf("chord start = %#v", started)
	}

	resolver.ResetPending()
	got := resolver.ResolveEvent(
		tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl},
		ContextChat,
	)
	if got.Kind != ResolutionNone {
		t.Fatalf("post-reset chord resolution = %#v", got)
	}
}

func TestResolverSetBindingsDefensiveCopy(t *testing.T) {
	blocks := []Block{{Context: ContextChat, Bindings: map[string]Action{"alt+up": ActionChatNextAgent}}}
	resolver := NewResolver()
	resolver.SetBindings(blocks)
	blocks[0].Bindings["alt+up"] = ActionChatPreviousAgent

	action, ok := resolver.Resolve(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}, ContextChat)
	if !ok || action != ActionChatNextAgent {
		t.Fatalf("resolver changed through caller map: %q ok=%v", action, ok)
	}
}

func TestValidateBindingsRejectsReservedConflictAndUnreachableAction(t *testing.T) {
	issues := ValidateBindings([]Block{
		{Context: ContextChat, Bindings: map[string]Action{
			"ctrl+c": ActionChatSubmit,
			"alt+p":  ActionChatSubmit,
		}},
		{Context: ContextChat, Bindings: map[string]Action{
			"meta+p": ActionChatNextAgent,
			"ctrl+g": ActionChatStash,
		}},
	})
	if !HasValidationErrors(issues) {
		t.Fatalf("expected validation errors: %#v", issues)
	}
	want := map[string]bool{"reserved": false, "conflict": false, "invalid_action": false}
	for _, issue := range issues {
		if _, ok := want[issue.Type]; ok {
			want[issue.Type] = true
		}
	}
	for issueType, found := range want {
		if !found {
			t.Fatalf("missing %s issue: %#v", issueType, issues)
		}
	}
}

func TestLoadUserBindingsAppliesValidConfigAndRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	valid := `{"bindings":[{"context":"Chat","bindings":{"alt+right":null,"alt+up":"chat:nextAgent"}}]}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver()
	issues, err := resolver.LoadUserBindings(dir)
	if err != nil || len(issues) != 0 {
		t.Fatalf("load valid config err=%v issues=%#v", err, issues)
	}
	if _, ok := resolver.Resolve(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt}, ContextChat); ok {
		t.Fatal("null unbind left the default active")
	}
	action, ok := resolver.Resolve(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}, ContextChat)
	if !ok || action != ActionChatNextAgent {
		t.Fatalf("custom action = %q ok=%v", action, ok)
	}

	invalid := `{"bindings":[{"context":"Chat","bindings":{"ctrl+c":"chat:submit"}}]}`
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err = resolver.LoadUserBindings(dir)
	if err != nil || !HasValidationErrors(issues) {
		t.Fatalf("invalid load err=%v issues=%#v", err, issues)
	}
	if action, ok = resolver.Resolve(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}, ContextChat); !ok || action != ActionChatNextAgent {
		t.Fatalf("invalid config replaced last valid bindings: %q ok=%v", action, ok)
	}
}

func TestLoadUserBindingsRejectsMissingOrNullBindingsArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	for _, content := range []string{`{}`, `{"bindings":null}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		resolver := NewResolver()
		if _, err := resolver.LoadUserBindings(dir); err == nil {
			t.Fatalf("accepted invalid config %s", content)
		}
	}
}
