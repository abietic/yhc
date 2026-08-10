package workboard

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
)

func TestLogicalWorkAdapterCutsOverBeforeFirstTaskMutation(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	first := manager.Create("first", "first description", "doing first", nil)
	missing := "2"
	if _, _, err := manager.Update(tools.TaskUpdate{
		TaskID:       first.ID,
		AddBlockedBy: []string{missing},
	}); err != nil {
		t.Fatalf("seed unresolved dependency: %v", err)
	}
	clock := fixedAdapterClock()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		Clock:       clock,
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
	if adapter.Mode() != AuthorityModeLegacy {
		t.Fatalf("initial mode = %q", adapter.Mode())
	}

	second, err := manager.CreateWithError(
		"second",
		"second description",
		"doing second",
		map[string]any{"key": "value"},
	)
	if err != nil {
		t.Fatalf("first authoritative mutation: %v", err)
	}
	if second.ID != "2" {
		t.Fatalf("created Task ID = %q", second.ID)
	}
	if adapter.Mode() != AuthorityModeWorkBoard {
		t.Fatalf("post-cutover mode = %q", adapter.Mode())
	}
	store := mustAuthorityStore(t, dir, "session")
	state, err := store.Inspect()
	if err != nil {
		t.Fatalf("inspect committed authority: %v", err)
	}
	if state.Record.BoardID != "board" || state.Record.Board.Revision != 2 {
		t.Fatalf("committed authority = %+v", state.Record)
	}
	firstItem := taskItemByLegacyID(t, state.Record.Board, "1")
	if len(firstItem.BlockedBy) != 0 {
		t.Fatalf(
			"target creation implicitly promoted dependency: %+v",
			firstItem.BlockedBy,
		)
	}
	firstCompatibility := taskCompatibilityByID(
		t,
		state.Record.Compatibility,
		"1",
	)
	if !reflect.DeepEqual(
		firstCompatibility.UnresolvedBlockedBy,
		[]string{"2"},
	) {
		t.Fatalf(
			"unresolved dependency = %+v",
			firstCompatibility.UnresolvedBlockedBy,
		)
	}

	if _, fields, err := manager.Update(tools.TaskUpdate{
		TaskID:       "1",
		AddBlockedBy: []string{"2"},
	}); err != nil {
		t.Fatalf("explicit dependency promotion: %v", err)
	} else if !reflect.DeepEqual(fields, []string{"blocked_by"}) {
		t.Fatalf("updated fields = %+v", fields)
	}
	state, err = store.Inspect()
	if err != nil {
		t.Fatalf("inspect promoted authority: %v", err)
	}
	firstItem = taskItemByLegacyID(t, state.Record.Board, "1")
	if !reflect.DeepEqual(firstItem.BlockedBy, []string{"task:2"}) {
		t.Fatalf("canonical promoted dependency = %+v", firstItem.BlockedBy)
	}
	firstCompatibility = taskCompatibilityByID(
		t,
		state.Record.Compatibility,
		"1",
	)
	if len(firstCompatibility.UnresolvedBlockedBy) != 0 ||
		!reflect.DeepEqual(firstCompatibility.BlockedBy, []string{"2"}) {
		t.Fatalf(
			"legacy dependency projection = %+v",
			firstCompatibility,
		)
	}
}

func TestLogicalWorkAdapterRepairsBareLegacyDirectoryBeforeMutation(
	t *testing.T,
) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod legacy directory: %v", err)
	}
	manager := tools.NewTaskManager()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		Clock:       fixedAdapterClock(),
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatalf("bind bare legacy directory: %v", err)
	}
	if adapter.Mode() != AuthorityModeLegacy {
		t.Fatalf("initial mode = %q", adapter.Mode())
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared transcript directory = %#v, %v", info, err)
	}

	if _, err := manager.CreateWithError(
		"first",
		"description",
		"doing first",
		nil,
	); err != nil {
		t.Fatalf("first mutation after repair: %v", err)
	}
	if adapter.Mode() != AuthorityModeWorkBoard {
		t.Fatalf("mutation mode = %q", adapter.Mode())
	}
	for _, suffix := range []string{
		AuthorityRecordSuffix,
		AuthorityMarkerSuffix,
		LegacyBackupSuffix,
	} {
		if _, err := os.Lstat(filepath.Join(dir, "session"+suffix)); err != nil {
			t.Fatalf("artifact %s missing: %v", suffix, err)
		}
	}
}

func TestLogicalWorkAdapterPreparesMissingPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	adapter, err := NewLogicalWorkAdapter(AdapterConfig{
		SessionID: "session",
		Dir:       dir,
	}, tools.TaskManagerSnapshot{NextID: 1})
	if err != nil || adapter == nil {
		t.Fatalf("prepare missing directory: adapter=%v err=%v", adapter, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared directory = %#v, %v", info, err)
	}
}

func TestLogicalWorkAdapterRejectsReplacementAfterInspection(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	moved := dir + "-moved"
	_, err := NewLogicalWorkAdapter(AdapterConfig{
		SessionID: "session",
		Dir:       dir,
		DirectoryPreparationAfterInspect: func() {
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}, tools.TaskManagerSnapshot{NextID: 1})
	if err == nil || !strings.Contains(err.Error(), "changed while securing") {
		t.Fatalf("replacement error = %v", err)
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
	assertAuthorityArtifactsAbsent(t, moved, "session")
}

func TestLogicalWorkAdapterRejectsReplacementBeforeFirstCutover(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	moved := dir + "-moved"
	manager := tools.NewTaskManager()
	if _, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID: "session",
		Dir:       dir,
	}, manager); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.CreateWithError(
		"first",
		"description",
		"doing first",
		nil,
	); err == nil || !strings.Contains(err.Error(), "changed after preparation") {
		t.Fatalf("first mutation error = %v", err)
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
	assertAuthorityArtifactsAbsent(t, moved, "session")
}

func TestLogicalWorkAdapterPreservesLegacyStatusAndTodoReplacement(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	task := manager.Create("task", "description", "", nil)
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		Clock:       fixedAdapterClock(),
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	deleted := tools.TaskStatus("deleted")
	updated, fields, err := manager.Update(tools.TaskUpdate{
		TaskID: task.ID,
		Status: &deleted,
	})
	if err != nil {
		t.Fatalf("update arbitrary status: %v", err)
	}
	if updated.Status != deleted ||
		!reflect.DeepEqual(fields, []string{"status"}) {
		t.Fatalf("legacy status projection = %+v fields=%+v", updated, fields)
	}
	state, err := mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatalf("inspect authority: %v", err)
	}
	item := taskItemByLegacyID(t, state.Record.Board, task.ID)
	if item.Status != StatusCancelled ||
		item.TerminalReason != "legacy_status" {
		t.Fatalf("canonical arbitrary status = %+v", item)
	}

	scope := tools.TodoScope{SessionID: "session"}
	duplicates := []tools.TodoItem{
		{Content: "same", Status: "pending", ActiveForm: "doing same"},
		{Content: "same", Status: "pending", ActiveForm: "doing same"},
	}
	if err := adapter.ReplaceTodos(scope, duplicates); err != nil {
		t.Fatalf("replace duplicate Todos: %v", err)
	}
	projected, err := adapter.Todos(scope)
	if err != nil {
		t.Fatalf("project duplicate Todos: %v", err)
	}
	if !reflect.DeepEqual(projected, duplicates) {
		t.Fatalf("duplicate Todo projection = %+v", projected)
	}
	state, err = mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatalf("inspect Todo authority: %v", err)
	}
	todoScope, found := todoScopeCompatibility(
		state.Record.Compatibility.TodoScopes,
		scope,
	)
	if !found ||
		len(todoScope.CurrentItemIDs) != 2 ||
		todoScope.CurrentItemIDs[0] == todoScope.CurrentItemIDs[1] {
		t.Fatalf("duplicate Todo identities = %+v", todoScope)
	}

	completed := []tools.TodoItem{
		{Content: "same", Status: "completed", ActiveForm: "doing same"},
		{Content: "same", Status: "completed", ActiveForm: "doing same"},
	}
	if err := adapter.ReplaceTodos(scope, completed); err != nil {
		t.Fatalf("complete duplicate Todos: %v", err)
	}
	projected, err = adapter.Todos(scope)
	if err != nil {
		t.Fatalf("project completed Todos: %v", err)
	}
	if projected != nil {
		t.Fatalf("all-complete legacy Todo view = %+v", projected)
	}
	state, err = mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatalf("inspect completed evidence: %v", err)
	}
	completedEvidence := 0
	for _, candidate := range state.Record.Board.Items {
		if candidate.Source.Kind == "todo" &&
			candidate.Status == StatusCompleted {
			completedEvidence++
		}
	}
	if completedEvidence != 2 {
		t.Fatalf("completed Todo evidence count = %d", completedEvidence)
	}
}

