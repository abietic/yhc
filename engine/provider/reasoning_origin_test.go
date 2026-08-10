package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/internal/providerorigin"
)

type originCaptureAgenticModel struct {
	mu    sync.Mutex
	input [][]*schema.AgenticMessage
}

func (m *originCaptureAgenticModel) Generate(
	_ context.Context,
	input []*schema.AgenticMessage,
	_ ...model.Option,
) (*schema.AgenticMessage, error) {
	m.mu.Lock()
	m.input = append(m.input, append([]*schema.AgenticMessage(nil), input...))
	m.mu.Unlock()
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: "ok"},
		}},
	}, nil
}

func (m *originCaptureAgenticModel) Stream(
	_ context.Context,
	input []*schema.AgenticMessage,
	_ ...model.Option,
) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.mu.Lock()
	m.input = append(m.input, append([]*schema.AgenticMessage(nil), input...))
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: "ok"},
		}},
	}}), nil
}

func (m *originCaptureAgenticModel) lastInput() []*schema.AgenticMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.input) == 0 {
		return nil
	}
	return m.input[len(m.input)-1]
}

type fixedOriginResolver struct {
	resolution providerorigin.BindingResolution
}

func (r fixedOriginResolver) ResolveAssistantOrigin(
	*schema.Message,
) providerorigin.BindingResolution {
	return r.resolution
}

func runtimeOriginForTest(t *testing.T, runtime *Runtime) providerorigin.Origin {
	t.Helper()
	runtime.routes.mu.Lock()
	defer runtime.routes.mu.Unlock()
	for _, published := range runtime.routes.published {
		return (&preparedRoute{
			published: published,
			apiModel:  runtime.Main.Model,
		}).origin()
	}
	t.Fatal("runtime has no published route")
	return providerorigin.Origin{}
}

func assertCapturedPrivateReasoning(
	t *testing.T,
	captured []*schema.AgenticMessage,
	want bool,
) {
	t.Helper()
	if len(captured) != 1 {
		t.Fatalf("captured messages = %d, want 1", len(captured))
	}
	message := captured[0]
	generated := message.ResponseMeta != nil && message.ResponseMeta.OpenAIExtension != nil
	hasReasoning := false
	for _, block := range message.ContentBlocks {
		if block.Type == schema.ContentBlockTypeReasoning {
			hasReasoning = true
		}
	}
	if generated != want || hasReasoning != want {
		t.Fatalf(
			"private continuation typed marker=%v reasoning=%v, want %v; message=%#v",
			generated,
			hasReasoning,
			want,
			message,
		)
	}
}

