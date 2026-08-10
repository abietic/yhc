package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/transcript"
)

func TestP302aDurablePromptRestartsWithoutInlineMedia(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302a-restart"
	firstModel := &captureInputModel{}
	largePNG := largeP302aPNG(t)
	largeBase64 := base64.StdEncoding.EncodeToString(largePNG)
	first := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		firstModel,
	)

	events, _ := first.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("alpha"),
			NewPromptImagePart(
				largeBase64,
				"image/png",
				PromptImageDetailLow,
			),
			NewPromptTextPart("beta"),
		),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	first.Close()

	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(largeBase64)) ||
		bytes.Contains(raw, []byte(`"digest"`)) ||
		bytes.Contains(raw, []byte(`"base64_data"`)) {
		t.Fatal("transcript contains private media bytes or digest")
	}
	if count := bytes.Count(raw, []byte(`"kind":"user-prompt"`)); count != 1 {
		t.Fatalf("user-prompt records = %d, want 1\n%s", count, raw)
	}
	if len(raw) > 64*1024 {
		t.Fatalf("bounded transcript bytes = %d", len(raw))
	}
	if _, err := os.Stat(path + ".media/manifest.json"); err != nil {
		t.Fatalf("media manifest: %v", err)
	}

	restartedModel := &captureInputModel{}
	restarted := newP302aTestEngine(
		t,
		"p302a-host",
		transcriptDir,
		cwd,
		restartedModel,
	)
	if _, err := restarted.ResumeSession(
		context.Background(),
		sessionID,
	); err != nil {
		t.Fatalf("resume ref-backed session: %v", err)
	}
	if err := restarted.recordTranscriptBoundary(
		transcript.LifecycleCheckpoint,
		restarted.GetMessages(),
	); err != nil {
		t.Fatalf("checkpoint resumed rich prompt: %v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(largeBase64)) ||
		bytes.Contains(raw, []byte(`"base64_data"`)) {
		t.Fatal("resumed lifecycle checkpoint re-inlined media")
	}
	events, _ = restarted.SubmitMessage(context.Background(), "follow up")
	terminal, _ = collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("follow-up terminal = %#v", terminal)
	}
	if len(restartedModel.inputs) != 1 {
		t.Fatalf("restart model calls = %d", len(restartedModel.inputs))
	}
	restored := findPromptInputUserMessage(
		restartedModel.inputs[0],
		"alphabeta",
	)
	if restored == nil || len(restored.UserInputMultiContent) != 3 {
		t.Fatalf("restored prompt = %#v", restartedModel.inputs[0])
	}
	assertPromptTextPart(t, restored.UserInputMultiContent[0], "alpha")
	imagePart := restored.UserInputMultiContent[1]
	if imagePart.Image == nil ||
		imagePart.Image.Base64Data == nil ||
		*imagePart.Image.Base64Data != largeBase64 ||
		imagePart.Image.MIMEType != "image/png" ||
		imagePart.Image.Detail != "low" {
		t.Fatalf("restored image part = %#v", imagePart)
	}
	assertPromptTextPart(t, restored.UserInputMultiContent[2], "beta")
}

func largeP302aPNG(t *testing.T) []byte {
	t.Helper()
	const side = 1300
	source := image.NewNRGBA(image.Rect(0, 0, side, side))
	state := uint32(0x9e3779b9)
	for index := 0; index < len(source.Pix); index += 4 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		source.Pix[index] = byte(state)
		source.Pix[index+1] = byte(state >> 8)
		source.Pix[index+2] = byte(state >> 16)
		source.Pix[index+3] = 0xff
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatalf("encode large PNG: %v", err)
	}
	data := encoded.Bytes()
	if len(data) < 4*1024*1024 || len(data) > maxUserImageBytes {
		t.Fatalf("large PNG bytes = %d", len(data))
	}
	return data
}

func TestP302aCorruptMediaFailsResumeBeforeModelCall(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302a-corrupt"
	first := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
	)
	events, _ := first.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
		),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	first.Close()
	root := filepath.Join(transcriptDir, sessionID+".jsonl.media")
	var blobPath string
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() &&
			entry.Name() != "manifest.json" &&
			!strings.HasPrefix(entry.Name(), ".") {
			blobPath = path
		}
		return nil
	})
	if err != nil || blobPath == "" {
		t.Fatalf("find blob: path=%q err=%v", blobPath, err)
	}
	info, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		blobPath,
		bytes.Repeat([]byte{0x7f}, int(info.Size())),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	model := &captureInputModel{}
	restarted := newP302aTestEngine(
		t,
		"p302a-corrupt-host",
		transcriptDir,
		cwd,
		model,
	)
	if _, err := restarted.ResumeSession(
		context.Background(),
		sessionID,
	); err == nil {
		t.Fatal("resume accepted corrupt media")
	}
	if len(model.inputs) != 0 || len(restarted.GetMessages()) != 0 {
		t.Fatalf(
			"corrupt resume mutated runtime: calls=%d messages=%#v",
			len(model.inputs),
			restarted.GetMessages(),
		)
	}
}

func TestP302aPersistenceFailurePreventsModelCallAndPromptRecord(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302a-store-failure"
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	outside := t.TempDir()
	if err := os.Symlink(outside, path+".media"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	model := &captureInputModel{}
	engine := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		model,
	)
	events, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("private"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
		),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalPersistenceError || len(model.inputs) != 0 {
		t.Fatalf("terminal=%#v model calls=%d", terminal, len(model.inputs))
	}
	if strings.Contains(terminal.Err.Error(), path) ||
		strings.Contains(terminal.Err.Error(), outside) ||
		strings.Contains(terminal.Err.Error(), testUserImagePNGBase64) {
		t.Fatalf("persistence error leaked private data: %v", terminal.Err)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"kind":"user-prompt"`)) ||
		bytes.Contains(raw, []byte(testUserImagePNGBase64)) {
		t.Fatalf("failed persistence published prompt: %s", raw)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsafe store wrote outside: %v, %v", entries, err)
	}
}

func newP302aTestEngine(
	t *testing.T,
	sessionID string,
	transcriptDir string,
	cwd string,
	model *captureInputModel,
) *QueryEngine {
	t.Helper()
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:                model,
		CWD:                      cwd,
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 2,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
		HookExecutor:             hooks.NewExecutor(),
	})
	t.Cleanup(func() {
		engine.Close()
	})
	return engine
}
