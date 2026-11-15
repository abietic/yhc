package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/provider/agenticdeepseek"
	"github.com/cloudwego/eino/schema"
)

func TestDeepSeekApplicationClassicBridgePreservesPublicReplay(t *testing.T) {
	const response = `{"id":"resp-app","object":"response","status":"completed","model":"deepseek-flash","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"before"}]},{"type":"web_search_call","id":"read-1","status":"completed","kind":"read_link","action":{"type":"open_page","url":"https://example.com"},"page":{"url":"https://example.com"},"signature":"public-replay-signature"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[stream], func(t *testing.T) {
			requests := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- string(body)
				if strings.Contains(string(body), `"stream":true`) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"output_index\":0,\"delta\":\"before\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"output_index\":2,\"delta\":\"answer\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":"+response+"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}
			}))
			defer server.Close()
			m, err := NewChatModel(t.Context(), Config{Provider: ProviderAgenticDeepSeek, Model: "deepseek-flash", APIKey: "fixture", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			input := []*schema.Message{schema.UserMessage("summarize the link")}
			option := agenticdeepseek.WithApplicationConfig(agenticdeepseek.ApplicationConfig{ReadURL: true})
			var out *schema.Message
			if stream {
				sr, err := m.Stream(t.Context(), input, option)
				if err != nil {
					t.Fatal(err)
				}
				defer sr.Close()
				var chunks []*schema.Message
				for {
					chunk, err := sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if chunk != nil {
						chunks = append(chunks, chunk)
					}
				}
				out, err = schema.ConcatMessages(chunks)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				out, err = m.Generate(t.Context(), input, option)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(out.ToolCalls) != 0 {
				t.Fatal("server tool exposed as a local function")
			}
			first := <-requests
			if !strings.Contains(first, `"type":"deepseek_read_url"`) {
				t.Fatalf("request=%s", first)
			}
			out.Extra["private_continuation_marker"] = "must-not-replay"
			raw, _ := json.Marshal(out)
			var restored schema.Message
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Generate(t.Context(), []*schema.Message{input[0], &restored, schema.UserMessage("follow up")}); err != nil {
				t.Fatal(err)
			}
			wire := <-requests
			before := strings.Index(wire, `"text":"before"`)
			serverIndex := strings.Index(wire, `"signature":"public-replay-signature"`)
			after := strings.Index(wire, `"text":"answer"`)
			if before < 0 || serverIndex < before || after < serverIndex {
				t.Fatalf("interleaved replay=%s", wire)
			}
			if !strings.Contains(wire, `"signature":"public-replay-signature"`) || strings.Contains(wire, "must-not-replay") || strings.Contains(wire, `"tools"`) {
				t.Fatalf("replay=%s", wire)
			}
		})
	}
}
