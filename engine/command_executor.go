package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type commandExecution struct {
	result     *CommandResultEvent
	attachment *QueryEvent
}

// executeCommand is the sole engine/service boundary that dispatches and
// applies runtime or session command actions. Entrypoints may classify
// presentation ownership, but they must not interpret engine-owned actions.
func (e *QueryEngine) executeCommand(
	ctx context.Context,
	input string,
	turnID string,
	emit func(QueryEvent) bool,
) commandExecution {
	name, _ := commands.ParseCommandInput(input)
	registry := e.GetCommandRegistry()
	cmd := registry.Get(name)
	canonicalName := name
	if cmd != nil {
		canonicalName = cmd.Name
	}
	outcome := &CommandResultEvent{
		Command: canonicalName,
		Status:  CommandResultSucceeded,
	}

	cmdCtx := e.CommandContext()
	if cmd != nil &&
		cmd.Entrypoints.Supports(e.config.CommandEntrypoint) &&
		cmd.ExecutionOwner != commands.ExecutionOwnerEngine {
		outcome.Status = CommandResultUnsupported
		outcome.Output = fmt.Sprintf(
			"/%s is owned by the %s entrypoint and cannot run through the engine command executor.",
			canonicalName,
			e.config.CommandEntrypoint,
		)
		return commandExecution{result: outcome}
	}
	result, dispatchErr := registry.Dispatch(
		ctx,
		e.config.CommandEntrypoint,
		cmdCtx,
		input,
	)
	if dispatchErr != nil {
		outcome.Status = CommandResultFailed
		if cmd == nil ||
			!cmd.Entrypoints.Supports(e.config.CommandEntrypoint) ||
			cmd.Availability == commands.AvailabilityDisabled ||
			cmd.Availability == commands.AvailabilityUnavailable {
			outcome.Status = CommandResultUnsupported
		}
		outcome.Error = dispatchErr.Error()
		outcome.Output = dispatchErr.Error()
		return commandExecution{result: outcome}
	}
	if result != nil &&
		(result.Availability == commands.AvailabilityDisabled ||
			result.Availability == commands.AvailabilityUnavailable) {
		outcome.Status = CommandResultUnsupported
		outcome.Output = result.Output
		return commandExecution{result: outcome}
	}
	if result == nil {
		outcome.Status = CommandResultFailed
		outcome.Error = "command returned no result"
		outcome.Output = outcome.Error
		return commandExecution{result: outcome}
	}

	outcome.Action = result.Action
	if err := ctx.Err(); err != nil {
		outcome.Status = CommandResultFailed
		outcome.Error = fmt.Sprintf("command canceled before action apply: %v", err)
		outcome.Output = outcome.Error
		return commandExecution{result: outcome}
	}
	attachment, followUpPrompt, applyErr := e.applyCommandAction(
		ctx,
		result,
		turnID,
		emit,
	)
	if applyErr != nil {
		outcome.Status = CommandResultFailed
		outcome.Error = applyErr.Error()
		outcome.Output = fmt.Sprintf("%s failed: %v", canonicalName, applyErr)
		return commandExecution{result: outcome}
	}
	outcome.Output = result.Output
	outcome.FollowUpPrompt = followUpPrompt
	return commandExecution{result: outcome, attachment: attachment}
}

func (e *QueryEngine) commandAdmissionFailure(
	input string,
	err error,
) *CommandResultEvent {
	name, _ := commands.ParseCommandInput(input)
	if cmd := e.GetCommandRegistry().Get(name); cmd != nil {
		name = cmd.Name
	}
	message := "command turn was not admitted"
	if err != nil {
		message = err.Error()
	}
	return &CommandResultEvent{
		Command: name,
		Status:  CommandResultFailed,
		Output:  message,
		Error:   message,
	}
}

