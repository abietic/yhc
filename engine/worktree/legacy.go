package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/abietic/yhc/internal/identity"
)

type LegacyInspectionStatus string

const (
	LegacyInspectionAbsent LegacyInspectionStatus = "absent"
	LegacyInspectionReady  LegacyInspectionStatus = "ready"
	LegacyInspectionUnsafe LegacyInspectionStatus = "unsafe"
)

type LegacyRecordStatus string

const (
	// LegacyRecordActive means a nonterminal record was read and verified. It
	// never grants quiescence, continuation, adoption, or cleanup authority.
	LegacyRecordActive      LegacyRecordStatus = "active"
	LegacyRecordDirty       LegacyRecordStatus = "dirty"
	LegacyRecordUnavailable LegacyRecordStatus = "unavailable"
	LegacyRecordTerminal    LegacyRecordStatus = "terminal"
)

// LegacyRecordInspection deliberately contains no path, branch, owner,
// command, diagnostic, or file content.
type LegacyRecordInspection struct {
	RecordID string
	Status   LegacyRecordStatus
}

type LegacyInspection struct {
	Status  LegacyInspectionStatus
	Records []LegacyRecordInspection
}

// InspectLegacy observes archived worktree records without creating state,
// changing a record, running recovery, or invoking a Git mutation.
func InspectLegacy(ctx context.Context, projectRoot string) (LegacyInspection, error) {
	return inspectLegacyWithGit(ctx, projectRoot, CommandGit{})
}

func inspectLegacyWithGit(
	ctx context.Context,
	projectRoot string,
	git ReadOnlyGit,
) (LegacyInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return LegacyInspection{}, err
	}
	canonicalProject, err := canonicalExistingPath(projectRoot)
	if err != nil || git == nil {
		return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
	}
	pinned, exists, err := openPinnedLegacyRecords(canonicalProject)
	if err != nil {
		return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
	}
	if !exists {
		return LegacyInspection{Status: LegacyInspectionAbsent}, nil
	}
	defer pinned.Close() //nolint:errcheck

	legacyRoot := filepath.Join(
		canonicalProject,
		identity.LegacyDirName,
		"worktrees",
		"v1",
	)
	store := NewStore(filepath.Join(legacyRoot, "records"))
	records, diagnostics, err := store.listFromRoot(ctx, pinned.root)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return LegacyInspection{}, ctxErr
		}
		return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
	}
	if !pinned.revalidate() {
		return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
	}

	statuses := make(map[string]LegacyRecordStatus, len(records)+len(diagnostics))
	for _, diagnostic := range diagnostics {
		if validateRecordID(diagnostic.RecordID) != nil {
			return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
		}
		statuses[diagnostic.RecordID] = LegacyRecordUnavailable
	}
	service := &Service{
		projectRoot: canonicalProject,
		managedRoot: filepath.Join(legacyRoot, "trees"),
	}
	for _, record := range records {
		if _, rejected := statuses[record.ID]; rejected {
			continue
		}
		switch record.State {
		case StateRemoved:
			statuses[record.ID] = LegacyRecordTerminal
			continue
		case StateFailed:
			// Creation can fail after git worktree add succeeds. Failed therefore
			// cannot prove that the checkout or branch is gone.
			statuses[record.ID] = LegacyRecordUnavailable
			continue
		}
		report, _, inspectErr := service.inspectRecoveryWithGit(
			ctx,
			ctx,
			record,
			true,
			git,
		)
		switch {
		case inspectErr != nil || report == nil:
			statuses[record.ID] = LegacyRecordUnavailable
		case report.Dirty:
			statuses[record.ID] = LegacyRecordDirty
		default:
			statuses[record.ID] = LegacyRecordActive
		}
	}
	if !pinned.revalidate() {
		return LegacyInspection{Status: LegacyInspectionUnsafe}, nil
	}
	if len(statuses) == 0 {
		return LegacyInspection{Status: LegacyInspectionAbsent}, nil
	}
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	inspection := LegacyInspection{
		Status:  LegacyInspectionReady,
		Records: make([]LegacyRecordInspection, 0, len(ids)),
	}
	for _, id := range ids {
		inspection.Records = append(inspection.Records, LegacyRecordInspection{
			RecordID: id,
			Status:   statuses[id],
		})
	}
	return inspection, nil
}

