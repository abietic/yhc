package tools

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type delayedWebFetchModel struct {
	webFetchGoalUsageModel
	delay time.Duration
}

func (m *delayedWebFetchModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	timer := time.NewTimer(m.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant, Content: "A complete summary.",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11}},
		}}), nil
	}
}

type delayedWebFetchAdmission struct {
	webFetchGoalUsageAdmitter
	delay time.Duration
}

func (a *delayedWebFetchAdmission) AdmitProviderUsage(ctx context.Context, d execution.ProviderUsageDescriptor) (execution.ProviderUsageCall, error) {
	timer := time.NewTimer(a.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return a.webFetchGoalUsageAdmitter.AdmitProviderUsage(ctx, d)
	}
}

func TestWebFetchSummaryFollowsCallerLifetime(t *testing.T) {
	for _, tc := range []struct {
		name           string
		admissionDelay time.Duration
		modelDelay     time.Duration
		parentTimeout  time.Duration
		cancelAfter    time.Duration
		wantElapsed    time.Duration
		wantError      error
		wantCalls      int
	}{
		{name: "slow reasoning", modelDelay: 73 * time.Second, parentTimeout: 2 * time.Minute, wantElapsed: 73 * time.Second, wantCalls: 1},
		{name: "queued admission then reasoning", admissionDelay: 40 * time.Second, modelDelay: 45 * time.Second, parentTimeout: 2 * time.Minute, wantElapsed: 85 * time.Second, wantCalls: 1},
		{name: "parent deadline during stream", modelDelay: 73 * time.Second, parentTimeout: 45 * time.Second, wantElapsed: 45 * time.Second, wantError: context.DeadlineExceeded, wantCalls: 1},
		{name: "parent deadline during admission", admissionDelay: 40 * time.Second, modelDelay: time.Second, parentTimeout: 10 * time.Second, wantElapsed: 10 * time.Second, wantError: context.DeadlineExceeded},
		{name: "user cancellation", modelDelay: 73 * time.Second, cancelAfter: 10 * time.Second, wantElapsed: 10 * time.Second, wantError: context.Canceled, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.parentTimeout > 0 {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, tc.parentTimeout)
					defer stop()
				}
				if tc.cancelAfter > 0 {
					timer := time.AfterFunc(tc.cancelAfter, cancel)
					defer timer.Stop()
				}
				owner := &delayedWebFetchAdmission{delay: tc.admissionDelay}
				client := &delayedWebFetchModel{delay: tc.modelDelay}
				ctx = execution.WithProviderUsageScope(ctx, owner, true)
				started := time.Now()
				result, err := webFetchAIProcess(ctx, client, "summarize", "local fixture", webFetchExampleURL)
				if !errors.Is(err, tc.wantError) || time.Since(started) != tc.wantElapsed {
					t.Fatalf("summary = %q, error = %v, elapsed = %s", result, err, time.Since(started))
				}
				if owner.admissions != tc.wantCalls || client.streamCalls != tc.wantCalls {
					t.Fatalf("admissions=%d streams=%d, want %d", owner.admissions, client.streamCalls, tc.wantCalls)
				}
				if tc.wantCalls == 0 {
					if owner.call != nil {
						t.Fatal("cancelled admission allocated a usage call")
					}
					return
				}
				if tc.wantError != nil {
					if !execution.IsProviderUsageTerminalError(err) || owner.call.ambiguous != 1 || owner.call.completed != 0 {
						t.Fatal("cancelled stream lost its unknown-usage fence")
					}
				} else if result != "A complete summary." || owner.call.completed != 1 || owner.call.ambiguous != 0 || owner.call.usage.TotalTokens != 11 {
					t.Fatal("successful summary did not settle its final usage exactly once")
				}
			})
		})
	}
}
