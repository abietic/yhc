package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestP242bCallModelProviderUsageIdentityAndPreProviderRetry(t *testing.T) {
	reporter := &p242bUsageReporter{}
	chatModel := &p242bToolBindingModel{
		withToolsErrors: []error{
			errors.New("rate_limit_error: 429 pre-provider tool bind"),
			nil,
		},
	}
	logicalRoundID := reporter.NewLogicalRoundID()
	budget := NewModelAttemptBudget(2, time.Minute)
	result, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond},
		func(ctx context.Context, _ int) (*CallModelResult, error) {
			return CallModel(
				ctx,
				chatModel,
				[]*schema.Message{{Role: schema.User, Content: "hello"}},
				nil,
				[]*schema.ToolInfo{{Name: "Read"}},
				CallModelOptions{
					ProviderUsage:       reporter,
					UsageLogicalRoundID: logicalRoundID,
					ProviderCallBudget:  budget,
				},
			)
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reporter.calls) != 1 ||
		chatModel.streamCalls != 1 ||
		budget.ProviderCalls() != 1 ||
		result.ProviderUsageCall == nil {
		t.Fatalf(
			"pre-provider retry usage calls=%d streams=%d budget=%d result=%#v",
			len(reporter.calls),
			chatModel.streamCalls,
			budget.ProviderCalls(),
			result,
		)
	}
	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok ||
		headers["x-client-request-id"] != reporter.calls[0].id ||
		result.ProviderUsageCall.ProviderCallID() != reporter.calls[0].id {
		t.Fatalf(
			"provider identity header=%q call=%q",
			headers["x-client-request-id"],
			result.ProviderUsageCall.ProviderCallID(),
		)
	}
}

func TestP242bCallModelAmbiguousDispatchStopsRetry(t *testing.T) {
	reporter := &p242bUsageReporter{}
	chatModel := &p242bStreamErrorModel{
		err: errors.New("overloaded_error: 529 after provider entry"),
	}
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 3, BaseDelay: time.Nanosecond},
		func(ctx context.Context, _ int) (*CallModelResult, error) {
			return CallModel(
				ctx,
				chatModel,
				nil,
				nil,
				nil,
				CallModelOptions{
					ProviderUsage:       reporter,
					UsageLogicalRoundID: reporter.NewLogicalRoundID(),
				},
			)
		},
		nil,
	)
	if !IsProviderUsageTerminalError(err) ||
		chatModel.calls != 1 ||
		len(reporter.calls) != 1 ||
		reporter.calls[0].ambiguous != 1 {
		t.Fatalf(
			"ambiguous retry err=%v streams=%d usage=%#v",
			err,
			chatModel.calls,
			reporter.calls,
		)
	}

	sideReporter := &p242bUsageReporter{}
	sideModel := &p242bStreamErrorModel{
		err: errors.New("rate_limit_error: 429 after side provider entry"),
	}
	_, sideErr := SideQueryWithRetry(
		context.Background(),
		sideModel,
		SideQueryOptions{
			ProviderUsage:       sideReporter,
			UsageLogicalRoundID: sideReporter.NewLogicalRoundID(),
		},
		&SideQueryRetryConfig{
			MaxRetries: 3,
			BaseDelay:  time.Nanosecond,
		},
	)
	if !IsProviderUsageTerminalError(sideErr) ||
		sideModel.calls != 1 ||
		len(sideReporter.calls) != 1 ||
		sideReporter.calls[0].ambiguous != 1 {
		t.Fatalf(
			"ambiguous side retry err=%v streams=%d usage=%#v",
			sideErr,
			sideModel.calls,
			sideReporter.calls,
		)
	}
}

