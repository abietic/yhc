package agenticdeepseek

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestParsedDocumentUploadAndReference(t *testing.T) {
	for _, purpose := range []FilePurpose{FilePurposeTextRecognition, FilePurposeRichLayoutRecognition} {
		t.Run(string(purpose), func(t *testing.T) {
			calls := 0
			client, err := NewFilesClient(&FilesConfig{APIKey: "fixture", BaseURL: "https://fixture.invalid/proxy/v1", HTTPClient: &http.Client{Transport: applicationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/proxy/v1/files" {
					t.Fatalf("path = %s", r.URL.Path)
				}
				if r.Method == http.MethodPost {
					reader, err := r.MultipartReader()
					if err != nil {
						t.Fatal(err)
					}
					part, err := reader.NextPart()
					if err != nil {
						t.Fatal(err)
					}
					value, _ := io.ReadAll(part)
					if part.FormName() != "purpose" || string(value) != string(purpose) {
						t.Fatalf("first field = %q %q", part.FormName(), value)
					}
					part, err = reader.NextPart()
					if err != nil || part.FormName() != "file" || part.FileName() != "report.pdf" {
						t.Fatalf("file part = %#v, %v", part, err)
					}
				} else if r.URL.Query().Get("purpose") != string(purpose) {
					t.Fatalf("query = %s", r.URL)
				}
				file := fmt.Sprintf(`{"id":"file-api-document","object":"file","bytes":3,"created_at":1,"filename":"report.pdf","purpose":%q}`, purpose)
				if r.Method == http.MethodGet {
					file = `{"object":"list","data":[` + file + `],"has_more":false}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(file))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			file, err := client.Upload(t.Context(), UploadFileParams{Purpose: purpose, Filename: "report.pdf", Content: strings.NewReader("pdf"), Size: 3})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.List(t.Context(), &ListFilesOptions{Purpose: purpose}); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("calls = %d", calls)
			}
			input := []*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: "before"}), NewFileIDDocumentBlock(file.ID), schema.NewContentBlock(&schema.UserInputText{Text: "after"})}}}
			items, err := messagesToInputItems(input, FlashModel)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(items)
			if string(wire) != `[{"type":"message","role":"user","content":[{"type":"input_text","text":"before"},{"type":"input_file","file_id":"file-api-document"},{"type":"input_text","text":"after"}]}]` {
				t.Fatalf("input = %s", wire)
			}
		})
	}
}

func TestDocumentLimitsAndInvalidSources(t *testing.T) {
	valid := UploadFileParams{Purpose: FilePurposeRichLayoutRecognition, Filename: "report.pdf", Content: strings.NewReader("x"), Size: 100 << 20}
	if err := validateUploadFileParams(valid); err != nil {
		t.Fatal(err)
	}
	valid.Size++
	if err := validateUploadFileParams(valid); err == nil {
		t.Fatal("accepted oversized document")
	}
	valid.Size = 1
	valid.Purpose = "unknown"
	if err := validateUploadFileParams(valid); err == nil {
		t.Fatal("accepted unknown purpose")
	}
	for _, tc := range []struct {
		name  string
		block *schema.ContentBlock
		model string
		role  schema.AgenticRoleType
	}{
		{"invalid ID", NewFileIDDocumentBlock("https://private.invalid/a"), FlashModel, schema.AgenticRoleTypeUser},
		{"model", NewFileIDDocumentBlock("file-api-doc"), "deepseek-v4-pro", schema.AgenticRoleTypeUser},
		{"system", NewFileIDDocumentBlock("file-api-doc"), FlashModel, schema.AgenticRoleTypeSystem},
		{"URL", schema.NewContentBlock(&schema.UserInputFile{URL: "https://private.invalid/a"}), FlashModel, schema.AgenticRoleTypeUser},
		{"base64", schema.NewContentBlock(&schema.UserInputFile{Base64Data: "cGRm"}), FlashModel, schema.AgenticRoleTypeUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := messagesToInputItems([]*schema.AgenticMessage{{Role: tc.role, ContentBlocks: []*schema.ContentBlock{tc.block}}}, tc.model)
			var conversion *ConversionError
			if !errors.As(err, &conversion) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	m, err := New(t.Context(), &Config{APIKey: "fixture", Model: FlashModel})
	if err != nil {
		t.Fatal(err)
	}
	common, specific := m.options()
	blocks := make([]*schema.ContentBlock, 21)
	for i := range blocks {
		blocks[i] = NewFileIDDocumentBlock("file-api-doc")
	}
	if _, err := buildResponseRequest([]*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}}, common, specific, false); err == nil {
		t.Fatal("accepted 21 document references")
	}
}
