package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	projectGraphFreezeInputNode    = "freeze_input"
	projectGraphPrepareRoundNode   = "prepare_round"
	projectGraphModelRoundNode     = "model_round"
	projectGraphToolRoundNode      = "tool_round"
	projectGraphReconcileRoundNode = "reconcile_round"
	projectGraphFinalizeNode       = "finalize"
	projectGraphDefaultMaxRunSteps = math.MaxInt
)

type projectGraphPrepareDecision string

const (
	projectGraphPrepareModel    projectGraphPrepareDecision = "model"
	projectGraphPrepareContinue projectGraphPrepareDecision = "continue"
	projectGraphPrepareTerminal projectGraphPrepareDecision = "terminal"
)

type projectGraphModelDecision string

const (
	projectGraphModelTerminal  projectGraphModelDecision = "terminal"
	projectGraphModelToolCalls projectGraphModelDecision = "tool_calls"
)

type projectGraphAfterToolDecision string

const (
	projectGraphAfterToolContinue  projectGraphAfterToolDecision = "continue"
	projectGraphAfterToolReturn    projectGraphAfterToolDecision = "return"
	projectGraphAfterToolInterrupt projectGraphAfterToolDecision = "interrupt"
)

type projectGraphReconcilePhase string

const (
	projectGraphReconcileAfterModel projectGraphReconcilePhase = "after_model"
	projectGraphReconcileAfterTool  projectGraphReconcilePhase = "after_tool"
)

type projectGraphReconcileDecision string

const (
	projectGraphReconcilePrepare  projectGraphReconcileDecision = "prepare"
	projectGraphReconcileTool     projectGraphReconcileDecision = "tool"
	projectGraphReconcileFinalize projectGraphReconcileDecision = "finalize"
)

type projectGraphResultKind string

const (
	projectGraphResultTerminal  projectGraphResultKind = "terminal"
	projectGraphResultReturned  projectGraphResultKind = "returned"
	projectGraphResultInterrupt projectGraphResultKind = "interrupt"
)

// projectGraphKernelInput is plain invocation data. The first node copies all
// reference-backed fields before any round hook can observe them, so downstream
// execution never retains caller-owned slices.
type projectGraphKernelInput struct {
	RunID         string
	InputRevision uint64
	Values        []string
}

type projectGraphRound struct {
	RunID     string
	Number    int
	Values    []string
	ToolCalls []schema.ToolCall
}

type projectGraphPreparedRound struct {
	Decision       projectGraphPrepareDecision
	Values         []string
	Value          string
	TerminalReason TerminalReason
}

type projectGraphModelRound struct {
	Decision  projectGraphModelDecision
	Value     string
	ToolCalls []schema.ToolCall
}

type projectGraphToolRound struct {
	Decision       projectGraphAfterToolDecision
	Value          string
	TerminalReason TerminalReason
	Messages       []*schema.Message
}

type projectGraphReconcileRound struct {
	Phase          projectGraphReconcilePhase
	Decision       projectGraphReconcileDecision
	Kind           projectGraphResultKind
	Value          string
	TerminalReason TerminalReason
}

type projectGraphNodeCalls struct {
	Prepare int
	Model   int
	Tool    int
}

// projectGraphKernelResult is a fixture-only terminal value. Interrupt is
// intentionally non-durable until P13.8 introduces Compose checkpoint/resume.
type projectGraphKernelResult struct {
	RunID          string
	Kind           projectGraphResultKind
	Value          string
	TerminalReason TerminalReason
	ToolMessages   []*schema.Message
	InputValues    []string
	Trace          []string
	Calls          projectGraphNodeCalls
}

type projectGraphKernelNodes struct {
	prepare   func(context.Context, projectGraphRound) (projectGraphPreparedRound, error)
	model     func(context.Context, projectGraphRound) (projectGraphModelRound, error)
	tool      func(context.Context, projectGraphRound) (projectGraphToolRound, error)
	reconcile func(
		context.Context,
		projectGraphRound,
		projectGraphReconcilePhase,
		projectGraphModelRound,
		projectGraphToolRound,
	) (projectGraphReconcileRound, error)
	finalize func(context.Context, projectGraphKernelResult) error
}

type projectGraphKernelConfig struct {
	maxRunSteps int
	nodes       projectGraphKernelNodes
}

type projectGraphCanonicalModelInput func(
	context.Context,
	projectGraphRound,
) (canonicalModelRoundInput, error)

