package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestDeepSeekLargeAutoPNGUsesBoundedTransportCopy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[stream], func(t *testing.T) {
			raster := image.NewNRGBA(image.Rect(0, 0, 1024, 768))
			for y := range 768 {
				for x := range 1024 {
					c := color.NRGBA{R: 240, A: 255}
					if x >= 512 {
						c = color.NRGBA{B: 240, A: 255}
					}
					raster.SetNRGBA(x, y, c)
				}
			}
			var raw bytes.Buffer
			encoder := png.Encoder{CompressionLevel: png.NoCompression}
			if err := encoder.Encode(&raw, raster); err != nil {
				t.Fatal(err)
			}
			original := base64.StdEncoding.EncodeToString(raw.Bytes())
			input := []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "before"},
				{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{MIMEType: "image/png", Base64Data: &original}, Detail: "auto",
				}},
				{Type: schema.ChatMessagePartTypeText, Text: "after"},
			}}}
			var requestBytes int
			var imageURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				requestBytes = len(body)
				var request struct {
					Input []struct {
						Content []struct {
							Type, Text string
							ImageURL   string `json:"image_url"`
							Detail     string
						}
					}
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					return
				}
				if len(request.Input) != 1 || len(request.Input[0].Content) != 3 {
					t.Error("ordered multipart shape changed")
					return
				}
				parts := request.Input[0].Content
				if parts[0].Text != "before" || parts[2].Text != "after" || parts[1].Detail != "auto" {
					t.Error("transport projection changed text order or detail")
				}
				imageURL = parts[1].ImageURL
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"fixture\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"fixture","object":"response","status":"completed","output":[]}`)
				}
			}))
			defer server.Close()
			inner, err := newAgenticDeepSeek(t.Context(), Config{Provider: ProviderAgenticDeepSeek, Model: "deepseek-flash", BaseURL: server.URL, APIKey: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			m := wrapAgenticModel(inner)
			if stream {
				sr, err := m.Stream(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				defer sr.Close()
				for {
					_, err = sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			} else if _, err := m.Generate(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if requestBytes >= raw.Len()/4 || !strings.HasPrefix(imageURL, "data:image/jpeg;base64,") {
				t.Fatalf("large auto PNG still dominates request: request_bytes=%d source_bytes=%d jpeg=%t", requestBytes, raw.Len(), strings.HasPrefix(imageURL, "data:image/jpeg;base64,"))
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(imageURL, "data:image/jpeg;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			projected, err := jpeg.Decode(bytes.NewReader(decoded))
			if err != nil || projected.Bounds() != raster.Bounds() {
				t.Fatal("small-pixel-budget image lost dimensions or valid JPEG encoding")
			}
			red, _, blue, _ := projected.At(100, 100).RGBA()
			if red <= blue {
				t.Fatal("transport copy changed left-band semantics")
			}
			red, _, blue, _ = projected.At(900, 100).RGBA()
			if blue <= red {
				t.Fatal("transport copy changed right-band semantics")
			}
			if *input[0].UserInputMultiContent[1].Image.Base64Data != base64.StdEncoding.EncodeToString(raw.Bytes()) || input[0].UserInputMultiContent[1].Image.MIMEType != "image/png" {
				t.Fatal("transport optimization mutated canonical input")
			}
		})
	}
}
