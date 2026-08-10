package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
)

const p390SnapshotVersion = 1

type p390FileState struct {
	Exists  bool        `json:"exists"`
	Mode    fs.FileMode `json:"mode"`
	Content []byte      `json:"content,omitempty"`
	Digest  string      `json:"digest"`
}

type p390Entry struct {
	Path   string         `json:"path"`
	Before p390FileState  `json:"before"`
	After  *p390FileState `json:"after,omitempty"`
}

type p390Snapshot struct {
	Version   int          `json:"version"`
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	TurnID    string       `json:"turn_id"`
	Root      string       `json:"root"`
	RootID    string       `json:"root_identity"`
	Entries   []*p390Entry `json:"entries"`

	rootHandle *os.Root
	rootInfo   os.FileInfo
}

type p390Operation struct {
	SnapshotID string          `json:"snapshot_id"`
	Completed  map[string]bool `json:"completed"`
}

type p390ItemResult struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

type p390ApplyResult struct {
	SnapshotID string           `json:"snapshot_id"`
	State      string           `json:"state"`
	Items      []p390ItemResult `json:"items"`
	Completed  map[string]bool  `json:"completed"`
}

func TestP390WorkspaceRecoveryPromotionMatrix(t *testing.T) {
	t.Run("clean tracked edit restores exact bytes and mode idempotently", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "tracked.txt", "before", 0o600)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "tracked.txt")
		p390Write(t, root, "tracked.txt", "agent", 0o644)
		p390RecordAfter(t, snapshot, "tracked.txt")
		p390Seal(t, snapshot)

		op := p390NewOperation(snapshot)
		result := p390Apply(context.Background(), snapshot, op, true, true, "")
		p390RequireResult(t, result, "complete", "tracked.txt", "applied")
		p390RequireFile(t, root, "tracked.txt", "before", 0o600)

		retry := p390Apply(context.Background(), snapshot, op, true, true, "")
		p390RequireResult(t, retry, "complete", "tracked.txt", "already_applied")
	})

	t.Run("dirty tracked edit restores captured workspace not HEAD", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "dirty.txt", "head", 0o644)
		p390Git(t, root, "init", "-q")
		p390Git(t, root, "add", "dirty.txt")
		p390Git(t, root, "-c", "user.name=P39", "-c", "user.email=p39@example.invalid", "commit", "-qm", "base")
		p390Write(t, root, "dirty.txt", "user-dirty", 0o640)

		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "dirty.txt")
		p390Write(t, root, "dirty.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "dirty.txt")
		p390Seal(t, snapshot)

		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")
		p390RequireResult(t, result, "complete", "dirty.txt", "applied")
		p390RequireFile(t, root, "dirty.txt", "user-dirty", 0o640)
	})

	t.Run("untracked creation is removed", func(t *testing.T) {
		root := p390Workspace(t)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "created.txt")
		p390Write(t, root, "created.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "created.txt")
		p390Seal(t, snapshot)

		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")
		p390RequireResult(t, result, "complete", "created.txt", "applied")
		if _, err := os.Lstat(filepath.Join(root, "created.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("created path remains after recovery: %v", err)
		}
	})

	t.Run("staged index is unchanged", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "staged.txt", "head", 0o644)
		p390Git(t, root, "init", "-q")
		p390Git(t, root, "add", "staged.txt")
		p390Git(t, root, "-c", "user.name=P39", "-c", "user.email=p39@example.invalid", "commit", "-qm", "base")
		p390Write(t, root, "staged.txt", "user-staged", 0o644)
		p390Git(t, root, "add", "staged.txt")
		cachedBefore := p390GitOutput(t, root, "diff", "--cached", "--binary")

		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "staged.txt")
		p390Write(t, root, "staged.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "staged.txt")
		p390Seal(t, snapshot)
		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")

		p390RequireResult(t, result, "complete", "staged.txt", "applied")
		p390RequireFile(t, root, "staged.txt", "user-staged", 0o644)
		if cachedAfter := p390GitOutput(t, root, "diff", "--cached", "--binary"); cachedAfter != cachedBefore {
			t.Fatalf("recovery mutated Git index\nbefore:\n%s\nafter:\n%s", cachedBefore, cachedAfter)
		}
	})

	t.Run("delete and rename restore in deterministic path order", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "a-source.txt", "source", 0o640)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "z-destination.txt")
		p390Capture(t, snapshot, "a-source.txt")
		if err := os.Rename(filepath.Join(root, "a-source.txt"), filepath.Join(root, "z-destination.txt")); err != nil {
			t.Fatal(err)
		}
		p390RecordAfter(t, snapshot, "z-destination.txt")
		p390RecordAfter(t, snapshot, "a-source.txt")
		p390Seal(t, snapshot)

		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")
		if got := []string{result.Items[0].Path, result.Items[1].Path}; strings.Join(got, ",") != "a-source.txt,z-destination.txt" {
			t.Fatalf("nondeterministic order: %v", got)
		}
		p390RequireFile(t, root, "a-source.txt", "source", 0o640)
		if _, err := os.Lstat(filepath.Join(root, "z-destination.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rename destination remains: %v", err)
		}
	})

	t.Run("external edit conflicts without overwrite", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "conflict.txt", "before", 0o600)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "conflict.txt")
		p390Write(t, root, "conflict.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "conflict.txt")
		p390Seal(t, snapshot)
		p390Write(t, root, "conflict.txt", "external", 0o644)

		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")
		p390RequireResult(t, result, "conflict", "conflict.txt", "conflict")
		p390RequireFile(t, root, "conflict.txt", "external", 0o644)
	})

	t.Run("symlink and root replacement fail closed", func(t *testing.T) {
		parent := p390CanonicalTemp(t)
		root := filepath.Join(parent, "workspace")
		outside := filepath.Join(parent, "outside")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		p390Write(t, outside, "sentinel.txt", "outside", 0o600)
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		snapshot := p390NewSnapshot(t, root)
		if err := snapshot.capture("escape/sentinel.txt"); errorCode390(err) != "path_symlink" {
			t.Fatalf("symlink capture error=%v", err)
		}

		p390Write(t, root, "safe.txt", "before", 0o600)
		p390Capture(t, snapshot, "safe.txt")
		p390Write(t, root, "safe.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "safe.txt")
		p390Seal(t, snapshot)
		if err := os.Remove(filepath.Join(root, "safe.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "sentinel.txt"), filepath.Join(root, "safe.txt")); err != nil {
			t.Fatal(err)
		}
		result := p390Apply(context.Background(), snapshot, p390NewOperation(snapshot), true, true, "")
		p390RequireResult(t, result, "conflict", "safe.txt", "conflict")
		if result.Items[0].Code != "path_symlink" {
			t.Fatalf("symlink conflict code=%s", result.Items[0].Code)
		}
		p390RequireFile(t, outside, "sentinel.txt", "outside", 0o600)

		rootTwo := filepath.Join(parent, "workspace-two")
		if err := os.Mkdir(rootTwo, 0o700); err != nil {
			t.Fatal(err)
		}
		p390Write(t, rootTwo, "root.txt", "before", 0o600)
		rootSnapshot := p390NewSnapshot(t, rootTwo)
		p390Capture(t, rootSnapshot, "root.txt")
		p390Write(t, rootTwo, "root.txt", "agent", 0o600)
		p390RecordAfter(t, rootSnapshot, "root.txt")
		p390Seal(t, rootSnapshot)
		moved := filepath.Join(parent, "moved-workspace")
		if err := os.Rename(rootTwo, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(rootTwo, 0o700); err != nil {
			t.Fatal(err)
		}
		p390Write(t, rootTwo, "root.txt", "replacement", 0o600)
		rootResult := p390Apply(context.Background(), rootSnapshot, p390NewOperation(rootSnapshot), true, true, "")
		p390RequireResult(t, rootResult, "conflict", "root.txt", "conflict")
		if rootResult.Items[0].Code != "root_replaced" {
			t.Fatalf("root replacement conflict code=%s", rootResult.Items[0].Code)
		}
		p390RequireFile(t, rootTwo, "root.txt", "replacement", 0o600)
		p390RequireFile(t, moved, "root.txt", "agent", 0o600)
	})

	t.Run("confirmation and permission deny before mutation", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "policy.txt", "before", 0o600)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "policy.txt")
		p390Write(t, root, "policy.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "policy.txt")
		p390Seal(t, snapshot)

		op := p390NewOperation(snapshot)
		p390RequireResult(t, p390Apply(context.Background(), snapshot, op, false, true, ""), "confirmation_required", "policy.txt", "pending")
		p390RequireFile(t, root, "policy.txt", "agent", 0o600)
		p390RequireResult(t, p390Apply(context.Background(), snapshot, op, true, false, ""), "permission_denied", "policy.txt", "pending")
		p390RequireFile(t, root, "policy.txt", "agent", 0o600)
	})

	t.Run("partial failure survives operation restart and retries safely", func(t *testing.T) {
		root := p390Workspace(t)
		for _, name := range []string{"a.txt", "b.txt"} {
			p390Write(t, root, name, "before-"+name, 0o600)
		}
		snapshot := p390NewSnapshot(t, root)
		for _, name := range []string{"b.txt", "a.txt"} {
			p390Capture(t, snapshot, name)
			p390Write(t, root, name, "agent-"+name, 0o600)
			p390RecordAfter(t, snapshot, name)
		}
		p390Seal(t, snapshot)
		op := p390NewOperation(snapshot)
		partial := p390Apply(context.Background(), snapshot, op, true, true, "b.txt")
		if partial.State != "partial" || !partial.Completed["a.txt"] || partial.Completed["b.txt"] {
			t.Fatalf("imprecise partial result: %#v", partial)
		}
		p390RequireFile(t, root, "a.txt", "before-a.txt", 0o600)
		p390RequireFile(t, root, "b.txt", "agent-b.txt", 0o600)

		encoded, err := json.Marshal(struct {
			Snapshot  *p390Snapshot  `json:"snapshot"`
			Operation *p390Operation `json:"operation"`
		}{snapshot, op})
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.rootHandle.Close(); err != nil {
			t.Fatal(err)
		}
		restartedSnapshot, restartedOperation := p390Restart(t, encoded)
		retry := p390Apply(context.Background(), restartedSnapshot, restartedOperation, true, true, "")
		if retry.State != "complete" || retry.Items[0].Status != "already_applied" || retry.Items[1].Status != "applied" {
			t.Fatalf("unsafe restart retry: %#v", retry)
		}
		p390RequireFile(t, root, "b.txt", "before-b.txt", 0o600)

		cancelRoot := p390Workspace(t)
		p390Write(t, cancelRoot, "cancel.txt", "before", 0o600)
		cancelSnapshot := p390NewSnapshot(t, cancelRoot)
		p390Capture(t, cancelSnapshot, "cancel.txt")
		p390Write(t, cancelRoot, "cancel.txt", "agent", 0o600)
		p390RecordAfter(t, cancelSnapshot, "cancel.txt")
		p390Seal(t, cancelSnapshot)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		canceled := p390Apply(ctx, cancelSnapshot, p390NewOperation(cancelSnapshot), true, true, "")
		p390RequireResult(t, canceled, "canceled", "cancel.txt", "pending")
		p390RequireFile(t, cancelRoot, "cancel.txt", "agent", 0o600)
	})

	t.Run("missing durable root fails restart without panic", func(t *testing.T) {
		root := p390Workspace(t)
		p390Write(t, root, "missing.txt", "before", 0o600)
		snapshot := p390NewSnapshot(t, root)
		p390Capture(t, snapshot, "missing.txt")
		p390Write(t, root, "missing.txt", "agent", 0o600)
		p390RecordAfter(t, snapshot, "missing.txt")
		p390Seal(t, snapshot)
		encoded, err := json.Marshal(struct {
			Snapshot  *p390Snapshot  `json:"snapshot"`
			Operation *p390Operation `json:"operation"`
		}{snapshot, p390NewOperation(snapshot)})
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.rootHandle.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p390RestoreDurableState(encoded); err == nil {
			t.Fatal("missing durable root was accepted")
		}
	})

	t.Run("entrypoint contract is bound to production identities", func(t *testing.T) {
		rootEntrypoints := []struct {
			policy  containment.Entrypoint
			command commands.Entrypoint
		}{
			{containment.EntrypointTUI, commands.EntrypointTUI},
			{containment.EntrypointPlain, commands.EntrypointPlain},
			{containment.EntrypointHeadless, commands.EntrypointHeadless},
			{containment.EntrypointHeadlessGoal, commands.EntrypointHeadlessGoal},
			{containment.EntrypointACP, commands.EntrypointACP},
		}
		for _, entrypoint := range rootEntrypoints {
			if string(entrypoint.policy) != string(entrypoint.command) {
				t.Fatalf("production entrypoint identity drift: containment=%q commands=%q", entrypoint.policy, entrypoint.command)
			}
			if got := p390EntrypointDisposition(entrypoint.policy); got != "root_session" {
				t.Fatalf("entrypoint %s disposition=%s want=root_session", entrypoint.policy, got)
			}
		}
		for _, entrypoint := range []containment.Entrypoint{
			containment.EntrypointChildAgent,
			containment.EntrypointStandaloneMCP,
		} {
			if got := p390EntrypointDisposition(entrypoint); got != "unsupported" {
				t.Fatalf("entrypoint %s disposition=%s want=unsupported", entrypoint, got)
			}
		}
	})
}

