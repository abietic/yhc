package session

import (
	"os"
	"strings"
	"testing"
)

func TestSessionViewStateRoundTripBoundsAndExcludesRuntimePayloads(t *testing.T) {
	dir := t.TempDir()
	state := PersistedSessionViewState{
		ActiveThreadID: "agent",
		Threads: []PersistedThreadViewState{
			{ThreadID: "leader", Draft: strings.Repeat("x", maxPersistedDraftRunes+10), Follow: true},
			{ThreadID: "agent", Draft: "review this", Mode: "replay_only", ScrollItem: 4, DetailTab: 2},
			{ThreadID: "agent", Draft: "duplicate"},
		},
	}
	if err := SaveSessionViewState(dir, "session", state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSessionViewState(dir, "session")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveThreadID != "agent" || len(loaded.Threads) != 2 {
		t.Fatalf("loaded state = %#v", loaded)
	}
	if len([]rune(loaded.Threads[0].Draft)) != maxPersistedDraftRunes {
		t.Fatalf("draft runes = %d", len([]rune(loaded.Threads[0].Draft)))
	}
	data, err := os.ReadFile(SessionViewStatePath(dir, "session"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"queue", "image", "permission", "undo", "payload"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("view state persisted forbidden field %q: %s", forbidden, data)
		}
	}
}

func TestSessionViewStateRejectsWrongIdentityAndCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionViewState(dir, "one", PersistedSessionViewState{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SessionViewStatePath(dir, "one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SessionViewStatePath(dir, "two"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSessionViewState(dir, "two"); err == nil {
		t.Fatal("expected identity mismatch")
	}
	if err := os.WriteFile(SessionViewStatePath(dir, "bad"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSessionViewState(dir, "bad"); err == nil {
		t.Fatal("expected corruption error")
	}
}
