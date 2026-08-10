package workboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/tools"
)

func TestCodecRoundTripAndStrictRejection(t *testing.T) {
	record := testRecord("session-codec")
	data, err := Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, record) {
		t.Fatalf("decoded record = %#v, want %#v", decoded, record)
	}
	decoded.Candidate.Items[0].Title = "detached"
	if record.Candidate.Items[0].Title == "detached" {
		t.Fatal("Decode returned aliased candidate state")
	}

	for name, mutate := range map[string]func([]byte) []byte{
		"corrupt": func([]byte) []byte {
			return []byte("{")
		},
		"unknown field": func(data []byte) []byte {
			return bytes.Replace(
				data,
				[]byte(`"version":1`),
				[]byte(`"version":1,"unknown":true`),
				1,
			)
		},
		"trailing value": func(data []byte) []byte {
			return append(data, []byte("{}")...)
		},
		"unknown version": func(data []byte) []byte {
			return bytes.Replace(
				data,
				[]byte(`"version":1`),
				[]byte(`"version":2`),
				1,
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(mutate(append([]byte(nil), data...)), record.SessionID); err == nil {
				t.Fatal("invalid record was accepted")
			}
		})
	}
	if _, err := Decode(data, "other-session"); err == nil ||
		!strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("ownership mismatch error = %v", err)
	}
}

func TestCodecRejectsDomainAndBudgetViolations(t *testing.T) {
	tests := map[string]func(*Record){
		"duplicate identity": func(record *Record) {
			record.Candidate.Items = append(
				record.Candidate.Items,
				record.Candidate.Items[0],
			)
		},
		"invalid status": func(record *Record) {
			record.Candidate.Items[0].Status = "deleted"
		},
		"missing dependency": func(record *Record) {
			record.Candidate.Items[0].Blocks = []string{"missing"}
		},
		"self dependency": func(record *Record) {
			record.Candidate.Items[0].Blocks = []string{"task:1"}
		},
		"cycle": func(record *Record) {
			record.Candidate.Items = append(record.Candidate.Items, WorkItem{
				ID:       "task:2",
				Revision: 1,
				Source: SourcePartition{
					Kind:     "task",
					LegacyID: "2",
				},
				Title:  "Second",
				Status: StatusPending,
				Blocks: []string{"task:1"},
			})
			record.Candidate.Items[0].Blocks = []string{"task:2"}
		},
		"field budget": func(record *Record) {
			record.Candidate.Items[0].Title = strings.Repeat("x", MaxFieldBytes+1)
		},
		"dependency budget": func(record *Record) {
			record.Candidate.Items[0].Blocks = make(
				[]string,
				MaxDependencyRefs+1,
			)
		},
		"diagnostic budget": func(record *Record) {
			record.Diagnostics = make([]Diagnostic, MaxDiagnostics+1)
			for index := range record.Diagnostics {
				record.Diagnostics[index] = Diagnostic{
					Sequence: uint64(index + 1),
					Kind:     "test",
					Message:  "test",
				}
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := testRecord("session-invalid")
			mutate(&record)
			if _, err := Encode(record); err == nil {
				t.Fatal("invalid record was encoded")
			}
		})
	}

	record := testRecord("session-items")
	record.Candidate.Items = make([]WorkItem, MaxItems+1)
	for index := range record.Candidate.Items {
		record.Candidate.Items[index] = WorkItem{
			ID:       "task:" + strconv.Itoa(index),
			Revision: 1,
			Source: SourcePartition{
				Kind:     "task",
				LegacyID: strconv.Itoa(index),
			},
			Title:  "item",
			Status: StatusPending,
		}
	}
	if _, err := Encode(record); err == nil {
		t.Fatal("item budget violation was encoded")
	}
}