type projectGraphCanonicalToolInput func(
	context.Context,
	projectGraphRound,
) (canonicalToolRoundInput, error)

// projectGraphModelRoundError keeps a canonical model terminal on the node
// execution stack. Failures must not be reduced to a successful Graph terminal
// value or stored in plain local state.
type projectGraphModelRoundError struct {
	terminal Terminal
}

func (failure *projectGraphModelRoundError) Error() string {
	if failure == nil {
		return "project graph model round failed"
	}
	if failure.terminal.Err != nil {
		return fmt.Sprintf(
			"project graph model round terminated with %q: %v",
			failure.terminal.Reason,
			failure.terminal.Err,
		)
	}
	return fmt.Sprintf(
		"project graph model round terminated with %q",
		failure.terminal.Reason,
	)
}

func (failure *projectGraphModelRoundError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.terminal.Err
}

// projectGraphKernelState is created once per Runnable invocation. It carries
// only reconstructable plain data: no context, function, registry, hook, model,
// tool, or other live runtime owner belongs here.
type projectGraphKernelState struct {
	Input     projectGraphKernelInput
	Round     int
	Prepared  projectGraphPreparedRound
	Model     projectGraphModelRound
	Tool      projectGraphToolRound
	Reconcile projectGraphReconcileRound
	Trace     []string
	NodeCalls projectGraphNodeCalls
}

type projectGraphStep struct {
	PrepareDecision   projectGraphPrepareDecision
	ReconcilePhase    projectGraphReconcilePhase
	ReconcileDecision projectGraphReconcileDecision
	ResultKind        projectGraphResultKind
	ResultValue       string
	TerminalReason    TerminalReason
}

// buildProjectGraphKernel compiles the internal typed Compose Graph shared by
// deterministic fixtures and eligible session-pinned staged execution.
func buildProjectGraphKernel(
	ctx context.Context,
	config projectGraphKernelConfig,
) (compose.Runnable[projectGraphKernelInput, projectGraphKernelResult], error) {
	if err := validateProjectGraphKernelConfig(config); err != nil {
		return nil, err
	}
	nodes := config.nodes
	if nodes.reconcile == nil {
		nodes.reconcile = reconcileProjectGraphRound
	}
	if nodes.finalize == nil {
		nodes.finalize = func(context.Context, projectGraphKernelResult) error {
			return nil
		}
	}

	maxRunSteps := config.maxRunSteps
	if maxRunSteps == 0 {
		maxRunSteps = projectGraphDefaultMaxRunSteps
	}

	graph := compose.NewGraph[projectGraphKernelInput, projectGraphKernelResult](
		compose.WithGenLocalState(func(context.Context) *projectGraphKernelState {
			return &projectGraphKernelState{}
		}),
	)

	if err := graph.AddLambdaNode(
		projectGraphFreezeInputNode,
		compose.InvokableLambda(freezeProjectGraphInput),
		compose.WithNodeName("FreezeProjectGraphInput"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphFreezeInputNode, err)
	}
	if err := graph.AddLambdaNode(
		projectGraphPrepareRoundNode,
		compose.InvokableLambda(projectGraphPrepareRound(nodes.prepare)),
		compose.WithNodeName("PrepareProjectGraphRound"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphPrepareRoundNode, err)
	}
	if err := graph.AddLambdaNode(
		projectGraphModelRoundNode,
		compose.InvokableLambda(makeProjectGraphModelRoundNode(nodes.model)),
		compose.WithNodeName("RunProjectGraphModelRound"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphModelRoundNode, err)
	}
	if err := graph.AddLambdaNode(
		projectGraphToolRoundNode,
		compose.InvokableLambda(makeProjectGraphToolRoundNode(nodes.tool)),
		compose.WithNodeName("RunProjectGraphToolRound"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphToolRoundNode, err)
	}
	if err := graph.AddLambdaNode(
		projectGraphReconcileRoundNode,
		compose.InvokableLambda(makeProjectGraphReconcileRoundNode(nodes.reconcile)),
		compose.WithNodeName("ReconcileProjectGraphRound"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphReconcileRoundNode, err)
	}
	if err := graph.AddLambdaNode(
		projectGraphFinalizeNode,
		compose.InvokableLambda(finalizeProjectGraphResult(nodes.finalize)),
		compose.WithNodeName("FinalizeProjectGraphResult"),
	); err != nil {
		return nil, fmt.Errorf("add %s node: %w", projectGraphFinalizeNode, err)
	}

	if err := graph.AddEdge(compose.START, projectGraphFreezeInputNode); err != nil {
		return nil, fmt.Errorf("add start edge: %w", err)
	}
	if err := graph.AddEdge(projectGraphFreezeInputNode, projectGraphPrepareRoundNode); err != nil {
		return nil, fmt.Errorf("add freeze-to-prepare edge: %w", err)
	}
	if err := graph.AddBranch(
		projectGraphPrepareRoundNode,
		compose.NewGraphBranch(
			projectGraphPrepareBranch,
			map[string]bool{
				projectGraphPrepareRoundNode: true,
				projectGraphModelRoundNode:   true,
				projectGraphFinalizeNode:     true,
			},
		),
	); err != nil {
		return nil, fmt.Errorf("add prepare branch: %w", err)
	}
	if err := graph.AddEdge(
		projectGraphModelRoundNode,
		projectGraphReconcileRoundNode,
	); err != nil {
		return nil, fmt.Errorf("add model-to-reconcile edge: %w", err)
	}
	if err := graph.AddEdge(
		projectGraphToolRoundNode,
		projectGraphReconcileRoundNode,
	); err != nil {
		return nil, fmt.Errorf("add tool-to-reconcile edge: %w", err)
	}
	if err := graph.AddBranch(
		projectGraphReconcileRoundNode,
		compose.NewGraphBranch(
			projectGraphReconcileBranch,
			map[string]bool{
				projectGraphPrepareRoundNode: true,
				projectGraphToolRoundNode:    true,
				projectGraphFinalizeNode:     true,
			},
		),
	); err != nil {
		return nil, fmt.Errorf("add reconcile branch: %w", err)
	}
	if err := graph.AddEdge(projectGraphFinalizeNode, compose.END); err != nil {
		return nil, fmt.Errorf("add final edge: %w", err)
	}

	runnable, err := graph.Compile(
		ctx,
		compose.WithMaxRunSteps(maxRunSteps),
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithGraphName("ProjectQueryKernel"),
		compose.WithCheckPointStore(projectGraphCheckpointStoreDelegate{}),
	)
	if err != nil {
		return nil, fmt.Errorf("compile project graph kernel: %w", err)
	}
	return runnable, nil
}

