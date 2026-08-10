package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGit struct {
	mu sync.Mutex

	projectRoot string
	commonDir   string
	baseCommit  string
	branches    map[string]string
	worktrees   map[string]Inspection
	status      map[string]string
	commits     map[string]int
	diffs       map[string]string

	addCalls        int
	removeCalls     int
	restoreCalls    int
	deleteCalls     int
	repositoryCalls int
	addWaitForCtx   bool
	addEntered      chan struct{}
	addRelease      <-chan struct{}
	addInFlight     int
	maxAddFlight    int
	addErr          error
	removeErr       error
	restoreErr      error
	deleteErr       error
	advanceOnRemove bool
	repositoryErr   error
	inspectionErr   error
	statusErr       error
	countErr        error
	diffErr         error
}

func newFakeGit(t *testing.T, projectRoot string) *fakeGit {
	t.Helper()
	commonDir := filepath.Join(projectRoot, ".git")
	if err := os.MkdirAll(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &fakeGit{
		projectRoot: projectRoot,
		commonDir:   commonDir,
		baseCommit:  strings.Repeat("a", 40),
		branches:    make(map[string]string),
		worktrees:   make(map[string]Inspection),
		status:      make(map[string]string),
		commits:     make(map[string]int),
		diffs:       make(map[string]string),
	}
}

func (g *fakeGit) Repository(ctx context.Context, cwd string) (Repository, error) {
	if err := ctx.Err(); err != nil {
		return Repository{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repositoryCalls++
	if g.repositoryErr != nil {
		return Repository{}, g.repositoryErr
	}
	if inspection, ok := g.worktrees[cwd]; ok {
		return inspection.Repository, nil
	}
	return Repository{Root: g.projectRoot, CommonDir: g.commonDir}, nil
}

func (g *fakeGit) ResolveCommit(
	ctx context.Context,
	_ string,
	_ string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.baseCommit, nil
}

func (g *fakeGit) BranchCommit(
	ctx context.Context,
	_ string,
	branch string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	commit, ok := g.branches[branch]
	return commit, ok, nil
}

func (g *fakeGit) AddWorktree(
	ctx context.Context,
	_ string,
	path string,
	branch string,
	baseCommit string,
) error {
	g.mu.Lock()
	g.addCalls++
	wait := g.addWaitForCtx
	entered := g.addEntered
	release := g.addRelease
	err := g.addErr
	g.addInFlight++
	if g.addInFlight > g.maxAddFlight {
		g.maxAddFlight = g.addInFlight
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.addInFlight--
		g.mu.Unlock()
	}()
	if entered != nil {
		entered <- struct{}{}
	}
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.branches[branch] = baseCommit
	g.worktrees[path] = Inspection{
		Repository: Repository{Root: path, CommonDir: g.commonDir},
		Head:       baseCommit,
		Branch:     branch,
	}
	return nil
}

func (g *fakeGit) InspectWorktree(
	ctx context.Context,
	path string,
) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inspectionErr != nil {
		return Inspection{}, g.inspectionErr
	}
	inspection, ok := g.worktrees[path]
	if !ok {
		return Inspection{}, errors.New("worktree not found")
	}
	return inspection, nil
}

func (g *fakeGit) StatusPorcelain(
	ctx context.Context,
	path string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.statusErr != nil {
		return "", g.statusErr
	}
	return g.status[path], nil
}

func (g *fakeGit) CountCommits(
	ctx context.Context,
	path string,
	_ string,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.countErr != nil {
		return 0, g.countErr
	}
	return g.commits[path], nil
}

func (g *fakeGit) Diff(
	ctx context.Context,
	path string,
	_ string,
	maxBytes int,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.diffErr != nil {
		return "", false, g.diffErr
	}
	diff := g.diffs[path]
	if len(diff) <= maxBytes {
		return diff, false, nil
	}
	return diff[:maxBytes], true, nil
}

func (g *fakeGit) RemoveWorktree(
	ctx context.Context,
	_ string,
	path string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.removeCalls++
	err := g.removeErr
	if err == nil {
		if g.advanceOnRemove {
			inspection := g.worktrees[path]
			advanced := strings.Repeat("b", 40)
			inspection.Head = advanced
			g.branches[inspection.Branch] = advanced
		}
		delete(g.worktrees, path)
	}
	g.mu.Unlock()
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func (g *fakeGit) RestoreWorktree(
	ctx context.Context,
	_ string,
	path string,
	branch string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.restoreCalls++
	if g.restoreErr != nil {
		return g.restoreErr
	}
	commit, exists := g.branches[branch]
	if !exists {
		return errors.New("restore branch not found")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	g.worktrees[path] = Inspection{
		Repository: Repository{Root: path, CommonDir: g.commonDir},
		Head:       commit,
		Branch:     branch,
	}
	return nil
}

func (g *fakeGit) DeleteBranch(
	ctx context.Context,
	_ string,
	branch string,
	expectedCommit string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteCalls++
	if g.deleteErr != nil {
		return g.deleteErr
	}
	if g.branches[branch] != expectedCommit {
		return errors.New("branch compare-and-delete mismatch")
	}
	delete(g.branches, branch)
	return nil
}

func testOwner(id string) Owner {
	return Owner{
		Kind:            OwnerAgent,
		ID:              id,
		SessionID:       "session-" + id,
		ThreadID:        "thread-" + id,
		ParentSessionID: "parent-session",
		ParentThreadID:  "parent-thread",
	}
}

func recoveryRecord(
	t *testing.T,
	service *Service,
	git *fakeGit,
	id string,
	owner Owner,
	state State,
) Record {
	t.Helper()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	return Record{
		Version:            RecordVersion,
		ID:                 id,
		Owner:              owner,
		RepositoryIdentity: git.commonDir,
		RepoRoot:           git.projectRoot,
		Path:               filepath.Join(service.managedRoot, id),
		Branch:             managedBranchPrefix + id,
		BaseCommit:         git.baseCommit,
		State:              state,
		Revision:           1,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func TestNewWorktreesUseYHCRootAndBranchPrefix(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "yhc-record" },
	})

	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(canonicalRoot, ".yhc", "worktrees", "v1")
	if service.StoreRoot() != filepath.Join(wantRoot, "records") ||
		service.ManagedRoot() != filepath.Join(wantRoot, "trees") {
		t.Fatalf(
			"store=%q managed=%q, want root %q",
			service.StoreRoot(),
			service.ManagedRoot(),
			wantRoot,
		)
	}
	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("yhc-record"),
		SourceDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Branch != "yhc/worktree/yhc-record" {
		t.Fatalf("branch = %q", record.Branch)
	}
	if _, err := os.Lstat(filepath.Join(root, ".eino-agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new service touched legacy root: %v", err)
	}
}

func TestServiceDiscoverRestoresMetadataWithoutGit(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{ProjectRoot: root, Git: git})
	owner := testOwner("recover")
	ready := recoveryRecord(
		t,
		service,
		git,
		"ready-record",
		owner,
		StateReady,
	)
	if err := os.MkdirAll(ready.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Create(t.Context(), ready); err != nil {
		t.Fatal(err)
	}
	removing := recoveryRecord(
		t,
		service,
		git,
		"removing-record",
		owner,
		StateRemoving,
	)
	if err := os.MkdirAll(removing.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Create(t.Context(), removing); err != nil {
		t.Fatal(err)
	}
	missing := recoveryRecord(
		t,
		service,
		git,
		"missing-record",
		owner,
		StateRetained,
	)
	if _, err := service.store.Create(t.Context(), missing); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(service.store.Root(), "corrupt.json"),
		[]byte("{"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	unknown := ready
	unknown.ID = "unknown-version"
	unknown.Version = RecordVersion + 1
	unknown.Path = filepath.Join(service.managedRoot, unknown.ID)
	unknown.Branch = managedBranchPrefix + unknown.ID
	unknownData, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(service.store.Root(), unknown.ID+".json"),
		unknownData,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	symlinkCreated := os.Symlink(
		filepath.Join(service.store.Root(), ready.ID+".json"),
		filepath.Join(service.store.Root(), "linked.json"),
	) == nil

	discovery, err := service.Discover(t.Context())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if git.repositoryCalls != 0 {
		t.Fatalf("discovery executed %d Git repository calls", git.repositoryCalls)
	}
	if len(discovery.Records) != 3 {
		t.Fatalf("records = %#v", discovery.Records)
	}
	dispositions := make(map[string]RecoveryDisposition)
	for _, recovered := range discovery.Records {
		dispositions[recovered.Record.ID] = recovered.Disposition
	}
	if dispositions[ready.ID] != RecoveryInspectOnly ||
		dispositions[removing.ID] != RecoveryPending ||
		dispositions[missing.ID] != RecoveryUnavailable {
		t.Fatalf("dispositions = %#v", dispositions)
	}
	wantDiagnostics := map[string]bool{
		"corrupt":         false,
		"unknown-version": false,
	}
	if symlinkCreated {
		wantDiagnostics["linked"] = false
	}
	for _, diagnostic := range discovery.Diagnostics {
		if _, expected := wantDiagnostics[diagnostic.RecordID]; expected {
			wantDiagnostics[diagnostic.RecordID] = true
		}
	}
	for id, found := range wantDiagnostics {
		if !found {
			t.Fatalf(
				"missing diagnostic %q in %#v",
				id,
				discovery.Diagnostics,
			)
		}
	}
	if len(discovery.Diagnostics) != len(wantDiagnostics) {
		t.Fatalf("diagnostics = %#v", discovery.Diagnostics)
	}
}

func TestServiceRecoverInterruptedCreationForContinuation(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{ProjectRoot: root, Git: git})
	owner := testOwner("recover-create")
	record := recoveryRecord(
		t,
		service,
		git,
		"recover-create",
		owner,
		StateCreating,
	)
	if err := os.MkdirAll(record.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	git.worktrees[record.Path] = Inspection{
		Repository: Repository{Root: record.Path, CommonDir: git.commonDir},
		Head:       record.BaseCommit,
		Branch:     record.Branch,
	}
	git.branches[record.Branch] = record.BaseCommit
	if _, err := service.store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}

	recovered, err := service.RecoverForContinuation(
		t.Context(),
		record.ID,
		owner,
	)
	if err != nil {
		t.Fatalf("recover continuation: %v", err)
	}
	if recovered.State != StateReady || recovered.Revision != 2 {
		t.Fatalf("recovered = %#v", recovered)
	}
	again, err := service.RecoverForContinuation(
		t.Context(),
		record.ID,
		owner,
	)
	if err != nil || again.State != StateReady ||
		again.Revision != recovered.Revision {
		t.Fatalf("idempotent recovery = %#v, err=%v", again, err)
	}
}

func TestServiceRecoveryRejectsOwnerBeforeGit(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{ProjectRoot: root, Git: git})
	record := recoveryRecord(
		t,
		service,
		git,
		"owner-check",
		testOwner("source"),
		StateReady,
	)
	if _, err := service.store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	_, err := service.RecoverForContinuation(
		t.Context(),
		record.ID,
		testOwner("fork"),
	)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("owner mismatch error = %v", err)
	}
	if git.repositoryCalls != 0 {
		t.Fatalf("owner mismatch executed %d Git calls", git.repositoryCalls)
	}
}

func TestServiceRecoveryFailsClosedOnUnavailableGitIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeGit, Record)
	}{
		{
			name: "missing path",
			mutate: func(git *fakeGit, record Record) {
				_ = os.RemoveAll(record.Path)
				delete(git.worktrees, record.Path)
			},
		},
		{
			name: "manual branch change",
			mutate: func(git *fakeGit, record Record) {
				inspection := git.worktrees[record.Path]
				inspection.Branch = "manual-branch"
				git.worktrees[record.Path] = inspection
			},
		},
		{
			name: "repository mismatch",
			mutate: func(git *fakeGit, record Record) {
				inspection := git.worktrees[record.Path]
				inspection.CommonDir = filepath.Join(
					git.projectRoot,
					"other.git",
				)
				git.worktrees[record.Path] = inspection
			},
		},
		{
			name: "unknown status",
			mutate: func(git *fakeGit, _ Record) {
				git.statusErr = errors.New("status unavailable")
			},
		},
		{
			name: "dirty status",
			mutate: func(git *fakeGit, record Record) {
				git.status[record.Path] = " M changed.go\n"
			},
		},
		{
			name: "new commit",
			mutate: func(git *fakeGit, record Record) {
				advanced := strings.Repeat("b", 40)
				inspection := git.worktrees[record.Path]
				inspection.Head = advanced
				git.worktrees[record.Path] = inspection
				git.branches[record.Branch] = advanced
				git.commits[record.Path] = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			git := newFakeGit(t, root)
			service := NewService(ServiceConfig{
				ProjectRoot: root,
				Git:         git,
				ID:          func() string { return "recover-unavailable" },
			})
			owner := testOwner("recover-unavailable")
			record, err := service.Create(
				t.Context(),
				CreateRequest{Owner: owner, SourceDir: root},
			)
			if err != nil {
				t.Fatal(err)
			}
			git.mu.Lock()
			test.mutate(git, record)
			git.mu.Unlock()

			recovered, err := service.RecoverForContinuation(
				t.Context(),
				record.ID,
				owner,
			)
			if err == nil {
				t.Fatalf("recovery unexpectedly succeeded: %#v", recovered)
			}
			persisted, found, getErr := service.Get(
				t.Context(),
				record.ID,
			)
			if getErr != nil || !found ||
				persisted.State != record.State ||
				persisted.Revision != record.Revision {
				t.Fatalf(
					"failed recovery mutated record: %#v found=%v err=%v",
					persisted,
					found,
					getErr,
				)
			}
			if git.removeCalls != 0 || git.deleteCalls != 0 {
				t.Fatalf(
					"failed recovery mutated Git: remove=%d delete=%d",
					git.removeCalls,
					git.deleteCalls,
				)
			}
		})
	}
}