type p390ContractError struct{ code string }

func (err *p390ContractError) Error() string { return err.code }

func p390Fail(code string) error { return &p390ContractError{code: code} }

func errorCode390(err error) string {
	var contractErr *p390ContractError
	if errors.As(err, &contractErr) {
		return contractErr.code
	}
	return "internal"
}

func p390NewSnapshot(t *testing.T, root string) *p390Snapshot {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		t.Fatalf("workspace root: %v", err)
	}
	handle, err := os.OpenRoot(canonical)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &p390Snapshot{
		Version: p390SnapshotVersion, SessionID: "session-p39", TurnID: "turn-p39", Root: canonical,
		RootID: p390RootIdentity(t, canonical, info), rootHandle: handle, rootInfo: info,
	}
	t.Cleanup(func() { _ = handle.Close() })
	return snapshot
}

func (snapshot *p390Snapshot) capture(path string) error {
	if snapshot.ID != "" {
		return p390Fail("snapshot_sealed")
	}
	if p390FindEntry(snapshot, path) != nil {
		return p390Fail("path_duplicate")
	}
	state, err := snapshot.read(path)
	if err != nil {
		return err
	}
	snapshot.Entries = append(snapshot.Entries, &p390Entry{Path: path, Before: state})
	return nil
}

func (snapshot *p390Snapshot) recordAfter(path string) error {
	entry := p390FindEntry(snapshot, path)
	if entry == nil || snapshot.ID != "" {
		return p390Fail("snapshot_state_invalid")
	}
	state, err := snapshot.read(path)
	if err != nil {
		return err
	}
	entry.After = &state
	return nil
}

