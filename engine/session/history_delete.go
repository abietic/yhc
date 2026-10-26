package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	workspaceHistorySuffix  = ".workspace-history"
	workspaceHistoryJournal = "revisions.jsonl"
)

// workspaceHistoryDeletePlan is deliberately narrow: a session owns exactly
// one private journal, never an arbitrary directory tree.
type workspaceHistoryDeletePlan struct {
	path        string
	name        string
	present     bool
	parent      *os.Root
	root        *os.Root
	dirInfo     os.FileInfo
	journalInfo os.FileInfo
}

func preflightWorkspaceHistoryDelete(dir, sessionID string) (*workspaceHistoryDeletePlan, error) {
	name := sessionID + workspaceHistorySuffix
	path := filepath.Join(dir, name)
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open workspace history parent: %w", err)
	}
	plan := &workspaceHistoryDeletePlan{path: path, name: name, parent: parent}
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
	entries, readErr := file.ReadDir(2)
	closeErr := file.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return errors.New("list workspace history failed")
	}

	if len(entries) == 0 {
		p.journalInfo = nil
		return nil
	}
	if len(entries) != 1 || entries[0].Name() != workspaceHistoryJournal {
		return errors.New("workspace history contains an unexpected entry")
	}
	info, err := p.root.Lstat(workspaceHistoryJournal)
	if err != nil {
		return fmt.Errorf("stat workspace history journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("workspace history journal must be a non-symlink mode-0600 regular file")
	}
	p.journalInfo = info
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
	expectedJournal := p.journalInfo
	if err := p.scan(); err != nil ||
		(expectedJournal == nil) != (p.journalInfo == nil) ||
		(expectedJournal != nil && !os.SameFile(expectedJournal, p.journalInfo)) {
		return errors.New("workspace history changed before deletion")
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
	if p.journalInfo != nil {
		if err := p.root.Remove(workspaceHistoryJournal); err != nil {
			return fmt.Errorf("remove workspace history journal: %w", err)
		}
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
	if p != nil && p.root != nil {
		_ = p.root.Close()
		p.root = nil
	}
	if p != nil && p.parent != nil {
		_ = p.parent.Close()
		p.parent = nil
	}
}