type pinnedLegacyStep struct {
	name string
	info os.FileInfo
}

type pinnedLegacyRecords struct {
	projectPath string
	base        *os.Root
	root        *os.Root
	projectInfo os.FileInfo
	steps       []pinnedLegacyStep
	handles     []*os.Root
}

func openPinnedLegacyRecords(
	projectPath string,
) (*pinnedLegacyRecords, bool, error) {
	projectInfo, err := os.Lstat(projectPath)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("worktree: project root is unsafe")
	}
	base, err := os.OpenRoot(projectPath)
	if err != nil {
		return nil, false, errors.New("worktree: open project root failed")
	}
	pinned := &pinnedLegacyRecords{
		projectPath: projectPath,
		base:        base,
		projectInfo: projectInfo,
	}
	current := base
	for _, name := range []string{
		identity.LegacyDirName,
		"worktrees",
		"v1",
		"records",
	} {
		info, err := current.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			_ = pinned.Close()
			return nil, false, nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = pinned.Close()
			return nil, false, errors.New("worktree: legacy record path is unsafe")
		}
		child, err := current.OpenRoot(name)
		if err != nil {
			_ = pinned.Close()
			return nil, false, errors.New("worktree: open legacy record path failed")
		}
		opened, err := child.Stat(".")
		if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
			_ = child.Close()
			_ = pinned.Close()
			return nil, false, errors.New("worktree: legacy record path changed")
		}
		pinned.steps = append(pinned.steps, pinnedLegacyStep{name: name, info: info})
		pinned.handles = append(pinned.handles, child)
		current = child
	}
	pinned.root = current
	if !pinned.revalidate() {
		_ = pinned.Close()
		return nil, false, errors.New("worktree: legacy record path changed")
	}
	return pinned, true, nil
}

func (pinned *pinnedLegacyRecords) revalidate() bool {
	if pinned == nil || pinned.base == nil || pinned.root == nil || pinned.projectInfo == nil {
		return false
	}
	openedProject, openErr := pinned.base.Stat(".")
	currentProject, pathErr := os.Lstat(pinned.projectPath)
	if openErr != nil || pathErr != nil ||
		!openedProject.IsDir() || !currentProject.IsDir() ||
		openedProject.Mode()&os.ModeSymlink != 0 || currentProject.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(pinned.projectInfo, openedProject) ||
		!os.SameFile(pinned.projectInfo, currentProject) {
		return false
	}
	current := pinned.base
	temporary := make([]*os.Root, 0, len(pinned.steps))
	defer func() {
		for index := len(temporary) - 1; index >= 0; index-- {
			_ = temporary[index].Close()
		}
	}()
	for _, step := range pinned.steps {
		info, err := current.Lstat(step.name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(step.info, info) {
			return false
		}
		child, err := current.OpenRoot(step.name)
		if err != nil {
			return false
		}
		opened, err := child.Stat(".")
		if err != nil || !opened.IsDir() || !os.SameFile(step.info, opened) {
			_ = child.Close()
			return false
		}
		temporary = append(temporary, child)
		current = child
	}
	opened, err := pinned.root.Stat(".")
	return err == nil && opened.IsDir() &&
		os.SameFile(pinned.steps[len(pinned.steps)-1].info, opened)
}

func (pinned *pinnedLegacyRecords) Close() error {
	if pinned == nil {
		return nil
	}
	var closeErr error
	for index := len(pinned.handles) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, pinned.handles[index].Close())
	}
	pinned.handles = nil
	pinned.root = nil
	if pinned.base != nil {
		closeErr = errors.Join(closeErr, pinned.base.Close())
		pinned.base = nil
	}
	return closeErr
}