func (snapshot *p390Snapshot) seal() error {
	if snapshot.ID != "" || len(snapshot.Entries) == 0 {
		return p390Fail("snapshot_state_invalid")
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool { return snapshot.Entries[i].Path < snapshot.Entries[j].Path })
	for _, entry := range snapshot.Entries {
		if entry.After == nil {
			return p390Fail("snapshot_state_invalid")
		}
	}
	payload, err := json.Marshal(struct {
		Version   int          `json:"version"`
		SessionID string       `json:"session_id"`
		TurnID    string       `json:"turn_id"`
		Root      string       `json:"root"`
		RootID    string       `json:"root_identity"`
		Entries   []*p390Entry `json:"entries"`
	}{snapshot.Version, snapshot.SessionID, snapshot.TurnID, snapshot.Root, snapshot.RootID, snapshot.Entries})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	snapshot.ID = hex.EncodeToString(digest[:])
	return nil
}

func (snapshot *p390Snapshot) read(path string) (p390FileState, error) {
	if !fs.ValidPath(path) {
		return p390FileState{}, p390Fail("path_invalid")
	}
	if err := snapshot.validateRoot(); err != nil {
		return p390FileState{}, err
	}
	if err := p390RejectSymlinks(snapshot.Root, path); err != nil {
		return p390FileState{}, err
	}
	native := filepath.FromSlash(path)
	info, err := snapshot.rootHandle.Lstat(native)
	if errors.Is(err, os.ErrNotExist) {
		return p390FileState{Digest: p390Digest(nil)}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return p390FileState{}, p390Fail("path_non_regular")
	}
	content, err := snapshot.rootHandle.ReadFile(native)
	if err != nil {
		return p390FileState{}, err
	}
	return p390FileState{Exists: true, Mode: info.Mode().Perm(), Content: content, Digest: p390Digest(content)}, nil
}

