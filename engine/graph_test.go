package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
)

func TestP135c0ProjectGraphTypedPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         func(context.Context, projectGraphRound) (projectGraphModelRound, error)
		tool          func(context.Context, projectGraphRound) (projectGraphToolRound, error)
		wantKind      projectGraphResultKind
		wantValue     string
		wantCalls     projectGraphNodeCalls
		wantTrace     []string
		wantToolCalls int32
	}{
		{
			name: "no-tool terminal",
			model: func(_ context.Context, round projectGraphRound) (projectGraphModelRound, error) {
				return projectGraphModelRound{
					Decision: projectGraphModelTerminal,
					Value:    fmt.Sprintf("terminal-%d", round.Number),
				}, nil
			},
			tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
				return projectGraphToolRound{}, errors.New("tool node must not run")
			},
			wantKind:  projectGraphResultTerminal,
			wantValue: "terminal-1",
			wantCalls: projectGraphNodeCalls{
				Prepare: 1,
				Model:   1,
			},
			wantTrace: []string{
				projectGraphPrepareRoundNode,
				projectGraphModelRoundNode,
			},
		},
		{
			name: "tool continue then terminal",
			model: func(_ context.Context, round projectGraphRound) (projectGraphModelRound, error) {
				if round.Number == 1 {
					return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
				}
				return projectGraphModelRound{
					Decision: projectGraphModelTerminal,
					Value:    fmt.Sprintf("terminal-%d", round.Number),
				}, nil
			},
			tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
				return projectGraphToolRound{Decision: projectGraphAfterToolContinue}, nil
			},
			wantKind:  projectGraphResultTerminal,
			wantValue: "terminal-2",
			wantCalls: projectGraphNodeCalls{
				Prepare: 2,
				Model:   2,
				Tool:    1,
			},
			wantTrace: []string{
				projectGraphPrepareRoundNode,
				projectGraphModelRoundNode,
				projectGraphToolRoundNode,
				projectGraphPrepareRoundNode,
				projectGraphModelRoundNode,
			},
			wantToolCalls: 1,
		},
		{
			name: "tool return",
			model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
				return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
			},
			tool: func(_ context.Context, round projectGraphRound) (projectGraphToolRound, error) {
				return projectGraphToolRound{
					Decision: projectGraphAfterToolReturn,
					Value:    fmt.Sprintf("return-%d", round.Number),
				}, nil
			},
			wantKind:  projectGraphResultReturned,
			wantValue: "return-1",
			wantCalls: projectGraphNodeCalls{
				Prepare: 1,
				Model:   1,
				Tool:    1,
			},
			wantTrace: []string{
				projectGraphPrepareRoundNode,
				projectGraphModelRoundNode,
				projectGraphToolRoundNode,
			},
			wantToolCalls: 1,
		},
		{
			name: "typed non-durable interrupt",
			model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
				return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
			},
			tool: func(_ context.Context, round projectGraphRound) (projectGraphToolRound, error) {
				return projectGraphToolRound{
					Decision: projectGraphAfterToolInterrupt,
					Value:    fmt.Sprintf("interrupt-%d", round.Number),
				}, nil
			},
			wantKind:  projectGraphResultInterrupt,
			wantValue: "interrupt-1",
			wantCalls: projectGraphNodeCalls{
				Prepare: 1,
				Model:   1,
				Tool:    1,
			},
			wantTrace: []string{
				projectGraphPrepareRoundNode,
				projectGraphModelRoundNode,
				projectGraphToolRoundNode,
			},
			wantToolCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var toolCalls atomic.Int32
			runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
				prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
					return projectGraphPreparedRound{Values: round.Values}, nil
				},
				model: test.model,
				tool: func(ctx context.Context, round projectGraphRound) (projectGraphToolRound, error) {
					toolCalls.Add(1)
					return test.tool(ctx, round)
				},
			})

			result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
				RunID:  test.name,
				Values: []string{"input"},
			})
			if err != nil {
				t.Fatalf("invoke project graph: %v", err)
			}
			if result.RunID != test.name {
				t.Fatalf("run ID = %q, want %q", result.RunID, test.name)
			}
			if result.Kind != test.wantKind {
				t.Fatalf("result kind = %q, want %q", result.Kind, test.wantKind)
			}
			if result.Value != test.wantValue {
				t.Fatalf("result value = %q, want %q", result.Value, test.wantValue)
			}
			if result.Calls != test.wantCalls {
				t.Fatalf("node calls = %#v, want %#v", result.Calls, test.wantCalls)
			}
			if !reflect.DeepEqual(result.Trace, test.wantTrace) {
				t.Fatalf("trace = %#v, want %#v", result.Trace, test.wantTrace)
			}
			if got := toolCalls.Load(); got != test.wantToolCalls {
				t.Fatalf("tool hook calls = %d, want %d", got, test.wantToolCalls)
			}
		})
	}
}

