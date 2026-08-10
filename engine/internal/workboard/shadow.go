package workboard

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/tools"
)

type Config struct {
	SessionID   string
	Dir         string
	BoardID     string
	BeforeStage func(string) error
}

type Shadow struct {
	mu               sync.Mutex
	config           Config
	path             string
	record           Record
	nextDiag         uint64
	configurationErr error
}

func NewShadow(config Config) *Shadow {
	boardID := strings.TrimSpace(config.BoardID)
	if boardID == "" {
		boardID = newBoardID(config.SessionID)
	}
	path, pathErr := SidecarPath(config.Dir, config.SessionID)
	shadow := &Shadow{
		config:           config,
		path:             path,
		configurationErr: pathErr,
		record: Record{
			Version:   CurrentVersion,
			SessionID: config.SessionID,
			BoardID:   boardID,
			Candidate: &Board{},
		},
	}
	if pathErr != nil {
		shadow.addDiagnosticLocked("configuration", "", "", pathErr)
		return shadow
	}
	shadow.inspectExistingLocked()
	return shadow
}

func SidecarPath(dir, sessionID string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("workboard: sidecar directory is empty")
	}
	if !validSessionID(sessionID) {
		return "", fmt.Errorf("workboard: invalid session identity")
	}
	return filepath.Join(
		filepath.Clean(dir),
		sessionID+".workboard-shadow-v1.json",
	), nil
}

func (s *Shadow) Diagnostics() []Diagnostic {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Diagnostic(nil), s.record.Diagnostics...)
}

