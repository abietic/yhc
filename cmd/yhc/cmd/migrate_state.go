package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/spf13/cobra"

	enginecron "github.com/abietic/yhc/engine/cron"
	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	enginesession "github.com/abietic/yhc/engine/session"
	engineworktree "github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
	"github.com/abietic/yhc/internal/tui"
	"github.com/abietic/yhc/internal/tui/keybindings"
	producttools "github.com/abietic/yhc/tools"
)

type migrateStateRequest struct {
	Owner                string
	Scope                string
	ProjectRoot          string
	Roots                statepath.Roots
	UserRoots            statepath.Roots
	SessionID            string
	ConfirmLegacyStopped bool
}

type migrateStateResult struct {
	Status   string
	Count    int
	HasCount bool
	Records  []migrateStateRecord
}

type migrateStateRecord struct {
	RecordID string
	Status   string
}

type migrateStateAction func(
	context.Context,
	migrateStateRequest,
) (migrateStateResult, error)

type migrateStateOwner struct {
	inspect        migrateStateAction
	apply          migrateStateAction
	projectContext bool
}

type migrateStateDependencies struct {
	owners      map[string]migrateStateOwner
	projectRoot func() (string, error)
	userHome    func() (string, error)
}

var migrateStateStatuses = map[string]struct{}{
	"absent":             {},
	"ready":              {},
	"imported":           {},
	"destination_exists": {},
	"legacy_busy":        {},
	"unsafe":             {},
}

var migrateStateRecordID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var migrateStateRecordStatuses = map[string]struct{}{
	"active":      {},
	"dirty":       {},
	"unavailable": {},
	"terminal":    {},
}

var errMigrateStateScopeUnavailable = errors.New("state migration scope is unavailable")

func newMigrateStateCommand() *cobra.Command {
	return newMigrateStateCommandWithDependencies(migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: os.Getwd,
		userHome:    os.UserHomeDir,
	})
}

func newMigrateStateCommandWithDependencies(
	dependencies migrateStateDependencies,
) *cobra.Command {
	dependencies = normalizeMigrateStateDependencies(dependencies)
	command := &cobra.Command{
		Use:   "migrate-state",
		Short: "Inspect or import one registered legacy state owner",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageErrorf("migrate-state requires one of: inspect, apply")
		},
	}
	command.AddCommand(
		newMigrateStateInspectCommand(dependencies),
		newMigrateStateApplyCommand(dependencies),
	)
	return command
}

func newMigrateStateInspectCommand(
	dependencies migrateStateDependencies,
) *cobra.Command {
	var owner, scope, sessionID string
	command := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect one registered legacy state owner without mutation",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if owner == "session" {
				if cmd.Flags().Changed("scope") && scope != "project" {
					return usageErrorf("session migration scope must be project")
				}
				scope = "project"
				if sessionID == "" {
					return usageErrorf("session migration inspect requires --session")
				}
			} else if owner == "cron" || owner == "worktree" {
				if cmd.Flags().Changed("scope") && scope != "project" {
					return usageErrorf("%s migration scope must be project", owner)
				}
				scope = "project"
				if sessionID != "" {
					return usageErrorf("--session is available only for the session owner")
				}
			} else if sessionID != "" {
				return usageErrorf("--session is available only for the session owner")
			}
			if scope != "project" && scope != "user" {
				return usageErrorf("state migration scope must be project or user")
			}
			if owner == "" {
				return listMigrateStateOwners(cmd, dependencies.owners)
			}
			return runMigrateStateAction(
				cmd,
				dependencies,
				owner,
				scope,
				false,
				sessionID,
				false,
			)
		},
	}
	command.Flags().StringVar(&scope, "scope", "project", "State scope (project or user)")
	command.Flags().StringVar(&owner, "owner", "", "Registered state owner")
	command.Flags().StringVar(&sessionID, "session", "", "Exact legacy session ID")
	return command
}

