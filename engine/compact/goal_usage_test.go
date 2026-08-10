package compact

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func TestP242bLLMCompactionPTLAttemptsUseOneRoundAndCountOnceEach(
	t *testing.T,
) {
	reporter := &p242bCompactUsageReporter{}
	chatModel := &p242bSequentialCompactModel{
		responses: []*schema.Message{
			{
				Role:    schema.Assistant,
				Content: PromptTooLongErrorPrefix,
				ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
					PromptTokens:     8,
					CompletionTokens: 2,
					TotalTokens:      10,
				}},
			},
			{
				Role:    schema.Assistant,
				Content: "final compact summary",
				ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
					PromptTokens:     15,
					CompletionTokens: 5,
					TotalTokens:      20,
				}},
			},
		},
	}
	result, err := RunLLMCompact(
		context.Background(),
		[]*schema.Message{
			{Role: schema.User, Content: "first"},
			{
				Role:    schema.Assistant,
				Content: "first answer",
				Extra:   map[string]any{"message_id": "first-answer"},
			},
			{Role: schema.User, Content: "second"},
			{
				Role:    schema.Assistant,
				Content: "second answer",
				Extra:   map[string]any{"message_id": "second-answer"},
			},
		},
		LLMCompactOptions{
			ChatModel:     chatModel,
			ModelName:     "test-model",
			ProviderUsage: reporter,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 30 {
		t.Fatalf("compaction diagnostic usage = %#v", result.Usage)
	}
	if len(reporter.calls) != 2 ||
		reporter.calls[0].completed != 1 ||
		reporter.calls[1].completed != 1 ||
		reporter.calls[0].usage.TotalTokens != 10 ||
		reporter.calls[1].usage.TotalTokens != 20 {
		t.Fatalf("compaction Goal usage calls = %#v", reporter.calls)
	}
	if reporter.descriptors[0].LogicalRoundID == "" ||
		reporter.descriptors[0].LogicalRoundID !=
			reporter.descriptors[1].LogicalRoundID {
		t.Fatalf("compaction logical rounds = %#v", reporter.descriptors)
	}
}

type p242bCompactUsageReporter struct {
	next        int
	descriptors []execution.ProviderUsageDescriptor
	calls       []*p242bCompactUsageCall
}

func (r *p242bCompactUsageReporter) NewLogicalRoundID() string {
	r.next++
	return fmt.Sprintf("compact-round-%d", r.next)
}

func (r *p242bCompactUsageReporter) AdmitProviderUsage(
	_ context.Context,
	descriptor execution.ProviderUsageDescriptor,
) (execution.ProviderUsageCall, error) {
	r.next++
	call := &p242bCompactUsageCall{
		id: fmt.Sprintf("compact-call-%d", r.next),
	}
	r.descriptors = append(r.descriptors, descriptor)
	r.calls = append(r.calls, call)
	return call, nil
}

type p242bCompactUsageCall struct {
	id        string
	completed int
	usage     *schema.TokenUsage
}

func (c *p242bCompactUsageCall) ProviderCallID() string { return c.id }

func (c *p242bCompactUsageCall) CompleteProviderUsage(
	usage *schema.TokenUsage,
) error {
	c.completed++
	if usage != nil {
		copied := *usage
		c.usage = &copied
	}
	return nil
}

func (*p242bCompactUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	return nil
}

func (*p242bCompactUsageCall) MarkProviderUsageAmbiguous(error) error {
	return nil
}

type p242bSequentialCompactModel struct {
	responses []*schema.Message
	calls     int
}

func (*p242bSequentialCompactModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *p242bSequentialCompactModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if m.calls >= len(m.responses) {
		return nil, errors.New("unexpected compact model call")
	}
	response := m.responses[m.calls]
	m.calls++
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}
