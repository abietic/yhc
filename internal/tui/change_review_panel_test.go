package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
)

func TestChangeReviewHunksAndSelectedContext(t *testing.T) {
	record := changeReviewRecord("r1", "turn-1", "alpha\nkeep\nold\n1\n2\n3\n4\n5\n6\n7\n8\nkeep\nomega\n", "alpha\nkeep\nnew\n1\n2\n3\n4\n5\n6\n7\n8\nkeep\nOMEGA\n")
	hunks, err := changeReviewHunks(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want two separated hunks: %q", len(hunks), hunks)
	}
	context, err := changeReviewContext(record, ChangeReviewSelection{RevisionID: record.ID, HunkIndexes: []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, "-old") || strings.Contains(context, "-omega") || !strings.Contains(context, "--- a/\"note.txt\"") {
		t.Fatalf("selected context did not independently retain only first hunk: %q", context)
	}
}

func TestChangeReviewRowsAndActions(t *testing.T) {
	complete := changeReviewRecord("complete", "turn-done", "a\n", "b\n")
	pending := changeReviewRecord("pending", "turn-pending", "a\n", "b\n")
	pending.Outcome = ""
	failed := changeReviewRecord("failed", "turn-failed", "a\n", "b\n")
	failed.Outcome = "failed"
	snapshot := engine.WorkspaceChangeSnapshot{
		Turns:   []filehistory.TurnRecord{{TurnID: "turn-done", TerminalReason: "succeeded", FinishedAt: time.Now().UTC()}, {TurnID: "turn-pending"}, {TurnID: "turn-failed", TerminalReason: "failed", FinishedAt: time.Now().UTC()}},
		Records: []filehistory.RevisionHeader{{ID: complete.ID, TurnID: complete.TurnID, ToolName: "Write", Path: complete.Path, Outcome: complete.Outcome, BeforeCaptured: true, AfterCaptured: true}, {ID: "known-gap", TurnID: complete.TurnID, Path: "gap.txt"}, {ID: pending.ID, TurnID: pending.TurnID, ToolName: "Edit", Path: pending.Path, Outcome: pending.Outcome, BeforeCaptured: true, AfterCaptured: true}, {ID: failed.ID, TurnID: failed.TurnID, ToolName: "Edit", Path: failed.Path, Outcome: failed.Outcome, BeforeCaptured: true, AfterCaptured: true}, {ID: "gap", TurnID: "gone", Path: "orphan.txt"}},
	}
	loaded := map[string]filehistory.RevisionRecord{complete.ID: complete, pending.ID: pending, failed.ID: failed}
	p := NewChangeReviewPanel(defaultStyles())
	p.Show(snapshot, func(id string) (filehistory.RevisionRecord, bool) { r, ok := loaded[id]; return r, ok })
	plain := stripANSIForTest(p.Overlay("", 80, 24))
	for _, want := range []string{"turn-failed", "[failed]", "pending", "capture gap", "unknown turn:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("list missing %q: %s", want, plain)
		}
	}
	// newest failed record opens despite its terminal failure; it still has captured changes.
	p.selected = 1
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.preview == nil {
		t.Fatal("terminal failed capture did not open")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace})
	action, selection := p.HandleKey(tea.KeyPressMsg{Code: 'a'})
	if action != ChangeReviewStage || selection.RevisionID != failed.ID || len(selection.HunkIndexes) != 1 || selection.WholeFile {
		t.Fatalf("stage intent = %#v %#v", action, selection)
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	action, _ = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if action != ChangeReviewClose || p.IsVisible() {
		t.Fatalf("close action=%v visible=%v", action, p.IsVisible())
	}
}

func TestChangeReviewLiteralPreservesCapturedBytes(t *testing.T) {
	record := changeReviewRecord("crlf", "turn", "first\r\nlast", "first\r\nLAST")
	context, err := changeReviewContext(record, ChangeReviewSelection{WholeFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, "first\r\nlast") || !strings.Contains(context, "first\r\nLAST") {
		t.Fatalf("literal context lost CRLF/no-final-newline: %q", context)
	}
}

func TestChangeReviewOverlayKeepsFooterAtSmallSize(t *testing.T) {
	p := NewChangeReviewPanel(defaultStyles())
	p.Show(engine.WorkspaceChangeSnapshot{Warning: strings.Repeat("warning ", 20)}, nil)
	p.SetFeedback(strings.Repeat("feedback ", 20))
	var golden strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		plain := stripANSIForTest(p.Overlay("", width, 12))
		if !strings.Contains(plain, "Esc close") {
			t.Fatalf("width %d clipped footer: %q", width, plain)
		}
		fmt.Fprintf(&golden, "== %d ==\n%s\n", width, changeReviewGoldenNormalize(plain))
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/change_review_panel.golden", []byte(strings.TrimSpace(golden.String())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/change_review_panel.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := strings.TrimSpace(golden.String()); got != strings.TrimSpace(string(want)) {
		t.Fatalf("change review golden mismatch:\n%s", got)
	}
}

