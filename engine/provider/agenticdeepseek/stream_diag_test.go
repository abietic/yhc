package agenticdeepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providerdiag"
)

type diagnosticFailReader struct{ used bool }

func (r *diagnosticFailReader) Read([]byte) (int, error) {
	if r.used {
		return 0, io.ErrUnexpectedEOF
	}
	r.used = true
	return 0, io.ErrUnexpectedEOF
}

func TestStreamDiagnosticsStructuralOutcomes(t *testing.T) {
	completed := `event: response.completed
data: {"type":"response.completed","sequence_number":3,"response":{"id":"private-response","object":"response","status":"completed","model":"deepseek-flash","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`
	failed := `event: response.failed
data: {"type":"response.failed","sequence_number":3,"response":{"id":"private-response","object":"response","status":"failed","model":"deepseek-flash","error":{"code":"fixture"},"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`
	for _, tc := range []struct {
		name, wire      string
		reader          io.Reader
		wantKind        string
		terminal, usage bool
		responseID      bool
	}{
		{name: "clean eof missing terminal", wire: "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n", wantKind: "eof_missing_terminal"},
		{name: "valid completed usage", wire: completed, wantKind: "completed", terminal: true, usage: true, responseID: true},
		{name: "valid incomplete usage", wire: strings.ReplaceAll(completed, "completed", "incomplete"), wantKind: "incomplete", terminal: true, usage: true, responseID: true},
		{name: "failed with final usage", wire: failed, wantKind: "api_error", terminal: true, usage: true, responseID: true},
		{name: "malformed failed envelope", wire: strings.ReplaceAll(failed, `"status":"failed"`, `"status":"completed"`), wantKind: "api_error"},
		{name: "reset after reasoning", reader: io.MultiReader(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"private-response\",\"object\":\"response\"}}\n\nevent: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"sequence_number\":1,\"delta\":\"private\"}\n\n"), &diagnosticFailReader{}), wantKind: "transport_error", responseID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := providerdiag.New(nil)
			reader := tc.reader
			if reader == nil {
				reader = strings.NewReader(tc.wire)
			}
			err := parseResponseStreamObserved(reader, 4096, o, nil)
			if tc.wantKind == "completed" || tc.wantKind == "incomplete" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("missing error")
				}
				observeDiagnosticError(providerdiag.WithObserver(context.Background(), o), err)
			}
			s := o.Snapshot()
			if s.TerminationKind != tc.wantKind || s.ValidatedTerminalEvent != tc.terminal || s.AuthoritativeUsage != tc.usage {
				t.Fatalf("snapshot=%+v err=%v", s, err)
			}
			if tc.responseID && s.ResponseIDHash != providerdiag.HashIdentifier("private-response") {
				t.Fatalf("response correlation lost: %+v", s)
			}
			if strings.Contains(s.ResponseIDHash, "private-response") {
				t.Fatal("response id leaked")
			}
		})
	}
}

func TestObserveDiagnosticErrorCancellation(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline_exceeded"}} {
		o := providerdiag.New(nil)
		observeDiagnosticError(providerdiag.WithObserver(context.Background(), o), &transportError{err: tc.err, phase: transportPhaseBody})
		if s := o.Snapshot(); s.TerminationKind != tc.kind || s.TransportPhase != "body" {
			t.Fatalf("snapshot=%+v", s)
		}
	}
}

func TestStreamDiagnosticsHTTPErrorIsCorrelatedWithoutPrivateContent(t *testing.T) {
	errorBody := `{"error":{"code":"rate_limit","message":"private-error-body"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "private-request-id")
		w.Header().Set("Set-Cookie", "private-cookie")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, errorBody)
	}))
	defer server.Close()
	o := providerdiag.New(nil)
	ctx := providerdiag.WithObserver(t.Context(), o)
	m, err := New(ctx, &Config{APIKey: "private-test-key", Model: FlashModel, BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("private-prompt")})
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("lost typed error: %v", err)
	}
	s := o.Snapshot()
	if s.HTTPStatus != 429 || s.RequestIDHash != providerdiag.HashIdentifier("private-request-id") || s.TransportPhase != "body" || s.BodyBytes != uint64(len(errorBody)) || s.TerminationKind != "api_error" || s.AuthoritativeUsage || s.ValidatedTerminalEvent {
		t.Fatalf("wrong error facts: %+v", s)
	}
	raw, _ := json.Marshal(s)
	for _, secret := range []string{"private-request-id", "private-cookie", "private-error-body", "private-test-key", "private-prompt", server.URL} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("diagnostic leaked content: %s", raw)
		}
	}
}

func TestGenerateDoesNotPublishStreamDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response","object":"response","status":"completed","model":"deepseek-flash","output":[]}`)
	}))
	defer server.Close()
	observations := 0
	ctx := providerdiag.WithObserver(t.Context(), providerdiag.New(func(providerdiag.Snapshot) { observations++ }))
	m, err := New(ctx, &Config{APIKey: "fixture", Model: FlashModel, BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("fixture")}); err != nil {
		t.Fatal(err)
	}
	if observations != 0 {
		t.Fatalf("Generate fabricated stream evidence: %d", observations)
	}
}