func newMigrateStateApplyCommand(
	dependencies migrateStateDependencies,
) *cobra.Command {
	var owner, scope, sessionID string
	var confirmLegacyStopped bool
	command := &cobra.Command{
		Use:   "apply",
		Short: "Import one registered legacy state owner",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if owner == "" {
				return usageErrorf("state migration apply requires one owner")
			}
			if owner == "session" {
				if cmd.Flags().Changed("scope") && scope != "project" {
					return usageErrorf("session migration scope must be project")
				}
				scope = "project"
				if sessionID == "" || !confirmLegacyStopped {
					return usageErrorf("session migration apply requires --session and --confirm-legacy-stopped")
				}
			} else if owner == "cron" {
				if cmd.Flags().Changed("scope") && scope != "project" {
					return usageErrorf("cron migration scope must be project")
				}
				scope = "project"
				if sessionID != "" || !confirmLegacyStopped {
					return usageErrorf("cron migration apply requires --confirm-legacy-stopped")
				}
			} else if sessionID != "" || confirmLegacyStopped {
				return usageErrorf("session migration flags require --owner session")
			} else if !cmd.Flags().Changed("scope") {
				return usageErrorf("state migration apply requires one owner and an explicit scope")
			}
			return runMigrateStateAction(
				cmd,
				dependencies,
				owner,
				scope,
				true,
				sessionID,
				confirmLegacyStopped,
			)
		},
	}
	command.Flags().StringVar(&scope, "scope", "", "State scope (project or user)")
	command.Flags().StringVar(&owner, "owner", "", "Registered state owner")
	command.Flags().StringVar(&sessionID, "session", "", "Exact legacy session ID")
	command.Flags().BoolVar(
		&confirmLegacyStopped,
		"confirm-legacy-stopped",
		false,
		"Attest that the archived legacy session producer has stopped",
	)
	return command
}

func runMigrateStateAction(
	command *cobra.Command,
	dependencies migrateStateDependencies,
	owner string,
	scope string,
	apply bool,
	sessionID string,
	confirmLegacyStopped bool,
) error {
	registered, ok := dependencies.owners[owner]
	if !ok {
		return usageErrorf("unknown state migration owner")
	}
	if scope != "project" && scope != "user" {
		return usageErrorf("state migration scope must be project or user")
	}
	action := registered.inspect
	if apply {
		action = registered.apply
	}
	if action == nil {
		return usageErrorf("state migration operation is unavailable for this owner")
	}

	projectRoot := ""
	if scope == "project" || registered.projectContext {
		var err error
		projectRoot, err = dependencies.projectRoot()
		if err != nil {
			return errors.New("resolve state migration roots failed")
		}
	}
	roots, err := migrateStateRoots(dependencies, scope, projectRoot)
	if err != nil {
		return errors.New("resolve state migration roots failed")
	}
	var userRoots statepath.Roots
	if owner == "session" {
		home, homeErr := dependencies.userHome()
		if homeErr != nil {
			return errors.New("resolve state migration roots failed")
		}
		userRoots, err = statepath.UserRoots(home)
		if err != nil {
			return errors.New("resolve state migration roots failed")
		}
	}
	result, err := action(command.Context(), migrateStateRequest{
		Owner:                owner,
		Scope:                scope,
		ProjectRoot:          projectRoot,
		Roots:                roots,
		UserRoots:            userRoots,
		SessionID:            sessionID,
		ConfirmLegacyStopped: confirmLegacyStopped,
	})
	if err != nil {
		if errors.Is(err, errMigrateStateScopeUnavailable) {
			return usageErrorf("state migration scope is unavailable for this owner")
		}
		return errors.New("state migration operation failed")
	}
	if _, ok := migrateStateStatuses[result.Status]; !ok {
		return errors.New("state migration returned an invalid status")
	}
	records, err := validatedMigrateStateRecords(result)
	if err != nil {
		return errors.New("state migration returned invalid records")
	}
	format := "owner=%s scope=%s status=%s\n"
	arguments := []any{owner, scope, result.Status}
	if result.HasCount {
		format = "owner=%s scope=%s status=%s count=%d\n"
		arguments = append(arguments, result.Count)
	}
	if _, err = fmt.Fprintf(command.OutOrStdout(), format, arguments...); err != nil {
		return err
	}
	for _, record := range records {
		if _, err = fmt.Fprintf(
			command.OutOrStdout(),
			"record=%s status=%s\n",
			record.RecordID,
			record.Status,
		); err != nil {
			return err
		}
	}
	return nil
}

