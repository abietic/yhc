package acp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

func TestP234bInitializeCapabilityTruth(t *testing.T) {
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	response, err := agent.Initialize(
		context.Background(),
		acpsdk.InitializeRequest{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !response.AgentCapabilities.LoadSession {
		t.Fatal("loadSession must be advertised after durable replay lands")
	}
	if response.AgentInfo == nil ||
		response.AgentInfo.Name != "yhc" ||
		response.AgentInfo.Title == nil ||
		*response.AgentInfo.Title != "YHC — Yet Hooked on Coding" ||
		response.AgentInfo.Version == "" {
		t.Fatalf("agentInfo = %#v", response.AgentInfo)
	}
	sessionCapabilities := response.AgentCapabilities.SessionCapabilities
	if sessionCapabilities.List == nil ||
		sessionCapabilities.Resume == nil ||
		sessionCapabilities.Close == nil ||
		sessionCapabilities.Delete == nil {
		t.Fatalf("session capabilities = %#v", sessionCapabilities)
	}
	promptCapabilities := response.AgentCapabilities.PromptCapabilities
	if promptCapabilities.Audio ||
		!promptCapabilities.Image ||
		!promptCapabilities.EmbeddedContext {
		t.Fatalf("prompt capabilities = %#v", promptCapabilities)
	}
	mcpCapabilities := response.AgentCapabilities.McpCapabilities
	if mcpCapabilities.Acp ||
		mcpCapabilities.Http ||
		mcpCapabilities.Sse {
		t.Fatalf("MCP capabilities = %#v", mcpCapabilities)
	}
}

func TestP23H1P300PromptPreservesResourceLinkOnlyAndMixedOrderWithoutFetch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks func() []acpsdk.ContentBlock
		want   string
	}{
		{
			name: "resource only",
			blocks: func() []acpsdk.ContentBlock {
				return []acpsdk.ContentBlock{
					acpsdk.ResourceLinkBlock(
						"context",
						"file:///p30-resource-link-must-not-be-fetched",
					),
				}
			},
			want: `<resource_link>{"type":"resource_link","uri":"file:///p30-resource-link-must-not-be-fetched","name":"context"}</resource_link>`,
		},
		{
			name: "mixed order and metadata",
			blocks: func() []acpsdk.ContentBlock {
				title := "Schema"
				description := "Current API"
				mimeType := "application/json"
				size := 42
				resource := acpsdk.ResourceLinkBlock(
					"schema.json",
					"file:///workspace/schema.json",
				)
				resource.ResourceLink.Title = &title
				resource.ResourceLink.Description = &description
				resource.ResourceLink.MimeType = &mimeType
				resource.ResourceLink.Size = &size
				return []acpsdk.ContentBlock{
					acpsdk.TextBlock("before"),
					resource,
					acpsdk.TextBlock("after"),
				}
			},
			want: "before" +
				`<resource_link>{"type":"resource_link","uri":"file:///workspace/schema.json","name":"schema.json","title":"Schema","description":"Current API","mimeType":"application/json","size":42}</resource_link>` +
				"after",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChatModel{responses: []*schema.Message{{
				Role: schema.Assistant, Content: "done",
			}}}
			conn, _, agent := setupTestACPWithAgent(t, model)
			sessionResponse, err := conn.NewSession(
				t.Context(),
				acpsdk.NewSessionRequest{
					Cwd:        t.TempDir(),
					McpServers: []acpsdk.McpServer{},
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			response, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
				SessionId: sessionResponse.SessionId,
				Prompt:    tc.blocks(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.StopReason != acpsdk.StopReasonEndTurn {
				t.Fatalf("stop reason = %q", response.StopReason)
			}
			if model.CallCount() != 1 {
				t.Fatalf("model calls = %d", model.CallCount())
			}

			agent.mu.Lock()
			active := agent.sessions[sessionResponse.SessionId]
			agent.mu.Unlock()
			if active == nil {
				t.Fatal("active session missing")
			}
			messages := active.Engine.GetMessages()
			if len(messages) == 0 ||
				messages[0].Role != schema.User ||
				messages[0].Content != tc.want {
				var got string
				if len(messages) > 0 {
					got = messages[0].Content
				}
				t.Fatalf("user content = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestP23H1ReservedResourceMetadataIsNotModelVisible(t *testing.T) {
	lastModified := "2026-07-27T00:00:00Z"
	priority := 0.5
	input, err := promptInputFromACP([]acpsdk.ContentBlock{{
		ResourceLink: &acpsdk.ContentBlockResourceLink{
			Uri:  "file:///workspace/context",
			Name: "context",
			Meta: map[string]any{"private": "must-not-appear"},
			Annotations: &acpsdk.Annotations{
				Meta:         map[string]any{"private": "must-not-appear"},
				Audience:     []acpsdk.Role{acpsdk.RoleAssistant},
				LastModified: &lastModified,
				Priority:     &priority,
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := input.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := `<resource_link>{"type":"resource_link","uri":"file:///workspace/context","name":"context","annotations":{"audience":["assistant"],"lastModified":"2026-07-27T00:00:00Z","priority":0.5}}</resource_link>`
	if rendered != want {
		t.Fatalf("rendered prompt = %q, want %q", rendered, want)
	}
}

func TestP305aPromptRejectsAudioBeforeMutation(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "must not run",
	}}}
	conn, _, agent := setupTestACPWithAgent(t, model)
	sessionResponse, err := conn.NewSession(
		t.Context(),
		acpsdk.NewSessionRequest{
			Cwd:        t.TempDir(),
			McpServers: []acpsdk.McpServer{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	active := agent.sessions[sessionResponse.SessionId]
	agent.mu.Unlock()

	_, err = conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionResponse.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("must not be committed"),
			acpsdk.AudioBlock("private-data", "audio/wav"),
		},
	})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != codeUnsupportedInput {
		t.Fatalf("request error = %#v", requestErr)
	}
	data, ok := requestErr.Data.(map[string]any)
	if !ok ||
		data["input"] != "prompt.audio" ||
		data["reason"] != "capability_unsupported" ||
		data["block"] != float64(1) {
		t.Fatalf("error data = %#v", requestErr.Data)
	}
	if model.CallCount() != 0 || len(active.Engine.GetMessages()) != 0 {
		t.Fatalf(
			"unsupported prompt mutated state: calls=%d messages=%#v",
			model.CallCount(),
			active.Engine.GetMessages(),
		)
	}
}

func TestP23H1MalformedPromptPrecedesUnknownSessionWithoutContentLeak(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "must not run",
	}}}
	conn, _ := setupTestACP(t, model)
	block := acpsdk.ResourceLinkBlock("private-resource-name", "")

	_, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: "private-missing-session",
		Prompt:    []acpsdk.ContentBlock{block},
	})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if requestErr.Code != CodeInvalidParams ||
		requestErr.Message != "Invalid params" {
		t.Fatalf("request error = %#v", requestErr)
	}
	data, ok := requestErr.Data.(map[string]any)
	if !ok ||
		data["input"] != "prompt.resourceLink" ||
		data["reason"] != "resource_uri_required" ||
		data["block"] != float64(0) ||
		len(data) != 3 {
		t.Fatalf("error data = %#v", requestErr.Data)
	}
	for _, secret := range []string{
		"private-resource-name",
		"private-missing-session",
	} {
		if containsRequestErrorFact(requestErr, secret) {
			t.Fatalf("request error leaked private input: %v", requestErr)
		}
	}
	if model.CallCount() != 0 {
		t.Fatalf("malformed prompt reached model %d times", model.CallCount())
	}
}

func TestP235SessionSetupStillRejectsAdditionalDirectoriesBeforeMCP(t *testing.T) {
	mcpServers := []acpsdk.McpServer{{
		Stdio: &acpsdk.McpServerStdio{
			Name: "test", Command: "test", Args: []string{}, Env: []acpsdk.EnvVariable{},
		},
	}}
	for _, tc := range []struct {
		name      string
		request   acpsdk.NewSessionRequest
		wantInput string
	}{
		{
			name: "new additional directories through SDK",
			request: acpsdk.NewSessionRequest{
				Cwd:                   t.TempDir(),
				AdditionalDirectories: []string{t.TempDir()},
				McpServers:            []acpsdk.McpServer{},
			},
			wantInput: "session.additionalDirectories",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChatModel{responses: []*schema.Message{{
				Role: schema.Assistant, Content: "must not run",
			}}}
			conn, _, agent := setupTestACPWithAgent(t, model)
			_, err := conn.NewSession(t.Context(), tc.request)
			requireP23H1UnsupportedError(t, err, tc.wantInput)

			agent.mu.Lock()
			sessionCount := len(agent.sessions)
			agent.mu.Unlock()
			if sessionCount != 0 || model.CallCount() != 0 {
				t.Fatalf(
					"sessions=%d modelCalls=%d",
					sessionCount,
					model.CallCount(),
				)
			}
		})
	}

	for _, tc := range []struct {
		name      string
		wantInput string
		invoke    func(*Agent) error
	}{
		{
			name:      "resume additional directories",
			wantInput: "session.additionalDirectories",
			invoke: func(agent *Agent) error {
				_, err := agent.ResumeSession(
					context.Background(),
					acpsdk.ResumeSessionRequest{
						SessionId:             "missing",
						Cwd:                   t.TempDir(),
						AdditionalDirectories: []string{t.TempDir()},
					},
				)
				return err
			},
		},
		{
			name:      "load additional directories",
			wantInput: "session.additionalDirectories",
			invoke: func(agent *Agent) error {
				_, err := agent.LoadSession(
					context.Background(),
					acpsdk.LoadSessionRequest{
						SessionId:             "missing",
						Cwd:                   t.TempDir(),
						AdditionalDirectories: []string{t.TempDir()},
						McpServers:            []acpsdk.McpServer{},
					},
				)
				return err
			},
		},
		{
			name:      "additional directories win deterministic precedence",
			wantInput: "session.additionalDirectories",
			invoke: func(agent *Agent) error {
				_, err := agent.NewSession(
					context.Background(),
					acpsdk.NewSessionRequest{
						Cwd:                   t.TempDir(),
						AdditionalDirectories: []string{t.TempDir()},
						McpServers:            mcpServers,
					},
				)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChatModel{responses: []*schema.Message{{
				Role: schema.Assistant, Content: "must not run",
			}}}
			agent, err := NewAgent(Config{
				CWD: t.TempDir(), ProviderFlag: "mock", ModelFlag: "mock",
			})
			if err != nil {
				t.Fatal(err)
			}
			agent.mockModel = model
			t.Cleanup(agent.Close)

			requireP23H1UnsupportedError(t, tc.invoke(agent), tc.wantInput)
			agent.mu.Lock()
			sessionCount := len(agent.sessions)
			agent.mu.Unlock()
			if sessionCount != 0 || model.CallCount() != 0 {
				t.Fatalf(
					"sessions=%d modelCalls=%d",
					sessionCount,
					model.CallCount(),
				)
			}
		})
	}
}

func TestP234bListUsesBoundedOpaqueCursorAndRejectsMalformed(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	agent.mu.Lock()
	for index := 0; index < 27; index++ {
		id := acpsdk.SessionId(
			"active-" + string(rune('a'+index)),
		)
		active := newSession(id, nil, cwd)
		active.CreatedAt = base.Add(time.Duration(index) * time.Minute)
		agent.sessions[id] = active
	}
	agent.mu.Unlock()

	first, err := agent.ListSessions(
		context.Background(),
		acpsdk.ListSessionsRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 25 ||
		first.NextCursor == nil ||
		*first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := agent.ListSessions(
		context.Background(),
		acpsdk.ListSessionsRequest{Cursor: first.NextCursor},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 2 || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}
	seen := make(map[acpsdk.SessionId]struct{}, 27)
	for _, page := range [][]acpsdk.SessionInfo{first.Sessions, second.Sessions} {
		for _, info := range page {
			if _, duplicate := seen[info.SessionId]; duplicate {
				t.Fatalf("duplicate session %s across pages", info.SessionId)
			}
			seen[info.SessionId] = struct{}{}
		}
	}

	agent.mu.Lock()
	staleID := acpsdk.SessionId("active-stale")
	stale := newSession(staleID, nil, cwd)
	stale.CreatedAt = base.Add(2 * time.Hour)
	agent.sessions[staleID] = stale
	agent.mu.Unlock()
	_, err = agent.ListSessions(
		context.Background(),
		acpsdk.ListSessionsRequest{Cursor: first.NextCursor},
	)
	var staleErr *acpsdk.RequestError
	if !errors.As(err, &staleErr) ||
		staleErr.Code != CodeInvalidParams {
		t.Fatalf("stale cursor error = %v", err)
	}

	malformed := "not-a-cursor"
	_, err = agent.ListSessions(
		context.Background(),
		acpsdk.ListSessionsRequest{Cursor: &malformed},
	)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != CodeInvalidParams {
		t.Fatalf("malformed cursor error = %v", err)
	}
}

func requireP23H1UnsupportedError(
	t *testing.T,
	err error,
	wantInput string,
) {
	t.Helper()
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if requestErr.Code != codeUnsupportedInput ||
		requestErr.Message != "Unsupported input" {
		t.Fatalf("request error = %#v", requestErr)
	}
	data, ok := requestErr.Data.(map[string]any)
	if !ok || data["input"] != wantInput || len(data) != 1 {
		t.Fatalf("error data = %#v", requestErr.Data)
	}
	if requestErr.Error() == "" {
		t.Fatal("request error string is empty")
	}
}

func containsRequestErrorFact(requestErr *acpsdk.RequestError, fact string) bool {
	if requestErr == nil {
		return false
	}
	return strings.Contains(requestErr.Error(), fact)
}
