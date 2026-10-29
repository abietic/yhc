package agenticglm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestGLM53FamilyAndPerCallMediaBoundary(t *testing.T) {
	for _, name := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx"} {
		client, err := New(context.Background(), &Config{APIKey: "test", Model: name})
		if err != nil {
			t.Errorf("New(%s): %v", name, err)
			continue
		}
		common, specific := client.options()
		req, err := buildChatRequest([]*schema.AgenticMessage{schema.UserAgenticMessage("hello")}, common, specific, true)
		if err != nil || req.Model != name || req.Thinking.Type != "enabled" {
			t.Fatalf("%s request = %#v, %v", name, req, err)
		}
		media := []*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/red.png"}),
		}}}
		_, err = buildChatRequest(media, common, specific, true)
		if (err != nil) != (name == "glm-5.3") {
			t.Errorf("media on %s: %v", name, err)
		}
		common, specific = client.options(model.WithModel("glm-5.3"))
		if _, err := buildChatRequest(media, common, specific, true); err == nil {
			t.Errorf("%s per-call text-only override accepted media", name)
		}
	}
}

func TestStreamFinalUsageAfterFinishIsPublishedExactlyOnce(t *testing.T) {
	stream := `data: {"id":"usage","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"content":"ok"}}]}

data: {"id":"usage","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"usage","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}}}

data: [DONE]

`
	terminals := 0
	err := parseChatStream(strings.NewReader(stream), 4096, func(message *schema.AgenticMessage) bool {
		if message.ResponseMeta != nil {
			terminals++
			u := message.ResponseMeta.TokenUsage
			if u == nil || u.TotalTokens != 18 || u.PromptTokenDetails.CachedTokens != 3 || u.CompletionTokensDetails.ReasoningTokens != 4 {
				t.Errorf("terminal usage = %#v", u)
			}
		}
		return false
	})
	if err != nil || terminals != 1 {
		t.Fatalf("stream: terminals=%d, err=%v", terminals, err)
	}
}

func TestInvalidUsageCannotBecomeKnownZeroOrNegativeCost(t *testing.T) {
	for _, usage := range []string{
		`{}`, `{"prompt_tokens":1,"completion_tokens":2}`,
		`{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":2}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":3}}`,
	} {
		var response chatResponse
		if err := json.Unmarshal([]byte(`{"id":"usage","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":`+usage+`}`), &response); err != nil {
			t.Fatal(err)
		}
		if _, err := responseToAgentic(&response); err == nil {
			t.Errorf("invalid usage accepted: %s", usage)
		}
	}
}

func TestStreamRejectsContentAfterFinishAndPrematureUsageOnly(t *testing.T) {
	for _, stream := range []string{
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\ndata: [DONE]\n\n",
		"data: {\"id\":\"x\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\ndata: [DONE]\n\n",
	} {
		if err := parseChatStream(strings.NewReader(stream), 4096, func(*schema.AgenticMessage) bool { return false }); err == nil {
			t.Fatal("invalid post-finish stream accepted")
		}
	}
}
