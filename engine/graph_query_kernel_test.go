package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestP135c3ProjectGraphFinalizesExactlyOnceAndFailsClosed(t *testing.T) {
	t.Parallel()

	var finalizerCalls atomic.Int32
	sentinel := errors.New("finalizer failed")
	build := func(finalizerErr error) *projectGraphQueryKernel {
		t.Helper()
		runnable, err := buildProjectGraphKernel(
			context.Background(),
			projectGraphKernelConfig{
				nodes: projectGraphKernelNodes{
					prepare: func(
						_ context.Context,
						round projectGraphRound,
					) (projectGraphPreparedRound, error) {
						return projectGraphPreparedRound{
							Decision: projectGraphPrepareModel,
							Values:   round.Values,
						}, nil
					},
					model: func(
						context.Context,
						projectGraphRound,
					) (projectGraphModelRound, error) {
						return projectGraphModelRound{
							Decision: projectGraphModelTerminal,
							Value:    "done",
						}, nil
					},
					tool: func(
						context.Context,
						projectGraphRound,
					) (projectGraphToolRound, error) {
						return projectGraphToolRound{}, errors.New(
							"tool node must not run",
						)
					},
					finalize: func(
						context.Context,
						projectGraphKernelResult,
					) error {
						finalizerCalls.Add(1)
						return finalizerErr
					},
				},
			},
		)
		if err != nil {
			t.Fatalf("build project graph: %v", err)
		}
		return &projectGraphQueryKernel{runnable: runnable}
	}

	success := build(nil)
	result, err := success.runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "success"},
	)
	if err != nil {
		t.Fatalf("invoke successful graph: %v", err)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "done" {
		t.Fatalf("successful result = %#v", result)
	}
	if got := finalizerCalls.Load(); got != 1 {
		t.Fatalf("successful finalizer calls = %d, want 1", got)
	}

	failed := build(sentinel)
	if _, err := failed.runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "failure"},
	); !errors.Is(err, sentinel) {
		t.Fatalf("failed finalizer error = %v, want sentinel", err)
	}
	if got := finalizerCalls.Load(); got != 2 {
		t.Fatalf("total finalizer calls = %d, want 2", got)
	}
}

func TestP135c3ProjectGraphQueryFinalizerCancelsLiveRound(t *testing.T) {
	t.Parallel()

	chain := NewCancellationChain(context.Background())
	runtime := &canonicalQueryRuntime{
		deps:              &QueryDeps{},
		cancellationChain: chain,
		terminal:          &Terminal{Reason: TerminalCompleted},
	}
	ctx := context.WithValue(
		context.Background(),
		projectGraphQueryRuntimeContextKey{},
		runtime,
	)
	if err := finalizeProjectGraphQuery(
		ctx,
		projectGraphKernelResult{Kind: projectGraphResultTerminal},
	); err != nil {
		t.Fatalf("finalize project graph query: %v", err)
	}
	if !chain.Cancelled() || chain.Reason() != "query_terminal" {
		t.Fatalf(
			"cancellation chain state = cancelled:%v reason:%q",
			chain.Cancelled(),
			chain.Reason(),
		)
	}
}