func (s *Shadow) Record() Record {
	if s == nil {
		return Record{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record.clone()
}

func (s *Shadow) ObserveTasks(tasks []*tools.TaskRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configurationErr != nil {
		return
	}
	candidate, err := s.taskCandidateLocked(tasks)
	if err != nil {
		s.recordComparisonFailureLocked("tasks", "", err)
		return
	}
	s.persistCandidateLocked(candidate, "tasks")
}

func (s *Shadow) ObserveTodos(
	scope tools.WorkBoardTodoScope,
	todos []tools.TodoItem,
) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configurationErr != nil {
		return
	}
	candidate, err := s.todoCandidateLocked(scope, todos)
	if err != nil {
		s.recordComparisonFailureLocked("todos", "", err)
		return
	}
	s.persistCandidateLocked(candidate, "todos")
}

func (s *Shadow) taskCandidateLocked(
	tasks []*tools.TaskRecord,
) (Board, error) {
	candidate := cloneBoard(*s.record.Candidate)
	prior := make(map[string]WorkItem)
	retained := make([]WorkItem, 0, len(candidate.Items)+len(tasks))
	for _, item := range candidate.Items {
		if item.Source.Kind == "task" {
			prior[item.Source.LegacyID] = item
			continue
		}
		retained = append(retained, item)
	}
	seen := make(map[string]struct{}, len(tasks))
	for order, task := range tasks {
		if task == nil {
			continue
		}
		legacyID := strings.TrimSpace(task.ID)
		if legacyID == "" {
			return Board{}, fmt.Errorf("task snapshot contains an empty identity")
		}
		if _, duplicate := seen[legacyID]; duplicate {
			return Board{}, fmt.Errorf("task snapshot repeats identity %q", legacyID)
		}
		seen[legacyID] = struct{}{}
		metadata, err := canonicalMetadata(task.Metadata)
		if err != nil {
			return Board{}, fmt.Errorf("task %q metadata: %w", legacyID, err)
		}
		item := WorkItem{
			ID:            taskWorkItemID(legacyID),
			Revision:      1,
			Source:        SourcePartition{Kind: "task", LegacyID: legacyID},
			Order:         order,
			Title:         task.Subject,
			Description:   task.Description,
			ActiveForm:    task.ActiveForm,
			Status:        taskStatus(task.Status),
			Owner:         task.Owner,
			Blocks:        taskDependencyIDs(task.Blocks),
			BlockedBy:     taskDependencyIDs(task.BlockedBy),
			Metadata:      metadata,
			ResultSummary: task.Output,
		}
		if task.Status == tools.TaskStatusKilled {
			item.TerminalReason = "legacy_task_stopped"
		}
		if previous, exists := prior[legacyID]; exists {
			item.Revision = previous.Revision
			if !sameWorkItemContent(previous, item) {
				item.Revision++
			}
		}
		retained = append(retained, item)
	}
	candidate.Items = retained
	normalizeItemOrder(candidate.Items)
	return candidate, nil
}

func (s *Shadow) todoCandidateLocked(
	scope tools.WorkBoardTodoScope,
	todos []tools.TodoItem,
) (Board, error) {
	if strings.TrimSpace(scope.SessionID) == "" {
		return Board{}, fmt.Errorf("trusted Todo scope has an empty session identity")
	}
	candidate := cloneBoard(*s.record.Candidate)
	partition := SourcePartition{
		Kind:      "todo",
		SessionID: scope.SessionID,
		AgentID:   scope.AgentID,
	}
	prior := make([]WorkItem, 0)
	retained := make([]WorkItem, 0, len(candidate.Items)+len(todos))
	usedIDs := make(map[string]struct{}, len(candidate.Items)+len(todos))
	for _, item := range candidate.Items {
		usedIDs[item.ID] = struct{}{}
		if sameTodoPartition(item.Source, partition) {
			prior = append(prior, item)
			continue
		}
		retained = append(retained, item)
	}

	priorByKey := make(map[string][]WorkItem)
	for _, item := range prior {
		key := todoContentKey(item.Title, item.ActiveForm)
		priorByKey[key] = append(priorByKey[key], item)
	}
	incomingCounts := make(map[string]int)
	for _, todo := range todos {
		incomingCounts[todoContentKey(todo.Content, todo.ActiveForm)]++
	}
	matched := make(map[string]struct{})
	for order, todo := range todos {
		key := todoContentKey(todo.Content, todo.ActiveForm)
		var previous WorkItem
		hasPrevious := len(priorByKey[key]) == 1 && incomingCounts[key] == 1
		if hasPrevious {
			previous = priorByKey[key][0]
			matched[previous.ID] = struct{}{}
		}
		status := Status(todo.Status)
		if !validStatus(status) {
			return Board{}, fmt.Errorf(
				"todo snapshot contains invalid status %q",
				todo.Status,
			)
		}
		if hasPrevious &&
			isTerminalStatus(previous.Status) &&
			!isTerminalStatus(status) {
			return Board{}, fmt.Errorf(
				"todo item %q reopens terminal state without an explicit reason",
				previous.ID,
			)
		}
		item := WorkItem{
			ID:         previous.ID,
			Revision:   previous.Revision,
			Source:     partition,
			Order:      order,
			Title:      todo.Content,
			ActiveForm: todo.ActiveForm,
			Status:     status,
		}
		if !hasPrevious {
			item.ID = nextTodoWorkItemID(&candidate, usedIDs)
			item.Revision = 1
			usedIDs[item.ID] = struct{}{}
		} else if !sameWorkItemContent(previous, item) {
			item.Revision++
		}
		retained = append(retained, item)
	}
	for _, previous := range prior {
		if _, ok := matched[previous.ID]; ok {
			continue
		}
		item := cloneWorkItem(previous)
		if !isTerminalStatus(item.Status) {
			item.Status = StatusCancelled
			item.TerminalReason = "legacy_full_replacement_omission"
			item.Revision++
		}
		retained = append(retained, item)
	}
	candidate.Items = retained
	normalizeItemOrder(candidate.Items)
	return candidate, nil
}

func (s *Shadow) persistCandidateLocked(candidate Board, source string) {
	current := cloneBoard(*s.record.Candidate)
	candidate.Revision = current.Revision
	if !reflect.DeepEqual(candidate, current) {
		candidate.Revision++
	}
	next := s.record.clone()
	next.Candidate = &candidate
	if err := validateRecord(next, s.config.SessionID); err != nil {
		s.recordComparisonFailureLocked(source, "", err)
		return
	}
	if reflect.DeepEqual(candidate, current) &&
		reflect.DeepEqual(next.Diagnostics, s.record.Diagnostics) {
		return
	}
	if err := s.writeRecordLocked(next); err != nil {
		s.addDiagnosticLocked("write_failure", failureStage(err), "", err)
		return
	}
	s.record = next
}

func (s *Shadow) recordComparisonFailureLocked(
	source string,
	itemID string,
	err error,
) {
	s.addDiagnosticLocked("comparison", source, itemID, err)
	next := s.record.clone()
	next.Diagnostics = append([]Diagnostic(nil), s.record.Diagnostics...)
	if writeErr := s.writeRecordLocked(next); writeErr != nil {
		s.addDiagnosticLocked(
			"write_failure",
			failureStage(writeErr),
			itemID,
			writeErr,
		)
		return
	}
	s.record = next
}

func (s *Shadow) writeRecordLocked(record Record) error {
	if err := s.beforeStageLocked("encode"); err != nil {
		return err
	}
	data, err := Encode(record)
	if err != nil {
		return stageError("encode", err)
	}
	return s.writeAtomicLocked(data)
}

func (s *Shadow) writeAtomicLocked(data []byte) (returnErr error) {
	if err := s.beforeStageLocked("mkdir"); err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return stageError("mkdir", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return stageError("mkdir", err)
	}
	if err := validatePrivateDirectoryInfo(dirInfo); err != nil {
		return stageError("mkdir", err)
	}
	if targetInfo, lstatErr := os.Lstat(s.path); lstatErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
			return stageError(
				"create_temp",
				errors.New("sidecar target is not a regular file"),
			)
		}
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		return stageError("create_temp", lstatErr)
	}

	if err := s.beforeStageLocked("create_temp"); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return stageError("create_temp", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			returnErr = errors.Join(returnErr, temp.Close())
		}
		if tempPath != "" {
			if removeErr := os.Remove(tempPath); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()

	if err := s.beforeStageLocked("chmod"); err != nil {
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		return stageError("chmod", err)
	}
	if err := s.beforeStageLocked("write"); err != nil {
		return err
	}
	if err := writeFull(temp, data); err != nil {
		return stageError("write", err)
	}
	if err := s.beforeStageLocked("sync"); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return stageError("sync", err)
	}
	if err := s.beforeStageLocked("close"); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return stageError("close", err)
	}
	temp = nil
	if err := s.beforeStageLocked("rename"); err != nil {
		return err
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return stageError("rename", err)
	}
	tempPath = ""
	return nil
}

