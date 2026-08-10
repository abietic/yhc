package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/model"
)

func TestCheckConnectivity_Reachable(t *testing.T) {
	// Create a mock server that returns 200 OK.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	// Use a custom HTTP client that routes to the mock server.
	result := CheckConnectivity(model.ProviderOpenAI, "sk-test-key", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	if result.Status != StatusReachable {
		t.Errorf("expected StatusReachable, got %q: %s", result.Status, result.Message)
	}
	if !result.IsOK() {
		t.Error("expected IsOK() = true")
	}
	if result.Latency <= 0 {
		t.Error("expected positive latency")
	}
	if result.HTTPStatus != 200 {
		t.Errorf("expected HTTP 200, got %d", result.HTTPStatus)
	}
}

func TestCheckConnectivityUsesCustomBaseURLAndContext(t *testing.T) {
	requestPath := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		BaseURL: server.URL + "/custom/v1",
	})
	if !result.IsOK() || requestPath != "/custom/v1/models" {
		t.Fatalf("custom base URL result = %#v, path = %q", result, requestPath)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result = CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		Context: canceled,
		BaseURL: server.URL + "/custom/v1",
	})
	if result.IsOK() || !strings.Contains(result.Message, "context canceled") {
		t.Fatalf("canceled context result = %#v", result)
	}
}

func TestCheckConnectivity_AuthFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_api_key"}`))
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-bad-key", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	if result.Status != StatusAuthFailed {
		t.Errorf("expected StatusAuthFailed, got %q: %s", result.Status, result.Message)
	}
	if result.IsOK() {
		t.Error("expected IsOK() = false for auth failure")
	}
	if result.HTTPStatus != 401 {
		t.Errorf("expected HTTP 401, got %d", result.HTTPStatus)
	}
}

func TestCheckConnectivity_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderAnthropic, "sk-expired", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	if result.Status != StatusAuthFailed {
		t.Errorf("expected StatusAuthFailed for 403, got %q", result.Status)
	}
}

func TestCheckConnectivity_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	if result.Status != StatusUnreachable {
		t.Errorf("expected StatusUnreachable for 500, got %q", result.Status)
	}
}

func TestCheckConnectivity_Timeout(t *testing.T) {
	// Create a server that delays longer than the timeout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		Timeout: 100 * time.Millisecond,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	if result.Status != StatusTimeout && result.Status != StatusUnreachable {
		t.Errorf("expected StatusTimeout or StatusUnreachable, got %q: %s", result.Status, result.Message)
	}
}

func TestCheckConnectivity_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	// Rate limited means the server is reachable.
	if result.Status != StatusReachable {
		t.Errorf("expected StatusReachable for 429, got %q", result.Status)
	}
}

func TestCheckConnectivity_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	result := CheckConnectivity(model.ProviderOpenAI, "sk-test", &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	})

	// 404 means the server exists.
	if result.Status != StatusReachable {
		t.Errorf("expected StatusReachable for 404, got %q", result.Status)
	}
}

func TestCheckConnectivity_UnknownProvider(t *testing.T) {
	result := CheckConnectivity(model.ProviderUnknown, "sk-test", nil)
	if result.Status != StatusUnreachable {
		t.Errorf("expected StatusUnreachable for unknown provider, got %q", result.Status)
	}
}

func TestCheckConnectivity_AuthHeaders(t *testing.T) {
	var capturedHeaders http.Header
	var capturedQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	opts := &ConnectivityCheckOptions{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: &rewriteTransport{baseURL: server.URL},
		},
	}

	// Test Anthropic headers.
	CheckConnectivity(model.ProviderAnthropic, "sk-ant-key", opts)
	if capturedHeaders.Get("x-api-key") != "sk-ant-key" {
		t.Errorf("expected x-api-key header for Anthropic, got %q", capturedHeaders.Get("x-api-key"))
	}
	if capturedHeaders.Get("anthropic-version") == "" {
		t.Error("expected anthropic-version header")
	}

	// Test OpenAI headers.
	CheckConnectivity(model.ProviderOpenAI, "sk-oai-key", opts)
	if capturedHeaders.Get("Authorization") != "Bearer sk-oai-key" {
		t.Errorf("expected Bearer auth for OpenAI, got %q", capturedHeaders.Get("Authorization"))
	}

	// Test Google query parameter.
	CheckConnectivity(model.ProviderGoogle, "google-key", opts)
	if capturedQuery == "" || !containsStr(capturedQuery, "key=google-key") {
		t.Errorf("expected key= query param for Google, got query: %q", capturedQuery)
	}
}

func TestCheckProviderConnectivity_UnknownProvider(t *testing.T) {
	result := CheckProviderConnectivity(model.ProviderUnknown)
	if result.Status != StatusUnreachable {
		t.Errorf("expected StatusUnreachable, got %q", result.Status)
	}
}

// rewriteTransport rewrites all request URLs to point to the test server.
type rewriteTransport struct {
	baseURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to the test server, preserving path and query.
	newURL := t.baseURL + req.URL.Path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}
