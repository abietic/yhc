package engine

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/tools"
)

func TestTaskExplorerSelectorIsDeterministicBoundedAndExplicit(t *testing.T) {
	longText := strings.Repeat("界", maxTaskExplorerTextRunes+1)
	record := taskExplorerTestRecord([]workboard.WorkItem{
		taskExplorerTestItem("blocked", 0, workboard.StatusPending, 1, "blocker"),
		taskExplorerTestItem("legacy-attention", 1, workboard.StatusPending, 1),
		taskExplorerTestItem("linked", 2, workboard.StatusInProgress, 1),
		taskExplorerTestItem("doing", 3, workboard.StatusInProgress, 1),
		taskExplorerTestItem("pending", 4, workboard.StatusPending, 1),
		taskExplorerTestItem("done-old", 5, workboard.StatusCompleted, 7),
		taskExplorerTestItem("done-new", 6, workboard.StatusCompleted, 8),
	})
	record.Board.Items[2].Title = longText
	record.Compatibility.Tasks = []workboard.TaskCompatibility{{
		ID:                  "legacy-attention",
		UnresolvedBlockedBy: []string{"missing"},
	}}
	projection := workboard.ProjectionSnapshot{
		Available: true,
		SessionID: record.SessionID,
		BoardID:   record.BoardID,
		Revision:  record.Board.Revision,
		Record:    record,
		Diagnostics: []workboard.ProjectionDiagnostic{{
			Sequence: 4,
			Kind:     "rejected_update",
			Message:  longText,
		}},
	}
	executions := map[RuntimeExecutionKey]RuntimeExecutionSnapshot{}
	addExecution := func(
		agentID string,
		generation int64,
		status string,
		ordinal uint64,
		ordinalPresent bool,
		replayOnly bool,
	) {
		key := RuntimeExecutionKey{AgentID: agentID, Generation: generation}
		executions[key] = RuntimeExecutionSnapshot{
			Key: key,
			Agent: RuntimeAgentSnapshot{
				AgentID:         agentID,
				Generation:      generation,
				SessionID:       "child-" + agentID,
				ThreadID:        "thread-" + agentID,
				ParentSessionID: "root-session",
				ParentThreadID:  "root-thread",
				Status:          status,
				Description:     longText,
			},
			ObservationOrdinal: ordinal,
			OrdinalPresent:     ordinalPresent,
			ReplayOnly:         replayOnly,
		}
	}
	addExecution("agent-failed", 1, "failed", 9, true, false)
	addExecution("agent-cancelled", 1, "aborted", 8, true, false)
	addExecution("agent-linked", 1, "running", 7, true, false)
	addExecution("agent-linked", 2, "completed", 6, true, false)
	addExecution("agent-restored", 1, "completed", 0, false, true)
	runtime := RuntimeTaskExplorerSnapshot{
		Runtime: RuntimeSnapshot{
			Revision:  11,
			Threads:   map[string]RuntimeThreadSnapshot{},
			Agents:    map[string]RuntimeAgentSnapshot{},
			Tasks:     map[string]RuntimeTaskSnapshot{},
			Worktrees: map[string]RuntimeWorktreeSnapshot{},
		},
		Executions:                  executions,
		DroppedEvents:               2,
		EvictedExecutionGenerations: 3,
	}
	links := []WorkExecutionLink{
		{BoardID: record.BoardID, WorkItemID: "linked", AgentID: "agent-linked", Generation: 2},
		{BoardID: record.BoardID, WorkItemID: "linked", AgentID: "agent-linked", Generation: 1},
		{BoardID: record.BoardID, WorkItemID: "linked", AgentID: "agent-linked", Generation: 3},
		{BoardID: record.BoardID, WorkItemID: "linked", AgentID: "missing", Generation: 1},
	}

	first := selectTaskExplorerSnapshot(projection, runtime, links)
	second := selectTaskExplorerSnapshot(projection, runtime, links)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("identical selector inputs differed:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !first.Available ||
		first.Revision != (TaskExplorerRevision{Board: 7, Runtime: 11}) {
		t.Fatalf("snapshot identity = %#v", first)
	}
	gotWorkItems := taskExplorerWorkItemIDs(first.WorkItems)
	wantWorkItems := []string{
		"blocked",
		"legacy-attention",
		"linked",
		"doing",
		"pending",
		"done-new",
		"done-old",
	}
	if !reflect.DeepEqual(gotWorkItems, wantWorkItems) {
		t.Fatalf("WorkItem order = %v, want %v", gotWorkItems, wantWorkItems)
	}
	linked := first.WorkItems[2]
	if !linked.LinkedLive ||
		len(linked.ExecutionKeys) != 2 ||
		len([]rune(linked.Title)) != maxTaskExplorerTextRunes {
		t.Fatalf("linked WorkItem = %#v", linked)
	}
	if first.Links[0].State != TaskExplorerLinkValid ||
		first.Links[1].State != TaskExplorerLinkValid ||
		first.Links[2].State != TaskExplorerLinkStale ||
		first.Links[3].State != TaskExplorerLinkMissing {
		t.Fatalf("explicit link states = %#v", first.Links)
	}
	for _, row := range first.Executions {
		if len(row.AllowedActions) != 1 ||
			row.AllowedActions[0] != TaskExplorerActionInspect ||
			len([]rune(row.Description)) != maxTaskExplorerTextRunes {
			t.Fatalf("execution row exceeded read-only bounds: %#v", row)
		}
	}
	if len(first.Executions[0].Attention) == 0 ||
		len(first.Executions[1].Attention) == 0 {
		t.Fatalf("attention executions were not ordered first: %#v", first.Executions)
	}
	if first.Executions[2].Phase != TaskExplorerExecutionRunning ||
		first.Executions[3].Phase != TaskExplorerExecutionReplayOnly ||
		first.Executions[4].Phase != TaskExplorerExecutionCompleted {
		t.Fatalf("live/replay/terminal execution order = %#v", first.Executions)
	}
	if first.Hidden.RuntimeEventsDropped != 2 ||
		first.Hidden.ExecutionGenerationsEvicted != 3 {
		t.Fatalf("runtime truncation facts = %#v", first.Hidden)
	}
	if len(first.Diagnostics) != 1 ||
		len([]rune(first.Diagnostics[0].Message)) != maxTaskExplorerTextRunes {
		t.Fatalf("diagnostic bounds = %#v", first.Diagnostics)
	}

	first.WorkItems[0].BlockedBy[0] = "mutated"
	first.WorkItems[0].Attention[0] = "mutated"
	first.Executions[0].Attention[0] = "mutated"
	first.Links[0].WorkItemID = "mutated"
	first.Diagnostics[0].Message = "mutated"
	first.Hidden.WorkItems["pending"] = 99
	third := selectTaskExplorerSnapshot(projection, runtime, links)
	if !reflect.DeepEqual(third, second) {
		t.Fatal("mutating a returned snapshot changed later selector output")
	}
}

func TestTaskExplorerUnavailableWorkBoardRetainsExactReadOnlyExecutions(
	t *testing.T,
) {
	key := RuntimeExecutionKey{AgentID: "legacy-agent", Generation: 3}
	runtime := RuntimeTaskExplorerSnapshot{
		Runtime: RuntimeSnapshot{
			Revision:  9,
			Threads:   map[string]RuntimeThreadSnapshot{},
			Agents:    map[string]RuntimeAgentSnapshot{},
			Tasks:     map[string]RuntimeTaskSnapshot{},
			Worktrees: map[string]RuntimeWorktreeSnapshot{},
		},
		Executions: map[RuntimeExecutionKey]RuntimeExecutionSnapshot{
			key: {
				Key: key,
				Agent: RuntimeAgentSnapshot{
					AgentID:     key.AgentID,
					Generation:  key.Generation,
					ThreadID:    "legacy-thread",
					Status:      "running",
					DisplayMode: "foreground",
					Progress: RuntimeAgentProgressSnapshot{
						LastToolName: "Read",
						ToolUses:     4,
						TotalTokens:  512,
					},
				},
			},
		},
	}
	snapshot := selectTaskExplorerSnapshot(
		workboard.ProjectionSnapshot{},
		runtime,
		nil,
	)
	declareTaskExplorerActions(
		&snapshot,
		runtime.Runtime,
		RuntimeThreadCatalogSnapshot{Revision: runtime.Runtime.Revision},
		tools.NewAgentRunner(1),
	)
	if snapshot.Available ||
		snapshot.UnavailableReason != "workboard_not_authoritative" ||
		snapshot.Revision.Runtime != 9 ||
		len(snapshot.Executions) != 1 ||
		snapshot.Executions[0].Key != key ||
		snapshot.Executions[0].DisplayMode != "foreground" ||
		snapshot.Executions[0].LastToolName != "Read" ||
		snapshot.Executions[0].ToolUseCount != 4 ||
		snapshot.Executions[0].TokenCount != 512 {
		t.Fatalf("legacy read-only snapshot = %+v", snapshot)
	}
	if !reflect.DeepEqual(
		snapshot.Executions[0].AllowedActions,
		[]TaskExplorerAction{TaskExplorerActionInspect},
	) {
		t.Fatalf(
			"legacy execution actions = %v",
			snapshot.Executions[0].AllowedActions,
		)
	}
}

func TestTaskExplorerSelectorReportsPrimaryBoundsAndArchivePage(t *testing.T) {
	items := make([]workboard.WorkItem, 0, 129)
	for index := range 129 {
		items = append(items, taskExplorerTestItem(
			fmt.Sprintf("item-%03d", index),
			index,
			workboard.StatusPending,
			1,
		))
	}
	record := taskExplorerTestRecord(items)
	runtime := RuntimeTaskExplorerSnapshot{
		Runtime: RuntimeSnapshot{
			Threads:   map[string]RuntimeThreadSnapshot{},
			Agents:    map[string]RuntimeAgentSnapshot{},
			Tasks:     map[string]RuntimeTaskSnapshot{},
			Worktrees: map[string]RuntimeWorktreeSnapshot{},
		},
		Executions: map[RuntimeExecutionKey]RuntimeExecutionSnapshot{},
	}
	links := make([]WorkExecutionLink, 0, 129)
	for index := range 129 {
		links = append(links, WorkExecutionLink{
			BoardID:    record.BoardID,
			WorkItemID: "item-000",
			AgentID:    fmt.Sprintf("missing-%03d", index),
			Generation: 1,
		})
	}
	snapshot := selectTaskExplorerSnapshot(
		workboard.ProjectionSnapshot{
			Available: true,
			SessionID: record.SessionID,
			BoardID:   record.BoardID,
			Revision:  record.Board.Revision,
			Record:    record,
		},
		runtime,
		links,
	)
	if len(snapshot.WorkItems) != maxTaskExplorerWorkItems ||
		snapshot.Hidden.WorkItems[string(workboard.StatusPending)] != 1 ||
		snapshot.Hidden.WorkBoardOutsidePrimary != 1 {
		t.Fatalf("WorkItem 129 boundary = %#v", snapshot.Hidden)
	}
	if len(snapshot.Links) != maxTaskExplorerLinks ||
		snapshot.Hidden.Links != 1 ||
		len(snapshot.Attention) != maxTaskExplorerAttention ||
		snapshot.Hidden.Attention["missing_link_target"] != 1 {
		t.Fatalf(
			"link/attention 129 boundary = links %d attention %d hidden %#v",
			len(snapshot.Links),
			len(snapshot.Attention),
			snapshot.Hidden,
		)
	}

	terminal := make([]workboard.WorkItem, 0, 101)
	for index := range 101 {
		terminal = append(terminal, taskExplorerTestItem(
			fmt.Sprintf("terminal-%03d", index),
			index,
			workboard.StatusCompleted,
			uint64(index+1),
		))
	}
	page := taskExplorerArchivePage(taskExplorerTestRecord(terminal), 0, 500)
	if len(page.Rows) != maxTaskExplorerPage ||
		!page.HasMore ||
		page.NextOffset != maxTaskExplorerPage {
		t.Fatalf("100-row archive page = %#v", page)
	}
	last := taskExplorerArchivePage(
		taskExplorerTestRecord(terminal),
		page.NextOffset,
		maxTaskExplorerPage,
	)
	if len(last.Rows) != 1 || last.HasMore || last.NextOffset != 101 {
		t.Fatalf("archive tail = %#v", last)
	}
}

func TestTaskExplorerConcurrentMutationReplayAndDefensiveReads(t *testing.T) {
	manager := tools.NewTaskManager()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod WorkBoard directory: %v", err)
	}
	adapter, err := workboard.BindLogicalWorkAdapter(workboard.AdapterConfig{
		SessionID:   "root-session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "root-session"},
		NewBoardID:  func() string { return "board-concurrent" },
	}, manager)
	if err != nil {
		t.Fatalf("bind WorkBoard adapter: %v", err)
	}
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 128, Agents: 128})
	engine := &QueryEngine{
		config:             QueryEngineConfig{ThreadID: "root-thread"},
		runtimeState:       store,
		logicalWorkAdapter: adapter,
	}

	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for index := range 32 {
			if _, createErr := manager.CreateWithError(
				fmt.Sprintf("work-%02d", index),
				strings.Repeat("x", maxTaskExplorerTextRunes+1),
				"",
				nil,
			); createErr != nil {
				t.Errorf("create WorkItem %d: %v", index, createErr)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		for index := range 32 {
			agentID := fmt.Sprintf("agent-%02d", index)
			event := runtimeTestEvent(
				1,
				"agent-launch:"+agentID+":1",
				EventAgentLifecycle,
				func(evt *QueryEvent) {
					evt.AgentID = agentID
					evt.ThreadID = "thread-" + agentID
					evt.AgentGeneration = 1
					evt.AgentLifecycle = &AgentLifecycleEvent{
						Phase:      "launched",
						Status:     "running",
						Generation: 1,
						StartedAt:  evt.Timestamp,
					}
				},
			)
			if applyErr := store.Apply(event); applyErr != nil {
				t.Errorf("apply execution %d: %v", index, applyErr)
				return
			}
		}
	}()
	for range 64 {
		snapshot := engine.TaskExplorerSnapshot()
		if snapshot.Available {
			snapshot.Hidden.WorkItems["mutated"] = 1
			if len(snapshot.WorkItems) > 0 {
				snapshot.WorkItems[0].Title = "mutated"
			}
		}
	}
	writers.Wait()
	final := engine.TaskExplorerSnapshot()
	if !final.Available ||
		len(final.WorkItems) != 32 ||
		len(final.Executions) != 32 {
		t.Fatalf("concurrent final snapshot = %#v", final)
	}
	if final.Hidden.WorkItems["mutated"] != 0 {
		t.Fatal("returned hidden-count mutation leaked into selector state")
	}
}

func taskExplorerTestRecord(items []workboard.WorkItem) workboard.AuthorityRecord {
	return workboard.AuthorityRecord{
		Version:   workboard.AuthorityRecordVersion,
		SessionID: "root-session",
		BoardID:   "board-1",
		Board: workboard.Board{
			Revision: 7,
			Items:    items,
		},
	}
}

func taskExplorerTestItem(
	id string,
	order int,
	status workboard.Status,
	revision uint64,
	blockedBy ...string,
) workboard.WorkItem {
	return workboard.WorkItem{
		ID:        id,
		Revision:  revision,
		Source:    workboard.SourcePartition{Kind: "task", LegacyID: id},
		Order:     order,
		Title:     id,
		Status:    status,
		BlockedBy: append([]string(nil), blockedBy...),
	}
}

func taskExplorerWorkItemIDs(rows []TaskExplorerWorkItem) []string {
	ids := make([]string, len(rows))
	for index := range rows {
		ids[index] = rows[index].WorkItemID
	}
	return ids
}