func (snapshot *p390Snapshot) validateRoot() error {
	current, err := os.Stat(snapshot.Root)
	if err != nil || !current.IsDir() || !os.SameFile(snapshot.rootInfo, current) {
		return p390Fail("root_replaced")
	}
	identity, err := p390DurableRootIdentity(snapshot.Root, current)
	if err != nil || identity != snapshot.RootID {
		return p390Fail("root_replaced")
	}
	return nil
}

func p390Apply(
	ctx context.Context,
	snapshot *p390Snapshot,
	operation *p390Operation,
	confirmed bool,
	permitted bool,
	failBefore string,
) p390ApplyResult {
	result := p390ApplyResult{SnapshotID: snapshot.ID, Completed: p390CopyCompleted(operation.Completed)}
	if operation.SnapshotID != snapshot.ID || snapshot.ID == "" {
		result.State = "invalid_operation"
		return p390FillPending(result, snapshot, operation)
	}
	if !confirmed {
		result.State = "confirmation_required"
		return p390FillPending(result, snapshot, operation)
	}
	if !permitted {
		result.State = "permission_denied"
		return p390FillPending(result, snapshot, operation)
	}
	for _, entry := range snapshot.Entries {
		if operation.Completed[entry.Path] {
			result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "already_applied"})
			continue
		}
		if ctx.Err() != nil {
			result.State = "canceled"
			return p390FillPending(result, snapshot, operation)
		}
		if entry.Path == failBefore {
			result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "failed", Code: "injected_failure"})
			result.State = "partial"
			return p390FillPending(result, snapshot, operation)
		}
		current, err := snapshot.read(entry.Path)
		if err != nil {
			result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "conflict", Code: errorCode390(err)})
			result.State = "conflict"
			return p390FillPending(result, snapshot, operation)
		}
		if !p390EqualState(current, *entry.After) {
			result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "conflict", Code: "external_edit"})
			result.State = "conflict"
			return p390FillPending(result, snapshot, operation)
		}
		if err := snapshot.restore(entry.Path, entry.Before); err != nil {
			result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "failed", Code: errorCode390(err)})
			result.State = "partial"
			return p390FillPending(result, snapshot, operation)
		}
		operation.Completed[entry.Path] = true
		result.Completed[entry.Path] = true
		result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: "applied"})
	}
	result.State = "complete"
	return result
}