func (s *Shadow) inspectExistingLocked() {
	dirInfo, err := os.Lstat(filepath.Dir(s.path))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.addDiagnosticLocked("load_failure", "read", "", err)
		}
		return
	}
	if err := validatePrivateDirectoryInfo(dirInfo); err != nil {
		s.addDiagnosticLocked("load_failure", "read", "", err)
		return
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.addDiagnosticLocked("load_failure", "read", "", err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		s.addDiagnosticLocked(
			"load_failure",
			"read",
			"",
			errors.New("sidecar is not a regular file"),
		)
		return
	}
	if err := s.beforeStageLocked("read"); err != nil {
		s.addDiagnosticLocked("load_failure", "read", "", err)
		return
	}
	data, err := readBounded(s.path)
	if err != nil {
		s.addDiagnosticLocked("load_failure", "read", "", err)
		return
	}
	if err := s.beforeStageLocked("decode"); err != nil {
		s.addDiagnosticLocked("load_failure", "decode", "", err)
		return
	}
	record, err := decodeRecord(data)
	if err != nil {
		s.addDiagnosticLocked("load_failure", "decode", "", err)
		return
	}
	if err := s.beforeStageLocked("identity"); err != nil {
		s.addDiagnosticLocked("load_failure", "identity", "", err)
		return
	}
	if record.SessionID != s.config.SessionID ||
		strings.TrimSpace(record.BoardID) == "" {
		s.addDiagnosticLocked(
			"load_failure",
			"identity",
			"",
			errors.New("sidecar identity does not match the current session"),
		)
		return
	}
	if err := s.beforeStageLocked("version"); err != nil {
		s.addDiagnosticLocked("load_failure", "version", "", err)
		return
	}
	if record.Version != CurrentVersion {
		s.addDiagnosticLocked(
			"load_failure",
			"version",
			"",
			fmt.Errorf("unsupported sidecar version %d", record.Version),
		)
		return
	}
	if err := validateRecord(record, s.config.SessionID); err != nil {
		s.addDiagnosticLocked("load_failure", "decode", "", err)
	}
	// P31.1a deliberately does not seed the live candidate from this record.
}

func (s *Shadow) beforeStageLocked(stage string) error {
	if s.config.BeforeStage == nil {
		return nil
	}
	if err := s.config.BeforeStage(stage); err != nil {
		return stageError(stage, err)
	}
	return nil
}