func (e *QueryEngine) applyCommandAction(
	ctx context.Context,
	result *commands.CommandResult,
	turnID string,
	emit func(QueryEvent) bool,
) (*QueryEvent, string, error) {
	switch result.Action {
	case commands.ActionNone:
		// Read-only engine-owned commands need no action application.
		return nil, "", nil
	case commands.ActionNew:
		started, err := e.startNewSessionForCommandTurn(ctx, turnID)
		if err != nil {
			return nil, "", fmt.Errorf("start new session: %w", err)
		}
		result.Output = fmt.Sprintf("Started new session %s.", started.SessionID)
		return startedSessionAttachment(started), "", nil
	case commands.ActionClear:
		if err := ctx.Err(); err != nil {
			return nil, "", fmt.Errorf("clear canceled before persistence: %w", err)
		}
		e.mu.Lock()
		recorder := e.transcript
		e.mu.Unlock()
		if recorder == nil {
			return nil, "", fmt.Errorf("clear requires a transcript recorder")
		}
		usage := e.providerUsageSummary()
		if err := recorder.RecordLifecycleBoundaryWithUsage(
			transcript.LifecycleReset,
			nil,
			nil,
			nil,
			usage,
			true,
		); err != nil {
			if transcript.IsDurabilityUncertain(err) {
				e.mu.Lock()
				e.transcriptCheckpointRequired = true
				e.mu.Unlock()
			}
			return nil, "", fmt.Errorf("persist reset boundary: %w", err)
		}
		e.mu.Lock()
		e.messages = make([]*schema.Message, 0)
		e.transcriptCheckpointRequired = false
		e.contentReplacementState = NewContentReplacementState()
		e.fileStateCache = NewFileStateCache()
		if e.subagentExecutor != nil {
			e.subagentExecutor.ParentFileState = e.fileStateCache
		}
		e.mu.Unlock()
		return nil, "", nil
	case commands.ActionCompact:
		customInstructions, err := result.OptionalString("custom_instructions")
		if err != nil {
			return nil, "", err
		}
		if err := e.checkModelCompaction(e.GetModelName()); err != nil {
			return nil, "", err
		}
		e.mu.Lock()
		messages := append([]*schema.Message(nil), e.messages...)
		e.mu.Unlock()
		if len(messages) == 0 {
			result.Output = "Nothing to compact."
			return nil, "", nil
		}
		providerUsage, usageErr := e.providerUsageForPotentialGoalCall()
		if usageErr != nil {
			return nil, "", fmt.Errorf(
				"compact conversation provider accounting: %w",
				usageErr,
			)
		}
		preTokens := compact.EstimateTokenCount(messages)
		compactedResult, err := compact.BuildLLMAutoCompact(ctx, messages, preTokens, compact.LLMCompactOptions{
			ChatModel:                 e.config.ChatModel,
			ModelName:                 e.config.Model,
			CustomInstructions:        customInstructions,
			SuppressFollowUpQuestions: true,
			ProviderUsage:             providerUsage,
		})
		if err != nil {
			return nil, "", fmt.Errorf("compact conversation: %w", err)
		}
		if compactedResult == nil {
			return nil, "", fmt.Errorf("compact conversation returned no result")
		}
		compacted := compact.BuildPostCompactMessages(compactedResult)
		if compactedResult.BoundaryMarker != nil {
			e.observeCompactBoundaryUsageMessage(compactedResult.BoundaryMarker)
		}
		if err := ctx.Err(); err != nil {
			return nil, "", fmt.Errorf("compact canceled before persistence: %w", err)
		}
		e.mu.Lock()
		recorder := e.transcript
		fileStateCache := e.fileStateCache
		e.mu.Unlock()
		if recorder == nil {
			return nil, "", fmt.Errorf("compact requires a transcript recorder")
		}
		usage := e.providerUsageSummary()
		commitCompact := func() error {
			return recorder.RecordLifecycleBoundaryWithUsage(
				transcript.LifecycleCompact,
				compacted,
				e.replacementRecords(),
				snapshotFileStateCache(fileStateCache),
				usage,
				true,
			)
		}
		var persistErr error
		if e.logicalWorkAdapter != nil {
			persistErr = e.logicalWorkAdapter.WithStableLifecycle(func(
				workboard.LifecycleSnapshot,
			) error {
				return commitCompact()
			})
		} else {
			persistErr = commitCompact()
		}
		if persistErr != nil {
			if transcript.IsDurabilityUncertain(persistErr) {
				e.mu.Lock()
				e.transcriptCheckpointRequired = true
				e.mu.Unlock()
			}
			return nil, "", fmt.Errorf(
				"persist compact boundary: %w",
				persistErr,
			)
		}
		e.mu.Lock()
		e.messages = compacted
		e.transcriptCheckpointRequired = false
		e.mu.Unlock()
		e.clearContextModelDispatchBlock(compacted)
		summary := compactedResult.Summary
		if len(summary) > 2000 {
			summary = summary[:2000] + "..."
		}
		result.Output = fmt.Sprintf(
			"Conversation compacted (~%d → ~%d tokens).\n\nSummary:\n%s",
			compactedResult.PreCompactTokenCount,
			compactedResult.PostCompactTokenCount,
			summary,
		)
		return nil, "", nil
	case commands.ActionSessions:
		operation, err := result.RequiredString("operation")
		if err != nil {
			return nil, "", err
		}
		if operation != "list" {
			return nil, "", fmt.Errorf("unsupported sessions operation %q", operation)
		}
		search, err := result.OptionalString("search")
		if err != nil {
			return nil, "", err
		}
		limitText, err := result.RequiredString("limit")
		if err != nil {
			return nil, "", err
		}
		limit, err := strconv.Atoi(limitText)
		if err != nil || limit <= 0 {
			return nil, "", fmt.Errorf("session limit must be a positive integer")
		}
		page, err := e.SessionService().Query(ctx, session.SessionQuery{
			Scope: session.SessionScopeCWD,
			Limit: limit,
			Filter: session.ListFilter{
				Search: search,
			},
		})
		if err != nil {
			return nil, "", fmt.Errorf("list sessions: %w", err)
		}
		result.Output = formatSessionPage(page, e.SessionID(), search)
		return nil, "", nil
	case commands.ActionResume:
		sessionID, err := result.OptionalString("session_id")
		if err != nil {
			return nil, "", err
		}
		resumed, err := e.SessionService().resumeForTurn(ctx, sessionID, turnID)
		if err != nil {
			return nil, "", fmt.Errorf("resume session: %w", err)
		}
		result.Output = fmt.Sprintf(
			"Resumed session %s (%d messages, ~%d tokens)",
			resumed.SessionID,
			len(resumed.Messages),
			resumed.TokenEstimate,
		)
		return resumedSessionAttachment(resumed), "", nil
	case commands.ActionExport:
		sessionID, err := result.OptionalString("session_id")
		if err != nil {
			return nil, "", err
		}
		filename, err := result.OptionalString("filename")
		if err != nil {
			return nil, "", err
		}
		exported, err := e.SessionService().Export(ctx, sessionID, filename)
		if err != nil {
			return nil, "", err
		}
		result.Output = fmt.Sprintf(
			"Exported session %s (%d messages) to %s",
			exported.SessionID,
			exported.MessageCount,
			exported.Path,
		)
		return nil, "", nil
	case commands.ActionChangeModel:
		modelID, err := result.RequiredString("model")
		if err != nil {
			return nil, "", err
		}
		state, err := e.changeModelForCommandTurn(ctx, modelID, turnID)
		if err != nil {
			return nil, "", err
		}
		result.Output = fmt.Sprintf(
			"Model switched to %s:%s.",
			state.Provider,
			state.Model,
		)
		if !state.Durable {
			result.Output += " Selection is process-local."
		}
		if state.ReasoningEffort != "" {
			result.Output += " Reasoning effort: " + state.ReasoningEffort + "."
		}
		for _, warning := range state.Warnings {
			if warning != "" {
				result.Output += " Warning: " + warning + "."
			}
		}
		return nil, "", nil
	case commands.ActionChangeMode:
		mode, err := result.RequiredString("mode")
		if err != nil {
			return nil, "", err
		}
		target := permission.Mode(mode)
		if !userSelectablePermissionMode(target) {
			return nil, "", fmt.Errorf("unsupported permission mode %q", mode)
		}
		confirmed, _, err := result.OptionalBool("bypass_confirmed")
		if err != nil {
			return nil, "", err
		}
		if target == permission.ModeBypassPermissions && !confirmed {
			return nil, "", fmt.Errorf("bypassPermissions requires explicit risk confirmation")
		}
		if err := e.transitionPermissionModeForCommandTurn(
			turnID,
			emit,
			target,
			"command-mode-"+turnID,
		); err != nil {
			return nil, "", err
		}
		result.Output = formatEffectivePermissionMode(e.PermissionMode(), e.PlanState())
		return nil, "", nil
	case commands.ActionPlanMode:
		enable, err := result.RequiredBool("enable")
		if err != nil {
			return nil, "", err
		}
		mode := permission.ModeDefault
		if enable {
			mode = permission.ModePlan
		}
		if err := e.transitionPermissionModeForCommandTurn(
			turnID,
			emit,
			mode,
			"command-plan-"+turnID,
		); err != nil {
			return nil, "", err
		}
		result.Output = formatEffectivePermissionMode(e.PermissionMode(), e.PlanState())
		description, err := result.OptionalString("description")
		if err != nil {
			return nil, "", err
		}
		return nil, description, nil
	case commands.ActionGoal:
		return e.applyGoalCommand(result, turnID)
	case commands.ActionSetEffort:
		level, err := result.RequiredString("level")
		if err != nil {
			return nil, "", err
		}
		effective, err := e.changeReasoningEffortForCommandTurn(ctx, level, turnID)
		if err != nil {
			return nil, "", err
		}
		result.Output = "Reasoning effort set to " + effective + "."
		return nil, "", nil
	case commands.ActionAddDir:
		path, err := result.RequiredString("path")
		if err != nil {
			return nil, "", err
		}
		canonical, added, err := e.addWorkingDirectoryForCommandTurn(ctx, path, turnID)
		if err != nil {
			return nil, "", err
		}
		if added {
			result.Output = "Added working directory: " + canonical
		} else {
			result.Output = "Directory is already accessible from an active workspace root: " + canonical
		}
		return nil, "", nil
	case commands.ActionFork:
		branchName, err := result.OptionalString("branch_name")
		if err != nil {
			return nil, "", err
		}
		resumed, forked, err := e.SessionService().forkAndActivateForTurn(
			ctx,
			SessionForkRequest{
				BranchName:  branchName,
				OperationID: turnID,
			},
			turnID,
		)
		if err != nil {
			return nil, "", err
		}
		result.Output = fmt.Sprintf(
			"Forked session %s to %s from turn %d. Now on branch: %s",
			forked.Info.ParentSessionID,
			forked.Info.SessionID,
			forked.Branch.MessagesCopied,
			forked.Branch.BranchName,
		)
		return resumedSessionAttachment(resumed), "", nil
	case commands.ActionRename:
		name, err := result.RequiredString("name")
		if err != nil {
			return nil, "", err
		}
		sessionID, err := result.OptionalString("session_id")
		if err != nil {
			return nil, "", err
		}
		renamed, err := e.SessionService().Rename(ctx, sessionID, name)
		if err != nil {
			return nil, "", err
		}
		result.Output = fmt.Sprintf("Session %s renamed to: %s", renamed.SessionID, renamed.Name)
		return nil, "", nil
	case commands.ActionPermissions:
		operation, err := result.RequiredString("operation")
		if err != nil {
			return nil, "", err
		}
		ruleActionText, err := result.RequiredString("rule_action")
		if err != nil {
			return nil, "", err
		}
		rule, err := result.RequiredString("rule")
		if err != nil {
			return nil, "", err
		}
		destinationText, err := result.RequiredString("destination")
		if err != nil {
			return nil, "", err
		}
		ruleAction := permission.PermissionAction(ruleActionText)
		switch ruleAction {
		case permission.ActionAllow, permission.ActionDeny, permission.ActionAsk:
		default:
			return nil, "", fmt.Errorf("unsupported permission action %q", ruleActionText)
		}
		destination := permission.SettingsDestination(destinationText)
		switch destination {
		case permission.DestLocalSettings,
			permission.DestProjectSettings,
			permission.DestUserSettings:
		default:
			return nil, "", fmt.Errorf("unsupported permission destination %q", destinationText)
		}
		switch operation {
		case "add":
			if err := permission.PersistPermissionRules(
				e.GetCWD(),
				[]string{rule},
				ruleAction,
				destination,
			); err != nil {
				return nil, "", fmt.Errorf("add permission rule: %w", err)
			}
		case "remove":
			if err := permission.RemovePermissionRules(
				e.GetCWD(),
				[]string{rule},
				ruleAction,
				destination,
			); err != nil {
				return nil, "", fmt.Errorf("remove permission rule: %w", err)
			}
		default:
			return nil, "", fmt.Errorf("unsupported permission operation %q", operation)
		}
		e.reloadPermissionRules()
		result.Output += "\n" + formatEffectivePermissionMode(e.PermissionMode(), e.PlanState())
		return nil, "", nil
	case commands.ActionReload:
		reloaded, err := e.ReloadPromptCommands()
		if err != nil {
			diagnostics := formatPluginDiagnostics(reloaded.Diagnostics)
			if diagnostics != "" {
				diagnostics = "; diagnostics: " + diagnostics
			}
			return nil, "", fmt.Errorf(
				"reload prompt commands; live generation %d retained%s: %w",
				reloaded.Generation.Revision,
				diagnostics,
				err,
			)
		}
		result.Output = formatPluginReload(reloaded)
		return nil, "", nil
	default:
		return nil, "", fmt.Errorf("engine does not own action %q", result.Action)
	}
}

