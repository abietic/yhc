package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

const (
	queryKernelVersionLegacy       = "legacy/v1"
	queryKernelVersionProjectGraph = "project_graph/v1"
)

// queryKernelStage is the durable ProjectGraph admission stage pinned in
// Session metadata. The exported Session metadata Go field
// QueryKernelStage and its JSON key query_kernel_canary_stage remain
// the durable compatibility boundary; active selection uses only these
// neutral stage names.
type queryKernelStage string

const (
	// queryKernelStageUnset is retained only as the durable "off" value so
	// older pinned metadata keeps failing closed with no executor.
	queryKernelStageUnset      queryKernelStage = "off"
	queryKernelStageNoTools    queryKernelStage = "no_tools"
	queryKernelStageReadOnly   queryKernelStage = "read_only"
	queryKernelStageLocalTools queryKernelStage = "local_tools"
	// queryKernelStageFull is the production root-session stage.
	queryKernelStageFull queryKernelStage = "full"
	// queryKernelStageForegroundChild is an internal durable stage admitted
	// only through exact pre-executor foreground child Session admission.
	queryKernelStageForegroundChild queryKernelStage = "foreground_child"
	// queryKernelStageBackgroundChild is an internal durable stage selected
	// only by AgentRunner's asynchronous child admission.
	queryKernelStageBackgroundChild queryKernelStage = "background_child"
)

type sessionQueryKernelSelection struct {
	kernel          queryKernel
	version         string
	stage           queryKernelStage
	incompatibility string
	err             error
}

type stagedProjectGraphQueryKernel struct {
	delegate queryKernel
	stage    queryKernelStage
	registry *tools.Registry
}

func (stagedProjectGraphQueryKernel) kind() queryKernelKind {
	return queryKernelProjectGraph
}

func (kernel stagedProjectGraphQueryKernel) run(
	ctx context.Context,
	request queryKernelRequest,
) Terminal {
	request.beforeModelRound = kernel.validateModelRound
	return kernel.delegate.run(ctx, request)
}

func (kernel stagedProjectGraphQueryKernel) validateModelRound(
	toolUseContext *ToolUseContext,
) error {
	var modelVisibleTools []*schema.ToolInfo
	if toolUseContext != nil && toolUseContext.Options != nil {
		modelVisibleTools = toolUseContext.Options.Tools
	}
	if incompatibility := projectGraphStageIncompatibility(
		kernel.stage,
		modelVisibleTools,
		kernel.registry,
	); incompatibility != "" {
		return fmt.Errorf(
			"session query kernel %q is incompatible with the current tool surface: %s",
			queryKernelVersionProjectGraph,
			incompatibility,
		)
	}
	return nil
}

var sharedProjectGraphKernel struct {
	once   sync.Once
	kernel *projectGraphQueryKernel
	err    error
}

func productionProjectGraphQueryKernel() (*projectGraphQueryKernel, error) {
	sharedProjectGraphKernel.once.Do(func() {
		sharedProjectGraphKernel.kernel, sharedProjectGraphKernel.err = newProjectGraphQueryKernel(context.Background())
	})
	return sharedProjectGraphKernel.kernel, sharedProjectGraphKernel.err
}

func (e *QueryEngine) queryKernelForTurn(ctx context.Context) (queryKernel, error) {
	if e == nil {
		return nil, fmt.Errorf("query engine is nil")
	}
	if err := e.ensureRestoreStagingCommitted(); err != nil {
		return nil, err
	}
	if fixtureKernel := fixtureQueryKernelFromContext(ctx); fixtureKernel != nil {
		return fixtureKernel, nil
	}
	e.mu.Lock()
	selection := e.queryKernelSelection
	inputCoordinatorErr := e.inputCoordinatorErr
	projectGraphCheckpointErr := e.projectGraphCheckpointErr
	durableInterruptsEnabled := e.projectGraphHITLEnabled
	e.mu.Unlock()
	if inputCoordinatorErr != nil {
		return nil, fmt.Errorf(
			"session runtime input coordinator is unavailable: %w",
			inputCoordinatorErr,
		)
	}
	if selection.err != nil {
		return nil, selection.err
	}
	if selection.kernel == nil {
		return nil, fmt.Errorf(
			"session query kernel %q is unavailable",
			selection.version,
		)
	}
	if selection.kernel.kind() == queryKernelProjectGraph {
		if projectGraphCheckpointErr != nil && durableInterruptsEnabled {
			return nil, fmt.Errorf(
				"session project graph checkpoint is unavailable: %w",
				projectGraphCheckpointErr,
			)
		}
		if incompatibility := projectGraphStageIncompatibility(
			selection.stage,
			e.modelVisibleTools(),
			e.toolRegistry,
		); incompatibility != "" {
			return nil, fmt.Errorf(
				"session query kernel %q is incompatible with the current tool surface: %s",
				selection.version,
				incompatibility,
			)
		}
		return stagedProjectGraphQueryKernel{
			delegate: selection.kernel,
			stage:    selection.stage,
			registry: e.toolRegistry,
		}, nil
	}
	return selection.kernel, nil
}

