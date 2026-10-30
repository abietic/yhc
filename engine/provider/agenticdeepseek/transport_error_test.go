package agenticdeepseek

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type traceFailureTransport func(*http.Request) (*http.Response, error)

func (f traceFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorReportsSafeFailurePhase(t *testing.T) {
	for _, tc := range []struct {
		phase string
		emit  func(*httptrace.ClientTrace)
	}{
		{"dns", func(trace *httptrace.ClientTrace) { trace.DNSStart(httptrace.DNSStartInfo{Host: "private-host"}) }},
		{"connect", func(trace *httptrace.ClientTrace) { trace.ConnectStart("tcp", "private-endpoint") }},
		{"tls_handshake", func(trace *httptrace.ClientTrace) { trace.TLSHandshakeStart() }},
		{"request_upload", func(trace *httptrace.ClientTrace) { trace.WroteHeaders() }},
		{"request_sent", func(trace *httptrace.ClientTrace) { trace.WroteRequest(httptrace.WroteRequestInfo{}) }},
		{"response_headers", func(trace *httptrace.ClientTrace) { trace.GotFirstResponseByte() }},
		{"tls_handshake", func(trace *httptrace.ClientTrace) {
			trace.TLSHandshakeStart()
			trace.ConnectStart("tcp", "late-private-address")
		}},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			cause := fmt.Errorf("private-token private-host: %w", context.DeadlineExceeded)
			callerTraceCalls := 0
			ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{WroteHeaders: func() { callerTraceCalls++ }})
			m, err := New(ctx, &Config{Model: FlashModel, APIKey: "sentinel-provider-credential", HTTPClient: &http.Client{Transport: traceFailureTransport(func(r *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(r.Context())
				if trace != nil && trace.DNSStart != nil && trace.ConnectStart != nil && trace.TLSHandshakeStart != nil && trace.WroteHeaders != nil && trace.WroteRequest != nil && trace.GotFirstResponseByte != nil {
					tc.emit(trace)
				}
				return nil, cause
			})}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := m.do(ctx, []byte(`{}`), true)
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "phase="+tc.phase) {
				t.Fatalf("failure lacks safe phase: %v", err)
			}
			if tc.phase == "request_upload" && callerTraceCalls != 1 {
				t.Fatal("adapter replaced caller trace")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("phase diagnostics leaked transport details")
			}
		})
	}
}

func TestTransportDeadlineAfterRequestSentBeforeResponseHeaders(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		<-release
	}))
	defer server.Close()
	defer close(release)
	var wroteRequest, gotResponse atomic.Bool
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wroteRequest.Store(true)
			}
		},
		GotFirstResponseByte: func() { gotResponse.Store(true) },
	})
	m, err := New(ctx, &Config{Model: FlashModel, APIKey: "sentinel-provider-credential", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := m.do(ctx, []byte(`{}`), true)
	if response != nil {
		_ = response.Body.Close()
	}
	if !wroteRequest.Load() || gotResponse.Load() {
		t.Fatal("fixture must finish uploading without receiving response headers")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "phase=request_sent") {
		t.Fatalf("waiting for the first response must preserve the request-sent phase: %v", err)
	}
}

func TestTransportErrorHasSafeActionableCause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"cancellation", context.Canceled, "canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"network timeout", &net.DNSError{Name: "private-host", IsTimeout: true}, "timeout"},
		{"DNS", &net.DNSError{Name: "private-host", IsNotFound: true}, "dns"},
		{"TLS", x509.UnknownAuthorityError{}, "tls"},
		{"reset", fmt.Errorf("private-endpoint: %w", syscall.ECONNRESET), "connection_reset"},
		{"refused", syscall.ECONNREFUSED, "connection_refused"},
		{"EOF", io.EOF, "connection_closed"},
		{"unexpected EOF", io.ErrUnexpectedEOF, "connection_closed"},
		{"unknown", errors.New("private-token private-endpoint private-host"), "network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &transportError{err: tc.err}
			if !errors.Is(err, tc.err) {
				t.Fatal("transport wrapper lost the underlying error identity")
			}
			formatted := err.Error()
			if !strings.Contains(formatted, "reason="+tc.code) {
				t.Fatalf("error lacks safe cause %q: %s", tc.code, formatted)
			}
			for _, private := range []string{"private-token", "private-endpoint", "private-host"} {
				if strings.Contains(formatted, private) {
					t.Fatalf("transport error leaked %q", private)
				}
			}
		})
	}
}