func TestServiceRetryInterruptedCleanupFailsClosedWhenDirty(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "retry-dirty" },
	})
	owner := testOwner("retry-dirty")
	record, err := service.Create(
		t.Context(),
		CreateRequest{Owner: owner, SourceDir: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err = service.transition(
		record.ID,
		StateReady,
		StateRemoving,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	git.status[record.Path] = " M changed.go\n"

	retried, err := service.RetryCleanup(t.Context(), record.ID, owner)
	if err == nil {
		t.Fatal("dirty retry should report the retained cleanup failure")
	}
	if retried.State != StateCleanupFailed ||
		retried.ResultDirtyReport == nil ||
		!retried.ResultDirtyReport.Dirty {
		t.Fatalf("retried = %#v", retried)
	}
	if git.removeCalls != 0 || git.deleteCalls != 0 {
		t.Fatalf(
			"dirty retry mutated Git: remove=%d delete=%d",
			git.removeCalls,
			git.deleteCalls,
		)
	}
}

func TestServiceRetryCleanupRejectsForkOwnerBeforeGit(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "fork-cleanup" },
	})
	owner := testOwner("fork-cleanup")
	record, err := service.Create(
		t.Context(),
		CreateRequest{Owner: owner, SourceDir: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	forkOwner := owner
	forkOwner.ParentSessionID = "fork-session"
	beforeRepositoryCalls := git.repositoryCalls
	retried, err := service.RetryCleanup(
		t.Context(),
		record.ID,
		forkOwner,
	)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("fork cleanup = %#v, err=%v", retried, err)
	}
	if git.repositoryCalls != beforeRepositoryCalls ||
		git.removeCalls != 0 ||
		git.deleteCalls != 0 {
		t.Fatalf(
			"fork cleanup reached Git: repo=%d remove=%d delete=%d",
			git.repositoryCalls-beforeRepositoryCalls,
			git.removeCalls,
			git.deleteCalls,
		)
	}
}

