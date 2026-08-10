package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

// TestACPRecoveryCascade exercises the ACP external entry point's recovery
// cascade when the model returns prompt-too-long (413) errors twice before
// eventually succeeding. It verifies:
//   - the model is called exactly three times;
//   - the prompt response reports StopReason=end_turn;
//   - the client receives at least one _session/status extension with
//     status=compaction (from collapse drain on the first 413);
//   - the final assistant text is delivered to the client via SessionUpdate;
//   - the test is deterministic, uses no network, and uses no real provider.
func TestACPRecoveryCascade(t *testing.T) {
	const successText = "Recovered successfully and here is the answer."

	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{
				Role:    schema.Assistant,
				Content: "API error 413",
				Extra: map[string]any{
					"api_error":  true,
					"error_type": "413",
				},
			},
			{
				Role:    schema.Assistant,
				Content: "API error 413 again",
				Extra: map[string]any{
					"api_error":  true,
					"error_type": "413",
				},
			},
			{Role: schema.Assistant, Content: successText},
		},
	}

	conn, client, agent := setupTestACPWithAgent(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// White-box access: inject resumed history so the first 413 has enough
	// drainable context to perform a collapse drain.
	agent.mu.Lock()
	acpSess := agent.sessions[sess.SessionId]
	agent.mu.Unlock()
	if acpSess == nil {
		t.Fatal("session not found in agent after NewSession")
	}

	history := make([]*schema.Message, 0, 8)
	for i := 0; i < 4; i++ {
		history = append(history, &schema.Message{Role: schema.User, Content: fmt.Sprintf("history user %d", i)})
		history = append(history, &schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("history assistant %d", i)})
	}
	acpSess.Engine.SetResumedMessages(history)

	promptResp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("please answer")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if promptResp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected stop reason %q, got %q", acpsdk.StopReasonEndTurn, promptResp.StopReason)
	}

	if got := mockModel.CallCount(); got != 3 {
		t.Fatalf("expected exactly 3 model calls, got %d", got)
	}

	extensions := client.getExtensions()
	var sawCompaction bool
	for _, ext := range extensions {
		if ext.Method != "_session/status" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(ext.Params, &payload) != nil {
			continue
		}
		if status, _ := payload["status"].(string); status == "compaction" {
			sawCompaction = true
			break
		}
	}
	if !sawCompaction {
		t.Fatalf("expected at least one _session/status extension with status=compaction, got %+v", extensions)
	}

	updates := client.getUpdates()
	var sawSuccess bool
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			if strings.Contains(u.Update.AgentMessageChunk.Content.Text.Text, successText) {
				sawSuccess = true
				break
			}
		}
	}
	if !sawSuccess {
		t.Fatalf("expected final success text %q in SessionUpdate, got updates %+v", successText, updates)
	}
}