func (s *Shadow) addDiagnosticLocked(
	kind string,
	stage string,
	itemID string,
	err error,
) {
	s.nextDiag++
	message := strings.TrimSpace(err.Error())
	if len(message) > MaxFieldBytes {
		message = message[:MaxFieldBytes]
	}
	s.record.Diagnostics = append(s.record.Diagnostics, Diagnostic{
		Sequence: s.nextDiag,
		Kind:     kind,
		Stage:    stage,
		ItemID:   itemID,
		Message:  message,
	})
	if overflow := len(s.record.Diagnostics) - MaxDiagnostics; overflow > 0 {
		s.record.Diagnostics = append(
			[]Diagnostic(nil),
			s.record.Diagnostics[overflow:]...,
		)
	}
}

type shadowStageError struct {
	stage string
	err   error
}

func (e *shadowStageError) Error() string {
	return fmt.Sprintf("workboard shadow %s: %v", e.stage, e.err)
}

func (e *shadowStageError) Unwrap() error { return e.err }

func stageError(stage string, err error) error {
	return &shadowStageError{stage: stage, err: err}
}

func failureStage(err error) string {
	var stageErr *shadowStageError
	if errors.As(err, &stageErr) {
		return stageErr.stage
	}
	return ""
}

func validSessionID(sessionID string) bool {
	if sessionID == "" ||
		sessionID == "." ||
		sessionID == ".." ||
		len(sessionID) > MaxFieldBytes {
		return false
	}
	return filepath.Base(sessionID) == sessionID &&
		!strings.ContainsAny(sessionID, `/\`+"\x00")
}

func newBoardID(sessionID string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	fallback := sha256.Sum256(
		[]byte(sessionID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)),
	)
	return hex.EncodeToString(fallback[:16])
}

func canonicalMetadata(metadata map[string]any) (json.RawMessage, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func taskStatus(status tools.TaskStatus) Status {
	switch status {
	case tools.TaskStatusPending:
		return StatusPending
	case tools.TaskStatusInProgress, tools.TaskStatusRunning:
		return StatusInProgress
	case tools.TaskStatusCompleted:
		return StatusCompleted
	case tools.TaskStatusFailed:
		return StatusFailed
	case tools.TaskStatusKilled:
		return StatusCancelled
	default:
		return Status(status)
	}
}

func taskWorkItemID(legacyID string) string {
	return "task:" + legacyID
}

func taskDependencyIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, taskWorkItemID(id))
	}
	return result
}

func sameTodoPartition(left, right SourcePartition) bool {
	return left.Kind == right.Kind &&
		left.SessionID == right.SessionID &&
		left.AgentID == right.AgentID
}

func todoContentKey(content, activeForm string) string {
	return content + "\x00" + activeForm
}

func nextTodoWorkItemID(board *Board, used map[string]struct{}) string {
	for {
		board.NextTodoID++
		id := "todo:" + strconv.FormatUint(board.NextTodoID, 10)
		if _, exists := used[id]; !exists {
			return id
		}
	}
}

func isTerminalStatus(status Status) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func sameWorkItemContent(left, right WorkItem) bool {
	left = cloneWorkItem(left)
	right = cloneWorkItem(right)
	left.Revision = 0
	right.Revision = 0
	return reflect.DeepEqual(left, right)
}

func normalizeItemOrder(items []WorkItem) {
	sort.SliceStable(items, func(left, right int) bool {
		a, b := items[left], items[right]
		if a.Source.Kind != b.Source.Kind {
			return a.Source.Kind < b.Source.Kind
		}
		if a.Source.SessionID != b.Source.SessionID {
			return a.Source.SessionID < b.Source.SessionID
		}
		if a.Source.AgentID != b.Source.AgentID {
			return a.Source.AgentID < b.Source.AgentID
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.ID < b.ID
	})
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxEncodedJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxEncodedJSONBytes {
		return nil, fmt.Errorf(
			"workboard: encoded record exceeds %d bytes",
			MaxEncodedJSONBytes,
		)
	}
	return data, nil
}

func validatePrivateDirectoryInfo(info os.FileInfo) error {
	if info == nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() {
		return errors.New("sidecar directory is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("sidecar directory permissions are not private")
	}
	return nil
}