func isNewSessionEscapeCommand(isCommand bool, prompt string) bool {
	if !isCommand {
		return false
	}
	name, _ := commands.ParseCommandInput(prompt)
	return name == "new"
}

// initialSessionQueryKernelSelection selects the query kernel for a new root
// Session. A fresh root unconditionally selects the compiled
// project_graph/v1 stage full; a durable transcript keeps its pinned
// selection.
func initialSessionQueryKernelSelection(
	loaded *transcript.LoadResult,
) sessionQueryKernelSelection {
	if hasDurableSessionTranscript(loaded) {
		full := session.ReadSessionMetadataFull(loaded)
		if full == nil || strings.TrimSpace(full.QueryKernelVersion) == "" {
			return retiredLegacySessionQueryKernelSelection(
				"",
				"pre_graph_session",
			)
		}
		return persistedSessionQueryKernelSelection(
			full.QueryKernelVersion,
			full.QueryKernelStage,
			full.QueryKernelIncompatibility,
		)
	}

	kernel, err := productionProjectGraphQueryKernel()
	if err != nil {
		return sessionQueryKernelSelection{
			version:         queryKernelVersionProjectGraph,
			stage:           queryKernelStageFull,
			incompatibility: "project_graph_compile_failed",
			err: fmt.Errorf(
				"compile session query kernel %q: %w",
				queryKernelVersionProjectGraph,
				err,
			),
		}
	}
	return sessionQueryKernelSelection{
		kernel:  kernel,
		version: queryKernelVersionProjectGraph,
		stage:   queryKernelStageFull,
	}
}

func initialForegroundChildSessionQueryKernelSelection(
	agentID string,
	loaded *transcript.LoadResult,
) sessionQueryKernelSelection {
	if hasDurableSessionTranscript(loaded) {
		full := session.ReadSessionMetadataFull(loaded)
		if full != nil && strings.TrimSpace(full.QueryKernelVersion) != "" {
			return persistedSessionQueryKernelSelection(
				full.QueryKernelVersion,
				full.QueryKernelStage,
				full.QueryKernelIncompatibility,
			)
		}
		return sessionQueryKernelSelection{
			version: queryKernelVersionProjectGraph,
			stage:   queryKernelStageForegroundChild,
			err: fmt.Errorf(
				"foreground child session has no durable query kernel selection",
			),
		}
	}
	if strings.TrimSpace(agentID) == "" {
		return sessionQueryKernelSelection{
			version: queryKernelVersionProjectGraph,
			stage:   queryKernelStageForegroundChild,
			err: fmt.Errorf(
				"foreground child query kernel requires an Agent identity",
			),
		}
	}
	kernel, err := productionProjectGraphQueryKernel()
	if err != nil {
		return sessionQueryKernelSelection{
			version: queryKernelVersionProjectGraph,
			stage:   queryKernelStageForegroundChild,
			err: fmt.Errorf(
				"compile foreground child %s query kernel: %w",
				queryKernelVersionProjectGraph,
				err,
			),
		}
	}
	return sessionQueryKernelSelection{
		kernel:  kernel,
		version: queryKernelVersionProjectGraph,
		stage:   queryKernelStageForegroundChild,
	}
}

func resumedSessionQueryKernelSelection(
	metadata session.SessionMetadata,
) sessionQueryKernelSelection {
	if strings.TrimSpace(metadata.QueryKernelVersion) == "" {
		return retiredLegacySessionQueryKernelSelection(
			"",
			"pre_graph_session",
		)
	}
	return persistedSessionQueryKernelSelection(
		metadata.QueryKernelVersion,
		metadata.QueryKernelStage,
		metadata.QueryKernelIncompatibility,
	)
}

