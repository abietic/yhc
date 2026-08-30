package agenticglm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilesClientLifecycleUsesOfficialGLMEndpoints(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer fixture-key" {
			t.Errorf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if got := r.FormValue("purpose"); got != string(FilePurposeAgent) {
				t.Errorf("purpose = %q", got)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body, _ := io.ReadAll(file)
			if header.Filename != "canary.txt" || string(body) != "glm-file" {
				t.Errorf("file = %q %q", header.Filename, body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file-glm-1","object":"file","bytes":8,"created_at":1700000000,"filename":"canary.txt","purpose":"agent"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			want := url.Values{"after": {"file-glm-0"}, "limit": {"2"}, "order": {"created_at"}, "purpose": {"agent"}}
			if got := r.URL.Query(); got.Encode() != want.Encode() {
				t.Errorf("query = %q, want %q", got.Encode(), want.Encode())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"file-glm-1","object":"file","bytes":8,"created_at":1700000000,"filename":"canary.txt","purpose":"agent"}],"has_more":false}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/files/file-glm-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file-glm-1","object":"file","deleted":true}`)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewFilesClient(&FilesConfig{APIKey: "fixture-key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := client.Upload(context.Background(), UploadFileParams{
		Filename: "canary.txt",
		Content:  strings.NewReader("glm-file"),
		Size:     8,
		Purpose:  FilePurposeAgent,
	})
	if err != nil || uploaded.ID != "file-glm-1" {
		t.Fatalf("Upload = %#v, %v", uploaded, err)
	}
	listed, err := client.List(context.Background(), &ListFilesOptions{
		After: "file-glm-0", Limit: 2, Order: FileOrderCreatedAt, Purpose: FilePurposeAgent,
	})
	if err != nil || len(listed.Data) != 1 || listed.Data[0].ID != uploaded.ID {
		t.Fatalf("List = %#v, %v", listed, err)
	}
	deleted, err := client.Delete(context.Background(), uploaded.ID)
	if err != nil || !deleted.Deleted {
		t.Fatalf("Delete = %#v, %v", deleted, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestFilesClientRejectsInvalidInputsBeforeDispatch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, config := range []*FilesConfig{
		nil,
		{BaseURL: server.URL},
		{APIKey: "key", BaseURL: "file:///tmp/files"},
		{APIKey: "key", BaseURL: "https://user:secret@example.com"},
		{APIKey: "key", BaseURL: server.URL, Timeout: -time.Second},
	} {
		if _, err := NewFilesClient(config); err == nil {
			t.Fatalf("NewFilesClient accepted %#v", config)
		}
	}
	client, err := NewFilesClient(&FilesConfig{APIKey: "key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []UploadFileParams{
		{},
		{Filename: "../x.txt", Content: strings.NewReader("x"), Size: 1, Purpose: FilePurposeAgent},
		{Filename: "x.txt", Content: strings.NewReader("x"), Size: 1},
		{Filename: "x.txt", Content: strings.NewReader("x"), Size: maxAgentFileUploadBytes + 1, Purpose: FilePurposeAgent},
		{Filename: "x.txt", Content: strings.NewReader("too long"), Size: 1, Purpose: FilePurposeAgent},
	} {
		if _, err := client.Upload(context.Background(), params); err == nil {
			t.Fatalf("Upload accepted %#v", params)
		}
	}
	for _, fileID := range []string{"", "bad/path", "bad?query"} {
		if _, err := client.Delete(context.Background(), fileID); err == nil {
			t.Fatalf("Delete accepted %q", fileID)
		}
	}
	for _, options := range []*ListFilesOptions{
		{},
		{Purpose: FilePurposeAgent, Limit: -1},
		{Purpose: FilePurposeAgent, Limit: 101},
		{Purpose: FilePurpose("unsupported")},
		{Purpose: FilePurposeAgent, Order: FileOrder("desc")},
	} {
		if _, err := client.List(context.Background(), options); err == nil {
			t.Fatalf("List accepted %#v", options)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}

func TestFilesClientReturnsTypedRedactedErrors(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "glm-files-request")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_file","message":"fixture-key rejected at `+server.URL+`/files"}}`)
	}))
	defer server.Close()
	client, err := NewFilesClient(&FilesConfig{APIKey: "fixture-key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), &ListFilesOptions{Purpose: FilePurposeAgent})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Code != "invalid_file" {
		t.Fatalf("error = %T %#v", err, err)
	}
	if strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error leaked secret or endpoint: %v", err)
	}
}
