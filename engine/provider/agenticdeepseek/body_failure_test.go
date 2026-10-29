package agenticdeepseek

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestStreamBodyFailureHasSafeResponseBodyPhase(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "4096")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n")
	}))
	defer server.Close()
	m, err := New(t.Context(), &Config{APIKey: "fixture", Model: FlashModel, BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := m.Stream(t.Context(), []*schema.AgenticMessage{schema.UserAgenticMessage("fixture")})
	if err != nil {
		t.Fatalf("headers must have been accepted before reading the failed body: %v", err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "reason=connection_closed phase=response_body") {
		t.Fatalf("body failure lost its phase or cause: %v", err)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatal("transport diagnostic exposed the endpoint")
	}
}
