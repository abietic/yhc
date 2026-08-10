package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestLoadRecentReturnsBoundedChronologicalMessages(t *testing.T) {
	recorder := NewRecorder("recent", t.TempDir())
	for index := 0; index < 8; index++ {
		role := schema.User
		if index%2 == 1 {
			role = schema.Assistant
		}
		if err := recorder.Record([]*schema.Message{{Role: role, Content: string(rune('a' + index))}}, role == schema.Assistant); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	result, err := LoadRecent(recorder.Path(), 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 3 || result.Messages[0].Content != "f" ||
		result.Messages[1].Content != "g" || result.Messages[2].Content != "h" {
		t.Fatalf("recent messages = %#v", result.Messages)
	}
	if result.BytesRead <= 0 || result.Truncated {
		t.Fatalf("recent result = %#v", result)
	}
}

func TestLoadRecentBoundsBytesAndSkipsPartialFirstLine(t *testing.T) {
	dir := t.TempDir()
	recorder := NewRecorder("large", dir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: strings.Repeat("x", 4096)}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record([]*schema.Message{{Role: schema.Assistant, Content: "tail"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	result, err := LoadRecent(recorder.Path(), 4, 512)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesRead != 512 || !result.Truncated {
		t.Fatalf("bounded result = %#v", result)
	}
	if len(result.Messages) != 1 || result.Messages[0].Content != "tail" {
		t.Fatalf("tail messages = %#v", result.Messages)
	}
}

func TestLoadRecentToleratesCorruptTailLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.jsonl")
	if err := os.WriteFile(path, []byte("{bad}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadRecent(path, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Corruptions != 1 || len(result.Messages) != 0 {
		t.Fatalf("corrupt result = %#v", result)
	}
}