func TestRoutingChatModelAllowsOnlyExactPublishedOrigin(t *testing.T) {
	t.Parallel()
	secret := "secret-1"
	originID := "local-record/r1"
	captures := make([]*originCaptureAgenticModel, 0)
	runtime, err := NewRuntime(context.Background(), RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-5.4",
			},
			CredentialLookup: func(string) (string, bool, error) {
				return secret, true, nil
			},
			CredentialOriginLookup: func(string) (string, string, bool, error) {
				return secret, originID, true, nil
			},
		},
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			capture := &originCaptureAgenticModel{}
			captures = append(captures, capture)
			return wrapAgenticModel(capture), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	message := &schema.Message{
		Role:             schema.Assistant,
		Content:          "public answer",
		ReasoningContent: "private reasoning",
		Extra:            map[string]any{"message_id": "assistant-1"},
	}
	exact := runtimeOriginForTest(t, runtime)
	ctx := providerorigin.WithBindingResolver(
		context.Background(),
		fixedOriginResolver{resolution: providerorigin.BindingResolution{
			State:  providerorigin.BindingVerified,
			Origin: exact,
		}},
	)
	if _, err := runtime.ChatModel.Generate(ctx, []*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	assertCapturedPrivateReasoning(t, captures[len(captures)-1].lastInput(), true)
	stream, err := runtime.ChatModel.Stream(ctx, []*schema.Message{message})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	assertCapturedPrivateReasoning(t, captures[len(captures)-1].lastInput(), true)

	if _, err := runtime.ChatModel.Generate(
		context.Background(),
		[]*schema.Message{message},
	); err != nil {
		t.Fatal(err)
	}
	assertCapturedPrivateReasoning(t, captures[len(captures)-1].lastInput(), false)

	secret = "secret-2"
	originID = "local-record/r2"
	if _, err := runtime.ChatModel.Generate(ctx, []*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 {
		t.Fatalf("client publications = %d, want 2 after rotation", len(captures))
	}
	assertCapturedPrivateReasoning(t, captures[len(captures)-1].lastInput(), false)
	if diagnostics := runtime.ReasoningOriginDiagnostics(); !hasOriginDiagnostic(diagnostics, "generate", providerorigin.ReasonCredentialMismatch) {
		t.Fatalf("rotation diagnostics = %#v", diagnostics)
	}
}

func TestRoutePublicationRejectsOlderCredentialResolution(t *testing.T) {
	t.Parallel()
	var factoryMu sync.Mutex
	factoryCredentials := make([]string, 0, 2)
	runtime, err := NewRuntime(context.Background(), RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{Provider: ProviderAgenticOpenAI, Model: "gpt-5.4"},
			CredentialLookup: func(string) (string, bool, error) {
				return "secret-1", true, nil
			},
			CredentialOriginLookup: func(string) (string, string, bool, error) {
				return "secret-1", "local-record/r1", true, nil
			},
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			factoryMu.Lock()
			factoryCredentials = append(factoryCredentials, config.APIKey)
			factoryMu.Unlock()
			return wrapAgenticModel(&originCaptureAgenticModel{}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var lookupMu sync.Mutex
	secret := "secret-1"
	originID := "local-record/r1"
	lookupCalls := 0
	oldResolved := make(chan struct{})
	releaseOld := make(chan struct{})
	runtime.routes.resolution.CredentialOriginLookup = func(string) (string, string, bool, error) {
		lookupMu.Lock()
		lookupCalls++
		call := lookupCalls
		resolvedSecret := secret
		resolvedOriginID := originID
		lookupMu.Unlock()
		if call == 1 {
			close(oldResolved)
			<-releaseOld
		}
		return resolvedSecret, resolvedOriginID, true, nil
	}

	type routeResult struct {
		published *publishedRoute
		err       error
	}
	oldDone := make(chan routeResult, 1)
	go func() {
		_, published, routeErr := runtime.routes.routePublished(
			context.Background(),
			"gpt-5.4",
		)
		oldDone <- routeResult{published: published, err: routeErr}
	}()
	<-oldResolved

	lookupMu.Lock()
	secret = "secret-2"
	originID = "local-record/r2"
	lookupMu.Unlock()
	newDone := make(chan routeResult, 1)
	go func() {
		_, published, routeErr := runtime.routes.routePublished(
			context.Background(),
			"gpt-5.4",
		)
		newDone <- routeResult{published: published, err: routeErr}
	}()
	newResult := <-newDone
	if newResult.err != nil {
		close(releaseOld)
		t.Fatal(newResult.err)
	}
	close(releaseOld)
	oldResult := <-oldDone
	if !errors.Is(oldResult.err, errRouteResolutionSuperseded) {
		t.Fatalf("old route error = %v, want superseded", oldResult.err)
	}
	if oldResult.published != nil {
		t.Fatal("superseded credential resolution returned a publication")
	}

	runtime.routes.mu.Lock()
	var current *publishedRoute
	for _, published := range runtime.routes.published {
		current = published
		break
	}
	runtime.routes.mu.Unlock()
	if current == nil {
		t.Fatal("runtime has no published route")
	}
	if current.credentialOriginID != "local-record/r2" {
		t.Fatalf("current credential origin = %q, want rotated origin", current.credentialOriginID)
	}
	if newResult.published != current {
		t.Fatal("rotated credential resolution is not the current publication")
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if !reflect.DeepEqual(factoryCredentials, []string{"secret-1", "secret-2"}) {
		t.Fatalf("factory credentials = %#v, want no stale republication", factoryCredentials)
	}
}

func TestRoutePublicationFencesSupersededAccountAcrossIdentityChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		newEndpoint string
		newAuthKind string
		newAuthRef  string
	}{
		{
			name:        "credential source",
			newEndpoint: "https://api.openai.com/v1",
			newAuthKind: "credential",
			newAuthRef:  "openai",
		},
		{
			name:        "endpoint",
			newEndpoint: "https://gateway.example/v1",
			newAuthKind: "env",
			newAuthRef:  "OPENAI_API_KEY",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &routeRegistry{
				factory: func(context.Context, Config) (model.BaseChatModel, error) {
					return wrapAgenticModel(&originCaptureAgenticModel{}), nil
				},
			}
			oldConfig := ResolvedConfig{Config: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-5.4",
				BaseURL:  "https://api.openai.com/v1",
			}}
			newConfig := oldConfig
			newConfig.BaseURL = test.newEndpoint
			oldIdentity, err := NewRouteIdentity(RouteIdentityInput{
				Provider:      ProviderAgenticOpenAI,
				Endpoint:      oldConfig.BaseURL,
				AuthKind:      "env",
				AuthReference: "OPENAI_API_KEY",
				AdapterDigest: "agenticopenai:v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			newIdentity, err := NewRouteIdentity(RouteIdentityInput{
				Provider:      ProviderAgenticOpenAI,
				Endpoint:      newConfig.BaseURL,
				AuthKind:      test.newAuthKind,
				AuthReference: test.newAuthRef,
				AdapterDigest: "agenticopenai:v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			oldAttempt := registry.reserveRouteResolution()
			newAttempt := registry.reserveRouteResolution()
			registry.mu.Lock()
			newPublished, err := registry.publishRouteLocked(
				context.Background(),
				newConfig,
				newIdentity,
				"account-a",
				resolvedRouteCredential{secret: "secret-2", originID: "origin/r2"},
				newAttempt,
			)
			registry.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			registry.mu.Lock()
			oldPublished, err := registry.publishRouteLocked(
				context.Background(),
				oldConfig,
				oldIdentity,
				"account-a",
				resolvedRouteCredential{secret: "secret-1", originID: "origin/r1"},
				oldAttempt,
			)
			current := registry.accountPublications["account-a"]
			_, oldWasPublished := registry.published[oldIdentity]
			registry.mu.Unlock()
			if !errors.Is(err, errRouteResolutionSuperseded) {
				t.Fatalf("old route error = %v, want superseded", err)
			}
			if oldPublished != nil || oldWasPublished {
				t.Fatal("superseded route identity was republished")
			}
			if current != newPublished {
				t.Fatal("account authority did not retain the newer route")
			}
		})
	}
}

func TestPrepareTransportRejectsReplacedAccountIdentity(t *testing.T) {
	t.Parallel()
	registry := &routeRegistry{
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			return wrapAgenticModel(&originCaptureAgenticModel{}), nil
		},
	}
	publish := func(identity RouteIdentity, endpoint, originID string) *publishedRoute {
		t.Helper()
		attempt := registry.reserveRouteResolution()
		registry.mu.Lock()
		defer registry.mu.Unlock()
		published, err := registry.publishRouteLocked(
			context.Background(),
			ResolvedConfig{Config: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-5.4",
				BaseURL:  endpoint,
			}},
			identity,
			"account-a",
			resolvedRouteCredential{secret: originID, originID: originID},
			attempt,
		)
		if err != nil {
			t.Fatal(err)
		}
		return published
	}
	oldIdentity, err := NewRouteIdentity(RouteIdentityInput{
		Provider:      ProviderAgenticOpenAI,
		Endpoint:      "https://api.openai.com/v1",
		AuthKind:      "env",
		AuthReference: "OPENAI_API_KEY",
		AdapterDigest: "agenticopenai:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	newIdentity := oldIdentity
	newIdentity.Endpoint = "https://gateway.example/v1"
	oldPublished := publish(oldIdentity, oldIdentity.Endpoint, "origin/r1")
	publish(newIdentity, newIdentity.Endpoint, "origin/r2")
	message := &schema.Message{
		Role:             schema.Assistant,
		Content:          "public",
		ReasoningContent: "private",
		Extra:            map[string]any{"message_id": "assistant-old-route"},
	}
	oldPrepared := &preparedRoute{published: oldPublished, apiModel: "gpt-5.4"}
	ctx := providerorigin.WithBindingResolver(
		context.Background(),
		fixedOriginResolver{resolution: providerorigin.BindingResolution{
			State:  providerorigin.BindingVerified,
			Origin: oldPrepared.origin(),
		}},
	)
	transport, proof := registry.prepareTransport(
		ctx,
		oldPrepared,
		oldPublished.client,
		[]*schema.Message{message},
		"generate",
	)
	if proof != nil {
		t.Fatal("replaced account route received a private continuation proof")
	}
	if transport[0].ReasoningContent != "" {
		t.Fatal("replaced account route retained private reasoning")
	}
	registry.mu.Lock()
	diagnosticCount := registry.diagnostics[originDiagnosticKey{
		path:   "generate",
		reason: providerorigin.ReasonRouteStale,
	}]
	registry.mu.Unlock()
	if diagnosticCount != 1 {
		t.Fatalf("route-stale diagnostics = %d, want 1", diagnosticCount)
	}
}

func TestRoutingChatModelFencesReplacementBeforeConversion(t *testing.T) {
	t.Parallel()
	secret := "secret-1"
	originID := "local-record/r1"
	captures := make([]*originCaptureAgenticModel, 0)
	runtime, err := NewRuntime(context.Background(), RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{Provider: ProviderAgenticOpenAI, Model: "gpt-5.4"},
			CredentialLookup: func(string) (string, bool, error) {
				return secret, true, nil
			},
			CredentialOriginLookup: func(string) (string, string, bool, error) {
				return secret, originID, true, nil
			},
		},
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			capture := &originCaptureAgenticModel{}
			captures = append(captures, capture)
			return wrapAgenticModel(capture), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	exact := runtimeOriginForTest(t, runtime)
	message := &schema.Message{
		Role:             schema.Assistant,
		Content:          "answer",
		ReasoningContent: "private",
		Extra:            map[string]any{"message_id": "assistant-race"},
	}
	ctx := providerorigin.WithBindingResolver(
		context.Background(),
		fixedOriginResolver{resolution: providerorigin.BindingResolution{
			State:  providerorigin.BindingVerified,
			Origin: exact,
		}},
	)
	var hookErr error
	var once sync.Once
	runtime.routes.beforeProof = func() {
		once.Do(func() {
			secret = "secret-2"
			originID = "local-record/r2"
			_, _, hookErr = runtime.routes.routePublished(
				context.Background(),
				"gpt-5.4",
			)
		})
	}
	if _, err := runtime.ChatModel.Generate(ctx, []*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if len(captures) != 2 {
		t.Fatalf("client publications = %d, want 2", len(captures))
	}
	assertCapturedPrivateReasoning(t, captures[0].lastInput(), false)
	if diagnostics := runtime.ReasoningOriginDiagnostics(); !hasOriginDiagnostic(diagnostics, "generate", providerorigin.ReasonRouteStale) {
		t.Fatalf("race diagnostics = %#v", diagnostics)
	}
}

func hasOriginDiagnostic(
	diagnostics []ReasoningOriginDiagnostic,
	path string,
	reason providerorigin.Reason,
) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == path && diagnostic.Reason == string(reason) &&
			diagnostic.Count > 0 {
			return true
		}
	}
	return false
}

func TestEvaluateOriginReasonCodes(t *testing.T) {
	t.Parallel()
	exact := providerorigin.Origin{
		Version:             providerorigin.OriginVersion,
		Provider:            string(ProviderAgenticOpenAI),
		AccountID:           "account",
		APIFamily:           providerorigin.OpenAIResponsesV1,
		APIModel:            "gpt-5.4",
		RouteIdentityDigest: strings.Repeat("a", 64),
		CredentialOriginID:  "local/r1",
		RoutePublication:    7,
	}
	resolution := providerorigin.BindingResolution{
		State:  providerorigin.BindingVerified,
		Origin: exact,
	}
	if allowed, reason := evaluateOrigin(resolution, exact, true); !allowed || reason != providerorigin.ReasonExact {
		t.Fatalf("exact = (%v, %s)", allowed, reason)
	}
	tests := []struct {
		name       string
		resolution providerorigin.BindingResolution
		published  bool
		want       providerorigin.Reason
	}{
		{name: "absent", resolution: providerorigin.BindingResolution{State: providerorigin.BindingAbsent}, published: true, want: providerorigin.ReasonAbsent},
		{name: "legacy", resolution: providerorigin.BindingResolution{State: providerorigin.BindingLegacyUnverified}, published: true, want: providerorigin.ReasonLegacyUnverified},
		{name: "recovery", resolution: providerorigin.BindingResolution{State: providerorigin.BindingRecoveryMismatch}, published: true, want: providerorigin.ReasonRecoveryMismatch},
		{name: "provider", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.Provider = "agenticclaude" }), published: true, want: providerorigin.ReasonProviderMismatch},
		{name: "account", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.AccountID = "other" }), published: true, want: providerorigin.ReasonAccountMismatch},
		{name: "api family", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.APIFamily = "chat-completions/v1" }), published: true, want: providerorigin.ReasonAPIFamilyMismatch},
		{name: "api model", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.APIModel = "gpt-5.4-mini" }), published: true, want: providerorigin.ReasonAPIModelMismatch},
		{name: "credential", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.CredentialOriginID = "" }), published: true, want: providerorigin.ReasonCredentialMismatch},
		{name: "route digest", resolution: changedOriginResolution(exact, func(origin *providerorigin.Origin) { origin.RouteIdentityDigest = strings.Repeat("b", 64) }), published: true, want: providerorigin.ReasonRouteStale},
		{name: "publication", resolution: resolution, published: false, want: providerorigin.ReasonRouteStale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if allowed, reason := evaluateOrigin(
				test.resolution,
				exact,
				test.published,
			); allowed || reason != test.want {
				t.Fatalf("decision = (%v, %s), want %s", allowed, reason, test.want)
			}
		})
	}
}

