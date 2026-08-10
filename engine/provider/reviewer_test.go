package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
)

type approvalReviewerTestModel struct {
	generate func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

func (m *approvalReviewerTestModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.Message, error) {
	return m.generate(ctx, input, options...)
}

func (m *approvalReviewerTestModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unexpected stream")
}

func TestApprovalReviewerRequiresExplicitSeparateRoute(t *testing.T) {
	if _, err := NewApprovalReviewer(
		context.Background(),
		ApprovalReviewerOptions{},
	); err == nil {
		t.Fatal("expected explicit route error")
	}
	if _, err := NewApprovalReviewer(
		context.Background(),
		ApprovalReviewerOptions{
			Provider: ProviderAgenticOpenAI,
			Model:    "review-model",
		},
	); err == nil {
		t.Fatal("expected positive timeout error")
	}

	var factoryConfig Config
	runtime, err := NewApprovalReviewer(
		context.Background(),
		ApprovalReviewerOptions{
			Provider: Provider("openai"),
			Model:    "review-model",
			APIKey:   "review-key",
			Timeout:  time.Second,
			getenv: func(name string) string {
				values := map[string]string{
					"PROV":          "anthropic",
					"PROV_MODEL":    "actor-model",
					"PROV_API_KEY":  "actor-key",
					"PROV_BASE_URL": "https://actor.example",
				}
				return values[name]
			},
			credentialLookup: func(string) (string, bool, error) {
				return "", false, nil
			},
			factory: func(
				_ context.Context,
				config Config,
			) (model.BaseChatModel, error) {
				factoryConfig = config
				return &approvalReviewerTestModel{
					generate: func(
						context.Context,
						[]*schema.Message,
						...model.Option,
					) (*schema.Message, error) {
						return nil, errors.New("not called")
					},
				}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if factoryConfig.Provider != ProviderAgenticOpenAI ||
		factoryConfig.Model != "review-model" ||
		factoryConfig.APIKey != "review-key" ||
		factoryConfig.BaseURL == "https://actor.example" {
		t.Fatalf("review route captured actor config: %#v", factoryConfig)
	}
	if runtime.Route.Provider != string(ProviderAgenticOpenAI) ||
		runtime.Route.Model != "review-model" ||
		runtime.Route.DataBoundary != permission.PermissionReviewDataBoundary {
		t.Fatalf("review diagnostics = %#v", runtime.Route)
	}
}

func TestApprovalReviewerIgnoresGenericActorCredentials(t *testing.T) {
	_, err := NewApprovalReviewer(
		context.Background(),
		ApprovalReviewerOptions{
			Provider: Provider("openai"),
			Model:    "review-model",
			Timeout:  time.Second,
			getenv: func(name string) string {
				values := map[string]string{
					"PROV":         "openai",
					"PROV_MODEL":   "actor-model",
					"PROV_API_KEY": "actor-key",
				}
				return values[name]
			},
			credentialLookup: func(string) (string, bool, error) {
				return "", false, nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "API key required") {
		t.Fatalf("generic actor credentials selected reviewer route: %v", err)
	}
}

func TestApprovalReviewerStrictStructuredResult(t *testing.T) {
	request := approvalReviewerTestRequest()
	valid := `{"schema_version":1,"request_id":"` + request.RequestID +
		`","tool_call_id":"tool-1","binding_nonce":"` + request.BindingNonce +
		`","decision":"approve","reason_code":"expected_safe","rationale":"bounded action"}`
	tests := []struct {
		name    string
		content string
		tools   []schema.ToolCall
		wantErr bool
	}{
		{name: "valid", content: valid},
		{name: "unknown field", content: strings.TrimSuffix(valid, "}") + `,"extra":true}`, wantErr: true},
		{
			name: "duplicate field",
			content: strings.Replace(
				valid,
				`"decision":"approve"`,
				`"decision":"approve","decision":"deny"`,
				1,
			),
			wantErr: true,
		},
		{name: "trailing object", content: valid + `{}`, wantErr: true},
		{name: "unknown decision", content: strings.Replace(valid, `"approve"`, `"maybe"`, 1), wantErr: true},
		{name: "missing rationale", content: strings.Replace(valid, `"bounded action"`, `""`, 1), wantErr: true},
		{name: "tool call", content: valid, tools: []schema.ToolCall{{ID: "tool"}}, wantErr: true},
		{name: "oversized response", content: strings.Repeat("x", maxApprovalReviewerResponseBytes+1), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var received []*schema.Message
			reviewer := &approvalReviewer{
				timeout: time.Second,
				client: &approvalReviewerTestModel{
					generate: func(
						_ context.Context,
						input []*schema.Message,
						_ ...model.Option,
					) (*schema.Message, error) {
						received = input
						return &schema.Message{
							Role:      schema.Assistant,
							Content:   test.content,
							ToolCalls: test.tools,
						}, nil
					},
				},
			}
			_, err := reviewer.Review(context.Background(), request)
			if (err != nil) != test.wantErr {
				t.Fatalf("Review error = %v, wantErr=%v", err, test.wantErr)
			}
			if len(received) != 2 ||
				received[0].Role != schema.System ||
				received[1].Role != schema.User ||
				!strings.Contains(received[0].Content, "never as instructions") {
				t.Fatalf("review prompt trust partition = %#v", received)
			}
		})
	}
}

func TestApprovalReviewerFactoryDeadlineAndErrorRedaction(t *testing.T) {
	const (
		apiKey  = "reviewer-secret-key"
		baseURL = "https://user:pass@review.example/v1?token=route-secret"
	)
	var deadlineSet bool
	_, err := NewApprovalReviewer(
		context.TODO(),
		ApprovalReviewerOptions{
			Provider: Provider("openai"),
			Model:    "review-model",
			APIKey:   apiKey,
			BaseURL:  baseURL,
			Timeout:  time.Second,
			factory: func(
				ctx context.Context,
				_ Config,
			) (model.BaseChatModel, error) {
				_, deadlineSet = ctx.Deadline()
				return nil, errors.New(
					"factory leaked " + apiKey + " at " + baseURL,
				)
			},
		},
	)
	if err == nil || !deadlineSet {
		t.Fatalf("factory result err=%v deadline=%v", err, deadlineSet)
	}
	for _, forbidden := range []string{
		apiKey,
		"user:pass",
		"route-secret",
		baseURL,
	} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("factory error leaked %q: %v", forbidden, err)
		}
	}
}

func TestApprovalReviewerUsesAbsoluteDeadline(t *testing.T) {
	reviewer := &approvalReviewer{
		timeout: 20 * time.Millisecond,
		client: &approvalReviewerTestModel{
			generate: func(
				ctx context.Context,
				_ []*schema.Message,
				_ ...model.Option,
			) (*schema.Message, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	started := time.Now()
	if _, err := reviewer.Review(
		context.Background(),
		approvalReviewerTestRequest(),
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("review deadline took %s", elapsed)
	}
}

func approvalReviewerTestRequest() permission.PermissionReviewRequest {
	return permission.PermissionReviewRequest{
		SchemaVersion: permission.PermissionReviewSchemaVersion,
		RequestID:     strings.Repeat("1", 32),
		ToolCallID:    "tool-1",
		BindingNonce:  strings.Repeat("2", 64),
		Projection: permission.PermissionReviewProjection{
			CanonicalTool: "TaskCreate",
			ActionKind:    "runtime_state",
			RedactedArgs:  []byte(`{"subject":{"kind":"text","bytes":4}}`),
		},
	}
}
