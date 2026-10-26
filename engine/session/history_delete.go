package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abietic/yhc/engine/filehistory"
)

const (
	workspaceHistorySuffix  = ".workspace-history"
	workspaceHistoryJournal = "revisions.jsonl"
)

// workspaceHistoryDeletePlan is deliberately narrow: a session owns exactly
// a fixed private sidecar set, never an arbitrary directory tree.
type workspaceHistoryDeletePlan struct {
	path      string
	name      string
	present   bool
	parent    *os.Root
	root      *os.Root
	dirInfo   os.FileInfo
	files     map[string]os.FileInfo
	unlock    func()
	sessionID string
}

func preflightWorkspaceHistoryDelete(dir, sessionID string) (*workspaceHistoryDeletePlan, error) {
	name := sessionID + workspaceHistorySuffix
	path := filepath.Join(dir, name)
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open workspace history parent: %w", err)
	}
	plan := &workspaceHistoryDeletePlan{path: path, name: name, parent: parent, sessionID: sessionID}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		plan.close()
		return nil, fmt.Errorf("preflight workspace history: %w", err)
	}
	if err := validateWorkspaceHistoryDirectory(info); err != nil {
		plan.close()
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		plan.close()
		return nil, fmt.Errorf("open workspace history: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		plan.close()
		return nil, errors.New("workspace history changed while opening")
	}
	plan.present = true
	plan.root = root
	plan.dirInfo = info
	plan.unlock, err = filehistory.LockWorkspaceHistoryDirectory(root)
	if err != nil {
		plan.close()
		return nil, err
	}
	if err := plan.scan(); err != nil {
		plan.close()
		return nil, err
	}
	return plan, nil
}

func validateWorkspaceHistoryDirectory(info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("workspace history must be a non-symlink mode-0700 directory")
	}
	return nil
}

func (p *workspaceHistoryDeletePlan) scan() error {
	file, err := p.root.Open(".")
	if err != nil {
		return fmt.Errorf("open workspace history directory: %w", err)
	}
	entries, readErr := file.ReadDir(4)
	closeErr := file.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return errors.New("list workspace history failed")
	}
	if len(entries) > 3 {
		return errors.New("workspace history contains an unexpected entry")
	}
	files := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name != workspaceHistoryJournal && name != "restore.json" && name != "restore.json.tmp" {
			return errors.New("workspace history contains an unexpected entry")
		}
		info, err := p.root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("workspace history sidecar must be a non-symlink mode-0600 regular file")
		}
		files[name] = info
	}
	if _, found := files["restore.json"]; found {
		if err := filehistory.RestoreStoreDeletionReady(p.path, p.sessionID); err != nil {
			return err
		}
	}
	p.files = files
	return nil
}

func (p *workspaceHistoryDeletePlan) revalidate() error {
	if p == nil {
		return nil
	}
	if !p.present {
		_, err := p.parent.Lstat(p.name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("revalidate workspace history: %w", err)
		}
		return errors.New("workspace history appeared before deletion")
	}
	if p.root == nil {
		return errors.New("workspace history plan is closed")
	}
	current, err := p.parent.Lstat(p.name)
	if err != nil || validateWorkspaceHistoryDirectory(current) != nil || !os.SameFile(p.dirInfo, current) {
		return errors.New("workspace history changed before deletion")
	}
	expected := p.files
	if err := p.scan(); err != nil || len(expected) != len(p.files) {
		return errors.New("workspace history changed before deletion")
	}
	for name, prior := range expected {
		current := p.files[name]
		if current == nil || !os.SameFile(prior, current) || prior.Mode() != current.Mode() || prior.Size() != current.Size() || !prior.ModTime().Equal(current.ModTime()) {
			return errors.New("workspace history changed before deletion")
		}
	}

	return nil
}

func (p *workspaceHistoryDeletePlan) remove() error {
	if p == nil || !p.present {
		return nil
	}
	if err := p.revalidate(); err != nil {
		return err
	}
	// Delete the recovery record before the journal, so an interrupted
	// cleanup can be retried without needing a journal already removed.
	for _, name := range []string{"restore.json.tmp", "restore.json", workspaceHistoryJournal} {
		prior := p.files[name]
		if prior == nil {
			continue
		}
		current, err := p.root.Lstat(name)
		if err != nil || !os.SameFile(prior, current) || prior.Mode() != current.Mode() || prior.Size() != current.Size() || !prior.ModTime().Equal(current.ModTime()) {
			return errors.New("workspace history changed during deletion")
		}
		if err := p.root.Remove(name); err != nil {
			return fmt.Errorf("remove workspace history sidecar: %w", err)
		}
	}
	if err := filehistory.SyncWorkspaceHistoryDirectory(p.root); err != nil {
		return errors.New("sync workspace history deletion failed")
	}

	// Keep the now-empty private directory. Portable path-based rmdir cannot
	// atomically bind the verified inode: removing its name could delete a newly
	// substituted directory. Retaining empty namespace metadata removes all
	// captured bytes without creating that destructive race.
	current, err := p.parent.Lstat(p.name)
	if err != nil || validateWorkspaceHistoryDirectory(current) != nil || !os.SameFile(p.dirInfo, current) {
		return errors.New("workspace history changed during deletion")
	}

	return nil
}

func (p *workspaceHistoryDeletePlan) close() {
	if p != nil && p.unlock != nil {
		p.unlock()
		p.unlock = nil
	}
	if p != nil && p.root != nil {
		_ = p.root.Close()
		p.root = nil
	}
	if p != nil && p.parent != nil {
		_ = p.parent.Close()
		p.parent = nil
	}
}
