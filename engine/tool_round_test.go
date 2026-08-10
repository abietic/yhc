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

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	toolpkg "github.com/abietic/yhc/tools"
)

func TestP135c2ProjectGraphCanonicalToolRoundPreservesStableBatchesAndOrder(
	t *testing.T,
) {
	t.Parallel()

	calls := []schema.ToolCall{
		p135c2ToolCall("safe-a", "SafeA", `{"value":"a"}`),
		p135c2ToolCall("safe-b", "SafeB", `{"value":"b"}`),
		p135c2ToolCall("serial", "Serial", `{"value":"serial"}`),
		p135c2ToolCall("safe-c", "SafeC", `{"value":"c"}`),
		p135c2ToolCall("safe-d", "SafeD", `{"value":"d"}`),
	}
	registry := toolpkg.NewRegistry()
	for _, call := range calls {
		name := call.Function.Name
		safe := strings.HasPrefix(name, "Safe")
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
			IsConcurrencySafe: func(map[string]any) bool {
				return safe
			},
		})
	}

	firstRelease := make(chan struct{})
	firstFinished := make(chan struct{})
	serialFinished := make(chan struct{})
	secondRelease := make(chan struct{})
	var firstStarted atomic.Int32
	var firstDone atomic.Int32
	var secondStarted atomic.Int32
	var current atomic.Int32
	var maximum atomic.Int32
	var executionCount atomic.Int32
	executor := func(ctx context.Context, name, _ string) (string, error) {
		executionCount.Add(1)
		inFlight := current.Add(1)
		p135c2RecordMaximum(&maximum, inFlight)
		defer current.Add(-1)
		switch name {
		case "SafeA", "SafeB":
			if firstStarted.Add(1) == 2 {
				close(firstRelease)
			}
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if firstDone.Add(1) == 2 {
				close(firstFinished)
			}
		case "Serial":
			select {
			case <-firstFinished:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if current.Load() != 1 {
				return "", fmt.Errorf(
					"serial barrier overlapped with %d calls",
					current.Load(),
				)
			}
			close(serialFinished)
		case "SafeC", "SafeD":
			select {
			case <-serialFinished:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if secondStarted.Add(1) == 2 {
				close(secondRelease)
			}
			select {
			case <-secondRelease:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "result:" + name, nil
	}

	var eventsMu sync.Mutex
	var resultIDs []string
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: calls,
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    "done",
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry:      registry,
					ToolExecutor:      executor,
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hooks.NewExecutor(),
				yield: func(event QueryEvent) {
					if event.Type != EventToolResult ||
						event.ToolResultMessage == nil {
						return
					}
					eventsMu.Lock()
					resultIDs = append(
						resultIDs,
						event.ToolResultMessage.ToolCallID,
					)
					eventsMu.Unlock()
				},
			}, nil
		}),
	})

	result, err := runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "stable-batches"},
	)
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "done" {
		t.Fatalf("graph result = %#v", result)
	}
	if result.Calls != (projectGraphNodeCalls{
		Prepare: 2,
		Model:   2,
		Tool:    1,
	}) {
		t.Fatalf("node calls = %#v", result.Calls)
	}
	if executionCount.Load() != int32(len(calls)) {
		t.Fatalf(
			"tool executions = %d, want %d",
			executionCount.Load(),
			len(calls),
		)
	}
	if maximum.Load() < 2 {
		t.Fatalf("maximum concurrency = %d, want at least 2", maximum.Load())
	}
	wantIDs := []string{"safe-a", "safe-b", "serial", "safe-c", "safe-d"}
	eventsMu.Lock()
	gotIDs := append([]string(nil), resultIDs...)
	eventsMu.Unlock()
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("event result IDs = %#v, want %#v", gotIDs, wantIDs)
	}
	if got := p135c2ToolMessageIDs(result.ToolMessages); !reflect.DeepEqual(
		got,
		wantIDs,
	) {
		t.Fatalf("stored result IDs = %#v, want %#v", got, wantIDs)
	}
}

