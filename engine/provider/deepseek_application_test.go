package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/provider/agenticdeepseek"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestDeepSeekApplicationClassicBridgePreservesFunctionOrder(t *testing.T) {
	const response = `{"id":"r","object":"response","status":"completed","model":"deepseek-flash","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"before"}]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}","status":"completed"},{"type":"web_search_call","id":"web-1","kind":"read_link","signature":"keep-signature"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"after"}]}]}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- body
				if bytes.Contains(body, []byte(`"stream":true`)) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"output_index\":0,\"delta\":\"before\"}\n\n"+
						"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}\n\n"+
						"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"output_index\":3,\"delta\":\"after\"}\n\n"+
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":"+response+"}\n\n")
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
			input := []*schema.Message{schema.UserMessage("q")}
			opts := []model.Option{agenticdeepseek.WithApplicationConfig(agenticdeepseek.ApplicationConfig{ReadURL: true}), model.WithTools([]*schema.ToolInfo{{Name: "lookup"}})}
			var out *schema.Message
			if stream {
				sr, err := m.Stream(t.Context(), input, opts...)
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
					chunks = append(chunks, chunk)
				}
				out, err = schema.ConcatMessages(chunks)
			} else {
				out, err = m.Generate(t.Context(), input, opts...)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Name != "lookup" || out.ToolCalls[0].Function.Arguments != `{"q":"x"}` {
				t.Fatalf("local function = %#v", out.ToolCalls)
			}
			<-requests
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var restored schema.Message
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(&restored)
			if _, err := m.Generate(t.Context(), []*schema.Message{input[0], &restored, schema.ToolMessage("ok", "call-1"), schema.UserMessage("next")}); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(&restored)
			if !bytes.Equal(before, after) {
				t.Fatal("replay mutated canonical history")
			}
			wire := <-requests
			var replay struct {
				Input []struct {
					Type      string                  `json:"type"`
					Role      string                  `json:"role"`
					CallID    string                  `json:"call_id"`
					Name      string                  `json:"name"`
					Arguments string                  `json:"arguments"`
					Signature string                  `json:"signature"`
					Content   []struct{ Text string } `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(wire, &replay); err != nil {
				t.Fatal(err)
			}
			var order []string
			for _, item := range replay.Input {
				switch item.Type {
				case "message":
					if item.Role == "assistant" {
						for _, content := range item.Content {
							order = append(order, content.Text)
						}
					}
				case "function_call":
					order = append(order, item.CallID+":"+item.Name+":"+item.Arguments)
				case "web_search_call":
					order = append(order, item.Signature)
				}
			}
			if strings.Join(order, "|") != `before|call-1:lookup:{"q":"x"}|keep-signature|after` {
				t.Fatalf("interleaved replay=%s", wire)
			}
		})
	}
}

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