func TestServiceRetryInterruptedCleanupRemovesCleanOwnedWorktree(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "retry-clean" },
	})
	owner := testOwner("retry-clean")
	record, err := service.Create(
		t.Context(),
		CreateRequest{Owner: owner, SourceDir: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.transition(
		record.ID,
		StateReady,
		StateRemoving,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryCleanup(t.Context(), record.ID, owner)
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if retried.State != StateRemoved {
		t.Fatalf("retried state = %s", retried.State)
	}
	again, err := service.RetryCleanup(t.Context(), record.ID, owner)
	if err != nil || again.State != StateRemoved {
		t.Fatalf("idempotent cleanup = %#v, err=%v", again, err)
	}
	if git.removeCalls != 1 || git.deleteCalls != 1 {
		t.Fatalf(
			"cleanup calls: remove=%d delete=%d",
			git.removeCalls,
			git.deleteCalls,
		)
	}
}

func TestServiceConcurrentCleanupRetryIsIdempotent(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "retry-concurrent" },
	})
	owner := testOwner("retry-concurrent")
	record, err := service.Create(
		t.Context(),
		CreateRequest{Owner: owner, SourceDir: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.transition(
		record.ID,
		StateReady,
		StateRemoving,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan Record, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, retryErr := service.RetryCleanup(
				context.Background(),
				record.ID,
				owner,
			)
			results <- result
			errs <- retryErr
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for retryErr := range errs {
		if retryErr != nil {
			t.Errorf("concurrent retry: %v", retryErr)
		}
	}
	for result := range results {
		if result.State != StateRemoved {
			t.Errorf("concurrent result = %#v", result)
		}
	}
	if git.removeCalls != 1 || git.deleteCalls != 1 {
		t.Fatalf(
			"concurrent cleanup calls: remove=%d delete=%d",
			git.removeCalls,
			git.deleteCalls,
		)
	}
}

func TestServiceRetryCleanupClassifiesInterruptedCreation(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{ProjectRoot: root, Git: git})
	owner := testOwner("retry-creating")
	record := recoveryRecord(
		t,
		service,
		git,
		"retry-creating",
		owner,
		StateCreating,
	)
	if err := os.MkdirAll(record.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	git.worktrees[record.Path] = Inspection{
		Repository: Repository{Root: record.Path, CommonDir: git.commonDir},
		Head:       record.BaseCommit,
		Branch:     record.Branch,
	}
	git.branches[record.Branch] = record.BaseCommit
	if _, err := service.store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryCleanup(
		t.Context(),
		record.ID,
		owner,
	)
	if err != nil {
		t.Fatalf("retry interrupted creation cleanup: %v", err)
	}
	if retried.State != StateRemoved ||
		git.removeCalls != 1 ||
		git.deleteCalls != 1 {
		t.Fatalf(
			"retry result=%#v remove=%d delete=%d",
			retried,
			git.removeCalls,
			git.deleteCalls,
		)
	}
}

func TestServiceCreatePersistsValidatedReadyLifecycle(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	var transitions []Transition
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "worktree-1" },
		Clock: func() time.Time {
			return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
		},
		TransitionSink: func(_ context.Context, transition Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})

	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("agent-1"),
		SourceDir: root,
	})
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if record.State != StateReady || record.Revision != 3 {
		t.Fatalf("ready record = %#v", record)
	}
	if record.RepositoryIdentity != git.commonDir ||
		record.BaseCommit != git.baseCommit ||
		record.Branch != "yhc/worktree/worktree-1" {
		t.Fatalf("ready identity = %#v", record)
	}
	if len(transitions) != 3 ||
		transitions[0].Record.State != StateCreating ||
		transitions[1].From != StateCreating ||
		transitions[1].Record.State != StateCreating ||
		transitions[2].From != StateCreating ||
		transitions[2].Record.State != StateReady {
		t.Fatalf("transitions = %#v", transitions)
	}
	persisted, found, err := service.Get(t.Context(), record.ID)
	if err != nil || !found || persisted.State != StateReady {
		t.Fatalf("persisted record = %#v, found=%v, err=%v", persisted, found, err)
	}
}

