package memdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/permission"
)

const (
	agentSnapshotBaseDir = "agent-memory-snapshots"
	agentSnapshotMeta    = "snapshot.json"
	agentSnapshotSynced  = ".snapshot-synced.json"
)

// AgentSnapshotAction describes the next observable snapshot action.
type AgentSnapshotAction string

const (
	AgentSnapshotNone         AgentSnapshotAction = "none"
	AgentSnapshotInitialize   AgentSnapshotAction = "initialize"
	AgentSnapshotPromptUpdate AgentSnapshotAction = "prompt-update"
)

// AgentSnapshotStatus is the result of comparing project and local metadata.
type AgentSnapshotStatus struct {
	Action            AgentSnapshotAction
	SnapshotTimestamp string
}

type agentSnapshotMetaFile struct {
	UpdatedAt string `json:"updatedAt"`
}

type agentSnapshotSyncedFile struct {
	SyncedFrom string `json:"syncedFrom"`
}

var agentSnapshotMu sync.Mutex

// GetAgentSnapshotDir returns the project snapshot source for one sanitized
// custom-agent key.
func GetAgentSnapshotDir(agentType, projectRoot string) string {
	return filepath.Join(resolveProjectRoot(projectRoot), ".claude", agentSnapshotBaseDir, sanitizeAgentType(agentType))
}

// CheckAgentMemorySnapshot compares strict RFC3339 metadata and local Markdown
// presence without changing durable state.
func CheckAgentMemorySnapshot(agentType string, scope AgentMemoryScope, projectRoot string) AgentSnapshotStatus {
	if ParseAgentMemoryScope(string(scope)) == "" {
		return AgentSnapshotStatus{Action: AgentSnapshotNone}
	}
	if err := validateAgentSnapshotPaths(agentType, scope, projectRoot); err != nil {
		return AgentSnapshotStatus{Action: AgentSnapshotNone}
	}
	meta, snapshotTime, ok := readSnapshotMeta(filepath.Join(GetAgentSnapshotDir(agentType, projectRoot), agentSnapshotMeta))
	if !ok {
		return AgentSnapshotStatus{Action: AgentSnapshotNone}
	}
	localDir := GetAgentMemoryDirForProject(agentType, scope, projectRoot)
	if !hasTopLevelMarkdown(localDir) {
		return AgentSnapshotStatus{Action: AgentSnapshotInitialize, SnapshotTimestamp: meta.UpdatedAt}
	}
	_, syncedTime, ok := readSyncedMeta(filepath.Join(localDir, agentSnapshotSynced))
	if !ok || snapshotTime.After(syncedTime) {
		return AgentSnapshotStatus{Action: AgentSnapshotPromptUpdate, SnapshotTimestamp: meta.UpdatedAt}
	}
	return AgentSnapshotStatus{Action: AgentSnapshotNone}
}

func validateAgentSnapshotPaths(agentType string, scope AgentMemoryScope, projectRoot string) error {
	projectRoot = resolveProjectRoot(projectRoot)
	snapshotDir := GetAgentSnapshotDir(agentType, projectRoot)
	if !permission.PermissionPathsWithinRoots(permission.ResolvePermissionPath(snapshotDir, projectRoot), []string{projectRoot}) {
		return fmt.Errorf("agent snapshot directory escapes project root")
	}
	localDir := GetAgentMemoryDirForProject(agentType, scope, projectRoot)
	localRoot := GetAgentMemoryRootForProject(scope, projectRoot)
	if localRoot == "" || !permission.PermissionPathsWithinRoots(permission.ResolvePermissionPath(localDir, projectRoot), []string{localRoot}) {
		return fmt.Errorf("agent memory directory escapes configured scope")
	}
	return nil
}

// InitializeAgentMemoryFromSnapshot copies direct snapshot files without
// deleting local content, then advances the sync marker.
func InitializeAgentMemoryFromSnapshot(agentType string, scope AgentMemoryScope, projectRoot, snapshotTimestamp string) error {
	agentSnapshotMu.Lock()
	defer agentSnapshotMu.Unlock()
	if _, err := parseSnapshotTimestamp(snapshotTimestamp); err != nil {
		return err
	}
	if err := validateAgentSnapshotPaths(agentType, scope, projectRoot); err != nil {
		return err
	}
	files, err := loadSnapshotFiles(agentType, projectRoot)
	if err != nil {
		return err
	}
	localDir := GetAgentMemoryDirForProject(agentType, scope, projectRoot)
	if err := applySnapshotFiles(localDir, files, false); err != nil {
		return err
	}
	return saveSnapshotSynced(localDir, snapshotTimestamp)
}

// ReplaceAgentMemoryFromSnapshot removes only top-level local Markdown files,
// installs the snapshot, and advances the sync marker after success.
func ReplaceAgentMemoryFromSnapshot(agentType string, scope AgentMemoryScope, projectRoot, snapshotTimestamp string) error {
	agentSnapshotMu.Lock()
	defer agentSnapshotMu.Unlock()
	if _, err := parseSnapshotTimestamp(snapshotTimestamp); err != nil {
		return err
	}
	if err := validateAgentSnapshotPaths(agentType, scope, projectRoot); err != nil {
		return err
	}
	files, err := loadSnapshotFiles(agentType, projectRoot)
	if err != nil {
		return err
	}
	localDir := GetAgentMemoryDirForProject(agentType, scope, projectRoot)
	if err := applySnapshotFiles(localDir, files, true); err != nil {
		return err
	}
	return saveSnapshotSynced(localDir, snapshotTimestamp)
}

