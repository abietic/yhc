package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestP302cBranchCopiesPrivateMediaAndExportIsSanitized(t *testing.T) {
	dir := t.TempDir()
	sourcePath := createP302aSession(t, dir, "source")
	sourceRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceListing, err := ReadSessionLite(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if sourceListing == nil || sourceListing.DurableMedia != DurableMediaRefs {
		t.Fatalf("source durable media state = %#v", sourceListing)
	}

	branch, err := BranchSession(BranchOptions{
		SourceSessionID: "source",
		MessageIndex:    1,
		NewSessionID:    "child",
		OperationID:     "p302c-rich-branch",
		Dir:             dir,
	})
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	if branch == nil || branch.MessagesCopied != 1 {
		t.Fatalf("BranchSession = %#v", branch)
	}
	afterRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceRaw, afterRaw) ||
		!sourceInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("branch mutated source transcript")
	}
	sourceBlobs := p302cBlobPaths(t, sourcePath+mediaSidecarSuffix)
	childBlobs := p302cBlobPaths(t, branch.TranscriptPath+mediaSidecarSuffix)
	if len(sourceBlobs) != 1 || len(childBlobs) != 1 {
		t.Fatalf("source blobs = %v, child blobs = %v", sourceBlobs, childBlobs)
	}
	sourceBlobInfo, err := os.Stat(sourceBlobs[0])
	if err != nil {
		t.Fatal(err)
	}
	childBlobInfo, err := os.Stat(childBlobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceBlobInfo, childBlobInfo) {
		t.Fatal("branch hard-linked source media")
	}
	reused, err := BranchSession(BranchOptions{
		SourceSessionID: "source",
		MessageIndex:    1,
		NewSessionID:    "child",
		OperationID:     "p302c-rich-branch",
		Dir:             dir,
	})
	if err != nil || reused == nil || !reused.Reused ||
		reused.TranscriptPath != branch.TranscriptPath {
		t.Fatalf("rich branch retry = %#v, %v", reused, err)
	}
	if paths := p302cBlobPaths(
		t,
		branch.TranscriptPath+mediaSidecarSuffix,
	); len(paths) != 1 {
		t.Fatalf("rich branch retry blobs = %v", paths)
	}
	stages, err := filepath.Glob(branch.TranscriptPath + mediaSidecarSuffix + ".stage-*")
	if err != nil || len(stages) != 0 {
		t.Fatalf("branch staging remains = %v, %v", stages, err)
	}
	if _, err := DeleteSession(DeleteOptions{
		SessionID: "source",
		Dir:       dir,
	}); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	childLoaded, err := transcript.NewRecorder("child", dir).LoadFull()
	if err != nil {
		t.Fatalf("load child after source delete: %v", err)
	}
	if len(childLoaded.Messages) != 1 ||
		len(childLoaded.Messages[0].UserInputMultiContent) != 1 {
		t.Fatalf("child projection = %#v", childLoaded)
	}

	exported, err := ExportSession(ExportOptions{
		SessionID: "child",
		Dir:       dir,
		Format:    ExportJSON,
	})
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	for _, forbidden := range []string{
		`"media_id"`,
		`"digest"`,
		`"base64_data"`,
		transcriptPathForTest(dir, "child") + ".media",
	} {
		if strings.Contains(exported.Content, forbidden) {
			t.Fatalf("export leaked %q: %s", forbidden, exported.Content)
		}
	}
	var decoded ExportedSession
	if err := json.Unmarshal([]byte(exported.Content), &decoded); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(decoded.Messages) != 1 ||
		len(decoded.Messages[0].Parts) != 1 ||
		decoded.Messages[0].Parts[0].Image == nil {
		t.Fatalf("sanitized export = %#v", decoded)
	}
	markdown, err := ExportSession(ExportOptions{
		SessionID: "child",
		Dir:       dir,
		Format:    ExportMarkdown,
	})
	if err != nil {
		t.Fatalf("markdown ExportSession: %v", err)
	}
	placeholder := fmt.Sprintf(
		"[image: image/png, 12x12, %d bytes, detail=auto]",
		childBlobInfo.Size(),
	)
	if !strings.Contains(markdown.Content, placeholder) {
		t.Fatalf("markdown descriptor = %s", markdown.Content)
	}
	for _, forbidden := range []string{
		`media_id`,
		`digest`,
		`base64_data`,
		transcriptPathForTest(dir, "child") + ".media",
	} {
		if strings.Contains(markdown.Content, forbidden) {
			t.Fatalf("markdown export leaked %q: %s", forbidden, markdown.Content)
		}
	}
}

func TestP302cLiteListingReportsMalformedRichRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.jsonl")
	if err := os.WriteFile(
		path,
		[]byte("{\"kind\":\"user-prompt\",\"user_prompt\":\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	info, err := ReadSessionLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.DurableMedia != DurableMediaRecordCorrupt {
		t.Fatalf("durable media state = %#v", info)
	}
}

func TestP302cLiteListingReportsUnknownForUnseenLargeMiddle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	padding := bytes.Repeat(
		[]byte("{\"kind\":\"metadata\",\"meta_key\":\"padding\",\"meta_value\":\"x\"}\n"),
		1400,
	)
	content := append(
		[]byte(
			"{\"kind\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n",
		),
		padding...,
	)
	content = append(
		content,
		[]byte("{\"kind\":\"user-prompt\",\"user_prompt\":{\"hidden\":true}}\n")...,
	)
	content = append(content, padding...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadSessionLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil ||
		info.Summary != "hello" ||
		info.DurableMedia != DurableMediaUnknown {
		t.Fatalf("large durable media state = %#v", info)
	}
}

func TestP302cBranchMediaFailurePublishesNoChildTranscript(t *testing.T) {
	dir := t.TempDir()
	sourcePath := createP302aSession(t, dir, "source-corrupt")
	sourceRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(findP302aBlob(t, sourcePath+mediaSidecarSuffix)); err != nil {
		t.Fatal(err)
	}

	result, err := BranchSession(BranchOptions{
		SourceSessionID: "source-corrupt",
		MessageIndex:    1,
		NewSessionID:    "child-corrupt",
		Dir:             dir,
	})
	if err == nil || result != nil {
		t.Fatalf("corrupt branch = %#v, %v", result, err)
	}
	afterRaw, readErr := os.ReadFile(sourcePath)
	if readErr != nil || !bytes.Equal(sourceRaw, afterRaw) {
		t.Fatalf("branch changed source transcript: %v", readErr)
	}
	for _, path := range []string{
		transcriptPathForTest(dir, "child-corrupt"),
		transcriptPathForTest(dir, "child-corrupt") + mediaSidecarSuffix,
	} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed branch published %s: %v", path, statErr)
		}
	}
	stages, globErr := filepath.Glob(
		transcriptPathForTest(dir, "child-corrupt") +
			mediaSidecarSuffix +
			".stage-*",
	)
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("failed branch staging remains = %v, %v", stages, globErr)
	}
}