func TestServiceCreateCollisionFailsBeforeGitMutation(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "collision" },
	})
	if err := os.MkdirAll(filepath.Join(service.ManagedRoot(), "collision"), 0o700); err != nil {
		t.Fatal(err)
	}

	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("agent-collision"),
		SourceDir: root,
	})
	if err == nil {
		t.Fatal("expected collision error")
	}
	if record.State != StateFailed ||
		record.LastErrorCategory != ErrorCategoryCollision {
		t.Fatalf("collision record = %#v, err=%v", record, err)
	}
	if git.addCalls != 0 {
		t.Fatalf("Git add calls = %d, want 0", git.addCalls)
	}
}

func TestServiceTimeoutPersistsFailedWithoutReady(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	git.addWaitForCtx = true
	var transitions []Transition
	service := NewService(ServiceConfig{
		ProjectRoot:      root,
		OperationTimeout: 20 * time.Millisecond,
		Git:              git,
		ID:               func() string { return "timeout" },
		TransitionSink: func(_ context.Context, transition Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})

	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("agent-timeout"),
		SourceDir: root,
	})
	if err == nil {
		t.Fatal("expected timeout")
	}
	if record.State != StateFailed ||
		record.LastErrorCategory != ErrorCategoryTimeout {
		t.Fatalf("timeout record = %#v, err=%v", record, err)
	}
	for _, transition := range transitions {
		if transition.Record.State == StateReady {
			t.Fatalf("unexpected Ready transition: %#v", transition)
		}
	}
	persisted, found, getErr := service.Get(t.Context(), record.ID)
	if getErr != nil || !found || persisted.State != StateFailed {
		t.Fatalf("persisted timeout record = %#v, found=%v, err=%v", persisted, found, getErr)
	}
}

