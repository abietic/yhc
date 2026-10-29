package agenticdeepseek

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providercost"
	enginemodel "github.com/abietic/yhc/engine/model"
)

// PrepareBudgetStream freezes the final wire body and uses the documented
// total context capacity as a conservative bound on all billable input.
// This is deliberately not a heuristic token count of the prompt.
func (m *Model) PrepareBudgetStream(ctx context.Context, input []*schema.AgenticMessage, outputCap int, opts ...model.Option) (providercost.AgenticStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport := m.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if _, ok := transport.(*http.Transport); !ok {
		return nil, providercost.ErrUnsupported
	}
	common, specific := m.options(opts...)
	if outputCap <= 0 {
		return nil, providercost.ErrUnsupported
	}
	if common.MaxTokens == nil || *common.MaxTokens > outputCap {
		common.MaxTokens = &outputCap
	}
	req, err := buildResponseRequest(input, common, specific, true)
	if err != nil {
		return nil, err
	}
	switch req.Model {
	case "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro":
	default:
		return nil, providercost.ErrUnsupported
	}
	cap, ok := enginemodel.LookupExactCapabilities(req.Model)
	if !ok || req.MaxOutputTokens == nil || *req.MaxOutputTokens < 1 || *req.MaxOutputTokens > cap.MaxOutputTokens {
		return nil, providercost.ErrUnsupported
	}
	if m.optimizeImageTransport {
		if err := optimizeResponseImages(ctx, req); err != nil {
			return nil, err
		}
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}
	// Pin the validated client and transport: admission must not be invalidated
	// by a caller replacing the client or the global default before dispatch.
	client := *m.httpClient
	client.Transport = transport
	owner := *m
	owner.httpClient = &client
	return &budgetStream{owner: &owner, input: input, tools: common.Tools, request: req, body: body, bound: providercost.Bound{Provider: "agenticdeepseek", Model: req.Model, InputTokens: uint64(cap.ContextWindow), OutputTokens: uint64(*req.MaxOutputTokens)}}, nil
}

type budgetStream struct {
	owner   *Model
	input   []*schema.AgenticMessage
	tools   []*schema.ToolInfo
	request *responseRequest
	body    []byte
	bound   providercost.Bound
	used    atomic.Bool
}

func (s *budgetStream) CostBound() providercost.Bound { return s.bound }
func (s *budgetStream) Stream(ctx context.Context) (*schema.StreamReader[*schema.AgenticMessage], error) {
	if s.used.Swap(true) {
		return nil, &ProtocolError{ReasonCode: "prepared_request_already_dispatched"}
	}
	ctx = callbacks.EnsureRunInfo(ctx, s.owner.GetType(), components.ComponentOfAgenticModel)
	return s.owner.streamRequest(ctx, s.input, s.tools, s.request, s.body, true)
}