func (snapshot *p390Snapshot) restore(path string, before p390FileState) error {
	if err := snapshot.validateRoot(); err != nil {
		return err
	}
	if err := p390RejectSymlinks(snapshot.Root, path); err != nil {
		return err
	}
	native := filepath.FromSlash(path)
	if !before.Exists {
		if err := snapshot.rootHandle.Remove(native); err != nil {
			return err
		}
	} else {
		if err := snapshot.rootHandle.WriteFile(native, before.Content, before.Mode); err != nil {
			return err
		}
		if err := snapshot.rootHandle.Chmod(native, before.Mode); err != nil {
			return err
		}
	}
	got, err := snapshot.read(path)
	if err != nil || !p390EqualState(got, before) {
		return p390Fail("restore_verification_failed")
	}
	return nil
}

func p390FillPending(result p390ApplyResult, snapshot *p390Snapshot, operation *p390Operation) p390ApplyResult {
	seen := make(map[string]bool, len(result.Items))
	for _, item := range result.Items {
		seen[item.Path] = true
	}
	for _, entry := range snapshot.Entries {
		if seen[entry.Path] {
			continue
		}
		status := "pending"
		if operation.Completed[entry.Path] {
			status = "already_applied"
		}
		result.Items = append(result.Items, p390ItemResult{Path: entry.Path, Status: status})
	}
	return result
}

func p390RejectSymlinks(root, path string) error {
	current := root
	parts := strings.Split(filepath.FromSlash(path), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return p390Fail("path_symlink")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return p390Fail("path_non_directory")
		}
	}
	return nil
}

func p390EqualState(left, right p390FileState) bool {
	return left.Exists == right.Exists && left.Mode == right.Mode && left.Digest == right.Digest &&
		bytes.Equal(left.Content, right.Content)
}

func p390Digest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func p390FindEntry(snapshot *p390Snapshot, path string) *p390Entry {
	for _, entry := range snapshot.Entries {
		if entry.Path == path {
			return entry
		}
	}
	return nil
}

func p390NewOperation(snapshot *p390Snapshot) *p390Operation {
	return &p390Operation{SnapshotID: snapshot.ID, Completed: map[string]bool{}}
}

func p390CopyCompleted(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for path, completed := range source {
		result[path] = completed
	}
	return result
}

