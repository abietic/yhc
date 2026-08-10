package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/cloudwego/eino/schema"
)

func TestP302aPromptRecordSurvivesLifecycleRewriteWithoutInlineBytes(
	t *testing.T,
) {
	dir := t.TempDir()
	recorder := NewRecorder("rich", dir)
	record, message := testP302aPrompt(t, recorder.Path(), "turn-1")
	if err := recorder.RecordUserPrompt(record, message); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	assistant := &schema.Message{Role: schema.Assistant, Content: "answer"}
	if err := recorder.RecordMessages([]*schema.Message{assistant}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	assertP302aTranscriptHasNoInlineMedia(t, recorder.Path())

	fresh := NewRecorder("rich", dir)
	loaded, err := fresh.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if !loaded.HasMediaRefs ||
		len(loaded.MediaMessageIndexes) != 1 ||
		loaded.MediaMessageIndexes[0] != 0 ||
		len(loaded.AllPromptRecords) != 1 ||
		len(loaded.Messages) != 2 {
		t.Fatalf("loaded media projection = %#v", loaded)
	}
	assertP302aMessage(t, loaded.Messages[0])

	if err := fresh.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		loaded.Messages,
		nil,
		nil,
	); err != nil {
		t.Fatalf("RecordLifecycleBoundary: %v", err)
	}
	assertP302aTranscriptHasNoInlineMedia(t, fresh.Path())

	loaded, err = fresh.LoadFull()
	if err != nil {
		t.Fatalf("reload lifecycle: %v", err)
	}
	if len(loaded.PromptRecords) != 1 ||
		len(loaded.AllPromptRecords) != 2 {
		t.Fatalf("lifecycle reachability = %#v", loaded.AllPromptRecords)
	}
	assertP302aMessage(t, loaded.Messages[0])
	if err := fresh.Replace(loaded.Messages); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	assertP302aTranscriptHasNoInlineMedia(t, fresh.Path())
	reloaded, err := fresh.LoadFull()
	if err != nil {
		t.Fatalf("reload rewrite: %v", err)
	}
	if len(reloaded.Messages) != 2 || !reloaded.HasMediaRefs {
		t.Fatalf("rewritten projection = %#v", reloaded)
	}
	assertP302aMessage(t, reloaded.Messages[0])

	page, err := LoadMessagePage(MessagePageRequest{
		Path:  fresh.Path(),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("LoadMessagePage: %v", err)
	}
	if len(page.Entries) != 2 ||
		page.Entries[0].PromptRecord == nil ||
		page.Entries[0].Message == nil ||
		page.Entries[0].Message.Content != "beforeafter" {
		t.Fatalf("ref-only page = %#v", page)
	}
	descriptor, err := page.Entries[0].PromptRecord.Describe()
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(descriptor.Parts) != 3 ||
		descriptor.Parts[1].Image == nil ||
		descriptor.Parts[1].Image.Detail != "high" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	newest, err := LoadMessagePage(MessagePageRequest{
		Path:  fresh.Path(),
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("LoadMessagePage newest: %v", err)
	}
	if len(newest.Entries) != 1 ||
		newest.Entries[0].Message == nil ||
		newest.Entries[0].Message.Role != schema.Assistant ||
		!newest.HasMore {
		t.Fatalf("newest page = %#v", newest)
	}
	older, err := LoadMessagePage(MessagePageRequest{
		Path:         fresh.Path(),
		Limit:        1,
		SnapshotSize: newest.SnapshotSize,
		Boundary:     newest.Next,
		ExpectedFile: newest.FileInfo,
	})
	if err != nil {
		t.Fatalf("LoadMessagePage older: %v", err)
	}
	if len(older.Entries) != 1 ||
		older.Entries[0].PromptRecord == nil ||
		older.Entries[0].Identity.Key() ==
			newest.Entries[0].Identity.Key() ||
		older.HasMore {
		t.Fatalf("older page = %#v", older)
	}
	projection, err := fresh.LoadRefProjection()
	if err != nil {
		t.Fatalf("LoadRefProjection: %v", err)
	}
	if len(projection.PromptRecords) != 1 ||
		projection.PromptRecords[0].MessageIndex != 0 ||
		len(projection.Messages) != 2 ||
		projection.Messages[0].Content != "beforeafter" {
		t.Fatalf("ref projection = %#v", projection)
	}
	child, err := fresh.Branch("child", 1)
	if !errors.Is(err, ErrMediaBranchUnsupported) || child != nil {
		t.Fatalf("Branch = %#v, %v", child, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "child.jsonl")); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("branch rejection created child: %v", statErr)
	}
}

func TestP302aPromptRecordStrictFailuresAreRedacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "strict.jsonl")
	validID := strings.Repeat("a", 43)
	cases := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","kind":"user-prompt","user_prompt":{"version":2,"turn_id":"turn-1","parts":[{"kind":"image","image":{"ref":{"version":1,"media_id":"` + validID + `","mime_type":"image/png","size_bytes":1,"width":1,"height":1},"detail":"auto"}}]}}`,
		`{"timestamp":"2026-01-01T00:00:00Z","kind":"user-prompt","user_prompt":{"version":1,"turn_id":"turn-1","unknown":"private","parts":[{"kind":"image","image":{"ref":{"version":1,"media_id":"` + validID + `","mime_type":"image/png","size_bytes":1,"width":1,"height":1},"detail":"auto"}}]}}`,
		`{"timestamp":"2026-01-01T00:00:00Z","kind":"user-prompt","message":{"role":"user","content":"inline"},"user_prompt":{"version":1,"turn_id":"turn-1","parts":[{"kind":"image","image":{"ref":{"version":1,"media_id":"` + validID + `","mime_type":"image/png","size_bytes":1,"width":1,"height":1},"detail":"auto"}}]}}`,
		`{"timestamp":"2026-01-01T00:00:00Z","kind" : "user-prompt",`,
	}
	for index, content := range cases {
		if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewRecorder("strict", dir).LoadFull()
		if err == nil {
			t.Fatalf("case %d accepted", index)
		}
		if strings.Contains(err.Error(), validID) ||
			strings.Contains(err.Error(), "private") ||
			strings.Contains(err.Error(), path) {
			t.Fatalf("case %d leaked durable identity: %v", index, err)
		}
	}
}

func TestP302cMessagePageRejectsMalformedRichRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-rich.jsonl")
	if err := os.WriteFile(
		path,
		[]byte(
			"{\"kind\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"safe\"}}\n"+
				"{\"kind\":\"user-prompt\",\"user_prompt\":\n",
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	page, err := LoadMessagePage(MessagePageRequest{
		Path:  path,
		Limit: 10,
	})
	var promptErr *promptrecord.Error
	if !errors.As(err, &promptErr) || page != nil ||
		promptErr.Category != "malformed_record" {
		t.Fatalf("malformed rich page = %#v, %v", page, err)
	}
}

func TestP302aTranscriptAppendAndFlushFaultsNeverReachModelVisibility(
	t *testing.T,
) {
	for _, test := range []struct {
		name  string
		setup func(*Recorder)
	}{
		{
			name: "append",
			setup: func(recorder *Recorder) {
				recorder.beforeEncode = func(string) error {
					return errors.New("injected append failure")
				}
			},
		},
		{
			name: "flush",
			setup: func(recorder *Recorder) {
				recorder.syncFile = func(*os.File) error {
					return errors.New("injected flush failure")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := NewRecorder("fault", t.TempDir())
			record, message := testP302aPrompt(
				t,
				recorder.Path(),
				"turn-fault",
			)
			test.setup(recorder)
			appendErr := recorder.RecordUserPrompt(record, message)
			if appendErr == nil {
				appendErr = recorder.Flush()
			}
			if appendErr == nil {
				t.Fatal("faulted durable prompt succeeded")
			}
			if strings.Contains(appendErr.Error(), "turn-fault") {
				t.Fatalf("fault leaked prompt identity: %v", appendErr)
			}
			fresh := NewRecorder("fault", recorder.Dir)
			loaded, loadErr := fresh.LoadFull()
			if loadErr != nil {
				t.Fatalf("recover faulted transcript: %v", loadErr)
			}
			if len(loaded.Messages) > 0 {
				assertP302aMessage(t, loaded.Messages[0])
			}
		})
	}
}

func testP302aPrompt(
	t *testing.T,
	transcriptPath string,
	turnID string,
) (promptrecord.Record, *schema.Message) {
	t.Helper()
	data := testP302aPNG(t)
	store := mediastore.New(transcriptPath + ".media")
	ref, err := store.Put(context.Background(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    16,
		Height:   16,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  turnID,
		Parts: []promptrecord.Part{
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "before"},
			},
			{
				Kind: promptrecord.PartImage,
				Image: &promptrecord.ImagePart{
					Ref:    ref,
					Detail: "high",
				},
			},
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "after"},
			},
		},
	}
	message, err := record.Materialize(context.Background(), store)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	return record, message
}

func testP302aPNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			source.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 11),
				G: uint8(y * 13),
				B: uint8((x + y) * 7),
				A: 0xff,
			})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func assertP302aTranscriptHasNoInlineMedia(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"base64_data"`)) ||
		bytes.Contains(raw, []byte(`"digest"`)) {
		t.Fatalf("transcript contains private media: %s", raw)
	}
	var records int
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var envelope struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Kind == promptrecord.Kind {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("user-prompt records = %d", records)
	}
}

func assertP302aMessage(t *testing.T, message *schema.Message) {
	t.Helper()
	if message == nil ||
		message.Role != schema.User ||
		message.Content != "beforeafter" ||
		len(message.UserInputMultiContent) != 3 {
		t.Fatalf("materialized message = %#v", message)
	}
	imagePart := message.UserInputMultiContent[1]
	if imagePart.Image == nil ||
		imagePart.Image.Base64Data == nil ||
		imagePart.Image.MIMEType != "image/png" ||
		imagePart.Image.Detail != "high" {
		t.Fatalf("materialized image = %#v", imagePart)
	}
}