func TestP135c0ProjectGraphNodeFailuresStopLaterNodes(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("fixture node failed")
	tests := []struct {
		name        string
		failNode    string
		wantPrepare int32
		wantModel   int32
		wantTool    int32
	}{
		{
			name:        "prepare",
			failNode:    projectGraphPrepareRoundNode,
			wantPrepare: 1,
		},
		{
			name:        "model",
			failNode:    projectGraphModelRoundNode,
			wantPrepare: 1,
			wantModel:   1,
		},
		{
			name:        "tool",
			failNode:    projectGraphToolRoundNode,
			wantPrepare: 1,
			wantModel:   1,
			wantTool:    1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var prepareCalls atomic.Int32
			var modelCalls atomic.Int32
			var toolCalls atomic.Int32
			runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
				prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
					prepareCalls.Add(1)
					if test.failNode == projectGraphPrepareRoundNode {
						return projectGraphPreparedRound{}, sentinel
					}
					return projectGraphPreparedRound{Values: round.Values}, nil
				},
				model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
					modelCalls.Add(1)
					if test.failNode == projectGraphModelRoundNode {
						return projectGraphModelRound{}, sentinel
					}
					return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
				},
				tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
					toolCalls.Add(1)
					if test.failNode == projectGraphToolRoundNode {
						return projectGraphToolRound{}, sentinel
					}
					return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
				},
			})

			_, err := runnable.Invoke(context.Background(), projectGraphKernelInput{RunID: test.name})
			if !errors.Is(err, sentinel) {
				t.Fatalf("invoke error = %v, want sentinel", err)
			}
			if got := prepareCalls.Load(); got != test.wantPrepare {
				t.Fatalf("prepare calls = %d, want %d", got, test.wantPrepare)
			}
			if got := modelCalls.Load(); got != test.wantModel {
				t.Fatalf("model calls = %d, want %d", got, test.wantModel)
			}
			if got := toolCalls.Load(); got != test.wantTool {
				t.Fatalf("tool calls = %d, want %d", got, test.wantTool)
			}
		})
	}
}

func TestP135c0ProjectGraphContextCancellationStopsBeforeTools(t *testing.T) {
	t.Parallel()

	enteredModel := make(chan struct{})
	var toolCalls atomic.Int32
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(ctx context.Context, _ projectGraphRound) (projectGraphModelRound, error) {
			close(enteredModel)
			<-ctx.Done()
			return projectGraphModelRound{}, ctx.Err()
		},
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			toolCalls.Add(1)
			return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := runnable.Invoke(ctx, projectGraphKernelInput{RunID: "cancel"})
		resultCh <- err
	}()

	waitP135c0Signal(t, enteredModel, "model entry")
	cancel()
	err := waitP135c0Error(t, resultCh, "cancelled invocation")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("invoke error = %v, want context canceled", err)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool calls after cancellation = %d, want 0", got)
	}
}