func formatEffectivePermissionMode(mode permission.Mode, state PlanState) string {
	effective := mode
	if planPhaseRequiresContainment(state.Phase) {
		effective = permission.ModePlan
	}
	return fmt.Sprintf(
		"Effective permission mode: %s (plan phase: %s, revision: %d).",
		effective,
		state.Phase,
		state.Revision,
	)
}

func formatPluginReload(reloaded commands.PromptCommandReloadResult) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"Reloaded prompt-command generation %d: %d bundled packs, %d plugins, %d commands",
		reloaded.Generation.Revision,
		reloaded.BundledPacks,
		reloaded.EnabledPlugins,
		reloaded.Commands,
	)
	if reloaded.Generation.Digest != "" {
		digest := reloaded.Generation.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		fmt.Fprintf(&builder, " (digest %s)", digest)
	}
	for _, source := range reloaded.Generation.Sources {
		version := source.Version
		if version == "" {
			version = "unknown"
		}
		health := source.Health
		if health == "" {
			health = "healthy"
		}
		fmt.Fprintf(
			&builder,
			"\n  %s@%s [%s; kind=%s; trust=%s]: commands=%d skills=%d hooks=%d mcp=%d source=%s",
			source.Name,
			version,
			health,
			source.Kind,
			source.Trust,
			source.Commands,
			source.Skills,
			source.Hooks,
			source.MCPServers,
			source.Directory,
		)
	}
	if len(reloaded.Diagnostics) > 0 {
		fmt.Fprintf(&builder, "\nDiagnostics: %s", formatPluginDiagnostics(reloaded.Diagnostics))
	}
	return builder.String()
}

