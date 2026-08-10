package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type scriptedProvider struct {
	server       *httptest.Server
	manifest     scenarioManifest
	script       providerScript
	outsidePath  string
	targetPath   string
	mu           sync.Mutex
	calls        int
	toolCalls    int
	inputTokens  int
	outputTokens int
	failure      error
}

type providerResult struct {
	Calls        int
	ToolCalls    int
	InputTokens  int
	OutputTokens int
	Failure      error
}

type providerRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
	Tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tools"`
}

func newScriptedProvider(manifest scenarioManifest, script providerScript, outsidePath, targetPath string) (*scriptedProvider, error) {
	provider := &scriptedProvider{
		manifest: manifest, script: script, outsidePath: outsidePath, targetPath: targetPath,
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(provider.serveHTTP))
	tcpAddress, ok := server.Listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.IsLoopback() {
		server.Close()
		return nil, fail("provider_loopback_unavailable", nil)
	}
	server.Start()
	provider.server = server
	return provider, nil
}

func (provider *scriptedProvider) URL() string { return provider.server.URL }

func (provider *scriptedProvider) CloseAndResult() providerResult {
	provider.server.Close()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return providerResult{
		Calls: provider.calls, ToolCalls: provider.toolCalls,
		InputTokens: provider.inputTokens, OutputTokens: provider.outputTokens,
		Failure: provider.failure,
	}
}

func (provider *scriptedProvider) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.failure != nil {
		http.Error(writer, "provider script already failed", http.StatusConflict)
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/responses" || request.URL.RawQuery != "" {
		provider.reject(writer, "provider_request_drift")
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+provider.manifest.Provider.FakeAPIKey {
		provider.reject(writer, "provider_auth_drift")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		provider.reject(writer, "provider_request_bound")
		return
	}
	var decoded providerRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		provider.reject(writer, "provider_request_malformed")
		return
	}
	if decoded.Model != provider.manifest.Execution.Model || len(decoded.Tools) != 1 ||
		decoded.Tools[0].Type != "function" || decoded.Tools[0].Name != "Write" {
		provider.reject(writer, "provider_tool_surface_drift")
		return
	}
	if provider.calls >= len(provider.script.Steps) {
		provider.reject(writer, "provider_budget_exceeded")
		return
	}
	step := provider.script.Steps[provider.calls]
	provider.calls++
	provider.inputTokens += step.InputTokens
	provider.outputTokens += step.OutputTokens
	if err := provider.validatePriorOutput(step, decoded.Input); err != nil {
		provider.failure = err
		http.Error(writer, "provider sequence drift", http.StatusBadRequest)
		return
	}
	if step.Kind != "final" {
		provider.toolCalls++
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	item := provider.responseItem(step)
	if step.Kind != "final" {
		_, _ = fmt.Fprintf(writer, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", item)
	} else {
		text, _ := json.Marshal(step.Text)
		_, _ = fmt.Fprintf(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg-final\",\"output_index\":0,\"content_index\":0,\"delta\":%s}\n\n", text)
		_, _ = fmt.Fprintf(writer, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	}
	_, _ = fmt.Fprintf(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":%q,\"output\":[],\"parallel_tool_calls\":false,\"tool_choice\":\"auto\",\"tools\":[],\"usage\":{\"input_tokens\":%d,\"output_tokens\":%d,\"total_tokens\":%d}}}\n\n",
		provider.manifest.Execution.Model, step.InputTokens, step.OutputTokens, step.InputTokens+step.OutputTokens)
}

func (provider *scriptedProvider) reject(writer http.ResponseWriter, code string) {
	provider.failure = fail(code, nil)
	http.Error(writer, "provider contract rejected", http.StatusBadRequest)
}

func (provider *scriptedProvider) validatePriorOutput(step providerStep, input json.RawMessage) error {
	switch step.Kind {
	case "outside_write":
		return nil
	case "contained_write":
		previous := provider.script.Steps[0]
		output, found, err := functionOutput(input, previous.CallID)
		if err != nil || !found || !strings.Contains(output, "permission denied for tool Write") ||
			!strings.Contains(output, "headless mode: no interactive permission prompt available") {
			return fail("outside_denial_missing", err)
		}
		return nil
	case "final":
		previous := provider.script.Steps[1]
		output, found, err := functionOutput(input, previous.CallID)
		want := fmt.Sprintf("Wrote %d bytes to %s", len(previous.Content), provider.targetPath)
		if err != nil || !found || output != want {
			return fail("contained_success_missing", err)
		}
		return nil
	default:
		return fail("provider_script_invalid", nil)
	}
}

func (provider *scriptedProvider) responseItem(step providerStep) string {
	if step.Kind == "final" {
		item, _ := json.Marshal(map[string]any{
			"type": "message", "id": "msg-final", "role": "assistant", "status": "completed",
			"content": []map[string]any{{"type": "output_text", "text": step.Text, "annotations": []any{}}},
		})
		return string(item)
	}
	path := provider.targetPath
	if step.Kind == "outside_write" {
		path = provider.outsidePath
	}
	arguments, _ := json.Marshal(map[string]string{"file_path": path, "content": step.Content})
	item, _ := json.Marshal(map[string]string{
		"type": "function_call", "id": step.CallID, "call_id": step.CallID,
		"name": "Write", "arguments": string(arguments), "status": "completed",
	})
	return string(item)
}

func functionOutput(input json.RawMessage, callID string) (string, bool, error) {
	var items []struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		return "", false, err
	}
	for _, item := range items {
		if item.Type != "function_call_output" || item.CallID != callID {
			continue
		}
		var output string
		if err := json.Unmarshal(item.Output, &output); err == nil {
			return output, true, nil
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item.Output, &parts); err != nil {
			return "", true, err
		}
		var joined strings.Builder
		for _, part := range parts {
			if part.Type != "input_text" {
				return "", true, fmt.Errorf("unexpected function output part %q", part.Type)
			}
			joined.WriteString(part.Text)
		}
		return joined.String(), true, nil
	}
	return "", false, nil
}