func changedOriginResolution(
	origin providerorigin.Origin,
	change func(*providerorigin.Origin),
) providerorigin.BindingResolution {
	change(&origin)
	return providerorigin.BindingResolution{
		State:  providerorigin.BindingVerified,
		Origin: origin,
	}
}

func TestAgenticOpenAILeafRequestsIncludeReasoningOnlyForExactOrigin(t *testing.T) {
	t.Parallel()
	var (
		requestMu sync.Mutex
		requests  [][]byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		requestMu.Lock()
		requests = append(requests, body)
		requestMu.Unlock()
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if streaming, _ := payload["stream"].(bool); streaming {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("event: response.completed\n" +
				`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_fixture","object":"response","created_at":1,"status":"completed","model":"gpt-5.4","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"resp_fixture","object":"response","created_at":1,
			"status":"completed","model":"gpt-5.4","output":[],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	runtime, err := NewRuntime(context.Background(), RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-5.4",
				BaseURL:  server.URL,
			},
			CredentialLookup: func(string) (string, bool, error) {
				return "fixture-secret", true, nil
			},
			CredentialOriginLookup: func(string) (string, string, bool, error) {
				return "fixture-secret", "fixture-origin/r1", true, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	privateMessage := &schema.Message{
		Role:    schema.Assistant,
		Content: "public text",
		Extra:   map[string]any{"message_id": "leaf-fixture"},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "fixture summary",
					Signature: "fixture-encrypted-reasoning",
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "public text"},
		},
		ToolCalls: []schema.ToolCall{{
			ID:   "call-fixture",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"README.md"}`,
			},
		}},
	}
	exact := runtimeOriginForTest(t, runtime)
	exactCtx := providerorigin.WithBindingResolver(
		context.Background(),
		fixedOriginResolver{resolution: providerorigin.BindingResolution{
			State:  providerorigin.BindingVerified,
			Origin: exact,
		}},
	)
	if _, err := runtime.ChatModel.Generate(
		exactCtx,
		[]*schema.Message{privateMessage},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ChatModel.Generate(
		context.Background(),
		[]*schema.Message{privateMessage},
	); err != nil {
		t.Fatal(err)
	}
	exactStream, err := runtime.ChatModel.Stream(exactCtx, []*schema.Message{privateMessage})
	if err != nil {
		t.Fatal(err)
	}
	drainMessageStream(t, exactStream)
	rejectedStream, err := runtime.ChatModel.Stream(context.Background(), []*schema.Message{privateMessage})
	if err != nil {
		t.Fatal(err)
	}
	drainMessageStream(t, rejectedStream)
	requestMu.Lock()
	captured := append([][]byte(nil), requests...)
	requestMu.Unlock()
	if len(captured) != 4 {
		t.Fatalf("leaf requests = %d, want Generate and Stream exact/rejected pairs", len(captured))
	}
	for index, path := range []string{"Generate", "Stream"} {
		exactBody := captured[index*2]
		rejectedBody := captured[index*2+1]
		if !strings.Contains(string(exactBody), "fixture-encrypted-reasoning") ||
			strings.Contains(string(rejectedBody), "fixture-encrypted-reasoning") {
			t.Fatalf("%s leaf private payloads: exact=%s rejected=%s", path, exactBody, rejectedBody)
		}
		exactPublic := openAIRequestPublicInput(t, exactBody)
		rejectedPublic := openAIRequestPublicInput(t, rejectedBody)
		if !reflect.DeepEqual(exactPublic, rejectedPublic) {
			t.Fatalf("%s public leaf input changed: exact=%#v rejected=%#v", path, exactPublic, rejectedPublic)
		}
	}
}