func productionMigrateStateOwners() map[string]migrateStateOwner {
	return map[string]migrateStateOwner{
		"session":            newSessionMigrationOwner(),
		"cron":               newCronMigrationOwner(),
		"worktree":           newWorktreeMigrationOwner(),
		"memory":             newMemoryMigrationOwner(memoryMigrationSpec),
		"agent-memory":       newMemoryMigrationOwner(memoryMigrationSpec),
		"agent-memory-local": newMemoryMigrationOwner(memoryMigrationSpec),
		"approvals": newArtifactMigrationOwner(func(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
			if request.Scope != "project" {
				return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
			}
			return permission.ApprovalMigrationSpec(request.ProjectRoot)
		}),
		"history": newArtifactMigrationOwner(func(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
			if request.Scope != "project" {
				return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
			}
			return tui.HistoryMigrationSpec(), nil
		}),
		"settings": newArtifactMigrationOwner(func(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
			return producttools.SettingsMigrationSpec(request.Scope), nil
		}),
		"keybindings": newArtifactMigrationOwner(func(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
			if request.Scope != "user" {
				return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
			}
			return keybindings.UserMigrationSpec(), nil
		}),
		"permission-review-audit": newArtifactMigrationOwner(func(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
			if request.Scope != "user" {
				return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
			}
			spec, err := permission.ReviewAuditMigrationSpec(request.Roots)
			if errors.Is(err, permission.ErrReviewAuditMigrationUnavailable) {
				return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
			}
			return spec, err
		}),
	}
}

func newWorktreeMigrationOwner() migrateStateOwner {
	return migrateStateOwner{
		projectContext: true,
		inspect: func(ctx context.Context, request migrateStateRequest) (migrateStateResult, error) {
			if request.Scope != "project" {
				return migrateStateResult{}, errMigrateStateScopeUnavailable
			}
			inspection, err := engineworktree.InspectLegacy(ctx, request.ProjectRoot)
			records := make([]migrateStateRecord, 0, len(inspection.Records))
			for _, record := range inspection.Records {
				records = append(records, migrateStateRecord{
					RecordID: record.RecordID,
					Status:   string(record.Status),
				})
			}
			return migrateStateResult{
				Status:   string(inspection.Status),
				Count:    len(records),
				HasCount: true,
				Records:  records,
			}, err
		},
	}
}

func validatedMigrateStateRecords(
	result migrateStateResult,
) ([]migrateStateRecord, error) {
	if result.Count < 0 || len(result.Records) > 4096 {
		return nil, errors.New("invalid state migration record count")
	}
	if result.Records != nil && (!result.HasCount || result.Count != len(result.Records)) {
		return nil, errors.New("state migration record count mismatch")
	}
	records := append([]migrateStateRecord(nil), result.Records...)
	sort.Slice(records, func(i, j int) bool {
		return records[i].RecordID < records[j].RecordID
	})
	for index, record := range records {
		if !migrateStateRecordID.MatchString(record.RecordID) {
			return nil, errors.New("invalid state migration record ID")
		}
		if _, ok := migrateStateRecordStatuses[record.Status]; !ok {
			return nil, errors.New("invalid state migration record status")
		}
		if index > 0 && records[index-1].RecordID == record.RecordID {
			return nil, errors.New("duplicate state migration record ID")
		}
	}
	return records, nil
}

