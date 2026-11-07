package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebFetchBinaryNeverReachesSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "invalid PDF", body: "%PDF-1.7\ninvalid document\x00\xff"},
		{name: "image", body: "\x89PNG\r\n\x1a\n\x00\x00"},
		{name: "archive", body: "PK\x03\x04\x00\x00\x00\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// A misleading text header must not turn a binary body into model input.
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			transport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			t.Cleanup(func() { http.DefaultTransport = transport })
			m := &pageSummaryRecorder{}
			ctx := WithWebFetchModel(t.Context(), m)
			for _, raw := range []bool{false, true} {
				input, _ := json.Marshal(map[string]any{"url": server.URL, "prompt": "summarize", "raw_mode": raw})
				result, err := WebFetchTool().ExecuteCtx(ctx, string(input))
				if err == nil || result != "" || len(m.inputs) != 0 {
					t.Fatalf("raw=%t: result=%q err=%v summary calls=%d", raw, result, err, len(m.inputs))
				}
			}
		})
	}
}

func TestWebFetchPDFRangesAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pages      string
		pageCount  int
		extractErr error
		text       string
		wantError  string
		wantCalls  int
	}{
		{name: "implicit short PDF", pageCount: 3, text: "first\fsecond\fthird", wantCalls: 2},
		{name: "explicit range skips metadata", pages: "4-5", text: "fourth\ffifth", wantCalls: 1},
		{name: "large PDF needs range", pageCount: 11, wantError: "11 pages", wantCalls: 1},
		{name: "oversized range", pages: "1-21", wantError: "maximum of 20"},
		{name: "missing parser", pages: "1", extractErr: &exec.Error{Name: "pdftotext", Err: exec.ErrNotFound}, wantError: "install poppler-utils", wantCalls: 1},
		{name: "scanned PDF", pages: "1", wantError: "no extractable text", wantCalls: 1},
		{name: "parser failure", pages: "1", extractErr: errors.New("parser failed"), wantError: "cannot extract PDF text", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var temporaryPath string
			calls := 0
			runner := pdfRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
				calls++
				temporaryPath = args[0]
				if name == "pdftotext" {
					temporaryPath = args[len(args)-2]
				}
				info, err := os.Stat(temporaryPath)
				if err != nil || info.Mode().Perm()&0o077 != 0 {
					t.Fatalf("PDF temporary file is not private: info=%v error=%v", info, err)
				}
				if name == "pdfinfo" {
					return []byte(fmt.Sprintf("Pages: %d\n", tc.pageCount)), nil, nil
				}
				return []byte(tc.text), nil, tc.extractErr
			})
			result, err := extractWebFetchText(t.Context(), webFetchResponse{body: []byte("%PDF-1.7\nfixture"), finalURL: webFetchExampleURL}, tc.pages, runner)
			if calls != tc.wantCalls || (tc.wantError == "" && err != nil) || (tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError))) {
				t.Fatalf("result=%q err=%v calls=%d", result, err, calls)
			}
			if tc.wantError == "" {
				for _, page := range strings.Split(tc.text, "\f") {
					if !strings.Contains(result, page) {
						t.Fatalf("PDF page text %q missing: %q", page, result)
					}
				}
			}
			if temporaryPath != "" {
				if _, err := os.Stat(temporaryPath); !os.IsNotExist(err) {
					t.Fatalf("PDF temporary file survived extraction: %v", err)
				}
			}
		})
	}
}

func TestWebFetchDownloadFollowsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = transport })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	m := &pageSummaryRecorder{}
	input, _ := json.Marshal(map[string]any{"url": server.URL, "prompt": "summarize"})
	result, err := WebFetchTool().ExecuteCtx(WithWebFetchModel(ctx, m), string(input))
	if !errors.Is(err, context.Canceled) || result != "" || len(m.inputs) != 0 {
		t.Fatalf("cancelled download: result=%q err=%v model calls=%d", result, err, len(m.inputs))
	}
}

func TestWebFetchDownloadRejectsOversizedBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", webFetchMaxDownloadBytes+1))
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = transport })
	_, err := fetchPage(t.Context(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Fatalf("oversized body error=%v", err)
	}
}

func TestWebFetchPDFExtractsBeforeSummary(t *testing.T) {
	bin := t.TempDir()
	for name, output := range map[string]string{"pdfinfo": "Pages: 1", "pdftotext": "A < B & C. Extracted PDF text."} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.7\nparser fixture\x00\xff")
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = transport })
	m := &pageSummaryRecorder{}
	ctx := WithWebFetchModel(context.Background(), m)
	input, _ := json.Marshal(map[string]any{"url": server.URL, "prompt": "summarize", "raw_mode": true})
	result, err := WebFetchTool().ExecuteCtx(ctx, string(input))
	if err != nil || !strings.Contains(result, "A < B & C.") || strings.Contains(result, "%PDF-") || len(m.inputs) != 0 {
		t.Fatalf("raw PDF result=%q err=%v model calls=%d", result, err, len(m.inputs))
	}
	input, _ = json.Marshal(map[string]any{"url": server.URL, "prompt": "summarize"})
	result, err = WebFetchTool().ExecuteCtx(ctx, string(input))
	if err != nil || result != "summary" || len(m.inputs) != 1 || !strings.Contains(m.inputs[0], "A < B & C.") || strings.Contains(m.inputs[0], "%PDF-") {
		t.Fatalf("PDF summary=%q err=%v inputs=%v", result, err, m.inputs)
	}
}