func validateProjectGraphKernelConfig(config projectGraphKernelConfig) error {
	if config.maxRunSteps < 0 {
		return fmt.Errorf("project graph max run steps must be zero (default) or positive")
	}
	if config.nodes.prepare == nil {
		return fmt.Errorf("project graph prepare node is required")
	}
	if config.nodes.model == nil {
		return fmt.Errorf("project graph model node is required")
	}
	if config.nodes.tool == nil {
		return fmt.Errorf("project graph tool node is required")
	}
	return nil
}

// bindProjectGraphCanonicalModelRound adapts the shared project model boundary
// to the typed Graph node contract. The live request is constructed and
// consumed on the node stack; only the committed decision and plain terminal
// value are stored in Graph local state.
func bindProjectGraphCanonicalModelRound(
	buildInput projectGraphCanonicalModelInput,
) func(context.Context, projectGraphRound) (projectGraphModelRound, error) {
	return func(ctx context.Context, round projectGraphRound) (projectGraphModelRound, error) {
		if buildInput == nil {
			return projectGraphModelRound{}, fmt.Errorf(
				"project graph canonical model input builder is required",
			)
		}
		input, err := buildInput(ctx, round)
		if err != nil {
			return projectGraphModelRound{}, err
		}
		result := runCanonicalModelRound(ctx, input)
		if result.terminal != nil {
			if result.terminal.Err != nil {
				return projectGraphModelRound{}, &projectGraphModelRoundError{
					terminal: *result.terminal,
				}
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    string(result.terminal.Reason),
			}, nil
		}
		if result.needsFollowUp && result.toolCallsCommitted {
			toolCalls, err := cloneProjectGraphToolCallPointers(
				result.toolUseBlocks,
			)
			if err != nil {
				return projectGraphModelRound{}, fmt.Errorf(
					"freeze project graph model tool calls: %w",
					err,
				)
			}
			return projectGraphModelRound{
				Decision:  projectGraphModelToolCalls,
				Value:     committedModelRoundValue(result),
				ToolCalls: toolCalls,
			}, nil
		}
		return projectGraphModelRound{
			Decision: projectGraphModelTerminal,
			Value:    committedModelRoundValue(result),
		}, nil
	}
}

