package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"

	"github.com/cloudwego/eino/schema"
	"golang.org/x/image/webp"
)

// Match the canonical user-image bounds without importing an engine owner.
const (
	readImageMaxBytes  = 5 * 1024 * 1024
	readImageMaxPixels = 25_000_000
)

func readImageForTool(ctx context.Context, displayPath, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !MediaSupported(ctx) {
		return "", fmt.Errorf("the active model or execution path cannot receive image attachments; use an image-capable model and entrypoint")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("image input must be a regular file")
	}
	if info.Size() > readImageMaxBytes {
		return "", fmt.Errorf("image exceeds the 5 MiB limit; resize or convert it before reading")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck // Read-only input.
	data, err := io.ReadAll(io.LimitReader(readImageContextReader{ctx, file}, readImageMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > readImageMaxBytes {
		return "", fmt.Errorf("image exceeds the 5 MiB limit; resize or convert it before reading")
	}
	mime := http.DetectContentType(data)
	var config image.Config
	var decode func(io.Reader) (image.Image, error)
	switch mime {
	case "image/png":
		config, err = png.DecodeConfig(bytes.NewReader(data))
		decode = png.Decode
	case "image/jpeg":
		config, err = jpeg.DecodeConfig(bytes.NewReader(data))
		decode = jpeg.Decode
	case "image/webp":
		config, err = webp.DecodeConfig(bytes.NewReader(data))
		decode = webp.Decode
	default:
		return "", fmt.Errorf("unsupported image encoding; convert to PNG, JPEG or static WebP before reading")
	}
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", fmt.Errorf("invalid image header")
	}
	if config.Width > readImageMaxPixels/config.Height {
		return "", fmt.Errorf("image exceeds the 25 million pixel limit; resize it before reading")
	}
	if _, err := decode(readImageContextReader{ctx, bytes.NewReader(data)}); err != nil {
		return "", fmt.Errorf("cannot decode image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	summary := fmt.Sprintf("Image %s (%s, %dx%d) delivered as an attachment", displayPath, mime, config.Width, config.Height)
	encoded := base64.StdEncoding.EncodeToString(data)
	attachment := &schema.Message{
		Role:    schema.User,
		Content: summary,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: summary},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &encoded,
				MIMEType:   mime,
			}}},
		},
		Extra: map[string]any{"is_meta": true, "attachment_kind": "image", "file_path": displayPath},
	}
	if !EmitAttachment(ctx, attachment) {
		return "", fmt.Errorf("this execution path cannot deliver image attachments")
	}
	return summary, nil
}

type readImageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r readImageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
