package workboard

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
)

func TestExecutionLinkUpgradeRepairsPreparedMarkerLastState(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	record := validAuthorityRecordFixture()
	store, err := NewStore(StoreConfig{Dir: dir, SessionID: record.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cutover(record, backupFromRecord(record)); err != nil {
		t.Fatal(err)
	}
	next := cloneAuthorityRecord(record)
	next.Version = AuthorityRecordVersionV3
	next.Board.Revision++
	next.ExecutionLinks = []ExecutionLink{testExecutionLink(record, "task:1", 1)}
	store.config.Failure = func(stage StoreStage) error {
		if stage == StoreStageMarkerEncode {
			return errors.New("stop before marker")
		}
		return nil
	}
	if _, err := store.UpgradeExecutionLinks(record.BoardID, record.Board.Revision, next); err == nil {
		t.Fatal("expected prepared upgrade failure")
	}
	store.config.Failure = nil
	state, err := store.Inspect()
	if err != nil {
		t.Fatalf("repair prepared upgrade: %v", err)
	}
	if state.Record.Version != AuthorityRecordVersionV3 || len(state.Record.ExecutionLinks) != 1 {
		t.Fatalf("repaired record = %+v", state.Record)
	}
	artifacts, err := store.artifacts(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := artifacts.Read(ArtifactMarker)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := DecodeAuthorityMarker(data, record.SessionID)
	if err != nil || marker.Version != AuthorityMarkerVersionV2 || marker.MinimumReader != MinimumReaderV3 {
		t.Fatalf("repaired marker = %+v, %v", marker, err)
	}
}

func TestExecutionLinkUpgradeFailureInventoryConverges(t *testing.T) {
	fileStages := []FailureStage{
		FailureCreate,
		FailureChmod,
		FailureWrite,
		FailureSync,
		FailureClose,
		FailureRename,
		FailureDirSync,
	}
	for _, kind := range []ArtifactKind{
		ArtifactAuthority,
		ArtifactMarker,
	} {
		for _, stage := range fileStages {
			t.Run(string(kind)+"/"+string(stage), func(t *testing.T) {
				dir := authorityPrivateTempDir(t)
				record := validAuthorityRecordFixture()
				store, err := NewStore(StoreConfig{
					Dir: dir, SessionID: record.SessionID,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Cutover(
					record,
					backupFromRecord(record),
				); err != nil {
					t.Fatal(err)
				}
				inject := true
				store.config.FileFailure = func(
					currentKind ArtifactKind,
					currentStage FailureStage,
				) error {
					if inject &&
						currentKind == kind &&
						currentStage == stage {
						inject = false
						return errors.New("stop")
					}
					return nil
				}
				next := executionLinkUpgradeFixture(record)
				if _, err := store.UpgradeExecutionLinks(
					record.BoardID,
					record.Board.Revision,
					next,
				); err == nil {
					t.Fatal("expected upgrade write failure")
				}
				state, err := store.Inspect()
				if err != nil {
					t.Fatalf("inspect failed upgrade: %v", err)
				}
				wantVersion := AuthorityRecordVersion
				wantLinks := 0
				if kind == ArtifactMarker {
					wantVersion = AuthorityRecordVersionV3
					wantLinks = 1
				}
				if state.Record.Version != wantVersion ||
					len(state.Record.ExecutionLinks) != wantLinks {
					t.Fatalf(
						"converged record = version %d links %d",
						state.Record.Version,
						len(state.Record.ExecutionLinks),
					)
				}
				assertNoArtifactTemps(t, dir)
			})
		}
	}

	for _, stage := range []StoreStage{
		StoreStageAuthorityEncode,
		StoreStageMarkerEncode,
		StoreStageMarkerReread,
	} {
		t.Run(string(stage), func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			record := validAuthorityRecordFixture()
			inject := true
			store, err := NewStore(StoreConfig{
				Dir: dir, SessionID: record.SessionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Cutover(
				record,
				backupFromRecord(record),
			); err != nil {
				t.Fatal(err)
			}
			store.config.Failure = func(current StoreStage) error {
				if inject && current == stage {
					inject = false
					return errors.New("stop")
				}
				return nil
			}
			if _, err := store.UpgradeExecutionLinks(
				record.BoardID,
				record.Board.Revision,
				executionLinkUpgradeFixture(record),
			); err == nil {
				t.Fatal("expected upgrade stage failure")
			}
			state, err := store.Inspect()
			if err != nil {
				t.Fatalf("inspect failed upgrade: %v", err)
			}
			wantVersion := AuthorityRecordVersionV3
			wantLinks := 1
			if stage == StoreStageAuthorityEncode {
				wantVersion = AuthorityRecordVersion
				wantLinks = 0
			}
			if state.Record.Version != wantVersion ||
				len(state.Record.ExecutionLinks) != wantLinks {
				t.Fatalf(
					"converged record = version %d links %d",
					state.Record.Version,
					len(state.Record.ExecutionLinks),
				)
			}
		})
	}
}

func TestAdapterExecutionLinkAdmissionAndTerminalSettlementGuard(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	first := manager.Create("task", "description", "", nil)
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID: "session", Dir: dir, LeaderScope: tools.TodoScope{SessionID: "session"},
		Clock: fixedAdapterClock(), NewBoardID: func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWithError("cutover", "", "", nil); err != nil {
		t.Fatal(err)
	}
	item := taskItemByLegacyID(t, adapter.record.Board, first.ID)
	request := AdmitExecutionLinkRequest{BoardID: adapter.record.BoardID, BoardRevision: adapter.record.Board.Revision, WorkItemID: item.ID, WorkItemRevision: item.Revision, AgentID: "child", Generation: 1, ParentSessionID: "session", ParentThreadID: "thread", ParentAgentID: "parent", ParentToolUseID: "tool", AdmittedAt: time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)}
	if err := adapter.AdmitExecutionLink(request); err != nil {
		t.Fatalf("admit link: %v", err)
	}
	if adapter.record.Version != AuthorityRecordVersionV3 || len(adapter.record.ExecutionLinks) != 1 {
		t.Fatalf("admitted record = %+v", adapter.record)
	}
	if err := adapter.AdmitExecutionLink(request); err != nil {
		t.Fatalf("idempotent admission: %v", err)
	}
	request.WorkItemID = "task:2"
	if err := adapter.AdmitExecutionLink(request); err == nil {
		t.Fatal("expected conflicting key rejection")
	}
	completed := tools.TaskStatusCompleted
	if _, _, err := manager.Update(tools.TaskUpdate{TaskID: first.ID, Status: &completed}); err == nil {
		t.Fatal("expected missing settlement callback rejection")
	}
	adapter.config.SettlementSnapshot = func(keys []ExecutionSettlementKey) ([]ExecutionSettlement, error) {
		return []ExecutionSettlement{{Key: keys[0], Settled: true}}, nil
	}
	if _, _, err := manager.Update(tools.TaskUpdate{TaskID: first.ID, Status: &completed}); err != nil {
		t.Fatalf("settled completion: %v", err)
	}
}

func TestP472TerminalSettlementUsesExactWorkItemLinks(t *testing.T) {
	t.Run("other item live does not block target", func(t *testing.T) {
		fixture := newP472SettlementFixture(t)
		p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 1)
		p472AdmitLink(t, fixture.adapter, fixture.secondItem, "agent-b", 1)

		var gotKeys []ExecutionSettlementKey
		fixture.adapter.config.SettlementSnapshot = func(
			keys []ExecutionSettlementKey,
		) ([]ExecutionSettlement, error) {
			gotKeys = append([]ExecutionSettlementKey(nil), keys...)
			settlements := make([]ExecutionSettlement, len(keys))
			for i, key := range keys {
				settlements[i] = ExecutionSettlement{Key: key}
				if key.AgentID == "agent-a" {
					settlements[i].Settled = true
				} else {
					settlements[i].Live = true
				}
			}
			return settlements, nil
		}

		completed := tools.TaskStatusCompleted
		if _, _, err := fixture.manager.Update(tools.TaskUpdate{
			TaskID: fixture.first.ID,
			Status: &completed,
		}); err != nil {
			t.Fatalf("complete settled first WorkItem: %v", err)
		}
		wantKeys := []ExecutionSettlementKey{{
			AgentID: "agent-a", Generation: 1,
		}}
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Fatalf("settlement keys = %+v, want %+v", gotKeys, wantKeys)
		}
	})

	t.Run("target live still blocks transition", func(t *testing.T) {
		fixture := newP472SettlementFixture(t)
		p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 1)
		p472AdmitLink(t, fixture.adapter, fixture.secondItem, "agent-b", 1)
		beforeRevision := fixture.adapter.record.Board.Revision
		fixture.adapter.config.SettlementSnapshot = func(
			keys []ExecutionSettlementKey,
		) ([]ExecutionSettlement, error) {
			settlements := make([]ExecutionSettlement, len(keys))
			for i, key := range keys {
				settlements[i] = ExecutionSettlement{Key: key, Settled: true}
				if key.AgentID == "agent-a" {
					settlements[i].Settled = false
					settlements[i].Live = true
				}
			}
			return settlements, nil
		}

		completed := tools.TaskStatusCompleted
		if _, _, err := fixture.manager.Update(tools.TaskUpdate{
			TaskID: fixture.first.ID,
			Status: &completed,
		}); err == nil {
			t.Fatal("live target execution allowed terminal transition")
		}
		state, err := fixture.adapter.store.Inspect()
		if err != nil {
			t.Fatalf("inspect rejected transition: %v", err)
		}
		first := taskItemByLegacyID(t, state.Record.Board, fixture.first.ID)
		if state.Record.Board.Revision != beforeRevision ||
			isTerminalStatus(first.Status) {
			t.Fatalf("rejected transition committed: %+v", state.Record)
		}
	})
}

func TestP472TerminalSettlementPreservesFailClosedAndCommitSemantics(
	t *testing.T,
) {
	t.Run("all target generations settle in durable order", func(t *testing.T) {
		fixture := newP472SettlementFixture(t)
		p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 1)
		p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 2)
		p472AdmitLink(t, fixture.adapter, fixture.secondItem, "agent-b", 1)
		beforeRevision := fixture.adapter.record.Board.Revision

		var gotKeys []ExecutionSettlementKey
		fixture.adapter.config.SettlementSnapshot = func(
			keys []ExecutionSettlementKey,
		) ([]ExecutionSettlement, error) {
			gotKeys = append([]ExecutionSettlementKey(nil), keys...)
			return []ExecutionSettlement{
				{Key: keys[1], Settled: true},
				{Key: keys[0], Settled: true},
			}, nil
		}
		completed := tools.TaskStatusCompleted
		if _, _, err := fixture.manager.Update(tools.TaskUpdate{
			TaskID: fixture.first.ID,
			Status: &completed,
		}); err != nil {
			t.Fatalf("complete all settled generations: %v", err)
		}
		wantKeys := []ExecutionSettlementKey{
			{AgentID: "agent-a", Generation: 1},
			{AgentID: "agent-a", Generation: 2},
		}
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Fatalf("settlement keys = %+v, want %+v", gotKeys, wantKeys)
		}
		state, err := fixture.adapter.store.Inspect()
		if err != nil {
			t.Fatalf("inspect completed transition: %v", err)
		}
		first := taskItemByLegacyID(t, state.Record.Board, fixture.first.ID)
		if state.Record.Board.Revision != beforeRevision+1 ||
			first.Status != StatusCompleted ||
			len(state.Record.ExecutionLinks) != 3 {
			t.Fatalf("completed authority = %+v", state.Record)
		}
	})

	for _, testCase := range []struct {
		name       string
		settlement func([]ExecutionSettlementKey) []ExecutionSettlement
	}{
		{
			name: "missing target generation",
			settlement: func(keys []ExecutionSettlementKey) []ExecutionSettlement {
				return []ExecutionSettlement{{Key: keys[0], Settled: true}}
			},
		},
		{
			name: "target cancellation pending",
			settlement: func(keys []ExecutionSettlementKey) []ExecutionSettlement {
				return []ExecutionSettlement{
					{Key: keys[0], Settled: true},
					{Key: keys[1], CancelPending: true},
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newP472SettlementFixture(t)
			p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 1)
			p472AdmitLink(t, fixture.adapter, fixture.firstItem, "agent-a", 2)
			p472AdmitLink(t, fixture.adapter, fixture.secondItem, "agent-b", 1)
			before := fixture.adapter.record
			fixture.adapter.config.SettlementSnapshot = func(
				keys []ExecutionSettlementKey,
			) ([]ExecutionSettlement, error) {
				return testCase.settlement(keys), nil
			}
			completed := tools.TaskStatusCompleted
			if _, _, err := fixture.manager.Update(tools.TaskUpdate{
				TaskID: fixture.first.ID,
				Status: &completed,
			}); err == nil {
				t.Fatal("unsettled target execution allowed terminal transition")
			}
			state, err := fixture.adapter.store.Inspect()
			if err != nil {
				t.Fatalf("inspect rejected transition: %v", err)
			}
			first := taskItemByLegacyID(t, state.Record.Board, fixture.first.ID)
			if state.Record.Board.Revision != before.Board.Revision ||
				isTerminalStatus(first.Status) ||
				!reflect.DeepEqual(
					state.Record.ExecutionLinks,
					before.ExecutionLinks,
				) {
				t.Fatalf("rejected transition committed: %+v", state.Record)
			}
		})
	}
}