// bindProjectGraphCanonicalToolRound adapts the committed call set stored by
// the model node to the canonical project tool boundary. Live dependencies are
// constructed on the node stack; only cloned messages and tagged control data
// enter Graph local state.
func bindProjectGraphCanonicalToolRound(
	buildInput projectGraphCanonicalToolInput,
) func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
	return func(ctx context.Context, round projectGraphRound) (projectGraphToolRound, error) {
		if buildInput == nil {
			return projectGraphToolRound{}, fmt.Errorf(
				"project graph canonical tool input builder is required",
			)
		}
		input, err := buildInput(ctx, round)
		if err != nil {
			return projectGraphToolRound{}, err
		}
		input.toolCalls = projectGraphToolCallPointers(round.ToolCalls)
		result, err := runCanonicalToolRound(ctx, input)
		if err != nil {
			return projectGraphToolRound{}, err
		}
		toolRound := projectGraphToolRound{
			TerminalReason: result.decision.TerminalReason,
			Messages:       result.toolResults,
		}
		switch result.decision.Kind {
		case afterToolContinue:
			toolRound.Decision = projectGraphAfterToolContinue
		case afterToolReturn:
			toolRound.Decision = projectGraphAfterToolReturn
			toolRound.Value = canonicalToolReturnValue(
				result.outcomes,
				result.decision.ReturnCallID,
			)
		case afterToolInterrupt:
			toolRound.Decision = projectGraphAfterToolInterrupt
			toolRound.Value = result.decision.InterruptID
		default:
			return projectGraphToolRound{}, fmt.Errorf(
				"canonical tool round returned invalid decision %q",
				result.decision.Kind,
			)
		}
		return toolRound, nil
	}
}

func canonicalToolReturnValue(
	outcomes []toolRoundOutcome,
	callID string,
) string {
	for _, outcome := range outcomes {
		if outcome.CallID == callID &&
			outcome.Outcome != nil &&
			outcome.Outcome.Result != nil {
			return outcome.Outcome.Result.Content
		}
	}
	return ""
}

func committedModelRoundValue(result canonicalModelRoundResult) string {
	if result.withheldReason != "" {
		return result.withheldReason
	}
	for index := len(result.assistantMessages) - 1; index >= 0; index-- {
		if result.assistantMessages[index] != nil {
			return result.assistantMessages[index].Content
		}
	}
	return ""
}

func freezeProjectGraphInput(
	ctx context.Context,
	input projectGraphKernelInput,
) (projectGraphStep, error) {
	frozen := cloneProjectGraphKernelInput(input)
	err := compose.ProcessState[*projectGraphKernelState](
		ctx,
		func(_ context.Context, state *projectGraphKernelState) error {
			state.Input = frozen
			return nil
		},
	)
	if err != nil {
		return projectGraphStep{}, fmt.Errorf("freeze project graph input: %w", err)
	}
	return projectGraphStep{}, nil
}

func projectGraphPrepareRound(
	prepare func(context.Context, projectGraphRound) (projectGraphPreparedRound, error),
) func(context.Context, projectGraphStep) (projectGraphStep, error) {
	return func(ctx context.Context, step projectGraphStep) (projectGraphStep, error) {
		var round projectGraphRound
		err := compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.Round++
				state.NodeCalls.Prepare++
				state.Trace = append(state.Trace, projectGraphPrepareRoundNode)
				round = projectGraphRound{
					RunID:  state.Input.RunID,
					Number: state.Round,
					Values: cloneStrings(state.Input.Values),
				}
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("load project graph prepare state: %w", err)
		}

		prepared, err := prepare(ctx, round)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("prepare project graph round %d: %w", round.Number, err)
		}
		if prepared.Decision == "" {
			prepared.Decision = projectGraphPrepareModel
		}
		switch prepared.Decision {
		case projectGraphPrepareModel, projectGraphPrepareContinue:
		case projectGraphPrepareTerminal:
			if prepared.TerminalReason == "" {
				prepared.TerminalReason = TerminalCompleted
			}
		default:
			return projectGraphStep{}, fmt.Errorf(
				"project graph prepare round %d returned invalid decision %q",
				round.Number,
				prepared.Decision,
			)
		}
		prepared.Values = cloneStrings(prepared.Values)
		err = compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.Prepared = prepared
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("store project graph prepare state: %w", err)
		}
		step.PrepareDecision = prepared.Decision
		step.ReconcilePhase = ""
		step.ReconcileDecision = ""
		if prepared.Decision == projectGraphPrepareTerminal {
			step.ResultKind = projectGraphResultTerminal
			step.ResultValue = prepared.Value
			step.TerminalReason = prepared.TerminalReason
		}
		return step, nil
	}
}