func TestP135c2ProjectGraphCanonicalToolRoundPreservesPlanAdmission(
	t *testing.T,
) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)

	exactPlanPath := toolpkg.GetPlanFilePath("session", "agent")
	calls := []schema.ToolCall{
		p135c2ToolCall(
			"exact-write",
			"Write",
			`{"file_path":`+p17H0JSONString(t, exactPlanPath)+`}`,
		),
		p135c2ToolCall("blocked-bash", "Bash", `{"command":"true"}`),
	}
	registry := toolpkg.NewRegistry()
	for _, name := range []string{"Bash", "Write"} {
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
		})
	}
	toolUseContext := &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
	var permissionChecks atomic.Int32
	var executions atomic.Int32
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: calls,
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    "done",
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry: registry,
					CanUseTool: func(
						context.Context,
						string,
						map[string]any,
						*ToolUseContext,
					) (bool, string) {
						permissionChecks.Add(1)
						return true, ""
					},
					ToolExecutor: func(
						context.Context,
						string,
						string,
					) (string, error) {
						executions.Add(1)
						return "executed", nil
					},
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				toolUseContext:    toolUseContext,
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hooks.NewExecutor(),
			}, nil
		}),
	})

	result, err := runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "plan-admission"},
	)
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "done" {
		t.Fatalf("graph result = %#v", result)
	}
	if len(result.ToolMessages) != len(calls) {
		t.Fatalf(
			"Graph Plan tool messages = %d, want %d",
			len(result.ToolMessages),
			len(calls),
		)
	}
	if permissionChecks.Load() != 1 || executions.Load() != 1 {
		t.Fatalf(
			"Graph Plan admission reached permission/execution = %d/%d, want 1/1; results = %q / %q",
			permissionChecks.Load(),
			executions.Load(),
			result.ToolMessages[0].Content,
			result.ToolMessages[1].Content,
		)
	}
	if got := p135c2ToolMessageIDs(result.ToolMessages); !reflect.DeepEqual(
		got,
		[]string{"exact-write", "blocked-bash"},
	) {
		t.Fatalf("tool messages = %#v", got)
	}
	if result.ToolMessages[0].Content != "executed" || !strings.Contains(
		result.ToolMessages[1].Content,
		"not available while plan mode is active",
	) {
		t.Fatalf("Plan tool results = %#v", result.ToolMessages)
	}
}

