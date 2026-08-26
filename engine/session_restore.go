package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	enginecfg "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

const maxDurableChildTranscriptScan = 10_000

type resumedExecutionContext struct {
	cwd            string
	worktreePath   string
	worktreeBranch string
	additionalDirs []string
	warnings       []string
}

type preparedResumedGuestExecution struct {
	bindings     *containment.Bindings
	shellManager *tools.ShellManager
	replace      bool
}

func (e *QueryEngine) prepareResumedGuestExecution(
	ctx context.Context,
	cwd string,
	restoreIdentity string,
) (*preparedResumedGuestExecution, error) {
	if e == nil || e.administrationOnly {
		return nil, nil
	}
	e.mu.Lock()
	current := e.executionBindings
	ownsShellManager := e.ownsShellManager
	e.mu.Unlock()
	bindings, err := DeriveRestoredExecutionBindings(ctx, current, cwd, restoreIdentity)
	if err != nil {
		return nil, fmt.Errorf("restore Guest execution binding: %w", err)
	}
	prepared := &preparedResumedGuestExecution{bindings: bindings}
	if bindings.Guest().Digest() == current.Guest().Digest() {
		return prepared, nil
	}
	if !ownsShellManager {
		return nil, fmt.Errorf("restore Guest execution binding: externally owned shell manager cannot change root")
	}
	manager, err := tools.NewShellManagerWithGuestBinding(bindings.Guest())
	if err != nil {
		return nil, fmt.Errorf("restore Guest shell binding: %w", err)
	}
	if guest := bindings.Guest(); guest.Availability() == containment.BindingAvailable && containment.IsContainedAdapter(guest.AdapterFamily()) {
		proof := manager.GuestExecutionProof()
		if proof.BindingDigest != guest.Digest() || proof.PolicyDigest != guest.PolicyDigest() {
			return nil, fmt.Errorf("restore Guest execution proof does not match binding")
		}
	}
	prepared.shellManager = manager
	prepared.replace = true
	return prepared, nil
}

func (e *QueryEngine) resolveResumedExecutionContext(opts session.ResumeOptions, resumed *session.ResumedSession) resumedExecutionContext {
	e.mu.Lock()
	currentCWD := e.config.CWD
	e.mu.Unlock()
	metadata := resumed.Metadata
	resolved := resumedExecutionContext{
		cwd:            firstSessionValue(metadata.CWD, opts.ProjectDir, currentCWD),
		worktreePath:   strings.TrimSpace(metadata.WorktreePath),
		worktreeBranch: metadata.WorktreeBranch,
	}
	if resolved.worktreePath != "" {
		if isExistingSessionDirectory(resolved.worktreePath) {
			resolved.cwd = resolved.worktreePath
		} else {
			resolved.warnings = append(resolved.warnings, fmt.Sprintf(
				"worktree %s is unavailable; restored the original working directory instead", resolved.worktreePath,
			))
			resolved.worktreePath = ""
			resolved.worktreeBranch = ""
		}
	}
	if !isExistingSessionDirectory(resolved.cwd) {
		resolved.warnings = append(resolved.warnings, fmt.Sprintf(
			"working directory %s is unavailable; kept %s", resolved.cwd, currentCWD,
		))
		resolved.cwd = currentCWD
		resolved.worktreePath = ""
		resolved.worktreeBranch = ""
	}
	seen := map[string]struct{}{canonicalSessionDirectory(resolved.cwd): {}}
	for _, dir := range metadata.AdditionalDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if !isExistingSessionDirectory(dir) {
			resolved.warnings = append(resolved.warnings, "ignored unavailable additional working directory "+dir)
			continue
		}
		key := canonicalSessionDirectory(dir)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		resolved.additionalDirs = append(resolved.additionalDirs, dir)
	}
	if metadata.PermissionMode != "" && !isPersistedPermissionMode(metadata.PermissionMode) {
		resolved.warnings = append(resolved.warnings, fmt.Sprintf(
			"unknown persisted permission mode %q; restored default mode", metadata.PermissionMode,
		))
	}
	return resolved
}

func isExistingSessionDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isPersistedPermissionMode(value string) bool {
	switch permission.Mode(value) {
	case permission.ModeDefault, permission.ModePlan, permission.ModeAcceptEdits,
		permission.ModeBypassPermissions, permission.ModeDontAsk, permission.ModeAuto,
		permission.ModeBubble:
		return true
	default:
		return false
	}
}