func TestServiceCancellationPersistsCategorizedFailure(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	git.addWaitForCtx = true
	git.addEntered = make(chan struct{}, 1)
	service := NewService(ServiceConfig{
		ProjectRoot:      root,
		OperationTimeout: time.Second,
		Git:              git,
		ID:               func() string { return "cancelled" },
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Record, 1)
	errs := make(chan error, 1)
	go func() {
		record, err := service.Create(ctx, CreateRequest{
			Owner:     testOwner("agent-cancelled"),
			SourceDir: root,
		})
		result <- record
		errs <- err
	}()
	<-git.addEntered
	cancel()
	record := <-result
	err := <-errs
	if err == nil ||
		record.State != StateFailed ||
		record.LastErrorCategory != ErrorCategoryCancelled {
		t.Fatalf("cancelled record = %#v, err=%v", record, err)
	}
}

func TestServiceBranchCollisionFailsBeforeGitMutation(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	git.branches["yhc/worktree/foreign"] = strings.Repeat("b", 40)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "foreign" },
	})
	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("agent-foreign"),
		SourceDir: root,
	})
	if err == nil ||
		record.State != StateFailed ||
		record.LastErrorCategory != ErrorCategoryCollision {
		t.Fatalf("branch collision record = %#v, err=%v", record, err)
	}
	if git.addCalls != 0 {
		t.Fatalf("Git add calls = %d, want 0", git.addCalls)
	}
}