func TestP472ReplaceTodosValidatesEveryTerminalizedItem(t *testing.T) {
	scope := tools.TodoScope{SessionID: "session"}
	pending := []tools.TodoItem{
		{Content: "first", Status: "pending", ActiveForm: "doing first"},
		{Content: "second", Status: "pending", ActiveForm: "doing second"},
	}
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID:   "session",
		Dir:         authorityPrivateTempDir(t),
		LeaderScope: scope,
		LeaderTodos: pending,
		Clock:       fixedAdapterClock(),
		NewBoardID:  func() string { return "board" },
	}, tools.NewTaskManager())
	if err != nil {
		t.Fatalf("bind Todo adapter: %v", err)
	}
	if err := adapter.ReplaceTodos(scope, pending); err != nil {
		t.Fatalf("cut over Todo authority: %v", err)
	}
	first := p472TodoItemByTitle(t, adapter.record.Board, "first")
	second := p472TodoItemByTitle(t, adapter.record.Board, "second")
	p472AdmitLink(t, adapter, first, "agent-a", 1)
	p472AdmitLink(t, adapter, second, "agent-b", 1)
	beforeRevision := adapter.record.Board.Revision

	var gotKeys []ExecutionSettlementKey
	adapter.config.SettlementSnapshot = func(
		keys []ExecutionSettlementKey,
	) ([]ExecutionSettlement, error) {
		gotKeys = append([]ExecutionSettlementKey(nil), keys...)
		return []ExecutionSettlement{
			{Key: keys[0], Settled: true},
			{Key: keys[1], Live: true},
		}, nil
	}
	completed := []tools.TodoItem{
		{Content: "first", Status: "completed", ActiveForm: "doing first"},
		{Content: "second", Status: "completed", ActiveForm: "doing second"},
	}
	if err := adapter.ReplaceTodos(scope, completed); err == nil {
		t.Fatal("live second WorkItem allowed batch terminal transition")
	}
	wantKeys := []ExecutionSettlementKey{
		{AgentID: "agent-a", Generation: 1},
		{AgentID: "agent-b", Generation: 1},
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("batch settlement keys = %+v, want %+v", gotKeys, wantKeys)
	}
	if adapter.record.Board.Revision != beforeRevision {
		t.Fatalf("rejected batch revision = %d, want %d", adapter.record.Board.Revision, beforeRevision)
	}

	adapter.config.SettlementSnapshot = func(
		keys []ExecutionSettlementKey,
	) ([]ExecutionSettlement, error) {
		return []ExecutionSettlement{
			{Key: keys[1], Settled: true},
			{Key: keys[0], Settled: true},
		}, nil
	}
	if err := adapter.ReplaceTodos(scope, completed); err != nil {
		t.Fatalf("complete settled Todo WorkItems: %v", err)
	}
}

