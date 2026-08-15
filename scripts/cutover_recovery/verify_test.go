package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyPreMoveRejectsUnresolvedClassification(t *testing.T) {
	m := testManifest(t)
	m.Classifications[0].Classification = "unresolved"
	m.Classifications[0].RestoreDisposition = "block"
	m.Classifications[0].RecordID = makeRecordID("classification", m.Classifications[0].TargetRecordID, "unresolved")
	deps := dependencies{Git: fakeGitReader{}, Processes: unavailableProcessReader{}, Now: time.Now}
	if err := verifyLiveState(context.Background(), deps, m, phasePreMove); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved manifest was accepted: %v", err)
	}
}

func TestVerifyPreMoveRejectsDestinationCollision(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := requireAbsent([]string{source}); err != nil {
		t.Fatalf("missing root rejected: %v", err)
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireAbsent([]string{destination}); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("existing destination accepted: %v", err)
	}
}

func TestRequireAbsentRejectsProspectivePathDrift(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	frozenParent := filepath.Join(base, "archive")
	if err := os.Mkdir(frozenParent, 0o755); err != nil {
		t.Fatal(err)
	}
	frozen := filepath.Join(frozenParent, "target")
	resolved, err := resolveProspectivePath(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != frozen {
		t.Fatalf("initial prospective path = %q, want %q", resolved, frozen)
	}
	relocatedParent := filepath.Join(base, "relocated-archive")
	if err := os.Rename(frozenParent, relocatedParent); err != nil {
		t.Fatal(err)
	}
	redirectedParent := filepath.Join(base, "redirected-archive")
	if err := os.Mkdir(redirectedParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(redirectedParent, frozenParent); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}
	if err := requireAbsent([]string{frozen}); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("prospective path drift accepted: %v", err)
	}
}

func TestVerifyMappingTopologyRejectsDuplicateNestedAndCrossOverlappingRoots(t *testing.T) {
	cases := []struct {
		name     string
		mappings []archiveMappingRecord
	}{
		{"duplicate source", []archiveMappingRecord{{Source: "/source", Destination: "/archive-a"}, {Source: "/source", Destination: "/archive-b"}}},
		{"nested destination", []archiveMappingRecord{{Source: "/source-a", Destination: "/archive"}, {Source: "/source-b", Destination: "/archive/linked"}}},
		{"cross overlap", []archiveMappingRecord{{Source: "/source", Destination: "/archive"}, {Source: "/archive/child", Destination: "/other"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMappingTopology(test.mappings); err == nil {
				t.Fatal("overlapping mappings were accepted")
			}
		})
	}
}

func TestPrunableRegistrationHasNoMappingButRetainsIdentity(t *testing.T) {
	m := testManifest(t)
	prunableSource := "/missing-prunable"
	prunableID := makeRecordID("worktree", m.Private.Root, prunableSource)
	m.Worktrees = append(m.Worktrees, worktreeRecord{RecordID: prunableID, Source: prunableSource, Head: strings.Repeat("3", 40), Branch: "gone", Prunable: true, Present: false, PorcelainBase64: ""})
	m.Classifications = append(m.Classifications, classificationRecord{RecordID: makeRecordID("classification", prunableID, "private_recovery"), TargetRecordID: prunableID, TargetKind: "worktree", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"})
	m.Aggregates.Worktrees++
	m.Aggregates.Classifications++
	if err := validateManifest(m, phasePostMove); err != nil {
		t.Fatalf("prunable registration rejected: %v", err)
	}
	for _, mapping := range m.ArchiveMapping {
		if mapping.WorktreeRecordID == prunableID {
			t.Fatal("prunable registration received a mapping")
		}
	}
}

func TestSameRecordsRejectsDuplicateIDsOnEitherSide(t *testing.T) {
	first := refRecord{RecordID: "same", RefName: "refs/heads/a"}
	second := refRecord{RecordID: "other", RefName: "refs/heads/b"}
	if sameRecords([]refRecord{first, second}, []refRecord{first, first}, func(record refRecord) string { return record.RecordID }) {
		t.Fatal("duplicate actual record IDs were accepted")
	}
	if sameRecords([]refRecord{first, first}, []refRecord{first, second}, func(record refRecord) string { return record.RecordID }) {
		t.Fatal("duplicate expected record IDs were accepted")
	}
}
