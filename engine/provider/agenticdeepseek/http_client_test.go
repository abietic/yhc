package agenticdeepseek

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestDefaultHTTPClientKeepsBoundedTLSBudgetAndGlobalTransport(t *testing.T) {
	global, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected standard default transport")
	}
	originalTLSBudget := global.TLSHandshakeTimeout
	m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", Model: FlashModel, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewFilesClient(&FilesConfig{APIKey: "sentinel-provider-credential", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []*http.Client{m.httpClient, files.httpClient} {
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatal("DeepSeek must own a cloned default transport")
		}
		t.Cleanup(transport.CloseIdleConnections)
		if transport == global || transport.TLSHandshakeTimeout != 30*time.Second {
			t.Fatal("DeepSeek TLS budget must be 30s without mutating the global transport")
		}
		if transport.ForceAttemptHTTP2 != global.ForceAttemptHTTP2 ||
			transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("TLS budgeting must not disable HTTP/2 or certificate verification")
		}
	}
	if global.TLSHandshakeTimeout != originalTLSBudget || m.httpClient.Timeout != 2*time.Second || files.httpClient.Timeout != 3*time.Second {
		t.Fatal("TLS budgeting changed the global transport or request timeout")
	}
	if m.httpClient.Transport != files.httpClient.Transport {
		t.Fatal("default Responses and Files clients must retain connection-pool reuse")
	}
}

func TestExplicitHTTPClientRemainsAuthoritative(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", Model: FlashModel, Timeout: 5 * time.Second, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewFilesClient(&FilesConfig{APIKey: "sentinel-provider-credential", Timeout: 5 * time.Second, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if m.httpClient != client || files.httpClient != client || client.Timeout != time.Second || client.Transport != nil {
		t.Fatal("the caller's HTTP client must not be cloned or changed")
	}
}

type callerDefaultTransport struct{}

func (*callerDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request on construction-only fixture")
}

func TestDefaultHTTPClientRespectsCallerNonstandardGlobalTransport(t *testing.T) {
	// Keep sequential: the process-wide default is caller-owned and mutable.
	original := http.DefaultTransport
	replacement := &callerDefaultTransport{}
	http.DefaultTransport = replacement
	t.Cleanup(func() { http.DefaultTransport = original })
	m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", Model: FlashModel, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewFilesClient(&FilesConfig{APIKey: "sentinel-provider-credential", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if m.httpClient.Transport != replacement || files.httpClient.Transport != replacement || http.DefaultTransport != replacement {
		t.Fatal("nonstandard global transport must remain caller-owned")
	}
	if m.httpClient.Timeout != time.Second || files.httpClient.Timeout != 2*time.Second {
		t.Fatal("the caller's transport must not change request budgets")
	}
}

func TestDefaultHTTPClientSharesPoolDuringConcurrentConstruction(t *testing.T) {
	original := http.DefaultTransport
	source := original.(*http.Transport).Clone()
	http.DefaultTransport = source
	t.Cleanup(func() { http.DefaultTransport = original; source.CloseIdleConnections() })
	type result struct {
		client  *http.Client
		timeout time.Duration
	}
	results := make(chan result, 32)
	for index := range cap(results) {
		go func() {
			timeout := time.Duration(index+1) * time.Second
			results <- result{client: newDefaultHTTPClient(timeout), timeout: timeout}
		}()
	}
	var shared http.RoundTripper
	for range cap(results) {
		entry := <-results
		if shared == nil {
			shared = entry.client.Transport
		}
		if entry.client.Transport != shared || entry.client.Timeout != entry.timeout {
			t.Fatal("concurrent construction split the connection pool or request budgets")
		}
	}
	if shared == source {
		t.Fatal("provider TLS policy mutated the caller's global transport")
	}
}

func TestDefaultHTTPClientHonorsCancellationDuringTLSHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() { _ = listener.Close(); close(release); <-finished }()
	go func() {
		defer close(finished)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		<-release // Deliberately never answer the TLS ClientHello.
	}()
	m, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", Model: FlashModel, BaseURL: "https://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.httpClient.CloseIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, callErr := m.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
		result <- callErr
	}()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS connection was not started")
	}
	cancel()
	select {
	case callErr := <-result:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatal("TLS handshake did not preserve cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt the TLS handshake")
	}
}