func TestP472ExecutionLinkAdmissionPreservesBoardAndItemIdentity(t *testing.T) {
	fixture := newP472SettlementFixture(t)
	baseline := fixture.adapter.record.Board.Revision
	for _, testCase := range []struct {
		name   string
		mutate func(*AdmitExecutionLinkRequest)
	}{
		{
			name: "other board",
			mutate: func(request *AdmitExecutionLinkRequest) {
				request.BoardID = "other-board"
			},
		},
		{
			name: "unknown WorkItem",
			mutate: func(request *AdmitExecutionLinkRequest) {
				request.WorkItemID = "missing-item"
			},
		},
		{
			name: "stale board revision",
			mutate: func(request *AdmitExecutionLinkRequest) {
				request.BoardRevision--
			},
		},
		{
			name: "stale WorkItem revision",
			mutate: func(request *AdmitExecutionLinkRequest) {
				request.WorkItemRevision++
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := p472LinkRequest(
				fixture.adapter,
				fixture.firstItem,
				"agent-invalid",
				1,
			)
			testCase.mutate(&request)
			if err := fixture.adapter.AdmitExecutionLink(request); err == nil {
				t.Fatal("invalid execution-link identity was admitted")
			}
			if fixture.adapter.record.Board.Revision != baseline ||
				len(fixture.adapter.record.ExecutionLinks) != 0 {
				t.Fatalf("invalid admission mutated authority: %+v", fixture.adapter.record)
			}
		})
	}
}