func newCronMigrationOwner() migrateStateOwner {
	return migrateStateOwner{
		projectContext: true,
		inspect: func(ctx context.Context, request migrateStateRequest) (migrateStateResult, error) {
			if request.Scope != "project" {
				return migrateStateResult{}, errMigrateStateScopeUnavailable
			}
			inspection, err := enginecron.InspectLegacy(ctx, request.ProjectRoot)
			return migrateStateResult{
				Status: string(inspection.Status), Count: inspection.TaskCount, HasCount: true,
			}, err
		},
		apply: func(ctx context.Context, request migrateStateRequest) (migrateStateResult, error) {
			if request.Scope != "project" {
				return migrateStateResult{}, errMigrateStateScopeUnavailable
			}
			inspection, err := enginecron.ImportLegacy(ctx, enginecron.ImportRequest{
				ProjectDir: request.ProjectRoot, ConfirmLegacyStopped: request.ConfirmLegacyStopped,
			})
			return migrateStateResult{
				Status: string(inspection.Status), Count: inspection.TaskCount, HasCount: true,
			}, err
		},
	}
}

func newSessionMigrationOwner() migrateStateOwner {
	return migrateStateOwner{
		projectContext: true,
		inspect: func(
			ctx context.Context,
			request migrateStateRequest,
		) (migrateStateResult, error) {
			err := admitSessionMigration(ctx, request)
			if err == nil {
				return migrateStateResult{Status: "destination_exists"}, nil
			}
			if _, targetErr := resolveLegacySessionMigrationTarget(ctx, request); targetErr == nil {
				return migrateStateResult{Status: "ready"}, nil
			} else if errors.Is(err, os.ErrNotExist) && errors.Is(targetErr, os.ErrNotExist) {
				return migrateStateResult{Status: "absent"}, nil
			}
			return migrateStateResult{}, err
		},
		apply: func(
			ctx context.Context,
			request migrateStateRequest,
		) (migrateStateResult, error) {
			err := admitSessionMigration(ctx, request)
			if err == nil {
				return migrateStateResult{Status: "destination_exists"}, nil
			}
			target, targetErr := resolveLegacySessionMigrationTarget(ctx, request)
			if targetErr != nil {
				return migrateStateResult{}, err
			}
			_, err = enginesession.ImportSessionForResume(ctx, enginesession.ImportRequest{
				Target:               target,
				UserRoots:            request.UserRoots,
				ConfirmLegacyStopped: request.ConfirmLegacyStopped,
			})
			if errors.Is(err, enginesession.ErrSessionImportAlreadyCommitted) {
				return migrateStateResult{Status: "destination_exists"}, nil
			}
			if err != nil {
				return migrateStateResult{}, err
			}
			return migrateStateResult{Status: "imported"}, nil
		},
	}
}

func admitSessionMigration(
	ctx context.Context,
	request migrateStateRequest,
) error {
	canonicalCatalog, legacyCatalog, err := sessionMigrationCatalogPaths(request)
	if err != nil {
		return err
	}
	_, err = enginesession.AdmitSessionResume(ctx, enginesession.ResumeAdmissionRequest{
		SessionID:         request.SessionID,
		CWD:               request.ProjectRoot,
		CatalogPath:       canonicalCatalog,
		LegacyCatalogPath: legacyCatalog,
		UserRoots:         request.UserRoots,
	})
	return err
}

func resolveLegacySessionMigrationTarget(
	ctx context.Context,
	request migrateStateRequest,
) (enginesession.LegacySessionTarget, error) {
	_, legacyCatalog, err := sessionMigrationCatalogPaths(request)
	if err != nil {
		return enginesession.LegacySessionTarget{}, err
	}
	targets, err := enginesession.InspectLegacySessions(ctx, legacyCatalog)
	if err != nil {
		return enginesession.LegacySessionTarget{}, err
	}
	expectedTranscriptDir := filepath.Join(request.Roots.Legacy, "transcripts")
	var selected *enginesession.LegacySessionTarget
	for index := range targets {
		target := targets[index]
		if target.SessionID != request.SessionID ||
			filepath.Clean(target.CWD) != filepath.Clean(request.ProjectRoot) ||
			filepath.Clean(target.TranscriptDir) != filepath.Clean(expectedTranscriptDir) {
			continue
		}
		if selected != nil {
			return enginesession.LegacySessionTarget{}, errors.New("legacy session migration target is ambiguous")
		}
		selected = &target
	}
	if selected == nil {
		return enginesession.LegacySessionTarget{}, os.ErrNotExist
	}
	return *selected, nil
}