func makeProjectGraphModelRoundNode(
	model func(context.Context, projectGraphRound) (projectGraphModelRound, error),
) func(context.Context, projectGraphStep) (projectGraphStep, error) {
	return func(ctx context.Context, step projectGraphStep) (projectGraphStep, error) {
		var round projectGraphRound
		err := compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.NodeCalls.Model++
				state.Trace = append(state.Trace, projectGraphModelRoundNode)
				round = projectGraphRound{
					RunID:  state.Input.RunID,
					Number: state.Round,
					Values: cloneStrings(state.Prepared.Values),
				}
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("load project graph model state: %w", err)
		}

		modelRound, err := model(ctx, round)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("run project graph model round %d: %w", round.Number, err)
		}
		if modelRound.Decision != projectGraphModelTerminal &&
			modelRound.Decision != projectGraphModelToolCalls {
			return projectGraphStep{}, fmt.Errorf(
				"project graph model round %d returned invalid decision %q",
				round.Number,
				modelRound.Decision,
			)
		}
		modelRound, err = cloneProjectGraphModelRound(modelRound)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf(
				"freeze project graph model round %d: %w",
				round.Number,
				err,
			)
		}
		err = compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.Model = modelRound
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("store project graph model state: %w", err)
		}
		step.PrepareDecision = ""
		step.ReconcilePhase = projectGraphReconcileAfterModel
		step.ReconcileDecision = ""
		return step, nil
	}
}

func makeProjectGraphToolRoundNode(
	tool func(context.Context, projectGraphRound) (projectGraphToolRound, error),
) func(context.Context, projectGraphStep) (projectGraphStep, error) {
	return func(ctx context.Context, step projectGraphStep) (projectGraphStep, error) {
		var round projectGraphRound
		err := compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				toolCalls, cloneErr := cloneProjectGraphToolCalls(
					state.Model.ToolCalls,
				)
				if cloneErr != nil {
					return cloneErr
				}
				state.NodeCalls.Tool++
				state.Trace = append(state.Trace, projectGraphToolRoundNode)
				round = projectGraphRound{
					RunID:     state.Input.RunID,
					Number:    state.Round,
					Values:    cloneStrings(state.Prepared.Values),
					ToolCalls: toolCalls,
				}
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("load project graph tool state: %w", err)
		}

		toolRound, err := tool(ctx, round)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("run project graph tool round %d: %w", round.Number, err)
		}
		switch toolRound.Decision {
		case projectGraphAfterToolContinue,
			projectGraphAfterToolReturn,
			projectGraphAfterToolInterrupt:
		default:
			return projectGraphStep{}, fmt.Errorf(
				"project graph tool round %d returned invalid decision %q",
				round.Number,
				toolRound.Decision,
			)
		}
		toolRound, err = cloneProjectGraphToolRound(toolRound)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf(
				"freeze project graph tool round %d: %w",
				round.Number,
				err,
			)
		}
		err = compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.Tool = toolRound
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf("store project graph tool state: %w", err)
		}
		step.ReconcilePhase = projectGraphReconcileAfterTool
		step.ReconcileDecision = ""
		return step, nil
	}
}

func projectGraphPrepareBranch(_ context.Context, step projectGraphStep) (string, error) {
	switch step.PrepareDecision {
	case projectGraphPrepareContinue:
		return projectGraphPrepareRoundNode, nil
	case projectGraphPrepareModel:
		return projectGraphModelRoundNode, nil
	case projectGraphPrepareTerminal:
		return projectGraphFinalizeNode, nil
	default:
		return "", fmt.Errorf(
			"invalid project graph prepare branch decision %q",
			step.PrepareDecision,
		)
	}
}