func TestP135c2ProjectGraphCanonicalToolRoundPreservesHooksPermissionContextAndReturn(
	t *testing.T,
) {
	t.Parallel()

	calls := []schema.ToolCall{
		p135c2ToolCall("denied", "DeniedTool", `{"value":"deny"}`),
		p135c2ToolCall("rewrite", "RewriteTool", `{"value":"before"}`),
		p135c2ToolCall("stop", "StopTool", `{"value":"stop"}`),
		p135c2ToolCall("enter-plan", "EnterPlanMode", `{}`),
	}
	registry := toolpkg.NewRegistry()
	for _, call := range calls {
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: call.Function.Name},
		})
	}
	registry.Register(toolpkg.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
		IsPlanModeTransition: true,
	})

	var orderMu sync.Mutex
	var order []string
	record := func(value string) {
		orderMu.Lock()
		order = append(order, value)
		orderMu.Unlock()
	}
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterPreTool(func(
		_ context.Context,
		name string,
		_ string,
		input map[string]any,
	) *hooks.PreToolHookResult {
		record("pre:" + name)
		if name == "RewriteTool" {
			return &hooks.PreToolHookResult{
				UpdatedInput: map[string]any{"value": "after"},
			}
		}
		return nil
	})
	hookExecutor.RegisterPostTool(func(
		_ context.Context,
		name string,
		_ string,
		_ map[string]any,
		result string,
	) *hooks.PostToolHookResult {
		record("post:" + name)
		if name == "StopTool" {
			return &hooks.PostToolHookResult{
				UpdatedResult:       result + ":post",
				ReplaceResult:       true,
				PreventContinuation: true,
				StopReason:          "fixture stop",
			}
		}
		return nil
	})
	toolUseContext := &ToolUseContext{
		Options: &ToolUseOptions{PermissionMode: permission.ModeDefault},
	}
	queryTracking := &QueryTracking{ChainID: "tool-round", Depth: 3}
	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: newRepeatedToolCallGuard(),
		CanUseTool: func(
			_ context.Context,
			name string,
			_ map[string]any,
			_ *ToolUseContext,
		) (bool, string) {
			record("permission:" + name)
			if name == "DeniedTool" {
				return false, "fixture denied"
			}
			return true, ""
		},
		ToolExecutor: func(
			_ context.Context,
			name string,
			input string,
		) (string, error) {
			record("execute:" + name + ":" + input)
			return "result:" + name, nil
		},
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			context.Context,
			projectGraphRound,
		) (projectGraphModelRound, error) {
			return projectGraphModelRound{
				Decision:  projectGraphModelToolCalls,
				ToolCalls: calls,
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params:            params,
				toolUseContext:    toolUseContext,
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hookExecutor,
				queryTracking:     queryTracking,
				yield:             func(QueryEvent) {},
			}, nil
		}),
	})

	result, err := runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "rich-outcomes"},
	)
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	if result.Kind != projectGraphResultReturned ||
		result.Value != "result:StopTool:post" ||
		result.TerminalReason != TerminalHookStopped {
		t.Fatalf("graph result = %#v", result)
	}
	if !toolUseContext.PlanMode ||
		toolUseContext.Options.PermissionMode != permission.ModePlan ||
		toolUseContext.QueryTracking != queryTracking {
		t.Fatalf("tool context = %#v", toolUseContext)
	}
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	if !p135c2ContainsOrdered(gotOrder, []string{
		"pre:DeniedTool",
		"permission:DeniedTool",
		"pre:RewriteTool",
		"permission:RewriteTool",
		`execute:RewriteTool:{"value":"after"}`,
		"post:RewriteTool",
		"pre:StopTool",
		"permission:StopTool",
		"execute:StopTool:" + calls[2].Function.Arguments,
		"post:StopTool",
		"pre:EnterPlanMode",
		"permission:EnterPlanMode",
		"execute:EnterPlanMode:" + calls[3].Function.Arguments,
		"post:EnterPlanMode",
	}) {
		t.Fatalf("hook/permission/execution order = %#v", gotOrder)
	}
	for _, item := range gotOrder {
		if strings.HasPrefix(item, "execute:DeniedTool:") {
			t.Fatalf("denied tool executed: %#v", gotOrder)
		}
	}
	if got := p135c2ToolMessageIDs(result.ToolMessages); !reflect.DeepEqual(
		got,
		[]string{"denied", "rewrite", "stop", "enter-plan"},
	) {
		t.Fatalf("tool messages = %#v", got)
	}
}

func TestP135c2ProjectGraphCanonicalToolRoundCancelsBashSiblings(
	t *testing.T,
) {
	t.Parallel()

	calls := []schema.ToolCall{
		p135c2ToolCall("bash", "Bash", `{"command":"fail"}`),
		p135c2ToolCall("read", "Read", `{"file_path":"/tmp/a"}`),
		p135c2ToolCall(
			"write",
			"Write",
			`{"file_path":"/tmp/b","content":"x"}`,
		),
	}
	registry := toolpkg.NewRegistry()
	for _, call := range calls {
		name := call.Function.Name
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
			IsConcurrencySafe: func(map[string]any) bool {
				return name != "Write"
			},
		})
	}
	readStarted := make(chan struct{})
	var executionsMu sync.Mutex
	var executions []string
	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: newRepeatedToolCallGuard(),
		ToolExecutor: func(
			ctx context.Context,
			name string,
			_ string,
		) (string, error) {
			executionsMu.Lock()
			executions = append(executions, name)
			executionsMu.Unlock()
			switch name {
			case "Bash":
				select {
				case <-readStarted:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return "", errors.New("bash fixture failed")
			case "Read":
				close(readStarted)
				<-ctx.Done()
				return "", ctx.Err()
			case "Write":
				return "", errors.New("write must remain queued")
			default:
				return "", fmt.Errorf("unexpected tool %q", name)
			}
		},
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: calls,
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    "done",
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params:            params,
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hooks.NewExecutor(),
			}, nil
		}),
	})

	result, err := runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "bash-sibling"},
	)
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	executionsMu.Lock()
	gotExecutions := append([]string(nil), executions...)
	executionsMu.Unlock()
	if !reflect.DeepEqual(gotExecutions, []string{"Bash", "Read"}) &&
		!reflect.DeepEqual(gotExecutions, []string{"Read", "Bash"}) {
		t.Fatalf("tool executions = %#v", gotExecutions)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "done" {
		t.Fatalf("graph result = %#v", result)
	}
	if got := p135c2ToolMessageIDs(result.ToolMessages); !reflect.DeepEqual(
		got,
		[]string{"bash", "read", "write"},
	) {
		t.Fatalf("tool messages = %#v", got)
	}
	if !strings.Contains(result.ToolMessages[2].Content, "parallel tool call Bash errored") {
		t.Fatalf("queued sibling result = %#v", result.ToolMessages[2])
	}
}