func TestP135c0ProjectGraphFreezesInputAndOwnsResult(t *testing.T) {
	t.Parallel()

	enteredPrepare := make(chan struct{})
	releasePrepare := make(chan struct{})
	var prepareOnce sync.Once
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			prepareOnce.Do(func() {
				close(enteredPrepare)
				<-releasePrepare
			})
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(_ context.Context, round projectGraphRound) (projectGraphModelRound, error) {
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    strings.Join(round.Values, ","),
			}, nil
		},
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			return projectGraphToolRound{}, errors.New("tool node must not run")
		},
	})

	input := projectGraphKernelInput{
		RunID:  "ownership",
		Values: []string{"original"},
	}
	type invokeResult struct {
		result projectGraphKernelResult
		err    error
	}
	resultCh := make(chan invokeResult, 1)
	go func() {
		result, err := runnable.Invoke(context.Background(), input)
		resultCh <- invokeResult{result: result, err: err}
	}()

	waitP135c0Signal(t, enteredPrepare, "prepare entry")
	input.Values[0] = "caller-mutated"
	close(releasePrepare)
	first := waitP135c0Result(t, resultCh, "first invocation")
	if first.err != nil {
		t.Fatalf("invoke project graph: %v", first.err)
	}
	if first.result.Value != "original" {
		t.Fatalf("model value = %q, want frozen original", first.result.Value)
	}
	if !reflect.DeepEqual(first.result.InputValues, []string{"original"}) {
		t.Fatalf("frozen input = %#v, want original", first.result.InputValues)
	}

	first.result.InputValues[0] = "result-mutated"
	first.result.Trace[0] = "trace-mutated"
	second, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
		RunID:  "ownership-second",
		Values: []string{"second"},
	})
	if err != nil {
		t.Fatalf("second invoke project graph: %v", err)
	}
	if !reflect.DeepEqual(second.InputValues, []string{"second"}) {
		t.Fatalf("second input = %#v, want independent result", second.InputValues)
	}
	if !reflect.DeepEqual(second.Trace, []string{
		projectGraphPrepareRoundNode,
		projectGraphModelRoundNode,
	}) {
		t.Fatalf("second trace = %#v, want independent trace", second.Trace)
	}
}

func TestP135c0ProjectGraphConcurrentInvocationsIsolateState(t *testing.T) {
	t.Parallel()

	const invocations = 32
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			values := append([]string(nil), round.Values...)
			values = append(values, fmt.Sprintf("%s-round-%d", round.RunID, round.Number))
			return projectGraphPreparedRound{Values: values}, nil
		},
		model: func(_ context.Context, round projectGraphRound) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    strings.Join(round.Values, "|"),
			}, nil
		},
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			return projectGraphToolRound{Decision: projectGraphAfterToolContinue}, nil
		},
	})

	type invokeResult struct {
		index  int
		result projectGraphKernelResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan invokeResult, invocations)
	var workers sync.WaitGroup
	workers.Add(invocations)
	for index := 0; index < invocations; index++ {
		go func() {
			defer workers.Done()
			<-start
			runID := fmt.Sprintf("run-%02d", index)
			result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
				RunID:  runID,
				Values: []string{fmt.Sprintf("input-%02d", index)},
			})
			results <- invokeResult{index: index, result: result, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	wantTrace := []string{
		projectGraphPrepareRoundNode,
		projectGraphModelRoundNode,
		projectGraphToolRoundNode,
		projectGraphPrepareRoundNode,
		projectGraphModelRoundNode,
	}
	wantCalls := projectGraphNodeCalls{Prepare: 2, Model: 2, Tool: 1}
	for invocation := range results {
		if invocation.err != nil {
			t.Fatalf("invoke %d: %v", invocation.index, invocation.err)
		}
		runID := fmt.Sprintf("run-%02d", invocation.index)
		wantValue := fmt.Sprintf(
			"input-%02d|%s-round-2",
			invocation.index,
			runID,
		)
		if invocation.result.RunID != runID {
			t.Fatalf("invoke %d run ID = %q, want %q", invocation.index, invocation.result.RunID, runID)
		}
		if invocation.result.Kind != projectGraphResultTerminal {
			t.Fatalf("invoke %d result kind = %q", invocation.index, invocation.result.Kind)
		}
		if invocation.result.Value != wantValue {
			t.Fatalf("invoke %d value = %q, want %q", invocation.index, invocation.result.Value, wantValue)
		}
		if invocation.result.Calls != wantCalls {
			t.Fatalf("invoke %d calls = %#v, want %#v", invocation.index, invocation.result.Calls, wantCalls)
		}
		if !reflect.DeepEqual(invocation.result.Trace, wantTrace) {
			t.Fatalf("invoke %d trace = %#v, want %#v", invocation.index, invocation.result.Trace, wantTrace)
		}
	}
}