func (e *QueryEngine) reloadResumedExecutionContext(
	ctx context.Context,
	cwd string,
	shellHooks *hooks.ShellHookConfig,
	preparedMCP *tools.MCPToolManager,
) {
	if e == nil {
		return
	}
	if e.config.HookExecutor != nil {
		// RegisterShellHooks replaces the complete project shell-hook
		// generation. An empty config deliberately clears the host project's
		// hooks when the resumed project has none.
		e.config.HookExecutor.RegisterShellHooks(shellHooks)
	}
	rules, _ := permission.LoadPermissionRules(cwd)
	rules = append(
		rules,
		toolSelectionDenyRules(e.toolRegistry, e.config.ToolSelection)...,
	)
	registry, _ := skills.LoadDefaultSkills(cwd)
	definitions, _ := LoadAgentDefinitions(cwd)
	var reloadedMCP *tools.MCPToolManager
	if preparedMCP != nil {
		reloadedMCP = preparedMCP
	} else if e.ownsMCPManager && e.toolRegistry != nil {
		reloadedMCP, _ = tools.InitMCPManagerWithBinding(ctx, cwd, e.toolRegistry, e.executionBindings.StdioMCP())
	}

	e.mu.Lock()
	oldWatcher := e.settingsWatcher
	oldMCP := e.mcpManager
	e.permissionRules = permission.NewRulesEngine(rules)
	if registry != nil {
		e.skillRegistry = registry
		if e.toolRegistry != nil {
			e.toolRegistry.Register(tools.SkillToolForRegistry(registry))
		}
	}
	if reloadedMCP != nil {
		e.mcpManager = reloadedMCP
	}
	if e.subagentExecutor != nil {
		e.subagentExecutor.AgentDefinitions = definitions
		e.subagentExecutor.ParentApprovals = e.approvalTracker
		e.subagentExecutor.SkillRegistry = e.skillRegistry
		e.subagentExecutor.TaskManager = e.taskManager
		e.subagentExecutor.MCPManager = e.mcpManager
		e.subagentExecutor.ParentFileState = e.fileStateCache
	}
	watcher := enginecfg.NewSettingsWatcher(cwd, 0, func(*enginecfg.Settings) {
		current := e.GetCWD()
		updated, _ := permission.LoadPermissionRules(current)
		e.mu.Lock()
		updated = append(
			updated,
			toolSelectionDenyRules(
				e.toolRegistry,
				e.config.ToolSelection,
			)...,
		)
		e.permissionRules = permission.NewRulesEngine(updated)
		e.mu.Unlock()
	})
	e.settingsWatcher = watcher
	catalogPath := e.config.SessionCatalogPath
	transcriptDir := e.config.TranscriptDir
	clock := e.config.Clock
	e.mu.Unlock()

	if oldWatcher != nil {
		oldWatcher.Stop()
	}
	if reloadedMCP != nil && oldMCP != nil && oldMCP != reloadedMCP {
		_ = oldMCP.DisconnectAll()
	}
	watcher.Start()
	e.approvalTracker.RevokeAll()
	if approvalsPath, err := permission.ApprovalStorePath(cwd); err == nil {
		_ = e.approvalTracker.LoadFrom(approvalsPath)
	}
	_ = session.RegisterSessionRoot(catalogPath, cwd, transcriptDir, clock())
}

type durableChildTranscript struct {
	metadata       session.SessionMetadataFull
	messages       []*schema.Message
	transcriptPath string
}

type durableChildParent struct {
	sessionID string
	threadID  string
	agentID   string
}