func TestLogicalWorkAdapterMarkerCommitFailureNeverRestoresLegacyWriter(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	manager.Create("seed", "description", "", nil)
	inject := true
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID:  func() string { return "board" },
		Failure: func(stage StoreStage) error {
			if inject && stage == StoreStageFirstMutation {
				inject = false
				return errors.New("stop before requested mutation")
			}
			return nil
		},
	}, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	if _, err := manager.CreateWithError("not committed", "description", "", nil); err == nil {
		t.Fatal("expected first-mutation failure")
	}
	if adapter.Mode() != AuthorityModeWorkBoard {
		t.Fatalf("marker-visible adapter mode = %q", adapter.Mode())
	}
	state, err := mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatalf("inspect marker-visible seed: %v", err)
	}
	if len(state.Record.Compatibility.Tasks) != 1 {
		t.Fatalf(
			"failed requested mutation changed seed: %+v",
			state.Record.Compatibility.Tasks,
		)
	}
	created, err := manager.CreateWithError("retried", "description", "", nil)
	if err != nil {
		t.Fatalf("retry authoritative mutation: %v", err)
	}
	if created.ID != "2" {
		t.Fatalf("retried Task ID = %q", created.ID)
	}
}

func TestLogicalWorkAdapterQuarantinesRetryAfterUncertainCommit(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	failCommit := false
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		Clock:       fixedAdapterClock(),
		NewBoardID:  func() string { return "board" },
		FileFailure: func(kind ArtifactKind, stage FailureStage) error {
			if !failCommit || kind != ArtifactAuthority {
				return nil
			}
			if stage == FailureDirSync || stage == FailureRollback {
				return errors.New("stop")
			}
			return nil
		},
	}, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	if _, err := manager.CreateWithError(
		"first",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatalf("establish authoritative state: %v", err)
	}

	failCommit = true
	if _, err := manager.CreateWithError(
		"uncertain",
		"description",
		"",
		nil,
	); err == nil || !IsDurabilityUncertain(err) {
		t.Fatalf("uncertain commit error = %v", err)
	}
	if _, err := manager.CreateWithError(
		"retry",
		"description",
		"",
		nil,
	); err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("retry error = %v", err)
	}
	tasks := adapter.List()
	if len(tasks) != 1 || tasks[0].Subject != "first" {
		t.Fatalf(
			"uncertain commit changed the readable adapter view: %+v",
			tasks,
		)
	}
	markerInfo, err := os.Lstat(
		filepath.Join(dir, "session"+AuthorityMarkerSuffix),
	)
	if err != nil {
		t.Fatalf("stat quarantined marker: %v", err)
	}
	if markerInfo.Mode().Perm() != 0o400 {
		t.Fatalf("quarantined marker mode = %o", markerInfo.Mode().Perm())
	}
	if _, err := NewLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
	}, tools.TaskManagerSnapshot{NextID: 1}); err == nil ||
		!strings.Contains(err.Error(), "marker artifact mode is not 0600") {
		t.Fatalf("restart did not fail closed on quarantine: %v", err)
	}
}

