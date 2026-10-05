package agenticdeepseek

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func transportTestPNG(t *testing.T) string {
	t.Helper()
	raster := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	for y := range 512 {
		for x := range 512 {
			raster.SetNRGBA(x, y, color.NRGBA{R: 200, A: 255})
		}
	}
	var data bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&data, raster); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestImageTransportProjectionIsOptInAndPreservesExplicitDetail(t *testing.T) {
	encoded := transportTestPNG(t)
	for _, enabled := range []bool{false, true} {
		var wire responseRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"fixture","object":"response","status":"completed","output":[]}`)
		}))
		m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", Model: FlashModel, BaseURL: server.URL, OptimizeImageTransport: enabled})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		blocks := make([]*schema.ContentBlock, 0, 4)
		for _, detail := range []string{"auto", "auto", "high", "original"} {
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{MIMEType: "image/png", Base64Data: encoded, Detail: schema.ImageURLDetail(detail)}))
		}
		if _, err := m.Generate(t.Context(), []*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}}); err != nil {
			server.Close()
			t.Fatal(err)
		}
		server.Close()
		parts := wire.Input[0].Content
		if len(parts) != 4 || parts[0].ImageURL != parts[1].ImageURL {
			t.Fatal("repeated image projection changed order or identity")
		}
		for i, part := range parts {
			if i < 2 && enabled {
				if !strings.HasPrefix(part.ImageURL, "data:image/jpeg;base64,") {
					t.Fatal("auto PNG not optimized")
				}
			} else if part.ImageURL != "data:image/png;base64,"+encoded {
				t.Fatal("direct SDK or explicit original detail lost exact source bytes")
			}
			if blocks[i].UserInputImage.Base64Data != encoded || blocks[i].UserInputImage.MIMEType != "image/png" {
				t.Fatal("canonical content block mutated")
			}
		}
	}
}

func TestImageTransportCoversToolOutputsAndStopsCanceledPreparation(t *testing.T) {
	encoded := transportTestPNG(t)
	request := &responseRequest{Input: []inputItem{{Type: "function_call_output", Output: []contentPart{
		{Type: "input_text", Text: "before"},
		{Type: "input_image", ImageURL: "data:image/png;base64," + encoded, Detail: "auto"},
		{Type: "input_text", Text: "after"},
		{Type: "input_image", FileID: "file-api-fixture"},
		{Type: "input_image", ImageURL: "https://api.deepseek.com/synthetic-image-fixture.png"},
	}}}}
	if err := optimizeResponseImages(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	parts := request.Input[0].Output
	if parts[0].Text != "before" || parts[2].Text != "after" || !strings.HasPrefix(parts[1].ImageURL, "data:image/jpeg;base64,") || parts[3].FileID != "file-api-fixture" || parts[4].ImageURL != "https://api.deepseek.com/synthetic-image-fixture.png" {
		t.Fatal("tool output projection changed non-image semantics")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request.Input[0].Output[1].ImageURL = "data:image/png;base64," + encoded
	if err := optimizeResponseImages(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