func TestChangeReviewVisibleNavigationPreviewFallbackAndRefresh(t *testing.T) {
	var records []filehistory.RevisionHeader
	var turns []filehistory.TurnRecord
	for i := 0; i < 31; i++ {
		id := fmt.Sprintf("t%02d", i)
		turns = append(turns, filehistory.TurnRecord{TurnID: id})
		records = append(records, filehistory.RevisionHeader{ID: id, TurnID: id, ToolName: "Edit", Path: "x", Outcome: "succeeded", BeforeCaptured: true, AfterCaptured: true})
	}
	p := NewChangeReviewPanel(defaultStyles())
	p.Show(engine.WorkspaceChangeSnapshot{Turns: turns, Records: records}, func(id string) (filehistory.RevisionRecord, bool) {
		return changeReviewRecord(id, id, strings.Repeat("a\n", 20001), strings.Repeat("b\n", 20001)), true
	})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if p.selected == 0 || !strings.Contains(stripANSIForTest(p.Overlay("", 40, 12)), "t00") && !strings.Contains(stripANSIForTest(p.Overlay("", 40, 12)), "t30") {
		t.Fatal("end selection was not rendered in bounded viewport")
	}
	p.selected = 0
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.selected == 0 {
		t.Fatal("collapsed turn did not skip hidden children")
	}
	p.selected = 3
	p.openSelected()
	if !p.fallback {
		t.Fatal("over-limit capture did not enter whole-file fallback")
	}
	action, selection := p.HandleKey(tea.KeyPressMsg{Code: 'a'})
	if action != ChangeReviewStage || !selection.WholeFile {
		t.Fatalf("fallback stage=%v %#v", action, selection)
	}
	short := changeReviewRecord("long", "t", "a\n", strings.Repeat("line\n", 80))
	p.preview = &short
	p.hunks = []string{"@@\n" + strings.Repeat("+body\n", 80)}
	p.fallback = false
	p.lineOffset = 0
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.lineOffset == 0 {
		t.Fatal("long hunk did not scroll body")
	}
	p.marked[0] = true
	p.Refresh(engine.WorkspaceChangeSnapshot{Records: []filehistory.RevisionHeader{{ID: "long"}}})
	if p.marked[0] != true {
		t.Fatal("refresh lost current record marks")
	}
}

func TestChangeReviewDisplayAndWholeFileMetadata(t *testing.T) {
	unsafe := changeReviewDisplay("ok\x1b]8;;x\x07\u202Ebad")
	if strings.Contains(unsafe, "\x1b") || strings.Contains(unsafe, "\u202e") {
		t.Fatalf("unsafe display remained active: %q", unsafe)
	}
	record := changeReviewRecord("mode", "t", "old\r\n", "new\r\n")
	record.Before.Mode = 0o644
	record.After.Mode = 0o755
	context, err := changeReviewContext(record, ChangeReviewSelection{WholeFile: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exists=true mode=0644", "exists=true mode=0755", "old\r\n", "new\r\n"} {
		if !strings.Contains(context, want) {
			t.Fatalf("context missing %q: %q", want, context)
		}
	}
}

func changeReviewGoldenNormalize(value string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func changeReviewRecord(id, turn, before, after string) filehistory.RevisionRecord {
	return filehistory.RevisionRecord{ID: id, TurnID: turn, ToolName: "Edit", Path: "note.txt", Outcome: "succeeded", Before: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o644, Data: []byte(before)}, After: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o644, Data: []byte(after)}}
}

func TestChangeReviewMetadataCannotForgeRows(t *testing.T) {
	record := changeReviewRecord("escape", "turn", "before\n", "after\n")
	record.Path = "one\ntwo\x1b[2J"
	p := NewChangeReviewPanel(defaultStyles())
	p.Show(engine.WorkspaceChangeSnapshot{Records: []filehistory.RevisionHeader{{ID: record.ID, Path: record.Path, ToolName: "Edit", Outcome: "succeeded", BeforeCaptured: true, AfterCaptured: true}}}, func(string) (filehistory.RevisionRecord, bool) { return record, true })
	lines := p.renderList(12)
	if len(lines) != 1 || strings.ContainsAny(lines[0], "\n\r\x1b") || !strings.Contains(lines[0], `one\ntwo\x1b`) {
		t.Fatalf("unsafe metadata: %q", lines)
	}
}