func TestP242bCallModelCancellationAfterAdmissionReleasesPreDispatch(
	t *testing.T,
) {
	ctx, cancel := context.WithCancel(context.Background())
	reporter := &p242bUsageReporter{onAdmit: cancel}
	chatModel := &p242bStreamErrorModel{}
	budget := NewModelAttemptBudget(1, time.Minute)
	_, err := CallModel(
		ctx,
		chatModel,
		nil,
		nil,
		nil,
		CallModelOptions{
			ProviderUsage:       reporter,
			UsageLogicalRoundID: reporter.NewLogicalRoundID(),
			ProviderCallBudget:  budget,
		},
	)
	if !errors.Is(err, context.Canceled) ||
		chatModel.calls != 0 ||
		budget.ProviderCalls() != 0 ||
		len(reporter.calls) != 1 ||
		reporter.calls[0].released != 1 ||
		reporter.calls[0].ambiguous != 0 {
		t.Fatalf(
			"pre-dispatch cancellation err=%v streams=%d budget=%d usage=%#v",
			err,
			chatModel.calls,
			budget.ProviderCalls(),
			reporter.calls,
		)
	}
}

func TestP242bSideQueryCommitsOneFinalCumulativeUsage(t *testing.T) {
	reporter := &p242bUsageReporter{}
	chatModel := &p242bCumulativeUsageModel{}
	message, err := SideQuery(
		context.Background(),
		chatModel,
		SideQueryOptions{
			Messages:            []*schema.Message{{Role: schema.User, Content: "compact"}},
			ProviderUsage:       reporter,
			UsageLogicalRoundID: reporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "done" ||
		len(reporter.calls) != 1 ||
		reporter.calls[0].completed != 1 ||
		reporter.calls[0].usage == nil ||
		reporter.calls[0].usage.TotalTokens != 20 {
		t.Fatalf("final cumulative usage message=%#v calls=%#v", message, reporter.calls)
	}
}

type p242bUsageReporter struct {
	mu      sync.Mutex
	next    int
	calls   []*p242bUsageCall
	onAdmit func()
}

func (r *p242bUsageReporter) NewLogicalRoundID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	return fmt.Sprintf("logical-%d", r.next)
}

func (r *p242bUsageReporter) AdmitProviderUsage(
	_ context.Context,
	_ ProviderUsageDescriptor,
) (ProviderUsageCall, error) {
	r.mu.Lock()
	r.next++
	call := &p242bUsageCall{id: fmt.Sprintf("provider-%d", r.next)}
	r.calls = append(r.calls, call)
	onAdmit := r.onAdmit
	r.mu.Unlock()
	if onAdmit != nil {
		onAdmit()
	}
	return call, nil
}

type p242bUsageCall struct {
	id        string
	completed int
	released  int
	ambiguous int
	usage     *schema.TokenUsage
}

func (c *p242bUsageCall) ProviderCallID() string { return c.id }

func (c *p242bUsageCall) CompleteProviderUsage(usage *schema.TokenUsage) error {
	c.completed++
	if usage != nil {
		copied := *usage
		c.usage = &copied
	}
	return nil
}

func (c *p242bUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	c.released++
	return nil
}

func (c *p242bUsageCall) MarkProviderUsageAmbiguous(error) error {
	c.ambiguous++
	return nil
}

type p242bToolBindingModel struct {
	withToolsErrors []error
	withToolsCalls  int
	streamCalls     int
	streamOptions   []model.Option
}

func (m *p242bToolBindingModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *p242bToolBindingModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	m.streamOptions = append([]model.Option(nil), opts...)
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		Content:      "done",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{}},
	}}), nil
}

func (m *p242bToolBindingModel) WithTools(
	[]*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	index := m.withToolsCalls
	m.withToolsCalls++
	if index < len(m.withToolsErrors) && m.withToolsErrors[index] != nil {
		return nil, m.withToolsErrors[index]
	}
	return m, nil
}

type p242bStreamErrorModel struct {
	calls int
	err   error
}

func (m *p242bStreamErrorModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *p242bStreamErrorModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant,
	}}), nil
}

type p242bCumulativeUsageModel struct{}

func (*p242bCumulativeUsageModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (*p242bCumulativeUsageModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{
		{
			Role:    schema.Assistant,
			Content: "do",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{
					PromptTokens:     5,
					CompletionTokens: 5,
					TotalTokens:      10,
				},
			},
		},
		{
			Role:    schema.Assistant,
			Content: "ne",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 10,
					TotalTokens:      20,
				},
			},
		},
	}), nil
}