func reconcileProjectGraphRound(
	_ context.Context,
	_ projectGraphRound,
	phase projectGraphReconcilePhase,
	modelRound projectGraphModelRound,
	toolRound projectGraphToolRound,
) (projectGraphReconcileRound, error) {
	result := projectGraphReconcileRound{Phase: phase}
	switch phase {
	case projectGraphReconcileAfterModel:
		switch modelRound.Decision {
		case projectGraphModelTerminal:
			result.Decision = projectGraphReconcileFinalize
			result.Kind = projectGraphResultTerminal
			result.Value = modelRound.Value
		case projectGraphModelToolCalls:
			result.Decision = projectGraphReconcileTool
		default:
			return projectGraphReconcileRound{}, fmt.Errorf(
				"invalid project graph model reconcile decision %q",
				modelRound.Decision,
			)
		}
	case projectGraphReconcileAfterTool:
		result.TerminalReason = toolRound.TerminalReason
		switch toolRound.Decision {
		case projectGraphAfterToolContinue:
			result.Decision = projectGraphReconcilePrepare
		case projectGraphAfterToolReturn:
			result.Decision = projectGraphReconcileFinalize
			result.Kind = projectGraphResultReturned
			result.Value = toolRound.Value
		case projectGraphAfterToolInterrupt:
			result.Decision = projectGraphReconcileFinalize
			result.Kind = projectGraphResultInterrupt
			result.Value = toolRound.Value
		default:
			return projectGraphReconcileRound{}, fmt.Errorf(
				"invalid project graph after-tool reconcile decision %q",
				toolRound.Decision,
			)
		}
	default:
		return projectGraphReconcileRound{}, fmt.Errorf(
			"invalid project graph reconcile phase %q",
			phase,
		)
	}
	return result, nil
}

func makeProjectGraphReconcileRoundNode(
	reconcile func(
		context.Context,
		projectGraphRound,
		projectGraphReconcilePhase,
		projectGraphModelRound,
		projectGraphToolRound,
	) (projectGraphReconcileRound, error),
) func(context.Context, projectGraphStep) (projectGraphStep, error) {
	return func(ctx context.Context, step projectGraphStep) (projectGraphStep, error) {
		var round projectGraphRound
		var modelRound projectGraphModelRound
		var toolRound projectGraphToolRound
		err := compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				var cloneErr error
				modelRound, cloneErr = cloneProjectGraphModelRound(state.Model)
				if cloneErr != nil {
					return cloneErr
				}
				toolRound, cloneErr = cloneProjectGraphToolRound(state.Tool)
				if cloneErr != nil {
					return cloneErr
				}
				round = projectGraphRound{
					RunID:  state.Input.RunID,
					Number: state.Round,
					Values: cloneStrings(state.Prepared.Values),
				}
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf(
				"load project graph reconcile state: %w",
				err,
			)
		}
		reconciled, err := reconcile(
			ctx,
			round,
			step.ReconcilePhase,
			modelRound,
			toolRound,
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf(
				"reconcile project graph round %d after %s: %w",
				round.Number,
				step.ReconcilePhase,
				err,
			)
		}
		if reconciled.Phase == "" {
			reconciled.Phase = step.ReconcilePhase
		}
		switch reconciled.Decision {
		case projectGraphReconcilePrepare, projectGraphReconcileTool:
		case projectGraphReconcileFinalize:
			switch reconciled.Kind {
			case projectGraphResultTerminal,
				projectGraphResultReturned,
				projectGraphResultInterrupt:
			default:
				return projectGraphStep{}, fmt.Errorf(
					"project graph reconcile round %d returned invalid result kind %q",
					round.Number,
					reconciled.Kind,
				)
			}
		default:
			return projectGraphStep{}, fmt.Errorf(
				"project graph reconcile round %d returned invalid decision %q",
				round.Number,
				reconciled.Decision,
			)
		}
		err = compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				state.Reconcile = reconciled
				return nil
			},
		)
		if err != nil {
			return projectGraphStep{}, fmt.Errorf(
				"store project graph reconcile state: %w",
				err,
			)
		}
		step.ReconcileDecision = reconciled.Decision
		if reconciled.Decision == projectGraphReconcileFinalize {
			step.ResultKind = reconciled.Kind
			step.ResultValue = reconciled.Value
			step.TerminalReason = reconciled.TerminalReason
		}
		return step, nil
	}
}