type p472SettlementFixture struct {
	manager    *tools.TaskManager
	adapter    *LogicalWorkAdapter
	first      *tools.TaskRecord
	second     *tools.TaskRecord
	firstItem  WorkItem
	secondItem WorkItem
}

func newP472SettlementFixture(t *testing.T) p472SettlementFixture {
	t.Helper()
	manager := tools.NewTaskManager()
	first := manager.Create("first", "first description", "", nil)
	second := manager.Create("second", "second description", "", nil)
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID: "session",
		Dir:       authorityPrivateTempDir(t),
		LeaderScope: tools.TodoScope{
			SessionID: "session",
		},
		Clock:      fixedAdapterClock(),
		NewBoardID: func() string { return "board" },
	}, manager)
	if err != nil {
		t.Fatalf("bind logical work adapter: %v", err)
	}
	if _, err := manager.CreateWithError("cutover", "", "", nil); err != nil {
		t.Fatalf("cut over WorkBoard authority: %v", err)
	}
	return p472SettlementFixture{
		manager:    manager,
		adapter:    adapter,
		first:      first,
		second:     second,
		firstItem:  taskItemByLegacyID(t, adapter.record.Board, first.ID),
		secondItem: taskItemByLegacyID(t, adapter.record.Board, second.ID),
	}
}