func formatPluginDiagnostics(diagnostics []commands.PromptCommandDiagnostic) string {
	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		source := diagnostic.Source
		if source == "" {
			source = diagnostic.Plugin
		}
		if source == "" {
			source = "candidate"
		}
		code := diagnostic.Code
		if code == "" {
			code = diagnostic.Severity
		}
		parts = append(
			parts,
			fmt.Sprintf("%s[%s]: %s", source, code, diagnostic.Message),
		)
	}
	return strings.Join(parts, "; ")
}

func formatSessionPage(page *session.SessionPage, currentID, search string) string {
	if page == nil || len(page.Sessions) == 0 {
		if strings.TrimSpace(search) != "" {
			return fmt.Sprintf("No saved sessions matched %q.", search)
		}
		return "No saved sessions found."
	}
	var builder strings.Builder
	if strings.TrimSpace(search) != "" {
		fmt.Fprintf(&builder, "Sessions matching %q (%d shown):\n", search, len(page.Sessions))
	} else {
		fmt.Fprintf(&builder, "Sessions (%d shown):\n", len(page.Sessions))
	}
	for _, info := range page.Sessions {
		marker := " "
		if info.SessionID == currentID {
			marker = ">"
		}
		title := strings.TrimSpace(info.CustomTitle)
		if title == "" {
			title = strings.TrimSpace(info.Summary)
		}
		if title == "" {
			title = strings.TrimSpace(info.FirstPrompt)
		}
		if title == "" {
			title = "(untitled)"
		}
		title = strings.Join(strings.Fields(title), " ")
		titleRunes := []rune(title)
		if len(titleRunes) > 72 {
			title = string(titleRunes[:69]) + "..."
		}
		modified := "unknown"
		if !info.LastModified.IsZero() {
			modified = info.LastModified.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&builder, "%s %s  %s  %s\n", marker, info.SessionID, modified, title)
	}
	if page.HasMore {
		builder.WriteString("More sessions are available; narrow the search or increase the limit.\n")
	}
	builder.WriteString("Use /sessions resume <session-id> to resume.")
	return strings.TrimRight(builder.String(), "\n")
}

func resumedSessionAttachment(resumed *session.ResumedSession) *QueryEvent {
	if resumed == nil {
		return nil
	}
	return &QueryEvent{
		Type: EventAttachment,
		AttachmentMessage: &schema.Message{
			Role:    schema.User,
			Content: fmt.Sprintf("Resumed session %s (%d messages, ~%d tokens)", resumed.SessionID, len(resumed.Messages), resumed.TokenEstimate),
			Extra:   map[string]any{"is_meta": true, "attachment_kind": "session_resumed"},
		},
	}
}

func startedSessionAttachment(started *session.ResumedSession) *QueryEvent {
	if started == nil {
		return nil
	}
	return &QueryEvent{
		Type: EventAttachment,
		AttachmentMessage: &schema.Message{
			Role:    schema.User,
			Content: fmt.Sprintf("Started new session %s", started.SessionID),
			Extra:   map[string]any{"is_meta": true, "attachment_kind": "session_started"},
		},
	}
}