func TestP135c0ProjectGraphUsesRunStepSafetyCeiling(t *testing.T) {
	t.Parallel()

	runnable, err := buildProjectGraphKernel(context.Background(), projectGraphKernelConfig{
		maxRunSteps: 4,
		nodes: projectGraphKernelNodes{
			prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
				return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
			},
			tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
				return projectGraphToolRound{Decision: projectGraphAfterToolContinue}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("build project graph: %v", err)
	}
	_, err = runnable.Invoke(context.Background(), projectGraphKernelInput{RunID: "bounded"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "max") {
		t.Fatalf("invoke error = %v, want max-run-step safety failure", err)
	}
}

func TestP135c0ProjectGraphFailsClosedOnInvalidConfigurationAndDecisions(t *testing.T) {
	t.Parallel()

	validNodes := projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
			return projectGraphModelRound{Decision: projectGraphModelTerminal}, nil
		},
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
		},
	}
	configs := []struct {
		name   string
		config projectGraphKernelConfig
	}{
		{
			name: "negative max steps",
			config: projectGraphKernelConfig{
				maxRunSteps: -1,
				nodes:       validNodes,
			},
		},
		{
			name: "missing prepare",
			config: projectGraphKernelConfig{
				nodes: projectGraphKernelNodes{
					model: validNodes.model,
					tool:  validNodes.tool,
				},
			},
		},
		{
			name: "missing model",
			config: projectGraphKernelConfig{
				nodes: projectGraphKernelNodes{
					prepare: validNodes.prepare,
					tool:    validNodes.tool,
				},
			},
		},
		{
			name: "missing tool",
			config: projectGraphKernelConfig{
				nodes: projectGraphKernelNodes{
					prepare: validNodes.prepare,
					model:   validNodes.model,
				},
			},
		},
	}
	for _, test := range configs {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if runnable, err := buildProjectGraphKernel(context.Background(), test.config); err == nil || runnable != nil {
				t.Fatalf("build result = (%T, %v), want nil error result", runnable, err)
			}
		})
	}

	t.Run("invalid model decision", func(t *testing.T) {
		t.Parallel()
		nodes := validNodes
		nodes.model = func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
			return projectGraphModelRound{Decision: projectGraphModelDecision("invalid")}, nil
		}
		runnable := mustBuildP135c0Graph(t, nodes)
		if _, err := runnable.Invoke(context.Background(), projectGraphKernelInput{}); err == nil {
			t.Fatal("invalid model decision should fail")
		}
	})

	t.Run("invalid tool decision", func(t *testing.T) {
		t.Parallel()
		nodes := validNodes
		nodes.model = func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
			return projectGraphModelRound{Decision: projectGraphModelToolCalls}, nil
		}
		nodes.tool = func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			return projectGraphToolRound{Decision: projectGraphAfterToolDecision("invalid")}, nil
		}
		runnable := mustBuildP135c0Graph(t, nodes)
		if _, err := runnable.Invoke(context.Background(), projectGraphKernelInput{}); err == nil {
			t.Fatal("invalid tool decision should fail")
		}
	})
}

func TestP1310ProductionKernelIsProjectGraphWithoutFixtureEffects(t *testing.T) {
	t.Parallel()

	var effects atomic.Int32
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(context.Context, projectGraphRound) (projectGraphPreparedRound, error) {
			effects.Add(1)
			return projectGraphPreparedRound{}, nil
		},
		model: func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
			effects.Add(1)
			return projectGraphModelRound{Decision: projectGraphModelTerminal}, nil
		},
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			effects.Add(1)
			return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
		},
	})
	if runnable == nil {
		t.Fatal("fixture graph runnable is nil")
	}
	if got := productionQueryKernel().kind(); got != queryKernelProjectGraph {
		t.Fatalf("production kernel = %q, want %q", got, queryKernelProjectGraph)
	}
	if got := effects.Load(); got != 0 {
		t.Fatalf("fixture graph effects during construction/production selection = %d, want 0", got)
	}
}

func mustBuildP135c0Graph(
	t *testing.T,
	nodes projectGraphKernelNodes,
) compose.Runnable[projectGraphKernelInput, projectGraphKernelResult] {
	t.Helper()
	runnable, err := buildProjectGraphKernel(context.Background(), projectGraphKernelConfig{
		nodes: nodes,
	})
	if err != nil {
		t.Fatalf("build project graph: %v", err)
	}
	if runnable == nil {
		t.Fatal("build project graph returned nil Runnable")
	}
	return runnable
}

func waitP135c0Signal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitP135c0Error(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func waitP135c0Result[T any](t *testing.T, result <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}
