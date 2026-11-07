package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestReadImageDeliversOriginalRasterAttachment(t *testing.T) {
	var encoded bytes.Buffer
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	pixels.Set(1, 2, color.NRGBA{R: 231, G: 52, B: 91, A: 255})
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "drawing.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var attachments []*schema.Message
	ctx := WithMediaSupport(t.Context(), true)
	ctx = WithFileReadGuards(ctx, NewFileReadGuards())
	ctx = WithAttachmentFn(ctx, func(message *schema.Message) { attachments = append(attachments, message) })
	result, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("Read delivered %d attachments, want one image; text bytes=%d", len(attachments), len(result))
	}
	message := attachments[0]
	if message.Role != schema.User || len(message.UserInputMultiContent) != 2 {
		t.Fatalf("image attachment shape = %#v", message)
	}
	part := message.UserInputMultiContent[1]
	if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.Base64Data == nil || part.Image.MIMEType != "image/png" {
		t.Fatalf("image part = %#v", part)
	}
	got, err := base64.StdEncoding.DecodeString(*part.Image.Base64Data)
	if err != nil || !bytes.Equal(got, encoded.Bytes()) {
		t.Fatalf("image bytes changed: decoded=%d error=%v", len(got), err)
	}
	if strings.Contains(result, "\x89PNG") || strings.Contains(result, "\x00") || !strings.Contains(result, "2x3") {
		t.Fatalf("text result contains binary content or lacks dimensions: %q", result)
	}
	entry := fileReadGuardsFromContext(ctx).m[path]
	if entry == nil || entry.sha256 != "" {
		t.Fatal("visual read must stay unverified for content-bound guard recovery")
	}
}

func TestReadImageDetectsEncodingAndUsesExecutionCWD(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "drawing.PNG"), encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionCWD(t.Context(), root)
	ctx = WithMediaSupport(ctx, true)
	var attachment *schema.Message
	ctx = WithAttachmentFn(ctx, func(m *schema.Message) { attachment = m })
	if _, err := ReadTool().ExecuteCtx(ctx, `{"file_path":"drawing.PNG"}`); err != nil {
		t.Fatal(err)
	}
	if attachment == nil || attachment.UserInputMultiContent[1].Image.MIMEType != "image/jpeg" {
		t.Fatal("MIME type must match bytes, including a mislabeled extension")
	}
}

func TestReadImageDeliversStaticWebP(t *testing.T) {
	// A complete 1x1 VP8 frame in a RIFF/WebP container.
	data := []byte{
		0x52, 0x49, 0x46, 0x46, 0x22, 0x00, 0x00, 0x00,
		0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x20,
		0x16, 0x00, 0x00, 0x00, 0x30, 0x01, 0x00, 0x9d,
		0x01, 0x2a, 0x01, 0x00, 0x01, 0x00, 0x0e, 0xc0,
		0xfe, 0x25, 0xa4, 0x00, 0x03, 0x70, 0x00, 0x00, 0x00, 0x00,
	}
	path := filepath.Join(t.TempDir(), "drawing.webp")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var attachment *schema.Message
	ctx := WithAttachmentFn(WithMediaSupport(t.Context(), true), func(m *schema.Message) { attachment = m })
	result, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if attachment == nil || attachment.UserInputMultiContent[1].Image.MIMEType != "image/webp" || *attachment.UserInputMultiContent[1].Image.Base64Data != base64.StdEncoding.EncodeToString(data) || !strings.Contains(result, "1x1") {
		t.Fatal("static WebP encoding or dimensions were not preserved")
	}
}

func TestReadImageRejectsUndeliverableOrInvalidContent(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewGray(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	oversizedHeader := append([]byte(nil), source.Bytes()...)
	binary.BigEndian.PutUint32(oversizedHeader[16:20], 25_000_001)
	binary.BigEndian.PutUint32(oversizedHeader[20:24], 1)
	binary.BigEndian.PutUint32(oversizedHeader[29:33], crc32.ChecksumIEEE(oversizedHeader[12:29]))
	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{"text-only-model", source.Bytes(), "cannot receive image"},
		{"missing-collector", source.Bytes(), "cannot deliver image"},
		{"invalid-header", []byte("not an image"), "convert to PNG"},
		{"truncated-body", source.Bytes()[:len(source.Bytes())-12], "cannot decode image"},
		{"pixel-bound", oversizedHeader, "25 million pixel"},
		{"file-bound", nil, "5 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.png")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.name == "file-bound" {
				if err := os.Truncate(path, 5*1024*1024+1); err != nil {
					t.Fatal(err)
				}
			}
			guards := NewFileReadGuards()
			ctx := WithFileReadGuards(t.Context(), guards)
			ctx = WithMediaSupport(ctx, test.name != "text-only-model")
			attachments := 0
			if test.name != "missing-collector" {
				ctx = WithAttachmentFn(ctx, func(*schema.Message) { attachments++ })
			}
			result, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path}))
			if err == nil || !strings.Contains(err.Error(), test.want) || result != "" || attachments != 0 || len(guards.m) != 0 {
				t.Fatalf("invalid read: error=%v result-bytes=%d attachments=%d guards=%d", err, len(result), attachments, len(guards.m))
			}
		})
	}
}

func TestReadImageCancellationAndLegacyDeliveryFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.png")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path})); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %v", err)
	}
	if _, err := ReadTool().Execute(fileToolInput(t, map[string]any{"file_path": path})); err == nil || !strings.Contains(err.Error(), "cannot receive image") {
		t.Fatalf("legacy read without media delivery = %v", err)
	}
}

func TestReadSVGRetainsTextEditEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drawing.svg")
	if err := os.WriteFile(path, []byte(`<svg><text>before</text></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithFileReadGuards(t.Context(), NewFileReadGuards())
	result, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path}))
	if err != nil || !strings.Contains(result, "<svg>") {
		t.Fatalf("SVG text read = %q error=%v", result, err)
	}
	if _, err := EditTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "old_string": "before", "new_string": "after"})); err != nil {
		t.Fatalf("SVG text edit = %v", err)
	}
}