func persistedSessionQueryKernelSelection(
	version string,
	stageValue string,
	incompatibility string,
) sessionQueryKernelSelection {
	stage, stageErr := parsePersistedQueryKernelStage(stageValue)
	if stageErr != nil {
		return sessionQueryKernelSelection{
			version:         strings.TrimSpace(version),
			stage:           queryKernelStageUnset,
			incompatibility: strings.TrimSpace(incompatibility),
			err: fmt.Errorf(
				"session query kernel %q has invalid stage",
				version,
			),
		}
	}
	switch strings.TrimSpace(version) {
	case queryKernelVersionLegacy:
		return retiredLegacySessionQueryKernelSelection(
			queryKernelVersionLegacy,
			incompatibility,
		)
	case queryKernelVersionProjectGraph:
		if stage == queryKernelStageUnset {
			return sessionQueryKernelSelection{
				version:         queryKernelVersionProjectGraph,
				stage:           stage,
				incompatibility: strings.TrimSpace(incompatibility),
				err: fmt.Errorf(
					"session query kernel %q has no stage",
					queryKernelVersionProjectGraph,
				),
			}
		}
		kernel, err := productionProjectGraphQueryKernel()
		if err != nil {
			return sessionQueryKernelSelection{
				version:         queryKernelVersionProjectGraph,
				stage:           stage,
				incompatibility: strings.TrimSpace(incompatibility),
				err: fmt.Errorf(
					"restore %s query kernel: %w",
					queryKernelVersionProjectGraph,
					err,
				),
			}
		}
		return sessionQueryKernelSelection{
			kernel:          kernel,
			version:         queryKernelVersionProjectGraph,
			stage:           stage,
			incompatibility: strings.TrimSpace(incompatibility),
		}
	default:
		return sessionQueryKernelSelection{
			version:         strings.TrimSpace(version),
			stage:           stage,
			incompatibility: strings.TrimSpace(incompatibility),
			err: fmt.Errorf(
				"unsupported persisted query kernel version %q",
				version,
			),
		}
	}
}

func retiredLegacySessionQueryKernelSelection(
	version string,
	incompatibility string,
) sessionQueryKernelSelection {
	if strings.TrimSpace(version) == "" {
		version = queryKernelVersionLegacy
	}
	return sessionQueryKernelSelection{
		version:         version,
		stage:           queryKernelStageUnset,
		incompatibility: strings.TrimSpace(incompatibility),
		err: fmt.Errorf(
			"session query kernel %q is retired; start a new ProjectGraph session",
			version,
		),
	}
}

func parseQueryKernelStage(
	value string,
) (queryKernelStage, error) {
	switch stage := queryKernelStage(
		strings.ToLower(strings.TrimSpace(value)),
	); stage {
	case "", queryKernelStageUnset:
		return queryKernelStageUnset, nil
	case queryKernelStageNoTools,
		queryKernelStageReadOnly,
		queryKernelStageLocalTools,
		queryKernelStageFull:
		return stage, nil
	default:
		return queryKernelStageUnset, fmt.Errorf(
			"unsupported project Graph stage",
		)
	}
}

func parsePersistedQueryKernelStage(
	value string,
) (queryKernelStage, error) {
	stage := queryKernelStage(
		strings.ToLower(strings.TrimSpace(value)),
	)
	if stage == queryKernelStageForegroundChild ||
		stage == queryKernelStageBackgroundChild {
		return stage, nil
	}
	return parseQueryKernelStage(value)
}

func projectGraphDurableInterruptEnabled(
	prompt PermissionPromptFn,
	selection sessionQueryKernelSelection,
) bool {
	return prompt != nil &&
		selection.stage != queryKernelStageForegroundChild &&
		selection.stage != queryKernelStageBackgroundChild
}

func projectGraphStageIncompatibility(
	stage queryKernelStage,
	modelVisibleTools []*schema.ToolInfo,
	registry *tools.Registry,
) string {
	switch stage {
	case queryKernelStageForegroundChild,
		queryKernelStageBackgroundChild,
		queryKernelStageFull:
		return ""
	case queryKernelStageNoTools:
		for _, info := range modelVisibleTools {
			if info != nil && strings.TrimSpace(info.Name) != "" {
				return "model_visible_tools_present:" +
					strings.TrimSpace(info.Name)
			}
		}
		return ""
	case queryKernelStageReadOnly:
		for _, info := range modelVisibleTools {
			if info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			name := strings.TrimSpace(info.Name)
			if registry == nil {
				return "tool_contract_unavailable:" + name
			}
			impl, ok := registry.Get(name)
			if !ok || !impl.IsReadOnly {
				return "tool_not_read_only:" + name
			}
		}
		return ""
	case queryKernelStageLocalTools:
		for _, info := range modelVisibleTools {
			if info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			name := strings.TrimSpace(info.Name)
			if projectGraphExternalTool(name) {
				return "external_tool_not_in_stage:" + name
			}
		}
		return ""
	default:
		return "stage_disabled"
	}
}

func projectGraphExternalTool(name string) bool {
	if tools.IsMCPToolName(name) {
		return true
	}
	switch name {
	case "mcp_tool", "ListMcpResourcesTool", "ReadMcpResourceTool", "McpAuth":
		return true
	default:
		return false
	}
}

func hasDurableSessionTranscript(loaded *transcript.LoadResult) bool {
	if loaded == nil {
		return false
	}
	return len(loaded.Messages) > 0 ||
		len(loaded.Replacements) > 0 ||
		len(loaded.Metadata) > 0 ||
		len(loaded.FileSnapshots) > 0 ||
		len(loaded.LifecycleBoundaries) > 0 ||
		len(loaded.Corruptions) > 0
}
