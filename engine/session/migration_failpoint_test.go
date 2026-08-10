package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSessionImportFailureIsRestartSafeAtEveryPhase(t *testing.T) {
	probe := newSessionImportFixture(t, true)
	fileNames := sessionBundleNames(probe.sessionID)
	cases := []struct {
		stage  sessionImportFailureStage
		detail string
	}{
		{failureAfterJournalWrite, string(PhasePrepared)},
		{failureAfterJournalWrite, string(PhaseFilesPromoted)},
		{failureAfterJournalWrite, string(PhaseMarkerCommitted)},
		{failureAfterJournalWrite, string(PhaseCatalogCommitted)},
		{failureAfterStageParentSync, "stage"},
		{failureAfterMarkerWrite, probe.sessionID},
		{failureAfterMarkerSync, probe.sessionID},
		{failureAfterCatalogReplace, probe.sessionID},
		{failureAfterCatalogSync, probe.sessionID},
	}
	for _, name := range fileNames {
		cases = append(cases,
			struct {
				stage  sessionImportFailureStage
				detail string
			}{failureAfterStageFileWrite, name},
			struct {
				stage  sessionImportFailureStage
				detail string
			}{failureAfterStageFileSync, name},
			struct {
				stage  sessionImportFailureStage
				detail string
			}{failureAfterFileRename, name},
			struct {
				stage  sessionImportFailureStage
				detail string
			}{failureAfterFileSync, name},
		)
	}

	for _, test := range cases {
		t.Run(fmt.Sprintf("%s/%s", test.stage, test.detail), func(t *testing.T) {
			fixture := newSessionImportFixture(t, true)
			injected := false
			ctx := withSessionImportFailureHook(
				context.Background(),
				func(stage sessionImportFailureStage, detail string) error {
					if !injected && stage == test.stage && detail == test.detail {
						injected = true
						return errors.New("stop")
					}
					return nil
				},
			)
			root, err := ImportSessionForResume(ctx, fixture.request(true))
			if !errors.Is(err, ErrSessionImportUnsafe) {
				t.Fatalf("first import root=%#v err=%v, want unsafe", root, err)
			}
			if !injected {
				t.Fatalf("failure hook %s/%s did not run", test.stage, test.detail)
			}
			fixture.assertLegacyUnchanged(t)

			if test.stage == failureAfterFileRename || test.stage == failureAfterFileSync {
				recoveryCtx := withSessionImportFailureHook(
					context.Background(),
					func(stage sessionImportFailureStage, _ string) error {
						if stage == failureAfterFirstSourceSnapshot {
							return errors.New("recovery attempted legacy recapture")
						}
						return nil
					},
				)
				if err := RecoverSessionImports(recoveryCtx, fixture.roots); err != nil {
					t.Fatalf("recover interrupted promotion: %v", err)
				}
				root, err = ImportSessionForResume(context.Background(), fixture.request(true))
				if !errors.Is(err, ErrSessionImportAlreadyCommitted) {
					t.Fatalf("post-recovery import root=%#v err=%v, want already committed", root, err)
				}
			} else {
				root, err = ImportSessionForResume(context.Background(), fixture.request(true))
				if err != nil && !errors.Is(err, ErrSessionImportAlreadyCommitted) {
					t.Fatalf("restart import root=%#v err=%v", root, err)
				}
			}
			fixture.assertLegacyUnchanged(t)
			fixture.assertCanonicalBundle(t)
			assertCommittedSessionImportVisible(t, fixture)
		})
	}
}

func TestRecoverSessionImportsCompletesDurableBundleWithoutLegacyRecapture(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	injected := false
	ctx := withSessionImportFailureHook(
		context.Background(),
		func(stage sessionImportFailureStage, detail string) error {
			if !injected && stage == failureAfterJournalWrite && detail == string(PhaseFilesPromoted) {
				injected = true
				return errors.New("stop after durable promotion")
			}
			return nil
		},
	)
	if _, err := ImportSessionForResume(ctx, fixture.request(true)); !errors.Is(err, ErrSessionImportUnsafe) {
		t.Fatalf("interrupted import error = %v", err)
	}
	if !injected {
		t.Fatal("durable promotion failure was not injected")
	}

	recoveryCtx := withSessionImportFailureHook(
		context.Background(),
		func(stage sessionImportFailureStage, _ string) error {
			if stage == failureAfterFirstSourceSnapshot {
				return errors.New("recovery attempted legacy recapture")
			}
			return nil
		},
	)
	if err := RecoverSessionImports(recoveryCtx, fixture.roots); err != nil {
		t.Fatalf("recover durable import: %v", err)
	}
	if _, err := ImportSessionForResume(context.Background(), fixture.request(true)); !errors.Is(err, ErrSessionImportAlreadyCommitted) {
		t.Fatalf("post-recovery import error = %v, want already committed", err)
	}
	fixture.assertLegacyUnchanged(t)
	fixture.assertCanonicalBundle(t)
	assertCommittedSessionImportVisible(t, fixture)
}

func assertCommittedSessionImportVisible(t *testing.T, fixture *sessionImportFixture) {
	t.Helper()
	info, err := ResolveSession(SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               fixture.project,
		TranscriptDir:     fixture.canonicalDir,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		Limit:             10,
	}, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if info.ReadOnly || info.NeedsImport || info.TranscriptDir != fixture.canonicalDir {
		t.Fatalf("committed session = %#v", info)
	}
}
