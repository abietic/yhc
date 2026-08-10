package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	// DefaultSideQueryMaxRetries is the default retry count for side queries.
	// Lower than main query retries since these are background/helper calls.
	DefaultSideQueryMaxRetries = 3
)

// SideQueryRetryConfig configures retry behavior for SideQueryWithRetry.
type SideQueryRetryConfig struct {
	MaxRetries int           // 0 uses DefaultSideQueryMaxRetries.
	BaseDelay  time.Duration // Override base delay (for testing); 0 uses BaseDelayMS.
}

// SideQueryOptions holds the narrow set of direct-model options needed by
// helper/classifier-style calls outside the main query loop.
type SideQueryOptions struct {
	SystemPrompt        string
	Messages            []*schema.Message
	Tools               []*schema.ToolInfo
	Model               string
	Provider            string
	ModelRole           string
	ModelProfile        string
	EffortValue         string
	ToolChoice          string
	ForcedToolName      string
	MaxOutputTokens     *int
	QuerySource         string
	ProviderUsage       ProviderUsageAdmitter
	UsageLogicalRoundID string
}

// SideQuery performs a lightweight direct model call that still reuses the
// default runtime seam for tool binding, model selection, and tool-choice
// forwarding.
func SideQuery(ctx context.Context, chatModel model.BaseChatModel, opts SideQueryOptions) (*schema.Message, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("side query: chat model is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var systemPrompt *schema.Message
	if text := strings.TrimSpace(opts.SystemPrompt); text != "" {
		systemPrompt = &schema.Message{Role: schema.System, Content: text}
	}

	callResult, err := CallModel(ctx, chatModel, opts.Messages, systemPrompt, opts.Tools, CallModelOptions{
		SystemPrompt:        systemPrompt,
		Signal:              ctx,
		Model:               opts.Model,
		Provider:            opts.Provider,
		ModelRole:           opts.ModelRole,
		ModelProfile:        opts.ModelProfile,
		EffortValue:         opts.EffortValue,
		ToolChoice:          opts.ToolChoice,
		ForcedToolName:      opts.ForcedToolName,
		MaxOutputTokens:     opts.MaxOutputTokens,
		QuerySource:         opts.QuerySource,
		ProviderUsage:       opts.ProviderUsage,
		UsageLogicalRoundID: opts.UsageLogicalRoundID,
	})
	if err != nil {
		return nil, err
	}

	streamResult, err := ProcessStream(ctx, callResult.StreamReader, nil, func(QueryEvent) {})
	if err != nil {
		return nil, MarkProviderUsageAmbiguous(callResult.ProviderUsageCall, err)
	}
	if err := CompleteProviderUsage(
		callResult.ProviderUsageCall,
		streamResult.AssistantMessages,
	); err != nil {
		return nil, err
	}
	if streamResult.Withheld != nil {
		message := strings.TrimSpace(streamResult.Withheld.Content)
		if message == "" {
			message = "model withheld response"
		}
		if reason := strings.TrimSpace(streamResult.WithheldReason); reason != "" {
			return nil, fmt.Errorf("side query withheld api error (%s): %s", reason, message)
		}
		return nil, fmt.Errorf("side query withheld api error: %s", message)
	}
	if len(streamResult.AssistantMessages) == 0 {
		return nil, fmt.Errorf("side query returned no assistant message")
	}

	return streamResult.AssistantMessages[len(streamResult.AssistantMessages)-1], nil
}

// SideQueryWithRetry performs a side query with automatic retry on transient
// 429/529 errors. Uses exponential backoff with jitter, mirroring the main
// query retry wrapper but with lower default max retries.
// Mirrors reference sideQuery.ts retry behavior.
func SideQueryWithRetry(ctx context.Context, chatModel model.BaseChatModel, opts SideQueryOptions, retryConfig *SideQueryRetryConfig) (*schema.Message, error) {
	if retryConfig == nil {
		retryConfig = &SideQueryRetryConfig{}
	}

	maxRetries := retryConfig.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultSideQueryMaxRetries
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		result, err := SideQuery(ctx, chatModel, opts)
		if err == nil {
			return result, nil
		}
		if IsProviderUsageTerminalError(err) {
			return nil, err
		}

		// Context cancelled — abort immediately
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Only retry transient API errors
		if !IsTransientAPIError(err) {
			return nil, err
		}

		lastErr = err

		// Check retry budget
		if attempt >= maxRetries {
			return nil, lastErr
		}

		// Calculate backoff delay
		delay := RetryDelay(attempt, false, retryConfig.BaseDelay)

		// Wait with cancellation support
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}
