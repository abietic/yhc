package agenticdeepseek

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

func TestFilesClientLifecycleUsesOfficialEndpoints(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer fixture-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if got := r.FormValue("purpose"); got != string(FilePurposeUserData) {
				t.Errorf("purpose = %q", got)
			}
			if got := r.FormValue("expires_after[anchor]"); got != "created_at" {
				t.Errorf("expires anchor = %q", got)
			}
			if got := r.FormValue("expires_after[seconds]"); got != "3600" {
				t.Errorf("expires seconds = %q", got)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil {
				t.Fatal(err)
			}
			if header.Filename != "canary.png" || string(body) != "png-bytes" {
				t.Errorf("file = %q %q", header.Filename, body)
			}
			_, _ = io.WriteString(w, `{"id":"file-api-abc123","object":"file","bytes":9,"created_at":1700000000,"filename":"canary.png","purpose":"user_data","expires_at":1700003600}`)
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			want := url.Values{
				"after":   []string{"file-api-cursor1"},
				"limit":   []string{"2"},
				"order":   []string{"desc"},
				"purpose": []string{"user_data"},
			}
			if got := r.URL.Query(); got.Encode() != want.Encode() {
				t.Errorf("query = %q, want %q", got.Encode(), want.Encode())
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"file-api-abc123","object":"file","bytes":9,"created_at":1700000000,"filename":"canary.png","purpose":"user_data"}],"first_id":"file-api-abc123","last_id":"file-api-abc123","has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/files/file-api-abc123":
			_, _ = io.WriteString(w, `{"id":"file-api-abc123","object":"file","bytes":9,"created_at":1700000000,"filename":"canary.png","purpose":"user_data"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/files/file-api-abc123":
			_, _ = io.WriteString(w, `{"id":"file-api-abc123","object":"file","deleted":true}`)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewFilesClient(&FilesConfig{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := client.Upload(context.Background(), UploadFileParams{
		Filename:            "canary.png",
		Content:             strings.NewReader("png-bytes"),
		Size:                9,
		ExpiresAfterSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.ID != "file-api-abc123" || uploaded.ExpiresAt == nil || *uploaded.ExpiresAt != 1700003600 {
		t.Fatalf("uploaded = %#v", uploaded)
	}

	listed, err := client.List(context.Background(), &ListFilesOptions{
		After:   "file-api-cursor1",
		Limit:   2,
		Order:   FileOrderDesc,
		Purpose: FilePurposeUserData,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != uploaded.ID || listed.HasMore {
		t.Fatalf("listed = %#v", listed)
	}

	retrieved, err := client.Retrieve(context.Background(), uploaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.ID != uploaded.ID || retrieved.Filename != "canary.png" {
		t.Fatalf("retrieved = %#v", retrieved)
	}

	deleted, err := client.Delete(context.Background(), uploaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != uploaded.ID || !deleted.Deleted {
		t.Fatalf("deleted = %#v", deleted)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("calls = %d, want 4", got)
	}
}

func TestFilesClientRejectsInvalidInputsBeforeDispatch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	invalidConfigs := []*FilesConfig{
		nil,
		{BaseURL: server.URL},
		{APIKey: "key", BaseURL: "file:///tmp/files"},
		{APIKey: "key", BaseURL: "https://user:secret@example.com"},
		{APIKey: "key", BaseURL: server.URL, Timeout: -time.Second},
	}
	for _, config := range invalidConfigs {
		if _, err := NewFilesClient(config); err == nil {
			t.Fatalf("NewFilesClient accepted %#v", config)
		}
	}

	client, err := NewFilesClient(&FilesConfig{APIKey: "key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	uploads := []UploadFileParams{
		{},
		{Filename: "../canary.png", Content: strings.NewReader("x"), Size: 1},
		{Filename: strings.Repeat("x", maxFileNameRunes+1), Content: strings.NewReader("x"), Size: 1},
		{Filename: "canary.png", Size: 1},
		{Filename: "canary.png", Content: strings.NewReader(""), Size: 0},
		{Filename: "canary.png", Content: strings.NewReader("x"), Size: maxFileUploadBytes + 1},
		{Filename: "canary.png", Content: strings.NewReader("x"), Size: 1, ExpiresAfterSeconds: minFileExpirySeconds - 1},
		{Filename: "canary.png", Content: strings.NewReader("x"), Size: 1, ExpiresAfterSeconds: maxFileExpirySeconds + 1},
		{Filename: "canary.png", Content: strings.NewReader("too long"), Size: 1},
		{Filename: "canary.png", Content: strings.NewReader("short"), Size: 9},
	}
	for index, params := range uploads {
		if _, err := client.Upload(context.Background(), params); err == nil {
			t.Fatalf("Upload case %d succeeded", index)
		}
	}

	for _, fileID := range []string{"", "file-other-123", "file-api-bad/path", "file-api-bad?query"} {
		if _, err := client.Retrieve(context.Background(), fileID); err == nil {
			t.Fatalf("Retrieve accepted %q", fileID)
		}
		if _, err := client.Delete(context.Background(), fileID); err == nil {
			t.Fatalf("Delete accepted %q", fileID)
		}
	}
	for _, options := range []*ListFilesOptions{
		{After: "bad-cursor"},
		{Limit: -1},
		{Limit: maxFilesListLimit + 1},
		{Order: FileOrder("newest")},
		{Purpose: FilePurpose("assistants")},
	} {
		if _, err := client.List(context.Background(), options); err == nil {
			t.Fatalf("List accepted %#v", options)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestFilesClientReturnsTypedBoundedErrors(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "request-files")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_file","message":"fixture-key rejected at `+server.URL+`/files"}}`)
	}))
	defer server.Close()
	client, err := NewFilesClient(&FilesConfig{APIKey: "fixture-key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Code != "invalid_file" || apiErr.RequestID != "request-files" {
		t.Fatalf("error = %T %#v", err, err)
	}
	if strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error leaked secret or endpoint: %v", err)
	}
}

func TestFilesEndpointPreservesPrefixAndExistingEndpoint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		baseURL string
		want    string
	}{
		{baseURL: "https://example.com", want: "https://example.com/files"},
		{baseURL: "https://example.com/proxy/v1/", want: "https://example.com/proxy/v1/files"},
		{baseURL: "https://example.com/proxy/files/", want: "https://example.com/proxy/files"},
	} {
		got, err := filesEndpoint(tc.baseURL)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("filesEndpoint(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}