func (e *QueryEngine) restoreSessionAgents(
	sessionID string,
	threadID string,
	metadata session.SessionMetadata,
) ([]session.RestoredAgent, []string) {
	if e == nil || e.agentRunner == nil || e.runtimeState == nil {
		return nil, nil
	}
	discovered, discoveryWarnings := discoverDurableProjectGraphChildren(
		e.agentRunner.DurableTranscriptDir(),
		durableChildParent{
			sessionID: sessionID,
			threadID:  threadID,
			agentID:   metadata.AgentID,
		},
	)
	agentIDs := append([]string(nil), metadata.AgentIDs...)
	discoveredByAgent := make(map[string]durableChildTranscript, len(discovered))
	for _, child := range discovered {
		agentIDs = append(agentIDs, child.metadata.AgentID)
		discoveredByAgent[child.metadata.AgentID] = child
	}
	if len(agentIDs) == 0 {
		return nil, discoveryWarnings
	}

	restored := make([]session.RestoredAgent, 0, len(agentIDs))
	warnings := append([]string(nil), discoveryWarnings...)
	seen := make(map[string]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		if _, duplicate := seen[agentID]; duplicate {
			continue
		}
		seen[agentID] = struct{}{}

		active, activeFound := e.agentRunner.GetAgentSnapshot(agentID)
		var durable tools.RunningAgent
		var durableErr error
		if activeFound {
			durable, durableErr = e.agentRunner.LoadPersistedAgentSnapshot(agentID)
		} else {
			durable, durableErr = e.agentRunner.RegisterPersistedAgent(agentID)
		}
		child, childFound := discoveredByAgent[agentID]
		orphaned := false
		if durableErr != nil {
			if childFound && errors.Is(durableErr, tools.ErrPersistedAgentMetadataMissing) {
				durable = orphanedDurableChildSnapshot(child)
				orphaned = true
				if child.metadata.AgentGeneration == 0 {
					warnings = append(warnings, fmt.Sprintf(
						"Agent %s uses a pre-P13.9c child admission without an explicit generation; inferred generation 1 for inert orphan replay only",
						agentID,
					))
				}
				warnings = append(warnings, fmt.Sprintf(
					"Agent %s child Session admission was interrupted before durable Agent metadata; restored an inert project_graph_orphan replay",
					agentID,
				))
			} else {
				warnings = append(warnings, fmt.Sprintf("Agent %s replay is unavailable: %v", agentID, durableErr))
				continue
			}
		} else if childFound && !durableAgentMatchesChildTranscript(durable, child.metadata) {
			warnings = append(warnings, fmt.Sprintf(
				"Agent %s replay is unavailable: durable Agent identity conflicts with its ProjectGraph child Session",
				agentID,
			))
			continue
		}
		live := activeFound &&
			active.Status == "running" &&
			durable.Status == "running" &&
			sameDurableAgentGeneration(active, durable)
		if activeFound && active.Status == "running" && !live {
			warnings = append(warnings, fmt.Sprintf(
				"Agent %s live attachment was rejected because runner identity or generation differs from durable state",
				agentID,
			))
		}
		snapshot := runtimeAgentSnapshotFromRunner(durable)
		if childFound &&
			child.metadata.AgentGeneration > 0 &&
			snapshot.Generation == child.metadata.AgentGeneration {
			if binding, bindingWarnings := restoreGoalBinding(
				child.metadata.GoalBinding,
				child.metadata.AgentID,
			); binding != nil {
				snapshot.GoalID = binding.GoalID
				snapshot.GoalObjectiveRevision = binding.ObjectiveRevision
				snapshot.GoalRootSessionID = binding.RootSessionID
				snapshot.GoalRootThreadID = binding.RootThreadID
				snapshot.GoalRootAgentID = binding.RootAgentID
				snapshot.GoalTurnID = binding.GoalTurnID
			} else {
				for _, warning := range bindingWarnings {
					warnings = append(warnings, fmt.Sprintf(
						"Agent %s Goal binding: %s",
						agentID,
						warning,
					))
				}
			}
		}
		if orphaned {
			snapshot.Generation = child.metadata.AgentGeneration
			if snapshot.Generation <= 0 {
				snapshot.Generation = 1
			}
		}
		if snapshot.ParentSessionID == "" {
			snapshot.ParentSessionID = sessionID
		}
		if !sameRestoredAgentGeneration(e.runtimeState, snapshot, live) {
			if err := e.runtimeState.RestoreAgentSnapshot(snapshot, durable.Messages, live); err != nil {
				warnings = append(warnings, fmt.Sprintf("Agent %s restore failed: %v", agentID, err))
				continue
			}
		}
		mode := string(ThreadModeReplayOnly)
		if live {
			mode = string(ThreadModeLiveAttach)
		}
		if restoredSnapshot, _, _, ok := e.runtimeState.AgentThreadSnapshot(agentID); ok {
			snapshot = restoredSnapshot
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"Agent %s restore failed: runtime projection is unavailable",
				agentID,
			))
			continue
		}
		restored = append(restored, session.RestoredAgent{
			AgentID: agentID, ThreadID: snapshot.ThreadID, Mode: mode, Status: snapshot.Status,
		})
	}
	return restored, warnings
}

