// Package statemigration imports one validated legacy state artifact into its
// canonical YHC owner without mutating or recursively discovering legacy state.
package statemigration

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/abietic/yhc/internal/statepath"
)

// Kind identifies the exact shape owned by one migration specification.
type Kind uint8

const (
	RegularFile Kind = iota
	DirectoryTree
)

// LegacyMode identifies the integrity policy accepted for an owner-declared
// legacy artifact. Canonical staged output always remains private.
type LegacyMode uint8

const (
	// LegacyPrivate accepts only owner-accessible legacy state.
	LegacyPrivate LegacyMode = iota
	// LegacyOwnerControlled additionally accepts historical read/execute bits
	// for group or other while still rejecting every non-owner write bit.
	LegacyOwnerControlled
)

// Snapshot is a bounded immutable view rooted at one declared legacy artifact.
// Regular files are opened as ".". Directory-tree paths use slash-separated
// fs.ValidPath names and Walk never leaves the declared SourceRel subtree.
type Snapshot interface {
	Open(relative string) (io.ReadCloser, fs.FileInfo, error)
	Walk(func(relative string, entry fs.DirEntry) error) error
	Digest() string
}

// ArtifactSpec delegates schema knowledge and transformation to one state
// owner. For RegularFile, Stage writes exactly path.Base(TargetRel) into the
// supplied empty staging root. For DirectoryTree, the staging root itself is
// the future TargetRel tree.
type ArtifactSpec struct {
	Owner      string
	Scope      string
	SourceRel  string
	TargetRel  string
	Kind       Kind
	LegacyMode LegacyMode
	MaxFiles   int
	MaxBytes   int64
	Validate   func(context.Context, Snapshot) error
	Stage      func(context.Context, Snapshot, *os.Root) error
	Quiescent  func(context.Context, Snapshot) (bool, error)
	// AcquireSourceLease optionally obtains an owner-defined, non-mutating
	// cross-process lease for the exact legacy artifact path. Import holds the
	// returned release function across recapture, staging, and promotion.
	// Inspect never invokes this callback.
	AcquireSourceLease func(context.Context, string) (release func(), acquired bool, err error)
}

// Status is a value-free migration outcome.
type Status string

const (
	StatusAbsent            Status = "absent"
	StatusReady             Status = "ready"
	StatusImported          Status = "imported"
	StatusDestinationExists Status = "destination_exists"
	StatusLegacyBusy        Status = "legacy_busy"
	StatusUnsafe            Status = "unsafe"
)

// Result contains only allowlisted diagnostic fields.
type Result struct {
	Owner  string
	Scope  string
	Status Status
}

// Importer performs path-only inspection or one atomic exact-artifact import.
type Importer struct{}

var (
	errMigrationUnsafe = errors.New("state migration is unsafe")
	ownerPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

type importerFailureStage string

const (
	failureAfterSourceSnapshot      importerFailureStage = "after-source-snapshot"
	failureAfterStagedWrite         importerFailureStage = "after-staged-write"
	failureAfterStageSync           importerFailureStage = "after-stage-sync"
	failureAfterTargetParentSync    importerFailureStage = "after-target-parent-sync"
	failureAfterRename              importerFailureStage = "after-rename"
	failureAfterPromotionTargetSync importerFailureStage = "after-promotion-target-sync"
	failureAfterPromotionSourceSync importerFailureStage = "after-promotion-source-sync"
)

type (
	importerFailureHook    func(importerFailureStage) error
	importerFailureHookKey struct{}
)

func withImporterFailureHook(ctx context.Context, hook importerFailureHook) context.Context {
	return context.WithValue(ctx, importerFailureHookKey{}, hook)
}

func fireImporterFailureHook(ctx context.Context, stage importerFailureStage) error {
	if ctx == nil {
		return nil
	}
	hook, _ := ctx.Value(importerFailureHookKey{}).(importerFailureHook)
	if hook == nil {
		return nil
	}
	if err := hook(stage); err != nil {
		return errMigrationUnsafe
	}
	return nil
}

// Inspect validates one exact artifact without creating roots, locks, or stage
// files and without invoking ArtifactSpec.Stage.
func (Importer) Inspect(
	ctx context.Context,
	roots statepath.Roots,
	spec ArtifactSpec,
) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(roots, spec, false); err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	legacy, snapshot, absent, err := openLegacySnapshot(roots.Legacy, spec)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if absent {
		return resultFor(spec, StatusAbsent), nil
	}
	defer legacy.Close() //nolint:errcheck
	if err := validateOwnerSnapshot(ctx, spec, snapshot); err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	collision, err := inspectCanonicalTarget(roots.Canonical, spec.TargetRel)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if collision {
		return resultFor(spec, StatusDestinationExists), nil
	}
	quiescent, err := ownerQuiescent(ctx, spec, snapshot)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if !quiescent {
		return resultFor(spec, StatusLegacyBusy), nil
	}
	return resultFor(spec, StatusReady), nil
}