func TestP135c3ProjectGraphQueryRuntimeIsolatesConcurrentInvocations(
	t *testing.T,
) {
	t.Parallel()

	kernel, err := newProjectGraphQueryKernel(context.Background())
	if err != nil {
		t.Fatalf("build project graph query kernel: %v", err)
	}
	const invocations = 32
	type outcome struct {
		index     int
		terminal  Terminal
		assistant []string
	}
	start := make(chan struct{})
	results := make(chan outcome, invocations)
	var workers sync.WaitGroup
	workers.Add(invocations)
	for index := 0; index < invocations; index++ {
		go func() {
			defer workers.Done()
			<-start
			response := fmt.Sprintf("graph-response-%02d", index)
			events := make([]string, 0, 1)
			terminal := queryWithKernel(
				context.Background(),
				QueryParams{
					Messages: []*schema.Message{{
						Role:    schema.User,
						Content: fmt.Sprintf("graph-input-%02d", index),
					}},
					ChatModel:   &fixedResponseModel{response: response},
					QuerySource: QuerySourceSDK,
					SessionID:   fmt.Sprintf("graph-session-%02d", index),
				},
				func(event QueryEvent) {
					if event.Type != EventAssistant {
						return
					}
					message := event.AssistantMessage
					if message == nil {
						message = event.Message
					}
					if message != nil {
						events = append(
							events,
							message.Content,
						)
					}
				},
				kernel,
			)
			results <- outcome{
				index:     index,
				terminal:  terminal,
				assistant: events,
			}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	for result := range results {
		if result.terminal.Reason != TerminalCompleted ||
			result.terminal.Err != nil {
			t.Fatalf(
				"invocation %d terminal = %#v",
				result.index,
				result.terminal,
			)
		}
		want := fmt.Sprintf("graph-response-%02d", result.index)
		if len(result.assistant) == 0 ||
			!strings.Contains(
				strings.Join(result.assistant, "\n"),
				want,
			) {
			t.Fatalf(
				"invocation %d assistant events = %#v, want %q",
				result.index,
				result.assistant,
				want,
			)
		}
		for other := 0; other < invocations; other++ {
			if other == result.index {
				continue
			}
			if strings.Contains(
				strings.Join(result.assistant, "\n"),
				fmt.Sprintf("graph-response-%02d", other),
			) {
				t.Fatalf(
					"invocation %d observed response from %d",
					result.index,
					other,
				)
			}
		}
	}
	if got := productionQueryKernel().kind(); got != queryKernelProjectGraph {
		t.Fatalf("production kernel = %q, want %q", got, queryKernelProjectGraph)
	}
}

func TestP135c3ProjectGraphDoesNotAddAUserVisibleTurnLimit(t *testing.T) {
	t.Parallel()

	kernel, err := newProjectGraphQueryKernel(context.Background())
	if err != nil {
		t.Fatalf("build project graph query kernel: %v", err)
	}
	const toolRounds = 40
	run := func(name string, queryKernel queryKernel) (Terminal, int, int32) {
		t.Helper()
		responses := make([]canonicalModelResponse, 0, toolRounds)
		for index := range toolRounds {
			responses = append(responses, canonicalModelResponse{
				chunks: []*schema.Message{{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   fmt.Sprintf("%s-call-%02d", name, index),
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "LongTool",
							Arguments: fmt.Sprintf(`{"round":%d}`, index),
						},
					}},
					ResponseMeta: &schema.ResponseMeta{
						FinishReason: "tool_calls",
					},
				}},
			})
		}
		model := &canonicalScriptModel{responses: responses}
		var toolCalls atomic.Int32
		unlimited := 0
		terminal := queryWithKernel(
			context.Background(),
			QueryParams{
				Messages: []*schema.Message{{
					Role:    schema.User,
					Content: "complete a long but finite tool sequence",
				}},
				ChatModel:   model,
				QuerySource: QuerySourceSDK,
				MaxTurns:    &unlimited,
				ToolUseContext: &ToolUseContext{
					Options: &ToolUseOptions{
						Tools: []*schema.ToolInfo{{Name: "LongTool"}},
					},
				},
				ToolExecutor: func(context.Context, string, string) (string, error) {
					toolCalls.Add(1)
					return "ok", nil
				},
			},
			func(QueryEvent) {},
			queryKernel,
		)
		return terminal, model.callCount, toolCalls.Load()
	}

	graphTerminal, graphModelCalls, graphToolCalls := run("graph", kernel)
	if graphTerminal.Reason != TerminalCompleted ||
		graphTerminal.Err != nil {
		t.Fatalf(
			"graph terminal = %#v, want end_turn",
			graphTerminal,
		)
	}
	if graphModelCalls != toolRounds+1 ||
		graphToolCalls != toolRounds {
		t.Fatalf(
			"long-run counts: graph=model:%d tool:%d",
			graphModelCalls,
			graphToolCalls,
		)
	}
}