func TestLogicalWorkAdapterCommittedProjectionFailureQuarantinesAndRepairs(
	t *testing.T,
) {
	for name, hook := range map[string]func() error{
		"error": func() error { return errors.New("injected projection failure") },
		"panic": func() error { panic("injected projection panic") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			manager := tools.NewTaskManager()
			failSwap := false
			config := AdapterConfig{
				SessionID:   "session",
				Dir:         dir,
				LeaderScope: tools.TodoScope{SessionID: "session"},
				Clock:       fixedAdapterClock(),
				NewBoardID:  func() string { return "board" },
				ProjectionSwapFailure: func() error {
					if failSwap {
						return hook()
					}
					return nil
				},
			}
			adapter, err := BindLogicalWorkAdapter(config, manager)
			if err != nil {
				t.Fatalf("bind adapter: %v", err)
			}
			if _, err := manager.CreateWithError("first", "description", "", nil); err != nil {
				t.Fatalf("establish authority: %v", err)
			}
			before := adapter.ProjectionSnapshot()
			failSwap = true
			if _, err := manager.CreateWithError("committed", "description", "", nil); err == nil {
				t.Fatal("expected committed projection error")
			} else {
				var uncertain *CommittedProjectionUncertainError
				if !errors.As(err, &uncertain) ||
					uncertain.Code() != "committed_projection_uncertain" ||
					uncertain.RetrySafe() {
					t.Fatalf("unexpected projection error: %v", err)
				}
			}
			state, err := mustAuthorityStore(t, dir, "session").Inspect()
			if err != nil {
				t.Fatalf("inspect durable record: %v", err)
			}
			if state.Record.Board.Revision != before.Record.Board.Revision+1 {
				t.Fatalf("durable revision = %d", state.Record.Board.Revision)
			}
			if got := adapter.ProjectionSnapshot().Record.Board.Revision; got != before.Record.Board.Revision {
				t.Fatalf("failed projection swap revision = %d", got)
			}
			if _, err := manager.CreateWithError("retry", "description", "", nil); err == nil ||
				!strings.Contains(err.Error(), "quarantined") {
				t.Fatalf("quarantined retry error = %v", err)
			}
			fresh, err := NewLogicalWorkAdapter(config, tools.TaskManagerSnapshot{NextID: 1})
			if err != nil {
				t.Fatalf("fresh construction: %v", err)
			}
			if got := fresh.ProjectionSnapshot().Record.Board.Revision; got != state.Record.Board.Revision {
				t.Fatalf("fresh projection revision = %d, want %d", got, state.Record.Board.Revision)
			}
		})
	}
}

