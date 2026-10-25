package engine

import (
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func contextUsageBinding(window *int) *session.PersistedModelBinding {
	return &session.PersistedModelBinding{
		Version: session.PersistedModelBindingVersion, Kind: session.ModelBindingKindProfile,
		Value: "fixture", Provider: "agenticopenai", APIModel: "gpt-4o",
		PortfolioRevision: strings.Repeat("a", 64), RouteIdentityDigest: strings.Repeat("b", 64), MetadataDigest: strings.Repeat("c", 64),
		ContextWindowTokens: window,
	}
}

func TestContextUsageSnapshotSeparatesCapacityAndMeasuredInput(t *testing.T) {
	override, invalid := 24000, 0
	for _, tt := range []struct {
		name, model string
		binding     *session.PersistedModelBinding
		known       bool
		input       int
		want        ContextUsageSnapshot
		percent     int
		legacyInput int
	}{
		{"legacy capacity before response", "gpt-4o", nil, false, 0, ContextUsageSnapshot{WindowTokens: 128000}, 0, 0},
		{"legacy measured input", "gpt-4o", nil, true, 32000, ContextUsageSnapshot{128000, 32000, true}, 25, 32000},
		{"binding override", "gpt-4o", contextUsageBinding(&override), true, 12000, ContextUsageSnapshot{24000, 12000, true}, 50, 12000},
		{"binding missing window", "gpt-4o", contextUsageBinding(nil), true, 12000, ContextUsageSnapshot{0, 12000, true}, 0, 0},
		{"invalid binding", "gpt-4o", contextUsageBinding(&invalid), true, 12000, ContextUsageSnapshot{0, 12000, true}, 0, 0},
		{"unknown model", "fixture-unknown", nil, true, 100, ContextUsageSnapshot{0, 100, true}, 0, 0},
		{"reported zero", "gpt-4o", nil, true, 0, ContextUsageSnapshot{128000, 0, true}, 0, 0},
		{"usage invalidated", "gpt-4o", nil, false, 32000, ContextUsageSnapshot{WindowTokens: 128000}, 0, 0},
		{"invalid usage", "gpt-4o", nil, true, -1, ContextUsageSnapshot{WindowTokens: 128000}, 0, 0},
		{"over window", "gpt-4o", contextUsageBinding(&override), true, 48000, ContextUsageSnapshot{24000, 48000, true}, 100, 48000},
		{"no model", "", nil, true, 100, ContextUsageSnapshot{}, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &QueryEngine{config: QueryEngineConfig{Model: tt.model}, modelBinding: tt.binding, usageSummary: transcript.UsageSummary{CurrentContextUsageKnown: tt.known, CurrentContextPromptTokens: tt.input}}
			if got := e.GetContextUsageSnapshot(); got != tt.want {
				t.Fatalf("snapshot = %#v, want %#v", got, tt.want)
			}
			if percent, input := e.GetContextUsage(); percent != tt.percent || input != tt.legacyInput {
				t.Fatalf("legacy usage = %d/%d, want %d/%d", percent, input, tt.percent, tt.legacyInput)
			}
		})
	}
	if got := (*QueryEngine)(nil).GetContextUsageSnapshot(); got != (ContextUsageSnapshot{}) {
		t.Fatal("nil engine invented a budget")
	}
}

func TestContextUsageSnapshotKeepsWindowAndUsageAtomic(t *testing.T) {
	first, second := 10000, 20000
	e := &QueryEngine{config: QueryEngineConfig{Model: "fixture"}, modelBinding: contextUsageBinding(&first), usageSummary: transcript.UsageSummary{CurrentContextUsageKnown: true, CurrentContextPromptTokens: 100}}
	start := make(chan struct{})
	var writes sync.WaitGroup
	writes.Add(1)
	go func() {
		defer writes.Done()
		<-start
		for i := 0; i < 1000; i++ {
			e.mu.Lock()
			if i%2 == 0 {
				e.modelBinding = contextUsageBinding(&first)
				e.usageSummary.CurrentContextPromptTokens = 100
			} else {
				e.modelBinding = contextUsageBinding(&second)
				e.usageSummary.CurrentContextPromptTokens = 200
			}
			e.mu.Unlock()
		}
	}()
	close(start)
	for i := 0; i < 1000; i++ {
		got := e.GetContextUsageSnapshot()
		if !got.InputKnown || got.WindowTokens != 10000 && got.WindowTokens != 20000 || got.InputTokens != got.WindowTokens/100 {
			t.Errorf("mixed usage/model generations: %#v", got)
			break
		}
	}
	writes.Wait()
}
