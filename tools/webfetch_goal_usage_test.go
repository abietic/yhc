package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

const webFetchExampleURL = "https://example.test"

type webFetchGoalUsageAdmitter struct {
	descriptor execution.ProviderUsageDescriptor
	admissions int
	call       *webFetchGoalUsageCall
}

func (a *webFetchGoalUsageAdmitter) NewLogicalRoundID() string {
	return "webfetch-logical-round"
}

func (a *webFetchGoalUsageAdmitter) AdmitProviderUsage(
	_ context.Context,
	descriptor execution.ProviderUsageDescriptor,
) (execution.ProviderUsageCall, error) {
	if descriptor.LogicalRoundID == "" ||
		descriptor.QuerySource != "webfetch_ai_processing" {
		return nil, errors.New("invalid WebFetch Goal usage descriptor")
	}
	a.descriptor = descriptor
	a.admissions++
	a.call = &webFetchGoalUsageCall{id: "webfetch-provider-call"}
	return a.call, nil
}

type webFetchGoalUsageCall struct {
	id        string
	completed int
	released  int
	ambiguous int
	usage     *schema.TokenUsage
}

func (c *webFetchGoalUsageCall) ProviderCallID() string {
	return c.id
}

func (c *webFetchGoalUsageCall) CompleteProviderUsage(
	usage *schema.TokenUsage,
) error {
	c.completed++
	if usage != nil {
		copied := *usage
		c.usage = &copied
	}
	return nil
}

func (c *webFetchGoalUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	c.released++
	return nil
}

func (c *webFetchGoalUsageCall) MarkProviderUsageAmbiguous(error) error {
	c.ambiguous++
	return nil
}

type webFetchGoalUsageModel struct {
	streamCalls int
}

func (m *webFetchGoalUsageModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *webFetchGoalUsageModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "accounted WebFetch result",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     8,
			CompletionTokens: 3,
			TotalTokens:      11,
		}},
	}}), nil
}

func TestP242bWebFetchAIProcessingUsesGoalUsageScope(t *testing.T) {
	admitter := &webFetchGoalUsageAdmitter{}
	chatModel := &webFetchGoalUsageModel{}
	ctx := execution.WithProviderUsageScope(
		context.Background(),
		admitter,
		true,
	)

	result, err := webFetchAIProcess(
		ctx,
		chatModel,
		"summarize",
		"bounded content",
		webFetchExampleURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "accounted WebFetch result" {
		t.Fatalf("WebFetch result = %q", result)
	}
	if admitter.admissions != 1 ||
		chatModel.streamCalls != 1 ||
		admitter.call == nil ||
		admitter.call.completed != 1 ||
		admitter.call.released != 0 ||
		admitter.call.ambiguous != 0 ||
		admitter.call.usage == nil ||
		admitter.call.usage.TotalTokens != 11 {
		t.Fatalf(
			"WebFetch accounting admitter=%#v call=%#v model_calls=%d",
			admitter,
			admitter.call,
			chatModel.streamCalls,
		)
	}
}

func TestP242bWebFetchAIProcessingFailsBeforeProviderWithoutCapability(
	t *testing.T,
) {
	chatModel := &webFetchGoalUsageModel{}
	ctx := execution.WithProviderUsageScope(
		context.Background(),
		nil,
		true,
	)

	_, err := webFetchAIProcess(
		ctx,
		chatModel,
		"summarize",
		"bounded content",
		webFetchExampleURL,
	)
	if err == nil || chatModel.streamCalls != 0 {
		t.Fatalf(
			"missing-capability error=%v model_calls=%d",
			err,
			chatModel.streamCalls,
		)
	}
}

type webFetchProfileModel struct{ webFetchGoalUsageModel }

func (*webFetchProfileModel) DefaultSideQueryRoute() (string, string, string) {
	return "research", "agenticdeepseek", "high"
}

func TestWebFetchAccountsForItsOwnModelRoute(t *testing.T) {
	admitter := &webFetchGoalUsageAdmitter{}
	ctx := execution.WithProviderUsageScope(t.Context(), admitter, true)
	_, err := webFetchAIProcess(ctx, &webFetchProfileModel{}, "summarize", "content", webFetchExampleURL)
	if err != nil {
		t.Fatal(err)
	}
	got := admitter.descriptor
	if got.Model != "research" || got.Provider != "agenticdeepseek" || got.ReasoningEffort != "high" {
		t.Fatalf("side request route = (%q, %q, %q)", got.Model, got.Provider, got.ReasoningEffort)
	}
}

func TestWebFetchCustomModelKeepsCostProtection(t *testing.T) {
	chatModel := &webFetchGoalUsageModel{}
	card := execution.RunPriceCard{
		Version: 1, Currency: "CNY", Source: webFetchExampleURL + "/pricing", AsOf: "2026-10-11T00:00:00Z", Window: "upper_bound",
		Routes: []execution.RunPriceRoute{{Provider: "agenticdeepseek", Model: "research", APIModel: "deepseek-flash", CachedInputPerMillion: "1", UncachedInputPerMillion: "2", OutputPerMillion: "4"}},
	}
	usage, err := execution.NewPricedRunUsage(execution.RunUsageLimits{MaxEstimatedCost: "1", ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 100}, card)
	if err != nil {
		t.Fatal(err)
	}
	ctx := execution.WithProviderUsageScope(t.Context(), usage, true)
	_, err = webFetchAIProcess(ctx, chatModel, "summarize", "page", webFetchExampleURL)
	if !errors.Is(err, execution.ErrRunUsageUnknown) || chatModel.streamCalls != 0 {
		t.Fatalf("unbounded custom model: calls=%d err=%v", chatModel.streamCalls, err)
	}
	result, err := webFetchAIProcess(t.Context(), chatModel, "summarize", "page", webFetchExampleURL)
	if err != nil || result != "accounted WebFetch result" || chatModel.streamCalls != 1 {
		t.Fatalf("custom model without budget: calls=%d result=%q err=%v", chatModel.streamCalls, result, err)
	}
}
