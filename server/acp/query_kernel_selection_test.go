package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

func TestP1310ACPNewSessionUsesFullProjectGraph(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          cwd,
		ToolsFlag:    []string{""},
		ToolsFlagSet: true,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	agent.mockModel = &mockChatModel{responses: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "graph-acp",
	}}}
	defer agent.Close()

	created, err := agent.NewSession(
		context.Background(),
		acpsdk.NewSessionRequest{
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	response, err := agent.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q", response.StopReason)
	}
	if got := agent.mockModel.(*mockChatModel).CallCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	agent.mu.Lock()
	active := agent.sessions[created.SessionId]
	agent.mu.Unlock()
	if active == nil ||
		active.Engine.SessionID() != string(created.SessionId) ||
		active.Engine.ThreadID() != string(created.SessionId) {
		t.Fatalf("ACP/engine session identity diverged: %#v", active)
	}

	loaded, err := transcript.NewRecorder(
		string(created.SessionId),
		acpSessionTranscriptDir(cwd),
	).LoadFull()
	if err != nil {
		t.Fatalf("load transcript: %v", err)
	}
	meta := session.ReadSessionMetadataFull(loaded)
	if meta == nil ||
		meta.QueryKernelVersion != "project_graph/v1" ||
		meta.QueryKernelStage != "full" ||
		meta.QueryKernelIncompatibility != "" {
		t.Fatalf("ACP query kernel metadata = %#v", meta)
	}
}

func TestP1310ACPLoadUnsupportedSessionFailsBeforeActivationOrRewrite(
	t *testing.T,
) {
	tests := []struct {
		name     string
		metadata *session.SessionMetadataFull
	}{
		{name: "unpinned"},
		{
			name: "retired legacy",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "legacy/v1",
			},
		},
		{
			name: "unknown version",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v2",
				QueryKernelStage:   "full",
			},
		},
		{
			name: "invalid stage",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v1",
				QueryKernelStage:   "future-stage",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			transcriptDir := acpSessionTranscriptDir(cwd)
			const sessionID = "unsupported-acp-session"
			recorder := transcript.NewRecorder(sessionID, transcriptDir)
			if err := recorder.Replace([]*schema.Message{
				{Role: schema.User, Content: "old prompt"},
				{Role: schema.Assistant, Content: "old answer"},
			}); err != nil {
				t.Fatal(err)
			}
			if test.metadata != nil {
				test.metadata.SessionID = sessionID
				test.metadata.ThreadID = sessionID
				test.metadata.CWD = cwd
				test.metadata.CreatedAt = time.Now().UTC()
				test.metadata.UpdatedAt = time.Now().UTC()
				if err := session.WriteSessionMetadata(
					recorder,
					test.metadata,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}

			model := &mockChatModel{}
			agent, err := NewAgent(Config{
				ProviderFlag: "mock",
				ModelFlag:    "mock-model",
				APIKeyFlag:   "mock-key",
				YoloMode:     true,
				MaxTurns:     10,
				CWD:          cwd,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(agent.Close)
			agent.mockModel = model

			_, err = agent.LoadSession(
				context.Background(),
				acpsdk.LoadSessionRequest{
					SessionId: acpsdk.SessionId(sessionID),
					Cwd:       cwd,
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), "query kernel") {
				t.Fatalf("ACP load error = %v", err)
			}
			agent.mu.Lock()
			_, active := agent.sessions[acpsdk.SessionId(sessionID)]
			agent.mu.Unlock()
			if active || model.CallCount() != 0 {
				t.Fatalf(
					"rejected ACP load activated=%v model_calls=%d",
					active,
					model.CallCount(),
				)
			}
			after, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected ACP load rewrote source transcript")
			}
		})
	}
}