func sessionMigrationCatalogPaths(
	request migrateStateRequest,
) (string, string, error) {
	expectedCanonicalCatalog := filepath.Join(request.UserRoots.Canonical, "session-roots.json")
	expectedLegacyCatalog := filepath.Join(request.UserRoots.Legacy, "session-roots.json")
	canonicalCatalog, legacyCatalog := enginesession.DefaultCatalogPaths()
	if canonicalCatalog != expectedCanonicalCatalog ||
		legacyCatalog != expectedLegacyCatalog {
		return "", "", errMigrateStateScopeUnavailable
	}
	return canonicalCatalog, legacyCatalog, nil
}

func memoryMigrationSpec(request migrateStateRequest) (statemigration.ArtifactSpec, error) {
	spec, err := memdir.MemoryMigrationSpec(
		request.Owner,
		request.Scope,
		request.ProjectRoot,
	)
	if errors.Is(err, memdir.ErrMemoryMigrationUnavailable) {
		return statemigration.ArtifactSpec{}, errMigrateStateScopeUnavailable
	}
	return spec, err
}

func newMemoryMigrationOwner(
	factory func(migrateStateRequest) (statemigration.ArtifactSpec, error),
) migrateStateOwner {
	owner := newArtifactMigrationOwner(factory)
	owner.projectContext = true
	return owner
}

func newArtifactMigrationOwner(
	factory func(migrateStateRequest) (statemigration.ArtifactSpec, error),
) migrateStateOwner {
	return migrateStateOwner{
		inspect: newArtifactMigrationAction(factory, false),
		apply:   newArtifactMigrationAction(factory, true),
	}
}

func newArtifactMigrationAction(
	factory func(migrateStateRequest) (statemigration.ArtifactSpec, error),
	apply bool,
) migrateStateAction {
	return func(
		ctx context.Context,
		request migrateStateRequest,
	) (migrateStateResult, error) {
		spec, err := factory(request)
		if err != nil {
			return migrateStateResult{}, err
		}
		if spec.Owner != request.Owner || spec.Scope != request.Scope {
			return migrateStateResult{}, errors.New("state migration owner contract is invalid")
		}
		var result statemigration.Result
		if apply {
			result, err = (statemigration.Importer{}).Import(ctx, request.Roots, spec)
		} else {
			result, err = (statemigration.Importer{}).Inspect(ctx, request.Roots, spec)
		}
		return migrateStateResult{Status: string(result.Status)}, err
	}
}

func migrateStateRoots(
	dependencies migrateStateDependencies,
	scope string,
	projectRoot string,
) (statepath.Roots, error) {
	if scope == "project" {
		return statepath.ProjectRoots(projectRoot)
	}
	home, err := dependencies.userHome()
	if err != nil {
		return statepath.Roots{}, err
	}
	return statepath.UserRoots(home)
}

func listMigrateStateOwners(
	command *cobra.Command,
	owners map[string]migrateStateOwner,
) error {
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	if _, err := fmt.Fprintln(command.OutOrStdout(), "Registered state migration owners:"); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "- %s\n", name); err != nil {
			return err
		}
	}
	return nil
}

func normalizeMigrateStateDependencies(
	dependencies migrateStateDependencies,
) migrateStateDependencies {
	if dependencies.owners == nil {
		dependencies.owners = map[string]migrateStateOwner{}
	} else {
		owners := make(map[string]migrateStateOwner, len(dependencies.owners))
		for name, owner := range dependencies.owners {
			owners[name] = owner
		}
		dependencies.owners = owners
	}
	if dependencies.projectRoot == nil {
		dependencies.projectRoot = os.Getwd
	}
	if dependencies.userHome == nil {
		dependencies.userHome = os.UserHomeDir
	}
	return dependencies
}