func TestP302cBranchRejectsPreexistingTargetMediaSidecar(t *testing.T) {
	dir := t.TempDir()
	_ = createP302aSession(t, dir, "source-preexisting")
	childPath := transcriptPathForTest(dir, "child-preexisting")
	if err := os.Mkdir(childPath+mediaSidecarSuffix, 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := BranchSession(BranchOptions{
		SourceSessionID: "source-preexisting",
		MessageIndex:    1,
		NewSessionID:    "child-preexisting",
		Dir:             dir,
	})
	if err == nil || result != nil {
		t.Fatalf("preexisting target branch = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(childPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preexisting target published transcript: %v", statErr)
	}
	if info, statErr := os.Lstat(childPath + mediaSidecarSuffix); statErr != nil ||
		!info.IsDir() {
		t.Fatalf("preexisting sidecar was changed: %#v, %v", info, statErr)
	}
}

func transcriptPathForTest(dir, sessionID string) string {
	return filepath.Join(dir, sessionID+".jsonl")
}

func p302cBlobPaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && len(entry.Name()) == 64 {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestP302aDeleteRemovesTranscriptBeforeValidatedMedia(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := createP302aSession(t, dir, "delete-rich")
	result, err := DeleteSession(DeleteOptions{
		SessionID: "delete-rich",
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !result.TranscriptRemoved || !result.MediaRemoved {
		t.Fatalf("DeleteResult = %#v", result)
	}
	for _, path := range []string{
		transcriptPath,
		transcriptPath + mediaSidecarSuffix,
	} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("deleted path remains %s: %v", filepath.Base(path), statErr)
		}
	}
}

func TestP302aDeleteMediaPreflightRejectsWithoutMutation(t *testing.T) {
	t.Run("unexpected entry", func(t *testing.T) {
		dir := t.TempDir()
		transcriptPath := createP302aSession(t, dir, "unexpected")
		unexpected := filepath.Join(
			transcriptPath+mediaSidecarSuffix,
			"private.txt",
		)
		if err := os.WriteFile(unexpected, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := DeleteSession(DeleteOptions{
			SessionID: "unexpected",
			Dir:       dir,
		}); err == nil {
			t.Fatal("DeleteSession accepted unexpected media entry")
		}
		assertP302aDeletePathExists(t, transcriptPath)
		assertP302aDeletePathExists(t, unexpected)
	})

	t.Run("symlinked blob", func(t *testing.T) {
		dir := t.TempDir()
		transcriptPath := createP302aSession(t, dir, "symlink")
		blob := findP302aBlob(t, transcriptPath+mediaSidecarSuffix)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(blob); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, blob); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := DeleteSession(DeleteOptions{
			SessionID: "symlink",
			Dir:       dir,
		}); err == nil {
			t.Fatal("DeleteSession accepted symlinked blob")
		}
		assertP302aDeletePathExists(t, transcriptPath)
		data, err := os.ReadFile(outside)
		if err != nil || string(data) != "outside" {
			t.Fatalf("outside target changed: %q, %v", data, err)
		}
	})

	t.Run("root replacement race", func(t *testing.T) {
		dir := t.TempDir()
		transcriptPath := createP302aSession(t, dir, "replace")
		mediaRoot := transcriptPath + mediaSidecarSuffix
		owned := mediaRoot + ".owned"
		outside := t.TempDir()
		_, err := DeleteSession(DeleteOptions{
			SessionID: "replace",
			Dir:       dir,
			beforeMutation: func() {
				if renameErr := os.Rename(mediaRoot, owned); renameErr != nil {
					t.Fatalf("rename media root: %v", renameErr)
				}
				if linkErr := os.Symlink(outside, mediaRoot); linkErr != nil {
					t.Fatalf("replace media root: %v", linkErr)
				}
			},
		})
		if err == nil {
			t.Fatal("DeleteSession accepted media root replacement")
		}
		assertP302aDeletePathExists(t, transcriptPath)
		entries, readErr := os.ReadDir(outside)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("replacement mutated outside: %v, %v", entries, readErr)
		}
	})
}

func createP302aSession(
	t *testing.T,
	dir string,
	sessionID string,
) string {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	transcriptPath := recorder.Path()
	data := p302aSessionPNG(t)
	store := mediastore.New(transcriptPath + mediaSidecarSuffix)
	ref, err := store.Put(context.Background(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    12,
		Height:   12,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  "turn-" + sessionID,
		Parts: []promptrecord.Part{{
			Kind: promptrecord.PartImage,
			Image: &promptrecord.ImagePart{
				Ref:    ref,
				Detail: "auto",
			},
		}},
	}
	message, err := record.Materialize(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordUserPrompt(record, message); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "answer",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func p302aSessionPNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 12, 12))
	for y := range 12 {
		for x := range 12 {
			source.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 17),
				G: uint8(y * 19),
				B: uint8((x + y) * 9),
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

func findP302aBlob(t *testing.T, root string) string {
	t.Helper()
	var blob string
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
			entry.Name()[0] != '.' {
			blob = path
		}
		return nil
	})
	if err != nil || blob == "" {
		t.Fatalf("find blob: path=%q err=%v", blob, err)
	}
	return blob
}

func assertP302aDeletePathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected path %s: %v", filepath.Base(path), err)
	}
}
