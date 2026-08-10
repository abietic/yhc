package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetPastedTextRefNumLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello", 0},
		{"line1\nline2", 1},
		{"line1\nline2\nline3", 2},
		{"line1\r\nline2\r\n", 2},
		{"a\rb\rc", 2},
	}
	for _, tt := range tests {
		got := GetPastedTextRefNumLines(tt.input)
		if got != tt.want {
			t.Errorf("GetPastedTextRefNumLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestFormatPastedTextRef(t *testing.T) {
	if got := FormatPastedTextRef(1, 0); got != "[Pasted text #1]" {
		t.Errorf("FormatPastedTextRef(1, 0) = %q", got)
	}
	if got := FormatPastedTextRef(2, 10); got != "[Pasted text #2 +10 lines]" {
		t.Errorf("FormatPastedTextRef(2, 10) = %q", got)
	}
}

func TestFormatImageRef(t *testing.T) {
	if got := FormatImageRef(3); got != "[Image #3]" {
		t.Errorf("FormatImageRef(3) = %q", got)
	}
}

func TestParseReferences(t *testing.T) {
	input := "Check [Pasted text #1 +5 lines] and [Image #2] please"
	refs := ParseReferences(input)
	if len(refs) != 2 {
		t.Fatalf("ParseReferences got %d refs, want 2", len(refs))
	}
	if refs[0].ID != 1 || refs[1].ID != 2 {
		t.Errorf("IDs = %d, %d; want 1, 2", refs[0].ID, refs[1].ID)
	}
	if !strings.Contains(refs[0].Match, "Pasted text") {
		t.Errorf("refs[0].Match = %q", refs[0].Match)
	}
}

func TestParseReferencesNoMatch(t *testing.T) {
	refs := ParseReferences("no references here")
	if len(refs) != 0 {
		t.Errorf("ParseReferences on empty = %d refs", len(refs))
	}
}

func TestExpandPastedTextRefs(t *testing.T) {
	input := "Start [Pasted text #1] end"
	contents := map[int]*PastedContent{
		1: {ID: 1, Type: "text", Content: "EXPANDED"},
	}
	got := ExpandPastedTextRefs(input, contents)
	want := "Start EXPANDED end"
	if got != want {
		t.Errorf("ExpandPastedTextRefs = %q, want %q", got, want)
	}
}

func TestExpandPastedTextRefsSkipsImages(t *testing.T) {
	input := "Start [Image #1] end"
	contents := map[int]*PastedContent{
		1: {ID: 1, Type: "image", Content: "data"},
	}
	got := ExpandPastedTextRefs(input, contents)
	if got != input {
		t.Errorf("ExpandPastedTextRefs should skip images, got %q", got)
	}
}

func TestManagerAddAndGetHistory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "session-1")
	t.Cleanup(func() { _ = m.Flush() })

	m.AddSimple("hello world")
	m.AddSimple("second command")

	entries, err := m.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) < 2 {
		t.Fatalf("got %d entries, want >= 2", len(entries))
	}
	if entries[0].Display != "second command" {
		t.Errorf("entries[0].Display = %q, want %q", entries[0].Display, "second command")
	}
}

func TestManagerSessionOrdering(t *testing.T) {
	dir := t.TempDir()

	m1 := NewManager(dir, "/project/test", "session-A")
	m1.AddSimple("from A")
	_ = m1.Flush()

	m2 := NewManager(dir, "/project/test", "session-B")
	m2.AddSimple("from B first")
	m2.AddSimple("from B second")

	entries, err := m2.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) < 2 {
		t.Fatalf("got %d entries, want >= 2", len(entries))
	}
	if entries[0].Display != "from B second" {
		t.Errorf("current session should be first, got %q", entries[0].Display)
	}
}

func TestManagerRemoveLast(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")
	t.Cleanup(func() { _ = m.Flush() })

	m.AddSimple("keep this")
	m.AddSimple("remove this")
	m.RemoveLast()

	entries, err := m.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries after RemoveLast, want 1", len(entries))
	}
	if entries[0].Display != "keep this" {
		t.Errorf("remaining entry = %q", entries[0].Display)
	}
}

func TestManagerClearPending(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	m.AddSimple("pending entry")
	m.ClearPending()

	entries, err := m.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries after ClearPending, want 0", len(entries))
	}
}