func TestP135c2ProjectGraphCanonicalToolRoundMapsAbortAndInvocationCancellation(
	t *testing.T,
) {
	t.Parallel()

	t.Run("abort controller becomes non-durable interrupt", func(t *testing.T) {
		t.Parallel()

		call := p135c2ToolCall("abort", "AbortTool", `{"value":1}`)
		registry := toolpkg.NewRegistry()
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: "AbortTool"},
		})
		abortCtx, cancelAbort := context.WithCancel(context.Background())
		controller := &AbortController{Ctx: abortCtx, Cancel: cancelAbort}
		toolUseContext := &ToolUseContext{AbortController: controller}
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				context.Context,
				projectGraphRound,
			) (projectGraphModelRound, error) {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{call},
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params: QueryParams{
						ToolRegistry: registry,
						ToolExecutor: func(
							context.Context,
							string,
							string,
						) (string, error) {
							controller.Abort()
							return "aborted after tool", nil
						},
						repeatedToolGuard: newRepeatedToolCallGuard(),
					},
					toolUseContext:    toolUseContext,
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
				}, nil
			}),
		})

		result, err := runnable.Invoke(
			context.Background(),
			projectGraphKernelInput{RunID: "abort"},
		)
		if err != nil {
			t.Fatalf("invoke project graph: %v", err)
		}
		if result.Kind != projectGraphResultInterrupt ||
			result.Value != "user_abort" ||
			result.TerminalReason != TerminalAbortedTools {
			t.Fatalf("graph result = %#v", result)
		}
	})

	t.Run("abort respects cancel and block registry metadata", func(t *testing.T) {
		t.Parallel()

		calls := []schema.ToolCall{
			p135c2ToolCall("cancel", "CancelTool", `{"value":1}`),
			p135c2ToolCall("block", "BlockTool", `{"value":2}`),
		}
		registry := toolpkg.NewRegistry()
		registry.Register(toolpkg.ToolImpl{
			Info:              &schema.ToolInfo{Name: "CancelTool"},
			InterruptBehavior: "cancel",
			IsConcurrencySafe: func(map[string]any) bool {
				return true
			},
		})
		registry.Register(toolpkg.ToolImpl{
			Info:              &schema.ToolInfo{Name: "BlockTool"},
			InterruptBehavior: "block",
			IsConcurrencySafe: func(map[string]any) bool {
				return true
			},
		})
		abortCtx, cancelAbort := context.WithCancel(context.Background())
		controller := &AbortController{Ctx: abortCtx, Cancel: cancelAbort}
		cancelEntered := make(chan struct{})
		cancelStopped := make(chan struct{})
		blockEntered := make(chan struct{})
		blockRelease := make(chan struct{})
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				context.Context,
				projectGraphRound,
			) (projectGraphModelRound, error) {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: calls,
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params: QueryParams{
						ToolRegistry: registry,
						ToolExecutor: func(
							toolCtx context.Context,
							name string,
							_ string,
						) (string, error) {
							switch name {
							case "CancelTool":
								close(cancelEntered)
								<-toolCtx.Done()
								close(cancelStopped)
								return "", toolCtx.Err()
							case "BlockTool":
								close(blockEntered)
								select {
								case <-blockRelease:
									return "block completed", nil
								case <-toolCtx.Done():
									return "", errors.New(
										"block context was cancelled",
									)
								}
							default:
								return "", fmt.Errorf(
									"unexpected tool %q",
									name,
								)
							}
						},
						repeatedToolGuard: newRepeatedToolCallGuard(),
					},
					toolUseContext: &ToolUseContext{
						AbortController: controller,
					},
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
				}, nil
			}),
		})

		type graphInvocation struct {
			result projectGraphKernelResult
			err    error
		}
		done := make(chan graphInvocation, 1)
		go func() {
			result, err := runnable.Invoke(
				context.Background(),
				projectGraphKernelInput{RunID: "interrupt-behavior"},
			)
			done <- graphInvocation{result: result, err: err}
		}()
		waitP135c0Signal(t, cancelEntered, "cancel tool execution")
		waitP135c0Signal(t, blockEntered, "block tool execution")
		controller.Abort()
		select {
		case invocation := <-done:
			t.Fatalf(
				"graph returned before block tool settled: %#v",
				invocation,
			)
		case <-time.After(50 * time.Millisecond):
		}
		close(blockRelease)
		invocation := waitP135c2Result(
			t,
			done,
			"interrupt-behavior graph invocation",
		)
		if invocation.err != nil {
			t.Fatalf("invoke project graph: %v", invocation.err)
		}
		waitP135c0Signal(t, cancelStopped, "cancel tool cancellation")
		if invocation.result.Kind != projectGraphResultInterrupt ||
			invocation.result.Value != "user_abort" ||
			invocation.result.TerminalReason != TerminalAbortedTools {
			t.Fatalf("graph result = %#v", invocation.result)
		}
		if len(invocation.result.ToolMessages) != 2 {
			t.Fatalf(
				"tool messages = %#v",
				invocation.result.ToolMessages,
			)
		}
		if invocation.result.ToolMessages[0].ToolCallID != "cancel" ||
			invocation.result.ToolMessages[0].Content !=
				"Interrupted by user" {
			t.Fatalf(
				"cancel message = %#v",
				invocation.result.ToolMessages[0],
			)
		}
		if invocation.result.ToolMessages[1].ToolCallID != "block" ||
			invocation.result.ToolMessages[1].Content !=
				"block completed" {
			t.Fatalf(
				"block message = %#v",
				invocation.result.ToolMessages[1],
			)
		}
	})

	t.Run("invocation cancellation remains an error", func(t *testing.T) {
		t.Parallel()

		call := p135c2ToolCall("cancel", "CancelTool", `{"value":1}`)
		registry := toolpkg.NewRegistry()
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: "CancelTool"},
		})
		entered := make(chan struct{})
		var executions atomic.Int32
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				context.Context,
				projectGraphRound,
			) (projectGraphModelRound, error) {
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{call},
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params: QueryParams{
						ToolRegistry: registry,
						ToolExecutor: func(
							toolCtx context.Context,
							_ string,
							_ string,
						) (string, error) {
							executions.Add(1)
							close(entered)
							<-toolCtx.Done()
							return "", toolCtx.Err()
						},
						repeatedToolGuard: newRepeatedToolCallGuard(),
					},
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
				}, nil
			}),
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := runnable.Invoke(
				ctx,
				projectGraphKernelInput{RunID: "cancel"},
			)
			done <- err
		}()
		waitP135c0Signal(t, entered, "tool execution")
		cancel()
		err := waitP135c0Error(t, done, "cancelled tool round")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("invoke error = %v, want context canceled", err)
		}
		if executions.Load() != 1 {
			t.Fatalf("tool executions = %d, want 1", executions.Load())
		}
	})
}