func TestServiceConcurrentIdentityDoesNotReuseSameName(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	var idMu sync.Mutex
	nextID := 0
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("same-name-%d", nextID)
		},
	})
	records := make(chan Record, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			record, err := service.Create(context.Background(), CreateRequest{
				Owner:     testOwner(fmt.Sprintf("agent-%d", index)),
				SourceDir: root,
			})
			records <- record
			errs <- err
		}(index)
	}
	wait.Wait()
	close(records)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}
	var created []Record
	for record := range records {
		created = append(created, record)
	}
	if len(created) != 2 ||
		created[0].ID == created[1].ID ||
		created[0].Path == created[1].Path ||
		created[0].Branch == created[1].Branch {
		t.Fatalf("concurrent identities = %#v", created)
	}
}

func TestServiceDoesNotSerializeDifferentRecordsAcrossGit(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	git.addEntered = entered
	git.addRelease = release
	var idMu sync.Mutex
	nextID := 0
	service := NewService(ServiceConfig{
		ProjectRoot:      root,
		OperationTimeout: time.Second,
		Git:              git,
		ID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("parallel-%d", nextID)
		},
	})
	errs := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			_, err := service.Create(context.Background(), CreateRequest{
				Owner:     testOwner(fmt.Sprintf("parallel-agent-%d", index)),
				SourceDir: root,
			})
			errs <- err
		}(index)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("different records did not reach Git concurrently")
		}
	}
	git.mu.Lock()
	maxInFlight := git.maxAddFlight
	git.mu.Unlock()
	if maxInFlight != 2 {
		t.Fatalf("max concurrent Git adds = %d, want 2", maxInFlight)
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-errs; err != nil {
			t.Fatalf("parallel create: %v", err)
		}
	}
}

func TestServiceRemoveRetainsDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "dirty" },
	})
	owner := testOwner("agent-dirty")
	record, err := service.Create(t.Context(), CreateRequest{Owner: owner, SourceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	git.mu.Lock()
	git.status[record.Path] = " M changed.go\n?? created.go\n!! ignored.log"
	git.commits[record.Path] = 1
	git.diffs[record.Path] = "diff --git a/changed.go b/changed.go\n"
	git.mu.Unlock()

	retained, err := service.Remove(t.Context(), record.ID, owner)
	if err == nil {
		t.Fatal("expected dirty retention error")
	}
	if retained.State != StateRetained ||
		retained.LastErrorCategory != ErrorCategoryDirty ||
		retained.ResultDirtyReport == nil ||
		!retained.ResultDirtyReport.Dirty ||
		retained.ResultDirtyReport.NewCommits != 1 ||
		len(retained.ResultDirtyReport.ChangedFiles) != 3 ||
		retained.ResultDirtyReport.Patch == "" ||
		!retained.ResultDirtyReport.PatchTruncated {
		t.Fatalf("retained record = %#v", retained)
	}
	if git.removeCalls != 0 || git.deleteCalls != 0 {
		t.Fatalf("cleanup calls remove=%d delete=%d", git.removeCalls, git.deleteCalls)
	}
}

func TestServiceCreateRejectsDirtySourceBeforeGitMutation(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	git.status[root] = "?? local-only.txt"
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "dirty-source" },
	})

	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     testOwner("agent-dirty-source"),
		SourceDir: root,
	})
	if err == nil || !strings.Contains(err.Error(), "ignore_dirty") {
		t.Fatalf("dirty source error = %v", err)
	}
	if record.State != StateFailed ||
		record.LastErrorCategory != ErrorCategoryDirty ||
		record.SourceDirtyReport == nil ||
		!record.SourceDirtyReport.Dirty ||
		len(record.SourceDirtyReport.ChangedFiles) != 1 {
		t.Fatalf("dirty source record = %#v", record)
	}
	if git.addCalls != 0 {
		t.Fatalf("dirty source reached Git mutation: %d", git.addCalls)
	}
}