func p472AdmitLink(
	t *testing.T,
	adapter *LogicalWorkAdapter,
	item WorkItem,
	agentID string,
	generation uint64,
) {
	t.Helper()
	err := adapter.AdmitExecutionLink(p472LinkRequest(
		adapter,
		item,
		agentID,
		generation,
	))
	if err != nil {
		t.Fatalf("admit %s@g%d: %v", agentID, generation, err)
	}
}

func p472LinkRequest(
	adapter *LogicalWorkAdapter,
	item WorkItem,
	agentID string,
	generation uint64,
) AdmitExecutionLinkRequest {
	return AdmitExecutionLinkRequest{
		BoardID:          adapter.record.BoardID,
		BoardRevision:    adapter.record.Board.Revision,
		WorkItemID:       item.ID,
		WorkItemRevision: item.Revision,
		AgentID:          agentID,
		Generation:       generation,
		ParentSessionID:  "session",
		ParentThreadID:   "thread",
		ParentAgentID:    "parent",
		ParentToolUseID:  fmt.Sprintf("tool-%s-%d", agentID, generation),
		AdmittedAt:       time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
	}
}

func p472TodoItemByTitle(t *testing.T, board Board, title string) WorkItem {
	t.Helper()
	for _, item := range board.Items {
		if item.Source.Kind == "todo" && item.Title == title {
			return item
		}
	}
	t.Fatalf("Todo WorkItem %q not found", title)
	return WorkItem{}
}

