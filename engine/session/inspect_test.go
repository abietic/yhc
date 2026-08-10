package session

import (
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

func TestInspectRecentAndFullUseSelectedSource(t *testing.T) {
	dir := t.TempDir()
	recorder := transcript.NewRecorder("selected", dir)
	if err := recorder.Record([]*schema.Message{
		{Role: schema.User, Content: "one"},
		{Role: schema.Assistant, Content: "two"},
		{Role: schema.User, Content: "three"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	info := SessionInfo{SessionID: "selected", TranscriptDir: dir, TranscriptPath: recorder.Path()}
	recent, err := InspectRecent(info, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Messages) != 2 || recent.Messages[0].Content != "two" || recent.Messages[1].Content != "three" {
		t.Fatalf("recent = %#v", recent.Messages)
	}
	full, err := InspectFull(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Messages) != 3 {
		t.Fatalf("full messages = %#v", full.Messages)
	}
}

func TestInspectRejectsMismatchedSourcePath(t *testing.T) {
	info := SessionInfo{SessionID: "wanted", TranscriptPath: filepath.Join(t.TempDir(), "other.jsonl")}
	if _, err := InspectFull(info); err == nil {
		t.Fatal("expected mismatched source path error")
	}
}