func TestChangeReviewRefreshPreservesFocusCollapseAndMarkedHeader(t *testing.T) {
	record := changeReviewRecord("r", "old", "before\n", strings.Repeat("after\n", 80))
	snapshot := engine.WorkspaceChangeSnapshot{SessionID: "s", ThreadID: "root", OwnerGeneration: 4, Turns: []filehistory.TurnRecord{{TurnID: "old"}}, Records: []filehistory.RevisionHeader{{ID: "r", TurnID: "old", Outcome: "succeeded", BeforeCaptured: true, AfterCaptured: true}}}
	p := NewChangeReviewPanel(defaultStyles())
	p.Show(snapshot, func(string) (filehistory.RevisionRecord, bool) { return record, true })
	p.selected = 1
	p.openSelected()
	_ = p.Overlay("", 80, 12)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace})
	plain := stripANSIForTest(p.Overlay("", 80, 12))
	if !strings.Contains(plain, "[x] hunk 1/1") || !strings.Contains(plain, "a add") {
		t.Fatalf("cached preview hides current selection or footer: %s", plain)
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	plain = stripANSIForTest(p.Overlay("", 80, 12))
	if !strings.Contains(plain, "[x] hunk 1/1") || !strings.Contains(plain, "a add") {
		t.Fatal("long hunk scroll lost fixed controls")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	p.selected = 0
	p.openSelected()
	snapshot.Turns = append(snapshot.Turns, filehistory.TurnRecord{TurnID: "new"})
	p.Refresh(snapshot)
	if p.rows[p.selected].turn.TurnID != "old" || !p.rows[p.selected].collapsed {
		t.Fatal("refresh moved focus or expanded previously collapsed turn")
	}
}

func TestChangeReviewPopulatedGolden(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 34, 56, 0, time.UTC)
	record := changeReviewRecord("r1", "turn-review", "alpha\nold\nlast", "alpha\nnew\nLAST")
	record.Timestamp = at
	snapshot := engine.WorkspaceChangeSnapshot{SessionID: "session", ThreadID: "root", Turns: []filehistory.TurnRecord{{TurnID: record.TurnID, StartedAt: at, FinishedAt: at, TerminalReason: "completed"}}, Records: []filehistory.RevisionHeader{{ID: record.ID, TurnID: record.TurnID, ToolName: "Edit", Path: record.Path, Outcome: "succeeded", Timestamp: at, BeforeCaptured: true, AfterCaptured: true}, {ID: "gap", TurnID: record.TurnID, ToolName: "Bash", Reason: "Non-file tool effects are not captured.", Timestamp: at}}}
	var golden strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		p := NewChangeReviewPanel(defaultStyles())
		p.Show(snapshot, func(string) (filehistory.RevisionRecord, bool) { return record, true })
		p.selected = 2
		fmt.Fprintf(&golden, "== list %d ==\n%s\n", width, changeReviewGoldenNormalize(stripANSIForTest(p.Overlay("", width, 12))))
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace})
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
		got := stripANSIForTest(p.Overlay("", width, 12))
		for _, want := range []string{"[x] hunk 1/1", "a add", "Esc list", "+new"} {
			if !strings.Contains(got, want) {
				t.Fatalf("width=%d missing %q: %s", width, want, got)
			}
		}
		fmt.Fprintf(&golden, "== preview %d ==\n%s\n", width, changeReviewGoldenNormalize(got))
	}
	actual := strings.TrimSpace(golden.String()) + "\n"
	path := "testdata/change_review_populated.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != actual {
		t.Fatalf("populated golden mismatch:\n%s", actual)
	}
}

func TestChangeReviewPreviewBudgetDoesNotClipAttachedSource(t *testing.T) {
	raw := strings.Repeat("x", changeReviewMaxPreviewBytes) + "CAPTURED_END"
	preview := changeReviewPreviewText(raw)
	if !strings.Contains(preview, "Preview clipped") || strings.Contains(preview, "CAPTURED_END") || len(preview) > changeReviewMaxPreviewBytes+100 {
		t.Fatal("preview work is not independently bounded")
	}
	record := changeReviewRecord("bounded", "t", "old", raw)
	context, err := changeReviewContext(record, ChangeReviewSelection{RevisionID: "bounded", TurnID: "t", HunkIndexes: []int{0}})
	if err != nil || !strings.Contains(context, "CAPTURED_END") {
		t.Fatalf("preview budget discarded captured review bytes: %v", err)
	}
}