func TestP135c2ProjectGraphCanonicalToolRoundUsesLiveRegistryAndRepeatedGuard(
	t *testing.T,
) {
	t.Parallel()

	t.Run("tool node observes registration completed by model round", func(t *testing.T) {
		t.Parallel()

		registry := toolpkg.NewRegistry()
		var validations atomic.Int32
		var executions atomic.Int32
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphModelRound, error) {
				if round.Number != 1 {
					return projectGraphModelRound{
						Decision: projectGraphModelTerminal,
						Value:    "done",
					}, nil
				}
				registry.Register(toolpkg.ToolImpl{
					Info: &schema.ToolInfo{Name: "DynamicTool"},
					ValidateInput: func(input map[string]any) error {
						validations.Add(1)
						if input["value"] != "fresh" {
							return fmt.Errorf("stale input: %#v", input)
						}
						return nil
					},
				})
				return projectGraphModelRound{
					Decision: projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{p135c2ToolCall(
						"dynamic",
						"DynamicTool",
						`{"value":"fresh"}`,
					)},
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params: QueryParams{
						ToolRegistry: registry,
						ToolExecutor: func(
							context.Context,
							string,
							string,
						) (string, error) {
							executions.Add(1)
							return "dynamic result", nil
						},
						repeatedToolGuard: newRepeatedToolCallGuard(),
					},
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
				}, nil
			}),
		})

		result, err := runnable.Invoke(
			context.Background(),
			projectGraphKernelInput{RunID: "dynamic-registry"},
		)
		if err != nil {
			t.Fatalf("invoke project graph: %v", err)
		}
		if result.Kind != projectGraphResultTerminal || result.Value != "done" {
			t.Fatalf("graph result = %#v", result)
		}
		if validations.Load() != 3 || executions.Load() != 1 {
			t.Fatalf(
				"validation/execution counts = %d/%d, want 3/1",
				validations.Load(),
				executions.Load(),
			)
		}
		if len(result.ToolMessages) != 1 ||
			result.ToolMessages[0].Content != "dynamic result" {
			t.Fatalf("tool messages = %#v", result.ToolMessages)
		}
	})

	t.Run("third identical call is blocked before side effects", func(t *testing.T) {
		t.Parallel()

		registry := toolpkg.NewRegistry()
		registry.Register(toolpkg.ToolImpl{
			Info: &schema.ToolInfo{Name: "RepeatTool"},
		})
		guard := newRepeatedToolCallGuard()
		var executions atomic.Int32
		var permissionChecks atomic.Int32
		params := QueryParams{
			ToolRegistry:      registry,
			repeatedToolGuard: guard,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				permissionChecks.Add(1)
				return true, ""
			},
			ToolExecutor: func(
				context.Context,
				string,
				string,
			) (string, error) {
				executions.Add(1)
				return "executed", nil
			},
		}
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphModelRound, error) {
				if round.Number > 3 {
					return projectGraphModelRound{
						Decision: projectGraphModelTerminal,
						Value:    "done",
					}, nil
				}
				return projectGraphModelRound{
					Decision: projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{p135c2ToolCall(
						fmt.Sprintf("repeat-%d", round.Number),
						"RepeatTool",
						`{"value":"same"}`,
					)},
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params:            params,
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
				}, nil
			}),
		})

		result, err := runnable.Invoke(
			context.Background(),
			projectGraphKernelInput{RunID: "repeated-tool"},
		)
		if err != nil {
			t.Fatalf("invoke project graph: %v", err)
		}
		if result.Kind != projectGraphResultTerminal || result.Value != "done" {
			t.Fatalf("graph result = %#v", result)
		}
		if result.Calls.Tool != 3 {
			t.Fatalf("tool node calls = %d, want 3", result.Calls.Tool)
		}
		if executions.Load() != 2 || permissionChecks.Load() != 2 {
			t.Fatalf(
				"execution/permission counts = %d/%d, want 2/2",
				executions.Load(),
				permissionChecks.Load(),
			)
		}
		if len(result.ToolMessages) != 1 ||
			!strings.Contains(
				result.ToolMessages[0].Content,
				"Repeated identical tool call blocked",
			) {
			t.Fatalf("last tool messages = %#v", result.ToolMessages)
		}
	})

	t.Run("canonical permission coordinator coalesces matching calls", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		promptEntered := make(chan string, 2)
		promptRelease := make(chan PermissionInteractionResult, 2)
		registry := toolpkg.NewRegistry()
		toolpkg.RegisterDefaults(registry)
		bash, ok := registry.Get("Bash")
		if !ok {
			t.Fatal("default Bash tool not registered")
		}
		bash.IsConcurrencySafe = func(map[string]any) bool {
			return true
		}
		registry.Update("Bash", bash)
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID:             "graph-session",
			RootSessionID:         "graph-session",
			CWD:                   root,
			PermissionProjectRoot: root,
			PermissionRegistry:    NewPermissionCoordinatorRegistry(),
			ToolRegistry:          registry,
			PermissionPrompt: func(
				ctx context.Context,
				request PermissionPromptRequest,
			) PermissionInteractionResult {
				promptEntered <- request.ToolUseID
				select {
				case result := <-promptRelease:
					return result
				case <-ctx.Done():
					return PermissionInteractionResult{
						Decision: PermissionDeny,
						Message:  ctx.Err().Error(),
					}
				}
			},
		})
		t.Cleanup(eng.Close)

		var executions atomic.Int32
		var eventsMu sync.Mutex
		var permissionEvents []QueryEvent
		params := QueryParams{
			ToolRegistry:      registry,
			repeatedToolGuard: newRepeatedToolCallGuard(),
			CanUseTool:        eng.wrappedCanUseTool,
			ToolExecutor: func(
				context.Context,
				string,
				string,
			) (string, error) {
				executions.Add(1)
				return "ok", nil
			},
		}
		calls := []schema.ToolCall{
			p135c2ToolCall("permission-a", "Bash", `{"command":"go test ./..."}`),
			p135c2ToolCall("permission-b", "Bash", `{"command":"go test ./..."}`),
		}
		runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
			prepare: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphPreparedRound, error) {
				return projectGraphPreparedRound{Values: round.Values}, nil
			},
			model: func(
				_ context.Context,
				round projectGraphRound,
			) (projectGraphModelRound, error) {
				if round.Number != 1 {
					return projectGraphModelRound{
						Decision: projectGraphModelTerminal,
						Value:    "done",
					}, nil
				}
				return projectGraphModelRound{
					Decision:  projectGraphModelToolCalls,
					ToolCalls: calls,
				}, nil
			},
			tool: bindProjectGraphCanonicalToolRound(func(
				ctx context.Context,
				_ projectGraphRound,
			) (canonicalToolRoundInput, error) {
				return canonicalToolRoundInput{
					params:            params,
					cancellationChain: NewCancellationChain(ctx),
					hookExecutor:      hooks.NewExecutor(),
					yield: func(event QueryEvent) {
						if event.Type != EventPermissionRequest &&
							event.Type != EventPermissionResolved {
							return
						}
						eventsMu.Lock()
						permissionEvents = append(permissionEvents, event)
						eventsMu.Unlock()
					},
				}, nil
			}),
		})

		type graphInvocation struct {
			result projectGraphKernelResult
			err    error
		}
		done := make(chan graphInvocation, 1)
		go func() {
			result, err := runnable.Invoke(
				context.Background(),
				projectGraphKernelInput{RunID: "permission-coalescing"},
			)
			done <- graphInvocation{result: result, err: err}
		}()
		firstID := waitP135c2Result(t, promptEntered, "first permission prompt")
		secondID := waitP135c2Result(t, promptEntered, "second permission prompt")
		if firstID == secondID {
			t.Fatalf("permission prompt IDs = %q/%q", firstID, secondID)
		}
		promptRelease <- PermissionInteractionResult{
			Decision: PermissionAllowSession,
			Message:  "approved once for matching scope",
		}
		invocation := waitP135c2Result(t, done, "coalesced graph invocation")
		promptRelease <- PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "late follower result must be ignored",
		}
		if invocation.err != nil {
			t.Fatalf("invoke project graph: %v", invocation.err)
		}
		if invocation.result.Kind != projectGraphResultTerminal ||
			invocation.result.Value != "done" {
			t.Fatalf("graph result = %#v", invocation.result)
		}
		eventsMu.Lock()
		gotEvents := append([]QueryEvent(nil), permissionEvents...)
		eventsMu.Unlock()
		if executions.Load() != 2 {
			t.Fatalf(
				"tool executions = %d, want 2; tool messages = %#v; "+
					"permission events = %#v; approvals = %#v; denials = %#v",
				executions.Load(),
				invocation.result.ToolMessages,
				gotEvents,
				eng.approvalTracker.List(),
				eng.GetPermissionDenials(),
			)
		}
		if eng.approvalTracker.Count() != 1 {
			t.Fatalf(
				"approval count = %d, want one source grant",
				eng.approvalTracker.Count(),
			)
		}
		resolved := 0
		coalesced := 0
		for _, event := range gotEvents {
			if event.Type != EventPermissionResolved ||
				event.PermissionResolved == nil {
				continue
			}
			resolved++
			if event.PermissionResolved.Reason == "coalesced" {
				coalesced++
			}
		}
		if resolved != 2 || coalesced != 1 {
			t.Fatalf(
				"permission events resolved/coalesced = %d/%d: %#v",
				resolved,
				coalesced,
				gotEvents,
			)
		}
	})
}