// Import validates, stages, durably promotes, and returns one exact artifact.
func (Importer) Import(
	ctx context.Context,
	roots statepath.Roots,
	spec ArtifactSpec,
) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(roots, spec, true); err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	legacy, snapshot, absent, err := openLegacySnapshot(roots.Legacy, spec)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if absent {
		return resultFor(spec, StatusAbsent), nil
	}
	defer legacy.Close() //nolint:errcheck
	if err := validateOwnerSnapshot(ctx, spec, snapshot); err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	quiescent, err := ownerQuiescent(ctx, spec, snapshot)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if !quiescent {
		return resultFor(spec, StatusLegacyBusy), nil
	}
	release, acquired, err := acquireOwnerSourceLease(ctx, roots.Legacy, spec)
	if err != nil {
		return unsafeResult(spec), errMigrationUnsafe
	}
	if !acquired {
		return resultFor(spec, StatusLegacyBusy), nil
	}
	if release != nil {
		defer release()
	}
	if spec.AcquireSourceLease != nil {
		current, captureErr := captureSourceSnapshot(legacy, spec)
		if captureErr != nil || compareSnapshots(snapshot, current) != nil ||
			validateOwnerSnapshot(ctx, spec, current) != nil {
			return unsafeResult(spec), errMigrationUnsafe
		}
		quiescent, err = ownerQuiescent(ctx, spec, current)
		if err != nil {
			return unsafeResult(spec), errMigrationUnsafe
		}
		if !quiescent {
			return resultFor(spec, StatusLegacyBusy), nil
		}
	}

	status, err := importPrepared(ctx, roots.Canonical, legacy, snapshot, spec)
	if err != nil {
		return resultFor(spec, StatusUnsafe), errMigrationUnsafe
	}
	return resultFor(spec, status), nil
}

func acquireOwnerSourceLease(
	ctx context.Context,
	legacyRoot string,
	spec ArtifactSpec,
) (func(), bool, error) {
	if spec.AcquireSourceLease == nil {
		return nil, true, nil
	}
	source := filepath.Join(legacyRoot, filepath.FromSlash(spec.SourceRel))
	release, acquired, err := spec.AcquireSourceLease(ctx, source)
	if err != nil {
		return nil, false, errMigrationUnsafe
	}
	if !acquired {
		if release != nil {
			release()
		}
		return nil, false, nil
	}
	if release == nil {
		return nil, false, errMigrationUnsafe
	}
	return release, true, nil
}

func validateRequest(roots statepath.Roots, spec ArtifactSpec, requireStage bool) error {
	if err := validateStateRoots(roots); err != nil {
		return err
	}
	if !ownerPattern.MatchString(spec.Owner) || len(spec.Owner) > 64 ||
		(spec.Scope != "project" && spec.Scope != "user") ||
		!validArtifactRelative(spec.SourceRel) ||
		!validArtifactRelative(spec.TargetRel) ||
		(spec.Kind != RegularFile && spec.Kind != DirectoryTree) ||
		(spec.LegacyMode != LegacyPrivate && spec.LegacyMode != LegacyOwnerControlled) ||
		spec.MaxFiles <= 0 || spec.MaxBytes <= 0 || spec.Validate == nil ||
		(requireStage && spec.Stage == nil) {
		return errMigrationUnsafe
	}
	if spec.TargetRel == ".migration" || strings.HasPrefix(spec.TargetRel, ".migration/") {
		return errMigrationUnsafe
	}
	if spec.Kind == RegularFile && path.Base(spec.TargetRel) == "." {
		return errMigrationUnsafe
	}
	return nil
}

func validateOwnerSnapshot(ctx context.Context, spec ArtifactSpec, snapshot Snapshot) error {
	if err := ctx.Err(); err != nil {
		return errMigrationUnsafe
	}
	if err := spec.Validate(ctx, snapshot); err != nil {
		return errMigrationUnsafe
	}
	return nil
}

func ownerQuiescent(ctx context.Context, spec ArtifactSpec, snapshot Snapshot) (bool, error) {
	if spec.Quiescent == nil {
		return true, nil
	}
	quiescent, err := spec.Quiescent(ctx, snapshot)
	if err != nil {
		return false, errMigrationUnsafe
	}
	return quiescent, nil
}

func resultFor(spec ArtifactSpec, status Status) Result {
	return Result{Owner: spec.Owner, Scope: spec.Scope, Status: status}
}

func unsafeResult(spec ArtifactSpec) Result {
	result := Result{Status: StatusUnsafe}
	if ownerPattern.MatchString(spec.Owner) {
		result.Owner = spec.Owner
	}
	if spec.Scope == "project" || spec.Scope == "user" {
		result.Scope = spec.Scope
	}
	return result
}
