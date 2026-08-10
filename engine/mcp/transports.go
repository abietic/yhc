package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSETransport implements MCP transport over Server-Sent Events.
// The client sends requests via HTTP POST and receives responses/notifications
// via an SSE stream.
//
// Reference: src/services/mcp/client.ts uses SSEClientTransport from MCP SDK
type SSETransport struct {
	mu      sync.Mutex
	baseURL string
	client  *http.Client
	headers map[string]string
	eventCh chan SSEEvent
	cancel  context.CancelFunc
	closed  bool
}

// SSEEvent represents a server-sent event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

// NewSSETransport creates an SSE transport for the given MCP server URL.
func NewSSETransport(baseURL string, headers map[string]string) *SSETransport {
	return &SSETransport{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 0}, // SSE has no timeout
		headers: headers,
		eventCh: make(chan SSEEvent, 64),
	}
}

// Connect establishes the SSE stream connection.
func (t *SSETransport) Connect(ctx context.Context) error {
	ctx, t.cancel = context.WithCancel(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/sse", nil)
	if err != nil {
		return fmt.Errorf("sse transport: create request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("sse transport: connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return fmt.Errorf("sse transport: unexpected status %d", resp.StatusCode)
	}

	go t.readStream(ctx, resp.Body)
	return nil
}

func (t *SSETransport) readStream(ctx context.Context, body io.ReadCloser) {
	defer body.Close() //nolint:errcheck
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	var event SSEEvent
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			if event.Data != "" {
				t.mu.Lock()
				if !t.closed {
					t.eventCh <- event
				}
				t.mu.Unlock()
				event = SSEEvent{}
			}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			if event.Data != "" {
				event.Data += "\n"
			}
			event.Data += strings.TrimSpace(line[5:])
		} else if strings.HasPrefix(line, "id:") {
			event.ID = strings.TrimSpace(line[3:])
		}
	}
}

// Send sends a JSON-RPC request via HTTP POST.
func (t *SSETransport) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  method,
		"params":  params,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/message", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sse transport: send: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("sse transport: response status %d: %s", resp.StatusCode, string(respBody))
	}

	return json.RawMessage(respBody), nil
}

// Events returns the channel for receiving SSE events.
func (t *SSETransport) Events() <-chan SSEEvent {
	return t.eventCh
}

// Close shuts down the SSE connection.
func (t *SSETransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	close(t.eventCh)
	return nil
}

// StreamableHTTPTransport implements MCP transport over HTTP with streaming.
// Uses standard HTTP POST with optional streaming responses.
//
// Reference: src/services/mcp/client.ts uses StreamableHTTPClientTransport
type StreamableHTTPTransport struct {
	baseURL   string
	client    *http.Client
	headers   map[string]string
	sessionID string
}

// NewStreamableHTTPTransport creates a streamable HTTP transport.
func NewStreamableHTTPTransport(baseURL string, headers map[string]string) *StreamableHTTPTransport {
	return &StreamableHTTPTransport{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Minute},
		headers: headers,
	}
}

// Send sends a JSON-RPC request and returns the response.
func (t *StreamableHTTPTransport) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  method,
		"params":  params,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/mcp", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("streamable http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("streamable http: status %d: %s", resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// Close terminates the session.
func (t *StreamableHTTPTransport) Close() error {
	if t.sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+"/mcp", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", t.sessionID)
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