func TestServiceCreateIgnoreDirtyRecordsOmittedSource(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	git.status[root] = " M local.txt\n?? local-only.txt"
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "ignored-source" },
	})

	record, err := service.Create(t.Context(), CreateRequest{
		Owner:      testOwner("agent-ignore-source"),
		SourceDir:  root,
		SourceMode: SourceIgnoreDirty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateReady ||
		record.SourceDirtyReport == nil ||
		!record.SourceDirtyReport.Dirty ||
		len(record.SourceDirtyReport.ChangedFiles) != 2 ||
		record.SourceDirtyReport.Patch != "" {
		t.Fatalf("ignored source record = %#v", record)
	}
	if git.addCalls != 1 {
		t.Fatalf("worktree add calls = %d, want 1", git.addCalls)
	}
}

func TestChangedFilesFromPorcelainExcludesLifecycleMetadata(t *testing.T) {
	files, truncated, patchIncomplete := changedFilesFromPorcelain(
		"?? .yhc/worktrees/v1/records/record.json\n?? user.txt",
		[]string{".yhc/worktrees/v1/records"},
	)
	if truncated || !patchIncomplete ||
		len(files) != 1 || files[0] != "user.txt" {
		t.Fatalf(
			"files=%#v truncated=%v patchIncomplete=%v",
			files,
			truncated,
			patchIncomplete,
		)
	}
}

func TestChangedFilesFromPorcelainUsesRenameDestinationAndPreservesArrowInFilename(t *testing.T) {
	files, truncated, patchIncomplete := changedFilesFromPorcelain(
		"R  \"old name.go\" -> \"new name.go\"\n M \"literal -> arrow.go\"",
		nil,
	)
	if truncated || patchIncomplete ||
		len(files) != 2 ||
		files[0] != "new name.go" ||
		files[1] != "literal -> arrow.go" {
		t.Fatalf(
			"files=%#v truncated=%v patchIncomplete=%v",
			files,
			truncated,
			patchIncomplete,
		)
	}
}

func TestServiceCleanupFailureRetainsRetryableRecord(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "retry" },
	})
	owner := testOwner("agent-retry")
	record, err := service.Create(t.Context(), CreateRequest{Owner: owner, SourceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	git.mu.Lock()
	git.removeErr = errors.New("injected remove failure")
	git.mu.Unlock()

	failed, err := service.Remove(t.Context(), record.ID, owner)
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	if failed.State != StateCleanupFailed ||
		failed.Owner != owner ||
		failed.Path != record.Path ||
		failed.BaseCommit != record.BaseCommit {
		t.Fatalf("cleanup-failed record = %#v", failed)
	}
	git.mu.Lock()
	git.removeErr = nil
	git.mu.Unlock()
	removed, err := service.Remove(t.Context(), record.ID, owner)
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if removed.State != StateRemoved {
		t.Fatalf("retry record = %#v", removed)
	}
}

func TestServiceRestoreFailureRetainsRecoveryMetadata(t *testing.T) {
	root := t.TempDir()
	git := newFakeGit(t, root)
	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         git,
		ID:          func() string { return "restore-failure" },
	})
	owner := testOwner("agent-restore-failure")
	record, err := service.Create(t.Context(), CreateRequest{Owner: owner, SourceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	git.mu.Lock()
	git.advanceOnRemove = true
	git.restoreErr = errors.New("injected restore failure")
	git.mu.Unlock()

	failed, err := service.Remove(t.Context(), record.ID, owner)
	if err == nil {
		t.Fatal("expected raced cleanup restore failure")
	}
	if failed.State != StateCleanupFailed ||
		failed.LastErrorCategory != ErrorCategoryDirty ||
		failed.Owner != owner ||
		failed.Path != record.Path ||
		failed.Branch != record.Branch ||
		failed.BaseCommit != record.BaseCommit ||
		!strings.Contains(failed.LastError, "injected restore failure") {
		t.Fatalf("restore-failed record = %#v, err=%v", failed, err)
	}
	git.mu.Lock()
	branchCommit := git.branches[record.Branch]
	git.mu.Unlock()
	if branchCommit == "" || branchCommit == record.BaseCommit {
		t.Fatalf("advanced recovery branch = %q", branchCommit)
	}
}

func TestStoreIgnoresPartialTempAndRejectsUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".partial.json.tmp-1"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(t.Context(), "partial"); err != nil || found {
		t.Fatalf("partial temp found=%v, err=%v", found, err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "future.json"),
		[]byte(`{"version":2,"id":"future"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(t.Context(), "future"); err == nil ||
		!strings.Contains(err.Error(), "unsupported record version") {
		t.Fatalf("unsupported version error = %v", err)
	}
}

func TestStoreListTreatsExistingEmptyDirectoryAsEmpty(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	records, diagnostics, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("list empty store: %v", err)
	}
	if len(records) != 0 || len(diagnostics) != 0 {
		t.Fatalf(
			"records=%#v, diagnostics=%#v",
			records,
			diagnostics,
		)
	}
}

func TestCommandGitServiceProcessLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	runTestGit(t, root, "init", "-b", "master")
	runTestGit(t, root, "config", "user.email", "test@example.com")
	runTestGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".gitignore"),
		[]byte(".yhc/\n.eino-agent/\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "tracked.txt", ".gitignore")
	runTestGit(t, root, "commit", "-m", "base")

	service := NewService(ServiceConfig{
		ProjectRoot: root,
		ID:          func() string { return "process" },
	})
	owner := testOwner("agent-process")
	record, err := service.Create(t.Context(), CreateRequest{Owner: owner, SourceDir: root})
	if err != nil {
		t.Fatalf("real Git create: %v", err)
	}
	if record.State != StateReady {
		t.Fatalf("real Git record = %#v", record)
	}
	removed, err := service.Remove(t.Context(), record.ID, owner)
	if err != nil {
		t.Fatalf("real Git cleanup: %v", err)
	}
	if removed.State != StateRemoved {
		t.Fatalf("real Git cleanup record = %#v", removed)
	}
	if _, err := os.Stat(record.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path still exists: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, ".eino-agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".eino-agent", "user-ignored.txt"),
		[]byte("user ignored change\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	service = NewService(ServiceConfig{
		ProjectRoot: root,
		ID:          func() string { return "process-ignore-dirty" },
	})
	record, err = service.Create(t.Context(), CreateRequest{
		Owner:      owner,
		SourceDir:  root,
		SourceMode: SourceIgnoreDirty,
	})
	if err != nil {
		t.Fatalf("real Git ignore-dirty create: %v", err)
	}
	report := record.SourceDirtyReport
	if report == nil || !report.Dirty ||
		!slices.Contains(report.ChangedFiles, ".eino-agent/user-ignored.txt") {
		t.Fatalf("source dirty report = %#v", report)
	}
	for _, changedFile := range report.ChangedFiles {
		if strings.HasPrefix(changedFile, ".yhc/worktrees/") {
			t.Fatalf("service-owned file leaked into source report: %#v", report)
		}
	}
	removed, err = service.Remove(t.Context(), record.ID, owner)
	if err != nil {
		t.Fatalf("real Git ignore-dirty cleanup: %v", err)
	}
	if removed.State != StateRemoved {
		t.Fatalf("real Git ignore-dirty cleanup record = %#v", removed)
	}
}

type commitBeforeRemoveGit struct {
	CommandGit
	t    *testing.T
	once sync.Once
}

func (g *commitBeforeRemoveGit) RemoveWorktree(
	ctx context.Context,
	repoRoot string,
	path string,
) error {
	g.once.Do(func() {
		if err := os.WriteFile(
			filepath.Join(path, "raced.txt"),
			[]byte("raced commit\n"),
			0o600,
		); err != nil {
			g.t.Fatal(err)
		}
		runTestGit(g.t, path, "add", "raced.txt")
		runTestGit(g.t, path, "commit", "-m", "raced commit")
	})
	return g.CommandGit.RemoveWorktree(ctx, repoRoot, path)
}

func TestCommandGitCleanupRestoresPathWhenCommitRacesRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	runTestGit(t, root, "init", "-b", "master")
	runTestGit(t, root, "config", "user.email", "test@example.com")
	runTestGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "tracked.txt")
	runTestGit(t, root, "commit", "-m", "base")

	service := NewService(ServiceConfig{
		ProjectRoot: root,
		Git:         &commitBeforeRemoveGit{t: t},
		ID:          func() string { return "raced-commit" },
	})
	owner := testOwner("agent-raced-commit")
	record, err := service.Create(t.Context(), CreateRequest{
		Owner:     owner,
		SourceDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.Remove(t.Context(), record.ID, owner)
	if err == nil {
		t.Fatal("expected raced commit to stop terminal cleanup")
	}
	if failed.State != StateCleanupFailed ||
		failed.LastErrorCategory != ErrorCategoryDirty {
		t.Fatalf("raced cleanup record = %#v, err=%v", failed, err)
	}
	if _, err := os.Stat(record.Path); err != nil {
		t.Fatalf("raced worktree path was not restored: %v", err)
	}
	count, err := CommandGit{}.CountCommits(t.Context(), record.Path, record.BaseCommit)
	if err != nil || count != 1 {
		t.Fatalf("restored commit count = %d, err=%v", count, err)
	}
	head, err := CommandGit{}.ResolveCommit(t.Context(), record.Path, "HEAD")
	if err != nil || head == record.BaseCommit {
		t.Fatalf("restored HEAD = %q, base=%q, err=%v", head, record.BaseCommit, err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
}
