package agenticdeepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCurrentAndLegacyFlashModelsPreserveMixedImageInput(t *testing.T) {
	t.Parallel()
	for _, modelID := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		t.Run(modelID, func(t *testing.T) {
			requests := make(chan responseRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body responseRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"flash-fixture","object":"response","status":"completed","output":[]}`)
			}))
			defer server.Close()
			m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", BaseURL: server.URL, Model: modelID, ReasoningEffort: ReasoningEffortMax})
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Generate(context.Background(), []*schema.AgenticMessage{{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.UserInputText{Text: "first"}),
					schema.NewContentBlock(&schema.UserInputImage{MIMEType: "image/png", Base64Data: "aW1hZ2U="}),
					schema.NewContentBlock(&schema.UserInputText{Text: "second"}),
					NewFileIDImageBlock("file-api-fixture"),
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			body := <-requests
			if body.Model != modelID || body.Reasoning == nil || body.Reasoning.Effort != ReasoningEffortMax || len(body.Input) != 1 {
				t.Fatal("Flash model identity, effort, or input count changed")
			}
			want := []contentPart{
				{Type: "input_text", Text: "first"},
				{Type: "input_image", ImageURL: "data:image/png;base64,aW1hZ2U="},
				{Type: "input_text", Text: "second"},
				{Type: "input_image", FileID: "file-api-fixture"},
			}
			if !reflect.DeepEqual(body.Input[0].Content, want) {
				t.Fatal("mixed image input changed order or bytes")
			}
		})
	}
}