func p390EntrypointDisposition(entrypoint containment.Entrypoint) string {
	switch entrypoint {
	case containment.EntrypointTUI,
		containment.EntrypointPlain,
		containment.EntrypointHeadless,
		containment.EntrypointHeadlessGoal,
		containment.EntrypointACP:
		return "root_session"
	case containment.EntrypointChildAgent, containment.EntrypointStandaloneMCP:
		return "unsupported"
	default:
		return "unknown"
	}
}

func p390Restart(t *testing.T, encoded []byte) (*p390Snapshot, *p390Operation) {
	t.Helper()
	snapshot, operation, err := p390RestoreDurableState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.rootHandle.Close() })
	return snapshot, operation
}

func p390RestoreDurableState(encoded []byte) (*p390Snapshot, *p390Operation, error) {
	var durable struct {
		Snapshot  *p390Snapshot  `json:"snapshot"`
		Operation *p390Operation `json:"operation"`
	}
	if err := json.Unmarshal(encoded, &durable); err != nil {
		return nil, nil, err
	}
	if durable.Snapshot == nil || durable.Operation == nil || durable.Snapshot.Version != p390SnapshotVersion {
		return nil, nil, errors.New("invalid durable recovery state")
	}
	canonical, err := filepath.EvalSymlinks(durable.Snapshot.Root)
	if err != nil {
		return nil, nil, fmt.Errorf("reopen workspace root: %w", err)
	}
	if canonical != durable.Snapshot.Root {
		return nil, nil, errors.New("reopen workspace root is not canonical")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, errors.New("workspace root is not a directory")
	}
	identity, identityErr := p390DurableRootIdentity(canonical, info)
	if identityErr != nil {
		return nil, nil, fmt.Errorf("reopen workspace root identity: %w", identityErr)
	}
	if identity != durable.Snapshot.RootID {
		return nil, nil, errors.New("reopen workspace root identity mismatch")
	}
	handle, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, nil, err
	}
	durable.Snapshot.rootHandle = handle
	durable.Snapshot.rootInfo = info

	expectedID := durable.Snapshot.ID
	durable.Snapshot.ID = ""
	if err := durable.Snapshot.seal(); err != nil {
		_ = handle.Close()
		return nil, nil, err
	}
	if durable.Snapshot.ID != expectedID || durable.Operation.SnapshotID != expectedID {
		_ = handle.Close()
		return nil, nil, errors.New("durable recovery identity mismatch")
	}
	return durable.Snapshot, durable.Operation, nil
}

func p390RootIdentity(t *testing.T, path string, info os.FileInfo) string {
	t.Helper()
	identity, err := p390DurableRootIdentity(path, info)
	if err != nil || identity == "" {
		t.Fatalf("durable root identity: %v", err)
	}
	return identity
}

func p390Capture(t *testing.T, snapshot *p390Snapshot, path string) {
	t.Helper()
	if err := snapshot.capture(path); err != nil {
		t.Fatal(err)
	}
}

func p390RecordAfter(t *testing.T, snapshot *p390Snapshot, path string) {
	t.Helper()
	if err := snapshot.recordAfter(path); err != nil {
		t.Fatal(err)
	}
}

func p390Seal(t *testing.T, snapshot *p390Snapshot) {
	t.Helper()
	if err := snapshot.seal(); err != nil {
		t.Fatal(err)
	}
}

func p390RequireResult(t *testing.T, result p390ApplyResult, state, path, status string) {
	t.Helper()
	if result.State != state || len(result.Items) != 1 || result.Items[0].Path != path || result.Items[0].Status != status {
		t.Fatalf("result=%#v want state=%s path=%s status=%s", result, state, path, status)
	}
}

func p390CanonicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func p390Workspace(t *testing.T) string {
	t.Helper()
	parent := p390CanonicalTemp(t)
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func p390Write(t *testing.T, root, path, content string, mode fs.FileMode) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, mode); err != nil {
		t.Fatal(err)
	}
}

func p390RequireFile(t *testing.T, root, path, content string, mode fs.FileMode) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	data, err := os.ReadFile(absolute)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content || info.Mode().Perm() != mode {
		t.Fatalf("file %s content=%q mode=%#o want content=%q mode=%#o", path, data, info.Mode().Perm(), content, mode)
	}
}

func p390Git(t *testing.T, root string, arguments ...string) {
	t.Helper()
	_ = p390GitOutput(t, root, arguments...)
}

func p390GitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(root), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func (result p390ApplyResult) String() string {
	return fmt.Sprintf("state=%s items=%v", result.State, result.Items)
}
