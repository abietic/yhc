package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type providerStressModel struct{}

func (*providerStressModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "unused"}, nil
}

func (*providerStressModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "unused"}}), nil
}

func TestProviderStressGolden(t *testing.T) {
	var actual strings.Builder
	writePreflightStressSnapshot(t, &actual)

	t.Setenv("CLAUDE_CODE_UNATTENDED_RETRY", "")
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "1")
	rateModels := make([]string, 0, 2)
	rateEvents, rateTerminal := runProviderStressQuery(t, "", func(opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		rateModels = append(rateModels, opts.Model)
		if len(rateModels) == 1 {
			return nil, fmt.Errorf("provider returned 429 rate_limit_error")
		}
		return providerStressResult("rate-limit recovered", opts.Model), nil
	})
	writeQueryStressSnapshot(&actual, "rate_limit", rateEvents, rateTerminal, rateModels)

	if err := os.Setenv("CLAUDE_CODE_MAX_RETRIES", "10"); err != nil {
		t.Fatal(err)
	}
	fallbackModels := make([]string, 0, 4)
	fallbackEvents, fallbackTerminal := runProviderStressQuery(t, "backup-model", func(opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		fallbackModels = append(fallbackModels, opts.Model)
		if opts.Model != "backup-model" {
			return nil, fmt.Errorf("provider returned 529 overloaded_error")
		}
		return providerStressResult("fallback recovered", opts.Model), nil
	})
	writeQueryStressSnapshot(&actual, "fallback", fallbackEvents, fallbackTerminal, fallbackModels)

	expected, err := os.ReadFile(filepath.Join("testdata", "provider_stress.golden"))
	if err != nil {
		t.Fatal(err)
	}
	actualText := strings.TrimSpace(actual.String()) + "\n"
	if actualText != string(expected) {
		t.Fatalf("provider stress golden mismatch\nactual:\n%s\nexpected:\n%s", actualText, expected)
	}
}

func writePreflightStressSnapshot(t *testing.T, out *strings.Builder) {
	t.Helper()

	timeoutServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer timeoutServer.Close()
	_, timeoutErr := provider.NewRuntime(context.Background(), provider.RuntimeOptions{
		Resolution: provider.ResolveInput{Explicit: provider.Config{
			Provider: provider.ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   "timeout-key",
			BaseURL:  timeoutServer.URL,
		}},
		Preflight:        true,
		PreflightTimeout: 10 * time.Millisecond,
	})
	if timeoutErr == nil {
		t.Fatal("provider timeout preflight unexpectedly succeeded")
	}
	fmt.Fprintf(out, "[timeout]\nerror=%s\n\n", timeoutErr)

	const invalidKey = "invalid-secret-key"
	authServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer authServer.Close()
	_, authErr := provider.NewRuntime(context.Background(), provider.RuntimeOptions{
		Resolution: provider.ResolveInput{Explicit: provider.Config{
			Provider: provider.ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   invalidKey,
			BaseURL:  authServer.URL,
		}},
		Preflight:        true,
		PreflightTimeout: time.Second,
	})
	if authErr == nil {
		t.Fatal("invalid credentials preflight unexpectedly succeeded")
	}
	fmt.Fprintf(out, "[invalid_credentials]\nerror=%s\nsecret_leaked=%t\n\n", authErr, strings.Contains(authErr.Error(), invalidKey))
}

func runProviderStressQuery(
	t *testing.T,
	fallbackModel string,
	call func(execution.CallModelOptions) (*execution.CallModelResult, error),
) ([]QueryEvent, Terminal) {
	t.Helper()
	maxTurns := 2
	params := QueryParams{
		Messages:      []*schema.Message{{Role: schema.User, Content: "test provider stress"}},
		SystemPrompt:  &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:   QuerySourceSDK,
		MaxTurns:      &maxTurns,
		ChatModel:     &providerStressModel{},
		FallbackModel: fallbackModel,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "main-model",
		}},
		Deps: &QueryDeps{
			UUID: func() string { return "provider-stress-chain" },
			CallModel: func(
				ctx context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				if err := opts.ProviderCallBudget.ReserveProviderCall(ctx); err != nil {
					return nil, err
				}
				return call(opts)
			},
		},
	}
	if fallbackModel != "" {
		chain := p294Chain()
		chain.Primary.Selector = "main-model"
		chain.Primary.APIModel = "main-model"
		chain.Alternates[0].ProfileID = "fallback"
		chain.Alternates[0].Call.Selector = fallbackModel
		chain.Alternates[0].Call.ProfileID = "fallback"
		chain.Alternates[0].Call.APIModel = fallbackModel
		params.modelResolver = &p294FailoverResolver{chain: chain}
		params.commandEntrypoint = "headless"
		params.modelCall = &modelCallIdentity{
			Role:      "main",
			Selector:  "main-model",
			Profile:   "primary",
			Provider:  "agenticopenai",
			APIModel:  "main-model",
			Reasoning: "medium",
		}
		params.retryBaseDelay = time.Millisecond
	}
	return collectEvents(context.Background(), params)
}

func providerStressResult(content, modelName string) *execution.CallModelResult {
	return &execution.CallModelResult{
		StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: content}}),
		Model:        modelName,
	}
}

var retryDelayPattern = regexp.MustCompile(`retrying in [^ ]+\.\.\.`)

func writeQueryStressSnapshot(out *strings.Builder, scenario string, events []QueryEvent, terminal Terminal, models []string) {
	fmt.Fprintf(out, "[%s]\n", scenario)
	for _, event := range events {
		switch event.Type {
		case EventAttachment:
			message := event.AttachmentMessage
			if message == nil {
				continue
			}
			kind, _ := message.Extra["attachment_kind"].(string)
			switch kind {
			case "system_api_error":
				content := retryDelayPattern.ReplaceAllString(message.Content, "retrying in <delay>...")
				fmt.Fprintf(out, "attachment kind=%s attempt=%v is_429=%v is_529=%v content=%q\n",
					kind, message.Extra["attempt"], message.Extra["is_429"], message.Extra["is_529"], content)
			case "model_fallback":
				fmt.Fprintf(out, "attachment kind=%s original=%v fallback=%v content=%q\n",
					kind, message.Extra["original_model"], message.Extra["fallback_model"], message.Content)
			}
		case EventTombstone:
			fmt.Fprintln(out, "tombstone")
		case EventAssistant:
			if message := assistantEventMessage(event); message != nil && message.Content != "" {
				fmt.Fprintf(out, "assistant content=%q\n", message.Content)
			}
		}
	}
	fmt.Fprintf(out, "terminal=%s calls=%v\n\n", terminal.Reason, models)
}