func TestShadowTaskProjectionAndComparisonDiagnostics(t *testing.T) {
	dir := privateTempDir(t)
	shadow := NewShadow(Config{
		SessionID: "session-tasks",
		Dir:       dir,
		BoardID:   "board-tasks",
	})
	manager := tools.NewTaskManager()
	first := manager.Create("First", "description", "Working", map[string]any{
		"b": "two",
		"a": "one",
	})
	second := manager.Create("Second", "description", "", nil)
	shadow.ObserveTasks(manager.List())

	record := shadow.Record()
	if record.Candidate.Revision != 1 ||
		len(record.Candidate.Items) != 2 ||
		record.Candidate.Items[0].ID != "task:"+first.ID ||
		record.Candidate.Items[1].ID != "task:"+second.ID {
		t.Fatalf("initial candidate = %#v", record.Candidate)
	}
	if string(record.Candidate.Items[0].Metadata) != `{"a":"one","b":"two"}` {
		t.Fatalf("canonical metadata = %s", record.Candidate.Items[0].Metadata)
	}
	path, err := SidecarPath(dir, "session-tasks")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %#o", info.Mode().Perm())
	}

	if _, _, err := manager.Update(tools.TaskUpdate{
		TaskID:    first.ID,
		Status:    taskStatusPointer(tools.TaskStatusRunning),
		AddBlocks: []string{second.ID},
	}); err != nil {
		t.Fatal(err)
	}
	shadow.ObserveTasks(manager.List())
	record = shadow.Record()
	if record.Candidate.Revision != 2 ||
		record.Candidate.Items[0].Revision != 2 ||
		record.Candidate.Items[0].Status != StatusInProgress {
		t.Fatalf("updated candidate = %#v", record.Candidate)
	}

	previous := record.clone()
	if _, _, err := manager.Update(tools.TaskUpdate{
		TaskID:       first.ID,
		AddBlockedBy: []string{"missing"},
	}); err != nil {
		t.Fatal(err)
	}
	shadow.ObserveTasks(manager.List())
	record = shadow.Record()
	if !reflect.DeepEqual(record.Candidate, previous.Candidate) {
		t.Fatalf("invalid graph replaced candidate: %#v", record.Candidate)
	}
	if len(record.Diagnostics) == 0 ||
		record.Diagnostics[len(record.Diagnostics)-1].Kind != "comparison" {
		t.Fatalf("comparison diagnostics = %#v", record.Diagnostics)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data, "session-tasks"); err != nil {
		t.Fatalf("diagnostic fallback record is invalid: %v", err)
	}
}

func TestShadowTodoIdentityReplacementAndPartitioning(t *testing.T) {
	shadow := NewShadow(Config{
		SessionID: "session-todos",
		Dir:       privateTempDir(t),
		BoardID:   "board-todos",
	})
	rootScope := tools.WorkBoardTodoScope{SessionID: "session-todos"}
	childScope := tools.WorkBoardTodoScope{
		SessionID: "session-todos",
		AgentID:   "child",
	}
	shadow.ObserveTodos(rootScope, []tools.TodoItem{
		{Content: "One", ActiveForm: "Doing one", Status: "pending"},
		{Content: "Two", ActiveForm: "Doing two", Status: "in_progress"},
	})
	first := shadow.Record()
	rootIDs := todoIDs(first, rootScope)
	if len(rootIDs) != 2 || rootIDs[0] == rootIDs[1] {
		t.Fatalf("initial Todo IDs = %#v", rootIDs)
	}

	shadow.ObserveTodos(rootScope, []tools.TodoItem{
		{Content: "One", ActiveForm: "Doing one", Status: "in_progress"},
		{Content: "Three", ActiveForm: "Doing three", Status: "pending"},
	})
	second := shadow.Record()
	items := todoItems(second, rootScope)
	if len(items) != 3 {
		t.Fatalf("replacement items = %#v", items)
	}
	byTitle := make(map[string]WorkItem)
	for _, item := range items {
		byTitle[item.Title] = item
	}
	if byTitle["One"].ID != rootIDs[0] ||
		byTitle["One"].Revision != 2 ||
		byTitle["Two"].Status != StatusCancelled ||
		byTitle["Two"].TerminalReason != "legacy_full_replacement_omission" ||
		byTitle["Three"].ID == rootIDs[0] ||
		byTitle["Three"].ID == rootIDs[1] {
		t.Fatalf("replacement identity = %#v", byTitle)
	}

	shadow.ObserveTodos(childScope, []tools.TodoItem{{
		Content:    "Child",
		ActiveForm: "Doing child",
		Status:     "pending",
	}})
	partitioned := shadow.Record()
	if len(todoItems(partitioned, rootScope)) != 3 ||
		len(todoItems(partitioned, childScope)) != 1 {
		t.Fatalf("partitioned candidate = %#v", partitioned.Candidate)
	}

	shadow.ObserveTodos(rootScope, []tools.TodoItem{
		{Content: "Duplicate", ActiveForm: "Doing duplicate", Status: "pending"},
		{Content: "Duplicate", ActiveForm: "Doing duplicate", Status: "pending"},
	})
	duplicates := todoItems(shadow.Record(), rootScope)
	var duplicateIDs []string
	for _, item := range duplicates {
		if item.Title == "Duplicate" && item.Status != StatusCancelled {
			duplicateIDs = append(duplicateIDs, item.ID)
		}
	}
	if len(duplicateIDs) != 2 || duplicateIDs[0] == duplicateIDs[1] {
		t.Fatalf("duplicate Todo IDs = %#v", duplicateIDs)
	}
}

