package execution

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// GenerateWithUsage preserves Generate options while applying the same
// admission and exact settlement boundary as streamed model calls.
func GenerateWithUsage(ctx context.Context, client model.BaseChatModel, messages []*schema.Message, admitter ProviderUsageAdmitter, descriptor ProviderUsageDescriptor, opts ...model.Option) (*schema.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return nil, fmt.Errorf("generate: chat model is required")
	}
	var call ProviderUsageCall
	if admitter != nil {
		if descriptor.LogicalRoundID == "" {
			descriptor.LogicalRoundID = admitter.NewLogicalRoundID()
		}
		var err error
		call, err = admitter.AdmitProviderUsage(ctx, descriptor)
		if err != nil {
			return nil, err
		}
		if call == nil || strings.TrimSpace(call.ProviderCallID()) == "" {
			return nil, ReleaseProviderUsageBeforeDispatch(call, &ProviderUsageTerminalError{Err: fmt.Errorf("provider usage admission returned no call identity")})
		}
		if err := ctx.Err(); err != nil {
			return nil, ReleaseProviderUsageBeforeDispatch(call, err)
		}
	}
	response, err := client.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, MarkProviderUsageAmbiguous(call, err)
	}
	if err := CompleteProviderUsage(call, []*schema.Message{response}); err != nil {
		return nil, err
	}
	return response, nil
}