func TestConfiguredLegacyFallbackSelectorReachesOpenAITransport(t *testing.T) {
	t.Parallel()
	var (
		requestMu sync.Mutex
		models    []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		models = append(models, payload.Model)
		requestMu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_fallback","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: &engineconfig.ConfigSources{
			User:    &engineconfig.Config{},
			Project: &engineconfig.Config{},
		},
		LegacyFallbackModel: "gpt-4o-mini",
		Resolution: ResolveInput{Explicit: Config{
			Provider: ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   "fixture-secret",
			BaseURL:  server.URL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolModel, ok := runtime.ChatModel.(model.ToolCallingChatModel)
	if !ok {
		t.Fatal("configured runtime does not support tool binding")
	}
	bound, err := toolModel.WithTools([]*schema.ToolInfo{{Name: "Write", Desc: "write a file"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := bound.Stream(
		context.Background(),
		[]*schema.Message{schema.UserMessage("probe")},
		model.WithModel("legacy:gpt-4o-mini"),
	)
	if err != nil {
		t.Fatal(err)
	}
	drainMessageStream(t, stream)
	requestMu.Lock()
	defer requestMu.Unlock()
	if !reflect.DeepEqual(models, []string{"gpt-4o-mini"}) {
		t.Fatalf("transport models = %v, want fallback model", models)
	}
}

func drainMessageStream(t *testing.T, stream *schema.StreamReader[*schema.Message]) {
	t.Helper()
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive leaf stream: %v", err)
		}
	}
}

func openAIRequestPublicInput(t *testing.T, body []byte) []any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	input, ok := request["input"].([]any)
	if !ok {
		t.Fatalf("request input = %#v", request["input"])
	}
	public := make([]any, 0, len(input))
	for _, item := range input {
		object, _ := item.(map[string]any)
		if object["type"] == "reasoning" {
			continue
		}
		public = append(public, item)
	}
	return public
}