func projectGraphReconcileBranch(
	_ context.Context,
	step projectGraphStep,
) (string, error) {
	switch step.ReconcileDecision {
	case projectGraphReconcilePrepare:
		return projectGraphPrepareRoundNode, nil
	case projectGraphReconcileTool:
		return projectGraphToolRoundNode, nil
	case projectGraphReconcileFinalize:
		return projectGraphFinalizeNode, nil
	default:
		return "", fmt.Errorf(
			"invalid project graph reconcile branch decision %q",
			step.ReconcileDecision,
		)
	}
}

func finalizeProjectGraphResult(
	finalize func(context.Context, projectGraphKernelResult) error,
) func(context.Context, projectGraphStep) (projectGraphKernelResult, error) {
	return func(ctx context.Context, step projectGraphStep) (projectGraphKernelResult, error) {
		var result projectGraphKernelResult
		err := compose.ProcessState[*projectGraphKernelState](
			ctx,
			func(_ context.Context, state *projectGraphKernelState) error {
				toolMessages, cloneErr := cloneProjectGraphMessages(
					state.Tool.Messages,
				)
				if cloneErr != nil {
					return cloneErr
				}
				result = projectGraphKernelResult{
					RunID:          state.Input.RunID,
					Kind:           step.ResultKind,
					Value:          step.ResultValue,
					TerminalReason: step.TerminalReason,
					ToolMessages:   toolMessages,
					InputValues:    cloneStrings(state.Input.Values),
					Trace:          cloneStrings(state.Trace),
					Calls:          state.NodeCalls,
				}
				if result.Kind == "" {
					return fmt.Errorf("cannot finalize project graph result without result kind")
				}
				return nil
			},
		)
		if err != nil {
			return projectGraphKernelResult{}, fmt.Errorf(
				"finalize project graph result: %w",
				err,
			)
		}
		if err := finalize(ctx, result); err != nil {
			return projectGraphKernelResult{}, fmt.Errorf(
				"run project graph finalizer: %w",
				err,
			)
		}
		return result, nil
	}
}

func cloneProjectGraphKernelInput(input projectGraphKernelInput) projectGraphKernelInput {
	return projectGraphKernelInput{
		RunID:         input.RunID,
		InputRevision: input.InputRevision,
		Values:        cloneStrings(input.Values),
	}
}

func cloneProjectGraphModelRound(
	round projectGraphModelRound,
) (projectGraphModelRound, error) {
	toolCalls, err := cloneProjectGraphToolCalls(round.ToolCalls)
	if err != nil {
		return projectGraphModelRound{}, err
	}
	round.ToolCalls = toolCalls
	return round, nil
}

func cloneProjectGraphToolRound(
	round projectGraphToolRound,
) (projectGraphToolRound, error) {
	messages, err := cloneProjectGraphMessages(round.Messages)
	if err != nil {
		return projectGraphToolRound{}, err
	}
	round.Messages = messages
	return round, nil
}

func cloneProjectGraphToolCallPointers(
	toolCalls []*schema.ToolCall,
) ([]schema.ToolCall, error) {
	values := make([]schema.ToolCall, 0, len(toolCalls))
	for index, toolCall := range toolCalls {
		if toolCall == nil {
			return nil, fmt.Errorf("tool call %d is nil", index)
		}
		values = append(values, *toolCall)
	}
	return cloneProjectGraphToolCalls(values)
}

func projectGraphToolCallPointers(
	toolCalls []schema.ToolCall,
) []*schema.ToolCall {
	pointers := make([]*schema.ToolCall, len(toolCalls))
	for index := range toolCalls {
		pointers[index] = &toolCalls[index]
	}
	return pointers
}

func cloneProjectGraphToolCalls(
	toolCalls []schema.ToolCall,
) ([]schema.ToolCall, error) {
	if toolCalls == nil {
		return nil, nil
	}
	var cloned []schema.ToolCall
	if err := cloneProjectGraphJSON(toolCalls, &cloned); err != nil {
		return nil, fmt.Errorf("clone tool calls: %w", err)
	}
	return cloned, nil
}

func cloneProjectGraphMessages(
	messages []*schema.Message,
) ([]*schema.Message, error) {
	if messages == nil {
		return nil, nil
	}
	var cloned []*schema.Message
	if err := cloneProjectGraphJSON(messages, &cloned); err != nil {
		return nil, fmt.Errorf("clone tool messages: %w", err)
	}
	return cloned, nil
}

func cloneProjectGraphJSON(value, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return err
	}
	return nil
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}