func TestLogicalWorkAdapterProjectionFollowsActivationAndRecovery(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	boardIDs := []string{"board-original", "board-recovered"}
	config := AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID: func() string {
			id := boardIDs[0]
			boardIDs = boardIDs[1:]
			return id
		},
	}
	adapter, err := BindLogicalWorkAdapter(config, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	if _, err := manager.CreateWithError("first", "description", "", nil); err != nil {
		t.Fatalf("cutover mutation: %v", err)
	}
	prepared, err := NewLogicalWorkAdapter(config, tools.TaskManagerSnapshot{NextID: 1})
	if err != nil {
		t.Fatalf("prepared adapter: %v", err)
	}
	unlock, err := adapter.BeginActivation(prepared)
	if err != nil {
		t.Fatalf("begin activation: %v", err)
	}
	unlock()
	if got, want := adapter.ProjectionSnapshot().Record.BoardID, prepared.ProjectionSnapshot().Record.BoardID; got != want {
		t.Fatalf("activated projection board = %q, want %q", got, want)
	}
	activeBefore := adapter.ProjectionSnapshot()
	prepared.mu.Lock()
	prepared.projection.diagnose("prepared adapter changed after activation")
	prepared.mu.Unlock()
	if got := adapter.ProjectionSnapshot(); !reflect.DeepEqual(got, activeBefore) {
		t.Fatalf("prepared projection alias changed active adapter: %#v", got)
	}
	before := adapter.Snapshot()
	if _, err := adapter.Recover(RecoveryRequest{
		SessionID: before.SessionID, BoardID: before.BoardID, Revision: before.Revision,
		AcknowledgeDataLoss: true,
	}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := adapter.ProjectionSnapshot().Record.BoardID; got != "board-recovered" {
		t.Fatalf("recovered projection board = %q", got)
	}
}

func TestLogicalWorkAdapterRecoveryRequiresExactAcknowledgedIdentity(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	manager.Create("baseline", "description", "", nil)
	boardIDs := []string{"board-original", "board-recovered"}
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID: func() string {
			id := boardIDs[0]
			boardIDs = boardIDs[1:]
			return id
		},
	}, manager)
	if err != nil {
		t.Fatalf("bind adapter: %v", err)
	}
	if _, err := manager.CreateWithError("post-cutover", "description", "", nil); err != nil {
		t.Fatalf("cutover mutation: %v", err)
	}
	if _, _, err := manager.Update(tools.TaskUpdate{
		TaskID: "1",
		Owner:  stringPointer("later-owner"),
	}); err != nil {
		t.Fatalf("post-cutover update: %v", err)
	}
	before := adapter.Snapshot()
	if _, err := adapter.Recover(RecoveryRequest{
		SessionID: before.SessionID,
		BoardID:   before.BoardID,
		Revision:  before.Revision,
	}); err == nil {
		t.Fatal("expected recovery acknowledgement rejection")
	}
	result, err := adapter.Recover(RecoveryRequest{
		SessionID:           before.SessionID,
		BoardID:             before.BoardID,
		Revision:            before.Revision,
		AcknowledgeDataLoss: true,
	})
	if err != nil {
		t.Fatalf("recover backup: %v", err)
	}
	if result.RecoveredBoardID != "board-recovered" ||
		result.PreviousBoardID != "board-original" {
		t.Fatalf("recovery result = %+v", result)
	}
	if _, found := manager.Get("2"); found {
		t.Fatal("post-cutover Task survived destructive recovery")
	}
	recovered, found := manager.Get("1")
	if !found || recovered.Owner != "" {
		t.Fatalf("recovered baseline Task = %+v found=%v", recovered, found)
	}
	markerData, err := os.ReadFile(
		filepath.Join(dir, "session"+AuthorityMarkerSuffix),
	)
	if err != nil {
		t.Fatalf("read retained marker: %v", err)
	}
	if _, err := DecodeAuthorityMarker(markerData, "session"); err != nil {
		t.Fatalf("retained marker invalid: %v", err)
	}
}