func TestShadowFailureStagesPreservePriorOrAbsentRecord(t *testing.T) {
	writerStages := []string{
		"encode",
		"mkdir",
		"create_temp",
		"chmod",
		"write",
		"sync",
		"close",
		"rename",
	}
	for _, stage := range writerStages {
		t.Run(stage, func(t *testing.T) {
			dir := privateTempDir(t)
			shadow := NewShadow(Config{
				SessionID: "session-failure-" + stage,
				Dir:       dir,
				BoardID:   "board-failure",
				BeforeStage: func(current string) error {
					if current == stage {
						return errors.New("injected " + stage)
					}
					return nil
				},
			})
			manager := tools.NewTaskManager()
			manager.Create("Task", "description", "", nil)
			shadow.ObserveTasks(manager.List())

			path, err := SidecarPath(dir, "session-failure-"+stage)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed %s created sidecar: %v", stage, err)
			}
			temps, err := filepath.Glob(
				filepath.Join(dir, "."+filepath.Base(path)+".tmp-*"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(temps) != 0 {
				t.Fatalf("failed %s left temp files: %#v", stage, temps)
			}
			diagnostics := shadow.Diagnostics()
			if len(diagnostics) == 0 ||
				diagnostics[len(diagnostics)-1].Stage != stage {
				t.Fatalf("%s diagnostics = %#v", stage, diagnostics)
			}
		})
	}

	dir := privateTempDir(t)
	failStage := ""
	shadow := NewShadow(Config{
		SessionID: "session-replacement",
		Dir:       dir,
		BoardID:   "board-replacement",
		BeforeStage: func(current string) error {
			if current == failStage {
				return errors.New("injected replacement failure")
			}
			return nil
		},
	})
	manager := tools.NewTaskManager()
	manager.Create("First", "description", "", nil)
	shadow.ObserveTasks(manager.List())
	path, err := SidecarPath(dir, "session-replacement")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manager.Create("Second", "description", "", nil)
	failStage = "rename"
	shadow.ObserveTasks(manager.List())
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed replacement changed prior complete record")
	}
	if len(shadow.Record().Candidate.Items) != 1 {
		t.Fatalf("failed replacement changed in-memory candidate: %#v", shadow.Record())
	}
}

func TestShadowExistingRecordValidationDoesNotRestore(t *testing.T) {
	loadStages := []string{"read", "decode", "identity", "version"}
	for _, stage := range loadStages {
		t.Run(stage, func(t *testing.T) {
			dir := privateTempDir(t)
			sessionID := "session-load-" + stage
			writeTestRecord(t, dir, testRecord(sessionID))
			shadow := NewShadow(Config{
				SessionID: sessionID,
				Dir:       dir,
				BoardID:   "new-board",
				BeforeStage: func(current string) error {
					if current == stage {
						return errors.New("injected " + stage)
					}
					return nil
				},
			})
			record := shadow.Record()
			if len(record.Candidate.Items) != 0 ||
				record.BoardID != "new-board" {
				t.Fatalf("existing record restored state: %#v", record)
			}
			if len(record.Diagnostics) != 1 ||
				record.Diagnostics[0].Stage != stage {
				t.Fatalf("%s diagnostics = %#v", stage, record.Diagnostics)
			}
		})
	}

	for name, mutate := range map[string]func(*Record){
		"identity": func(record *Record) {
			record.SessionID = "other"
		},
		"version": func(record *Record) {
			record.Version = CurrentVersion + 1
		},
	} {
		t.Run("invalid_"+name, func(t *testing.T) {
			dir := privateTempDir(t)
			record := testRecord("session-invalid-load")
			mutate(&record)
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			path, err := SidecarPath(dir, "session-invalid-load")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			shadow := NewShadow(Config{
				SessionID: "session-invalid-load",
				Dir:       dir,
				BoardID:   "new-board",
			})
			diagnostics := shadow.Diagnostics()
			if len(diagnostics) != 1 ||
				diagnostics[0].Stage != name {
				t.Fatalf("%s diagnostics = %#v", name, diagnostics)
			}
		})
	}
}