func TestP135c2ProjectGraphCanonicalToolRoundIsolatesConcurrentInvocations(
	t *testing.T,
) {
	t.Parallel()

	const invocationCount = 16
	registry := toolpkg.NewRegistry()
	registry.Register(toolpkg.ToolImpl{
		Info: &schema.ToolInfo{Name: "EchoTool"},
		IsConcurrencySafe: func(map[string]any) bool {
			return true
		},
	})
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number != 1 {
				return projectGraphModelRound{
					Decision: projectGraphModelTerminal,
					Value:    round.RunID,
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelToolCalls,
				ToolCalls: []schema.ToolCall{p135c2ToolCall(
					"call-"+round.RunID,
					"EchoTool",
					fmt.Sprintf(`{"run_id":%q}`, round.RunID),
				)},
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry: registry,
					ToolExecutor: func(
						_ context.Context,
						_ string,
						input string,
					) (string, error) {
						return input, nil
					},
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hooks.NewExecutor(),
			}, nil
		}),
	})

	type invocationResult struct {
		index  int
		result projectGraphKernelResult
		err    error
	}
	results := make(chan invocationResult, invocationCount)
	for index := 0; index < invocationCount; index++ {
		go func() {
			runID := fmt.Sprintf("concurrent-%02d", index)
			result, err := runnable.Invoke(
				context.Background(),
				projectGraphKernelInput{RunID: runID},
			)
			results <- invocationResult{
				index:  index,
				result: result,
				err:    err,
			}
		}()
	}
	for range invocationCount {
		invocation := waitP135c2Result(
			t,
			results,
			"concurrent graph invocation",
		)
		if invocation.err != nil {
			t.Fatalf(
				"invoke graph %d: %v",
				invocation.index,
				invocation.err,
			)
		}
		runID := fmt.Sprintf("concurrent-%02d", invocation.index)
		if invocation.result.RunID != runID ||
			invocation.result.Value != runID ||
			len(invocation.result.ToolMessages) != 1 ||
			invocation.result.ToolMessages[0].ToolCallID != "call-"+runID ||
			invocation.result.ToolMessages[0].Content !=
				fmt.Sprintf(`{"run_id":%q}`, runID) {
			t.Fatalf(
				"invocation %d leaked state: %#v",
				invocation.index,
				invocation.result,
			)
		}
	}
}

func p135c2ToolCall(
	id string,
	name string,
	arguments string,
) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func p135c2RecordMaximum(maximum *atomic.Int32, candidate int32) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func p135c2ToolMessageIDs(messages []*schema.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool {
			ids = append(ids, message.ToolCallID)
		}
	}
	return ids
}

func p135c2ContainsOrdered(values, expected []string) bool {
	position := 0
	for _, value := range values {
		if position < len(expected) && value == expected[position] {
			position++
		}
	}
	return position == len(expected)
}

func waitP135c2Result[T any](
	t *testing.T,
	channel <-chan T,
	label string,
) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}