func TestLogicalWorkAdapterRejectedLegacyMutationDoesNotCutOver(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Update(tools.TaskUpdate{
		TaskID: "missing",
	}); err == nil {
		t.Fatal("missing Task update unexpectedly succeeded")
	}
	if _, err := adapter.Create(
		"invalid metadata",
		"description",
		"",
		map[string]any{"function": func() {}},
	); err == nil {
		t.Fatal("non-JSON metadata unexpectedly succeeded")
	}
	if err := adapter.ReplaceTodos(
		tools.TodoScope{},
		[]tools.TodoItem{{
			Content:    "invalid scope",
			Status:     "pending",
			ActiveForm: "invalid",
		}},
	); err == nil {
		t.Fatal("empty Todo scope unexpectedly succeeded")
	}
	if adapter.Mode() != AuthorityModeLegacy {
		t.Fatalf("rejected mutation changed mode to %q", adapter.Mode())
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
}

func TestLogicalWorkAdapterStopEmitsOneAtomicStoppedEvent(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	task, err := adapter.Create("stop", "description", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = adapter.DrainLifecycleEvents()
	if _, err := adapter.Stop(task.ID); err != nil {
		t.Fatal(err)
	}
	events := adapter.DrainLifecycleEvents()
	if len(events) != 1 ||
		events[0].Phase != tools.TaskLifecycleStopped ||
		events[0].Task == nil ||
		events[0].Task.Status != tools.TaskStatusKilled {
		t.Fatalf("stop lifecycle events = %+v", events)
	}
}

func TestLogicalWorkAdapterMarkedLoadIgnoresLegacyTodoSnapshot(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "session"},
		NewBoardID:  func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Create("cut over", "description", "", nil); err != nil {
		t.Fatal(err)
	}
	before := adapter.Snapshot()
	childScope := tools.TodoScope{
		SessionID: "session",
		AgentID:   "child",
	}
	reloaded, err := NewLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         dir,
		LeaderScope: childScope,
		LeaderTodos: []tools.TodoItem{{
			Content:    "stale process Todo",
			Status:     "pending",
			ActiveForm: "stale",
		}},
		NewBoardID: func() string { return "unused" },
	}, tools.TaskManagerSnapshot{NextID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if todos, err := reloaded.Todos(childScope); err != nil || len(todos) != 0 {
		t.Fatalf("marked load Todos = %+v err=%v", todos, err)
	}
	after := reloaded.Snapshot()
	stored, err := mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Record, stored.Record) ||
		!reflect.DeepEqual(after.Record, before.Record) {
		t.Fatalf(
			"marked load rewrote in-memory authority: before=%+v after=%+v stored=%+v",
			before,
			after,
			stored,
		)
	}
	childTodos := []tools.TodoItem{{
		Content:    "first authoritative child Todo",
		Status:     "pending",
		ActiveForm: "working child Todo",
	}}
	if err := reloaded.ReplaceTodos(childScope, childTodos); err != nil {
		t.Fatal(err)
	}
	stored, err = mustAuthorityStore(t, dir, "session").Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Snapshot().Record, stored.Record) {
		t.Fatal("first child Todo mutation did not commit the registered scope")
	}
	if todos, err := reloaded.Todos(childScope); err != nil ||
		!reflect.DeepEqual(todos, childTodos) {
		t.Fatalf("authoritative child Todos = %+v err=%v", todos, err)
	}
}

func TestLogicalWorkAdapterForkRequiresExactSourceAndFreshBoardID(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "source",
		Dir:         dir,
		LeaderScope: tools.TodoScope{SessionID: "source"},
		NewBoardID:  func() string { return "same-board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Create("cut over", "description", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.PrepareFork("other", "child", dir); err == nil {
		t.Fatal("mismatched fork source unexpectedly succeeded")
	}
	if _, err := adapter.PrepareFork("source", "child", dir); err == nil {
		t.Fatal("reused fork BoardID unexpectedly succeeded")
	}
	assertAuthorityArtifactsAbsent(t, dir, "child")
}

func fixedAdapterClock() func() time.Time {
	current := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	return func() time.Time {
		current = current.Add(time.Second)
		return current
	}
}

func mustAuthorityStore(t *testing.T, dir, sessionID string) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{Dir: dir, SessionID: sessionID})
	if err != nil {
		t.Fatalf("new authority store: %v", err)
	}
	return store
}

func assertAuthorityArtifactsAbsent(
	t *testing.T,
	dir string,
	sessionID string,
) {
	t.Helper()
	for _, suffix := range []string{
		AuthorityRecordSuffix,
		AuthorityMarkerSuffix,
		LegacyBackupSuffix,
	} {
		if _, err := os.Lstat(filepath.Join(dir, sessionID+suffix)); !errors.Is(
			err,
			os.ErrNotExist,
		) {
			t.Fatalf("artifact %s exists before mutation: %v", suffix, err)
		}
	}
}

func taskItemByLegacyID(t *testing.T, board Board, legacyID string) WorkItem {
	t.Helper()
	for _, item := range board.Items {
		if item.Source.Kind == "task" && item.Source.LegacyID == legacyID {
			return item
		}
	}
	t.Fatalf("Task WorkItem %q not found", legacyID)
	return WorkItem{}
}

func taskCompatibilityByID(
	t *testing.T,
	payload CompatibilityPayload,
	id string,
) TaskCompatibility {
	t.Helper()
	index := compatibilityTaskIndex(payload.Tasks, id)
	if index < 0 {
		t.Fatalf("Task compatibility %q not found", id)
	}
	return payload.Tasks[index]
}

func stringPointer(value string) *string {
	return &value
}