func TestShadowRejectsSymlinkAndBoundsDiagnostics(t *testing.T) {
	dir := privateTempDir(t)
	path, err := SidecarPath(dir, "session-symlink")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	shadow := NewShadow(Config{
		SessionID: "session-symlink",
		Dir:       dir,
		BoardID:   "board-symlink",
	})
	manager := tools.NewTaskManager()
	manager.Create("Task", "description", "", nil)
	shadow.ObserveTasks(manager.List())
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside target changed: data=%q err=%v", data, err)
	}

	bounded := NewShadow(Config{
		SessionID: "session-bounded-diagnostics",
		Dir:       privateTempDir(t),
		BoardID:   "board-bounded",
	})
	for index := 0; index < MaxDiagnostics+20; index++ {
		bounded.ObserveTodos(tools.WorkBoardTodoScope{}, nil)
	}
	diagnostics := bounded.Diagnostics()
	if len(diagnostics) != MaxDiagnostics {
		t.Fatalf("diagnostic count = %d", len(diagnostics))
	}
	if diagnostics[0].Sequence != 21 ||
		diagnostics[len(diagnostics)-1].Sequence != MaxDiagnostics+20 {
		t.Fatalf("diagnostic window = %#v", diagnostics)
	}
}

func TestShadowRejectsNonPrivateExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing-transcripts")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shadow := NewShadow(Config{
		SessionID: "session-public-dir",
		Dir:       dir,
		BoardID:   "board-public-dir",
	})
	manager := tools.NewTaskManager()
	manager.Create("Private", "must not be written", "", nil)
	shadow.ObserveTasks(manager.List())

	path, err := SidecarPath(dir, "session-public-dir")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-private directory received a sidecar: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("observer changed existing directory mode to %#o", info.Mode().Perm())
	}
	diagnostics := shadow.Diagnostics()
	if len(diagnostics) != 2 ||
		diagnostics[0].Kind != "load_failure" ||
		diagnostics[0].Stage != "read" ||
		diagnostics[1].Kind != "write_failure" ||
		diagnostics[1].Stage != "mkdir" {
		t.Fatalf("directory diagnostics = %#v", diagnostics)
	}
}

func TestShadowSerializesConcurrentObservers(t *testing.T) {
	dir := privateTempDir(t)
	shadow := NewShadow(Config{
		SessionID: "session-concurrent",
		Dir:       dir,
		BoardID:   "board-concurrent",
	})
	manager := tools.NewTaskManager()
	manager.Create("Task", "description", "", nil)

	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			shadow.ObserveTasks(manager.List())
		}()
		go func(index int) {
			defer wait.Done()
			shadow.ObserveTodos(
				tools.WorkBoardTodoScope{
					SessionID: "session-concurrent",
					AgentID:   "agent",
				},
				[]tools.TodoItem{{
					Content:    "Todo",
					ActiveForm: "Doing todo",
					Status:     "pending",
				}},
			)
		}(index)
	}
	wait.Wait()
	path, err := SidecarPath(dir, "session-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := Decode(data, "session-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Candidate.Items) != 2 {
		t.Fatalf("concurrent candidate = %#v", record.Candidate)
	}
}

func testRecord(sessionID string) Record {
	return Record{
		Version:   CurrentVersion,
		SessionID: sessionID,
		BoardID:   "board-test",
		Candidate: &Board{
			Revision: 1,
			Items: []WorkItem{{
				ID:       "task:1",
				Revision: 1,
				Source: SourcePartition{
					Kind:     "task",
					LegacyID: "1",
				},
				Title:  "First",
				Status: StatusPending,
			}},
		},
		Diagnostics: []Diagnostic{{
			Sequence: 1,
			Kind:     "test",
			Message:  "test",
		}},
	}
}

func writeTestRecord(t *testing.T, dir string, record Record) {
	t.Helper()
	path, err := SidecarPath(dir, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func todoItems(
	record Record,
	scope tools.WorkBoardTodoScope,
) []WorkItem {
	var result []WorkItem
	for _, item := range record.Candidate.Items {
		if item.Source.Kind == "todo" &&
			item.Source.SessionID == scope.SessionID &&
			item.Source.AgentID == scope.AgentID {
			result = append(result, item)
		}
	}
	return result
}

func todoIDs(record Record, scope tools.WorkBoardTodoScope) []string {
	items := todoItems(record, scope)
	result := make([]string, len(items))
	for index := range items {
		result[index] = items[index].ID
	}
	return result
}

func taskStatusPointer(status tools.TaskStatus) *tools.TaskStatus {
	return &status
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