func TestExecutionLinksRejectDestructiveRecoveryAndDoNotFork(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	manager := tools.NewTaskManager()
	first := manager.Create("task", "description", "", nil)
	adapter, err := BindLogicalWorkAdapter(AdapterConfig{
		SessionID: "source",
		Dir:       dir,
		LeaderScope: tools.TodoScope{
			SessionID: "source",
		},
		Clock:      fixedAdapterClock(),
		NewBoardID: func() string { return "source-board" },
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWithError("cutover", "", "", nil); err != nil {
		t.Fatal(err)
	}
	item := taskItemByLegacyID(t, adapter.record.Board, first.ID)
	if err := adapter.AdmitExecutionLink(AdmitExecutionLinkRequest{
		BoardID:          adapter.record.BoardID,
		BoardRevision:    adapter.record.Board.Revision,
		WorkItemID:       item.ID,
		WorkItemRevision: item.Revision,
		AgentID:          "child",
		Generation:       1,
		ParentSessionID:  "source",
		ParentThreadID:   "thread",
		ParentToolUseID:  "tool",
		AdmittedAt: time.Date(
			2026, 7, 31, 1, 0, 0, 0, time.UTC,
		),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Recover(RecoveryRequest{
		SessionID:           "source",
		BoardID:             adapter.record.BoardID,
		Revision:            adapter.record.Board.Revision,
		AcknowledgeDataLoss: true,
	}); err == nil {
		t.Fatal("linked destructive recovery succeeded")
	}

	adapter.config.NewBoardID = func() string { return "child-board" }
	fork, err := adapter.PrepareFork("source", "child-session", dir)
	if err != nil {
		t.Fatalf("prepare fork: %v", err)
	}
	if fork.Record.Version != AuthorityRecordVersion ||
		len(fork.Record.ExecutionLinks) != 0 {
		t.Fatalf("fork copied execution relations: %+v", fork.Record)
	}
}

func TestExecutionLinkValidationRetainsFieldAndReaderFloorLimits(t *testing.T) {
	record := validAuthorityRecordFixture()
	record.Version = AuthorityRecordVersionV3
	record.ExecutionLinks = []ExecutionLink{
		testExecutionLink(record, "task:1", 1),
	}
	record.ExecutionLinks[0].AgentID = string(
		make([]byte, MaxFieldBytes+1),
	)
	if _, err := EncodeAuthorityRecord(record); err == nil {
		t.Fatal("oversized execution-link field encoded")
	}

	dir := authorityPrivateTempDir(t)
	store, err := NewStore(StoreConfig{
		Dir: dir, SessionID: record.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := validAuthorityRecordFixture()
	if _, err := store.Cutover(base, backupFromRecord(base)); err != nil {
		t.Fatal(err)
	}
	artifacts, err := store.artifacts(false)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := EncodeAuthorityMarker(AuthorityMarker{
		Version:       AuthorityMarkerVersionV2,
		SessionID:     base.SessionID,
		MinimumReader: MinimumReaderV3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Write(ArtifactMarker, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(); err == nil {
		t.Fatal("marker-v2/record-v2 pairing was accepted")
	}
}

func testExecutionLink(record AuthorityRecord, itemID string, revision uint64) ExecutionLink {
	return ExecutionLink{BoardID: record.BoardID, WorkItemID: itemID, WorkItemRevision: revision, AgentID: "child", Generation: 1, Actor: "agent_launch_admission", ParentSessionID: record.SessionID, ParentThreadID: "thread", ParentAgentID: "parent", ParentToolUseID: "tool", AdmittedAt: time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)}
}

func executionLinkUpgradeFixture(record AuthorityRecord) AuthorityRecord {
	next := cloneAuthorityRecord(record)
	next.Version = AuthorityRecordVersionV3
	next.Board.Revision++
	next.ExecutionLinks = []ExecutionLink{
		testExecutionLink(record, "task:1", 1),
	}
	return next
}