func TestManagerFlushAndRead(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	m.AddSimple("flushed entry")
	if err := m.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	m2 := NewManager(dir, "/project/test", "sess")
	entries, err := m2.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries from disk, want 1", len(entries))
	}
	if entries[0].Display != "flushed entry" {
		t.Errorf("disk entry = %q", entries[0].Display)
	}
}

func TestManagerProjectScoping(t *testing.T) {
	dir := t.TempDir()

	m1 := NewManager(dir, "/project/A", "sess")
	m1.AddSimple("project A")
	_ = m1.Flush()

	m2 := NewManager(dir, "/project/B", "sess")
	m2.AddSimple("project B")
	_ = m2.Flush()
	entries, err := m2.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	for _, e := range entries {
		if e.Display == "project A" {
			t.Error("should not see project A entries from project B manager")
		}
	}
}

func TestManagerGetDisplayHistory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")
	t.Cleanup(func() { _ = m.Flush() })

	m.AddSimple("first")
	m.AddSimple("second")
	m.AddSimple("first") // duplicate
	_ = m.Flush()

	display := m.GetDisplayHistory()
	if len(display) != 2 {
		t.Fatalf("got %d display entries (should dedup), want 2", len(display))
	}
}

func TestManagerMaxHistoryItems(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")
	t.Cleanup(func() { _ = m.Flush() })

	for i := 0; i < MaxHistoryItems+20; i++ {
		m.AddSimple(strings.Repeat("x", i+1))
	}
	_ = m.Flush()

	entries, err := m.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) > MaxHistoryItems {
		t.Errorf("got %d entries, max should be %d", len(entries), MaxHistoryItems)
	}
}

func TestManagerPasteStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	largeContent := strings.Repeat("x", MaxPastedContentLength+100)
	m.Add(HistoryEntry{
		Display: "with paste [Pasted text #1]",
		PastedContents: map[int]*PastedContent{
			1: {ID: 1, Type: "text", Content: largeContent},
		},
	})

	if err := m.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	hash := hashPastedText(largeContent)
	pastePath := filepath.Join(dir, pasteStoreDir, hash)
	if _, err := os.Stat(pastePath); err != nil {
		t.Fatalf("paste store write missing after flush: %v", err)
	}

	m2 := NewManager(dir, "/project/test", "sess")
	entries, err := m2.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(entries) == 0 {
		t.Fatal("no entries after reload")
	}
	if pc, ok := entries[0].PastedContents[1]; !ok || pc.Content != largeContent {
		t.Error("paste store content not resolved correctly")
	}
}

func TestManagerSmallPasteInline(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	smallContent := "small paste"
	m.Add(HistoryEntry{
		Display: "with small paste",
		PastedContents: map[int]*PastedContent{
			1: {ID: 1, Type: "text", Content: smallContent},
		},
	})
	_ = m.Flush()

	m2 := NewManager(dir, "/project/test", "sess")
	entries, _ := m2.GetHistory()
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	if pc, ok := entries[0].PastedContents[1]; !ok || pc.Content != smallContent {
		t.Error("inline paste content not preserved")
	}
}

func TestManagerImagePasteFiltered(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	m.Add(HistoryEntry{
		Display: "with image",
		PastedContents: map[int]*PastedContent{
			1: {ID: 1, Type: "image", Content: "base64data"},
		},
	})
	_ = m.Flush()

	m2 := NewManager(dir, "/project/test", "sess")
	entries, _ := m2.GetHistory()
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	if _, ok := entries[0].PastedContents[1]; ok {
		t.Error("image paste should be filtered out of stored history")
	}
}

func TestRemoveLastAfterFlush(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "/project/test", "sess")

	m.AddSimple("will be removed")
	_ = m.Flush()
	m.RemoveLast()

	entries, err := m.GetHistory()
	if err != nil {
		t.Fatal(err)
		return
	}
	for _, e := range entries {
		if e.Display == "will be removed" {
			t.Error("entry should be skipped after RemoveLast (post-flush skip set)")
		}
	}
}

func TestFileLocking(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	unlock, err := acquireFileLock(lockPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
		return
	}
	defer unlock()

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file should exist")
	}

	unlock()

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after unlock")
	}
}

func TestStaleLockRecovery(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "stale.lock")

	_ = os.WriteFile(lockPath, []byte("old-pid"), 0o600)
	past := time.Now().Add(-20 * time.Second)
	_ = os.Chtimes(lockPath, past, past)

	unlock, err := acquireFileLock(lockPath, 10*time.Second)
	if err != nil {
		t.Fatal("should recover stale lock:", err)
		return
	}
	unlock()
}
