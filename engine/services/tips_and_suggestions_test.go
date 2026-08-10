package services

import (
	"path/filepath"
	"testing"
)

func TestPersistentTipHistoryRotatesAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tip-history.json")
	seen := make(map[string]bool)
	var first string
	for session := 0; session < 6; session++ {
		history, err := NewPersistentTipHistory(path)
		if err != nil {
			t.Fatal(err)
		}
		tip := NewTipScheduler(NewTipRegistry(), history).NextTip()
		if tip == nil {
			t.Fatal("expected a tip")
		}
		if session == 0 {
			first = tip.ID
		}
		if session < 5 && seen[tip.ID] {
			t.Fatalf("tip %q repeated before every tip had been shown", tip.ID)
		}
		seen[tip.ID] = true
		history.MarkShown(tip.ID)
		if session == 5 && tip.ID != first {
			t.Fatalf("rotation after restart = %q, want oldest %q", tip.ID, first)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("rotated through %d tips, want 5", len(seen))
	}
}