// MarkAgentMemorySnapshotSynced advances metadata without changing memory
// content, implementing the reference's explicit keep choice.
func MarkAgentMemorySnapshotSynced(agentType string, scope AgentMemoryScope, projectRoot, snapshotTimestamp string) error {
	agentSnapshotMu.Lock()
	defer agentSnapshotMu.Unlock()
	if _, err := parseSnapshotTimestamp(snapshotTimestamp); err != nil {
		return err
	}
	if err := validateAgentSnapshotPaths(agentType, scope, projectRoot); err != nil {
		return err
	}
	return saveSnapshotSynced(GetAgentMemoryDirForProject(agentType, scope, projectRoot), snapshotTimestamp)
}

func readSnapshotMeta(path string) (agentSnapshotMetaFile, time.Time, bool) {
	var meta agentSnapshotMetaFile
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		return meta, time.Time{}, false
	}
	parsed, err := parseSnapshotTimestamp(meta.UpdatedAt)
	return meta, parsed, err == nil
}

func readSyncedMeta(path string) (agentSnapshotSyncedFile, time.Time, bool) {
	var meta agentSnapshotSyncedFile
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		return meta, time.Time{}, false
	}
	parsed, err := parseSnapshotTimestamp(meta.SyncedFrom)
	return meta, parsed, err == nil
}

func parseSnapshotTimestamp(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid agent snapshot timestamp %q: %w", raw, err)
	}
	return parsed, nil
}

func hasTopLevelMarkdown(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return true
		}
	}
	return false
}

func loadSnapshotFiles(agentType, projectRoot string) (map[string][]byte, error) {
	dir := GetAgentSnapshotDir(agentType, projectRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading agent snapshot directory: %w", err)
	}
	files := make(map[string][]byte)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() == agentSnapshotMeta || entry.Name() == agentSnapshotSynced {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("reading agent snapshot file %s: %w", entry.Name(), readErr)
		}
		files[entry.Name()] = data
	}
	return files, nil
}

type localFileBackup struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

func applySnapshotFiles(localDir string, snapshot map[string][]byte, replaceMarkdown bool) error {
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return fmt.Errorf("creating agent memory directory: %w", err)
	}
	backups := make(map[string]localFileBackup)
	remember := func(path string) error {
		if _, ok := backups[path]; ok {
			return nil
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			backups[path] = localFileBackup{}
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("agent memory target is not a regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		backups[path] = localFileBackup{data: data, mode: info.Mode().Perm(), exists: true}
		return nil
	}

	if replaceMarkdown {
		entries, err := os.ReadDir(localDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				if err := remember(filepath.Join(localDir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	for name := range snapshot {
		if err := remember(filepath.Join(localDir, name)); err != nil {
			return err
		}
	}

	rollback := func() {
		paths := make([]string, 0, len(backups))
		for path := range backups {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			backup := backups[path]
			if !backup.exists {
				_ = os.Remove(path)
				continue
			}
			_ = writeAtomicFile(path, backup.data, backup.mode)
		}
	}

	if replaceMarkdown {
		for path, backup := range backups {
			if backup.exists && strings.EqualFold(filepath.Ext(path), ".md") {
				if err := os.Remove(path); err != nil {
					rollback()
					return err
				}
			}
		}
	}
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeAtomicFile(filepath.Join(localDir, name), snapshot[name], 0o600); err != nil {
			rollback()
			return fmt.Errorf("writing agent snapshot file %s: %w", name, err)
		}
	}
	return nil
}

func saveSnapshotSynced(localDir, timestamp string) error {
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(agentSnapshotSyncedFile{SyncedFrom: timestamp})
	if err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(localDir, agentSnapshotSynced), data, 0o600)
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	renameErr := os.Rename(tmpPath, path)
	if renameErr == nil {
		return nil
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return errors.Join(renameErr, statErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("atomic snapshot target is not a regular file: %s", path)
	}

	backup, err := os.CreateTemp(filepath.Dir(path), ".snapshot-backup-*")
	if err != nil {
		return errors.Join(renameErr, err)
	}
	backupPath := backup.Name()
	if closeErr := backup.Close(); closeErr != nil {
		_ = os.Remove(backupPath)
		return errors.Join(renameErr, closeErr)
	}
	if removeErr := os.Remove(backupPath); removeErr != nil {
		return errors.Join(renameErr, removeErr)
	}
	if moveErr := os.Rename(path, backupPath); moveErr != nil {
		return errors.Join(renameErr, moveErr)
	}
	defer func() { _ = os.Remove(backupPath) }()
	if chmodErr := os.Chmod(backupPath, 0o600); chmodErr != nil {
		_ = os.Rename(backupPath, path)
		return errors.Join(renameErr, chmodErr)
	}
	if moveErr := os.Rename(tmpPath, path); moveErr != nil {
		restoreErr := os.Rename(backupPath, path)
		if restoreErr == nil {
			restoreErr = os.Chmod(path, info.Mode().Perm())
		}
		return errors.Join(moveErr, restoreErr)
	}
	return nil
}
