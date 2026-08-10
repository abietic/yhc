package transcript

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
)

func TestP303PromptRecordBindingsRequireExactMessageObjects(t *testing.T) {
	recorder := NewRecorder("p303-bindings", t.TempDir())
	message := &schema.Message{Role: schema.User, Content: "rich"}
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  "turn-p303",
		Parts: []promptrecord.Part{{
			Kind: promptrecord.PartImage,
			Image: &promptrecord.ImagePart{
				Ref: mediastore.Ref{
					Version:   mediastore.RefVersion,
					MediaID:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
					MIMEType:  "image/png",
					SizeBytes: 1,
					Width:     1,
					Height:    1,
				},
				Detail: "auto",
			},
		}},
	}
	if err := recorder.RecordUserPrompt(record, message); err != nil {
		t.Fatal(err)
	}
	sameContent := *message
	bindings := recorder.PromptRecordBindings(
		[]*schema.Message{&sameContent, message},
	)
	if len(bindings) != 1 ||
		bindings[0].MessageIndex != 1 ||
		bindings[0].Record.TurnID != "turn-p303" {
		t.Fatalf("bindings = %#v", bindings)
	}
	bindings[0].Record.TurnID = "mutated"
	again := recorder.PromptRecordBindings([]*schema.Message{message})
	if len(again) != 1 || again[0].Record.TurnID != "turn-p303" {
		t.Fatalf("recorder record was aliased: %#v", again)
	}
}

func TestP303ResumedPromptRetainsExactRecordBinding(t *testing.T) {
	dir := t.TempDir()
	initial := NewRecorder("p303-resume", dir)
	record, message := testP302aPrompt(
		t,
		initial.Path(),
		"turn-p303-resumed",
	)
	if err := initial.RecordUserPrompt(record, message); err != nil {
		t.Fatal(err)
	}
	if err := initial.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	resumed := NewRecorder("p303-resume", dir)
	loaded, err := resumed.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	bindings := resumed.PromptRecordBindings(loaded.Messages)
	if len(bindings) != 1 ||
		bindings[0].MessageIndex != 0 ||
		bindings[0].Record.TurnID != "turn-p303-resumed" {
		t.Fatalf("resumed bindings = %#v", bindings)
	}
}