func runtimeAgentSnapshotFromRunner(agent tools.RunningAgent) RuntimeAgentSnapshot {
	updatedAt := agent.CompletedAt
	if updatedAt.IsZero() {
		updatedAt = agent.StartedAt
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	errorText := ""
	if agent.Error != nil {
		errorText = agent.Error.Error()
	}
	activities := make([]RuntimeAgentActivitySnapshot, 0, len(agent.Progress.RecentActivities))
	for _, activity := range agent.Progress.RecentActivities {
		activities = append(activities, RuntimeAgentActivitySnapshot{
			ToolName: activity.ToolName, Description: activity.ActivityDescription,
			IsSearch: activity.IsSearch, IsRead: activity.IsRead,
		})
	}
	return RuntimeAgentSnapshot{
		AgentID:         agent.ID,
		SessionID:       firstSessionValue(agent.SessionID, agent.ID),
		ThreadID:        firstSessionValue(agent.ThreadID, agent.ID),
		ParentSessionID: agent.ParentSessionID,
		ParentThreadID:  agent.ParentThreadID,
		ParentAgentID:   agent.ParentAgentID,
		ParentToolUseID: agent.ToolUseID,
		Name:            agent.Name,
		Task:            agent.Task,
		AgentType:       agent.Options.SubagentType,
		Model:           agent.Options.Model,
		PermissionMode:  firstSessionValue(agent.Options.Mode, agent.Options.InheritedPermissionMode),
		Isolation:       agent.Options.Isolation,
		CWD:             agent.Options.CWD,
		WorktreePath:    agent.WorktreePath,
		WorktreeBranch:  agent.WorktreeBranch,
		TranscriptPath:  agent.TranscriptPath,
		OutputFile:      agent.OutputFile,
		Description:     agent.Description,
		Status:          agent.Status,
		Error:           errorText,
		Generation:      agent.ExecutionGeneration(),
		Progress: RuntimeAgentProgressSnapshot{
			ToolUses: agent.Progress.ToolUseCount, TotalTokens: agent.Progress.TokenCount,
			Summary: agent.Progress.DisplaySummary(), RecentActivities: activities,
		},
		StartedAt: agent.StartedAt, UpdatedAt: updatedAt, CompletedAt: agent.CompletedAt,
	}
}

func sameDurableAgentGeneration(active, durable tools.RunningAgent) bool {
	return active.ID == durable.ID &&
		active.SessionID == durable.SessionID &&
		active.ThreadID == durable.ThreadID &&
		active.ParentSessionID == durable.ParentSessionID &&
		active.ParentThreadID == durable.ParentThreadID &&
		active.ParentAgentID == durable.ParentAgentID &&
		active.ToolUseID == durable.ToolUseID &&
		active.Options.CWD == durable.Options.CWD &&
		active.Options.Isolation == durable.Options.Isolation &&
		active.WorktreePath == durable.WorktreePath &&
		active.WorktreeBranch == durable.WorktreeBranch &&
		active.TranscriptPath == durable.TranscriptPath &&
		active.ExecutionGeneration() > 0 &&
		active.ExecutionGeneration() == durable.ExecutionGeneration()
}

func sameRestoredAgentGeneration(
	store *RuntimeStateStore,
	incoming RuntimeAgentSnapshot,
	live bool,
) bool {
	existing, thread, _, ok := store.AgentThreadSnapshot(incoming.AgentID)
	expectedStatus := incoming.Status
	expectedThreadStatus := runtimeStatusForAgentLifecycle(incoming.Status)
	if !live && !isRuntimeTerminalStatus(expectedThreadStatus) {
		expectedStatus = string(RuntimeThreadAborted)
		expectedThreadStatus = RuntimeThreadAborted
	}
	return ok &&
		existing.AgentID == incoming.AgentID &&
		existing.SessionID == incoming.SessionID &&
		existing.ThreadID == incoming.ThreadID &&
		existing.ParentSessionID == incoming.ParentSessionID &&
		existing.ParentThreadID == incoming.ParentThreadID &&
		existing.ParentAgentID == incoming.ParentAgentID &&
		existing.ParentToolUseID == incoming.ParentToolUseID &&
		existing.Generation > 0 &&
		existing.Generation == incoming.Generation &&
		existing.Status == expectedStatus &&
		thread.ThreadID == incoming.ThreadID &&
		thread.Status == expectedThreadStatus
}

func durableAgentMatchesChildTranscript(
	agent tools.RunningAgent,
	metadata session.SessionMetadataFull,
) bool {
	return agent.ID == metadata.AgentID &&
		agent.SessionID == metadata.SessionID &&
		agent.ThreadID == metadata.ThreadID &&
		agent.ParentSessionID == metadata.ParentSessionID &&
		agent.ParentThreadID == metadata.ParentThreadID &&
		agent.ParentAgentID == metadata.ParentAgentID &&
		agent.ToolUseID == metadata.ParentToolUseID &&
		(metadata.AgentGeneration == 0 ||
			agent.ExecutionGeneration() == metadata.AgentGeneration)
}

func orphanedDurableChildSnapshot(child durableChildTranscript) tools.RunningAgent {
	metadata := child.metadata
	completedAt := metadata.UpdatedAt
	if completedAt.IsZero() {
		completedAt = metadata.CreatedAt
	}
	task := ""
	for _, message := range child.messages {
		if message != nil && message.Role == schema.User {
			task = message.Content
			break
		}
	}
	return tools.RunningAgent{
		ID:              metadata.AgentID,
		SessionID:       metadata.SessionID,
		ThreadID:        metadata.ThreadID,
		ParentSessionID: metadata.ParentSessionID,
		ParentThreadID:  metadata.ParentThreadID,
		ParentAgentID:   metadata.ParentAgentID,
		ToolUseID:       metadata.ParentToolUseID,
		Type:            "local_agent",
		Task:            task,
		Description:     "Interrupted ProjectGraph child admission",
		Status:          "aborted",
		Error:           errors.New("project_graph_orphan: child Session committed before durable Agent metadata"),
		Messages:        child.messages,
		StartedAt:       metadata.CreatedAt,
		CompletedAt:     completedAt,
		TranscriptPath:  child.transcriptPath,
		Options: tools.AgentExecOptions{
			AgentID:                 metadata.AgentID,
			SessionID:               metadata.SessionID,
			ThreadID:                metadata.ThreadID,
			ParentSessionID:         metadata.ParentSessionID,
			ParentThreadID:          metadata.ParentThreadID,
			ParentAgentID:           metadata.ParentAgentID,
			ToolUseID:               metadata.ParentToolUseID,
			Model:                   metadata.Model,
			SubagentType:            metadata.AgentRole,
			Mode:                    metadata.PermissionMode,
			InheritedPermissionMode: metadata.PermissionMode,
			CWD:                     metadata.CWD,
		},
	}
}

func discoverDurableProjectGraphChildren(
	transcriptDir string,
	root durableChildParent,
) ([]durableChildTranscript, []string) {
	return discoverDurableProjectGraphChildrenWithLimit(
		transcriptDir,
		root,
		maxDurableChildTranscriptScan,
	)
}

func discoverDurableProjectGraphChildrenWithLimit(
	transcriptDir string,
	root durableChildParent,
	scanLimit int,
) ([]durableChildTranscript, []string) {
	transcriptDir = strings.TrimSpace(transcriptDir)
	if transcriptDir == "" || strings.TrimSpace(root.sessionID) == "" {
		return nil, nil
	}
	if scanLimit <= 0 {
		return nil, []string{"durable child Session scan limit must be positive"}
	}
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{"could not scan durable child Sessions: " + err.Error()}
	}
	warnings := make([]string, 0)
	type childTranscriptFile struct {
		sessionID string
		modified  time.Time
	}
	files := make([]childTranscriptFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(name, ".jsonl")
		if sessionID == "" || sessionID == "." || sessionID == ".." ||
			filepath.Base(sessionID) != sessionID ||
			strings.ContainsAny(sessionID, `/\`) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			warnings = append(warnings, fmt.Sprintf(
				"ignored non-regular durable child Session %s",
				sessionID,
			))
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"ignored unreadable durable child Session %s",
				sessionID,
			))
			continue
		}
		files = append(files, childTranscriptFile{
			sessionID: sessionID,
			modified:  info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].sessionID < files[j].sessionID
		}
		return files[i].modified.After(files[j].modified)
	})
	if len(files) > scanLimit {
		warnings = append(warnings, fmt.Sprintf(
			"durable child Session scan exceeded %d transcripts; orphan discovery inspected the newest bounded set",
			scanLimit,
		))
		files = files[:scanLimit]
	}

	candidates := make([]durableChildTranscript, 0, len(files))
	for _, file := range files {
		sessionID := file.sessionID
		recorder := transcript.NewRecorder(sessionID, transcriptDir)
		loaded, loadErr := recorder.LoadFull()
		if loadErr != nil {
			warnings = append(warnings, fmt.Sprintf(
				"ignored corrupt durable child Session %s: %v",
				sessionID,
				loadErr,
			))
			continue
		}
		metadata := session.ReadSessionMetadataFull(loaded)
		if metadata == nil ||
			metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
			(metadata.QueryKernelStage != string(queryKernelStageForegroundChild) &&
				metadata.QueryKernelStage != string(queryKernelStageBackgroundChild)) {
			continue
		}
		if len(loaded.Corruptions) > 0 ||
			metadata.SessionID != sessionID ||
			strings.TrimSpace(metadata.AgentID) == "" ||
			strings.TrimSpace(metadata.ThreadID) == "" ||
			strings.TrimSpace(metadata.ParentSessionID) == "" ||
			metadata.AgentGeneration < 0 {
			warnings = append(warnings, fmt.Sprintf(
				"ignored invalid ProjectGraph child Session %s",
				sessionID,
			))
			continue
		}
		candidates = append(candidates, durableChildTranscript{
			metadata:       *metadata,
			messages:       loaded.Messages,
			transcriptPath: recorder.Path(),
		})
	}

	childrenByParent := make(map[string][]durableChildTranscript)
	for _, candidate := range candidates {
		childrenByParent[candidate.metadata.ParentSessionID] = append(
			childrenByParent[candidate.metadata.ParentSessionID],
			candidate,
		)
	}
	queue := []durableChildParent{root}
	reachableCandidates := make([]durableChildTranscript, 0)
	seenSessions := map[string]struct{}{root.sessionID: {}}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[parent.sessionID] {
			if child.metadata.ParentThreadID != parent.threadID ||
				child.metadata.ParentAgentID != parent.agentID {
				warnings = append(warnings, fmt.Sprintf(
					"ignored ProjectGraph child Session %s with conflicting parent lineage",
					child.metadata.SessionID,
				))
				continue
			}
			if _, duplicate := seenSessions[child.metadata.SessionID]; duplicate {
				continue
			}
			seenSessions[child.metadata.SessionID] = struct{}{}
			reachableCandidates = append(reachableCandidates, child)
			queue = append(queue, durableChildParent{
				sessionID: child.metadata.SessionID,
				threadID:  child.metadata.ThreadID,
				agentID:   child.metadata.AgentID,
			})
		}
	}
	agentCounts := make(map[string]int, len(reachableCandidates))
	for _, child := range reachableCandidates {
		agentCounts[child.metadata.AgentID]++
	}
	reachable := make([]durableChildTranscript, 0, len(reachableCandidates))
	validParents := map[string]struct{}{root.sessionID: {}}
	warnedDuplicateAgents := make(map[string]struct{})
	for _, child := range reachableCandidates {
		if _, valid := validParents[child.metadata.ParentSessionID]; !valid {
			warnings = append(warnings, fmt.Sprintf(
				"ignored ProjectGraph child Session %s below an invalid parent lineage",
				child.metadata.SessionID,
			))
			continue
		}
		if agentCounts[child.metadata.AgentID] > 1 {
			if _, warned := warnedDuplicateAgents[child.metadata.AgentID]; !warned {
				warnedDuplicateAgents[child.metadata.AgentID] = struct{}{}
				warnings = append(warnings, fmt.Sprintf(
					"ignored all ProjectGraph child Sessions with duplicate Agent identity %s",
					child.metadata.AgentID,
				))
			}
			continue
		}
		reachable = append(reachable, child)
		validParents[child.metadata.SessionID] = struct{}{}
	}
	return reachable, warnings
}
