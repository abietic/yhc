package agenticglm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestIdleSSEContextCancellationReleasesResponseBody(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": idle\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := New(ctx, &Config{APIKey: "test", BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cancel()
	if _, err := reader.Recv(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled idle stream error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not release provider response body")
	}
}

func TestSSEReaderCloseReleasesBackpressuredResponseBody(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 64; i++ {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"close\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := New(ctx, &Config{APIKey: "test", BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader Close did not release backpressured response body")
	}
}
