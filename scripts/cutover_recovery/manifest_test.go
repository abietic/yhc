package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalPayloadIgnoresRecordOrderAndChecksum(t *testing.T) {
	first := testManifest(t)
	first.Refs = append(first.Refs, refRecord{RecordID: makeRecordID("ref", "/private", "refs/heads/z"), RepositoryRole: "private", RefName: "refs/heads/z", ObjectID: strings.Repeat("a", 40)})
	first.Classifications = append(first.Classifications, classificationRecord{
		RecordID:           makeRecordID("classification", first.Refs[1].RecordID, "private_recovery"),
		TargetRecordID:     first.Refs[1].RecordID,
		TargetKind:         "ref",
		Classification:     "private_recovery",
		Owner:              "operator",
		RestoreDisposition: "preserve",
		ChecksumPolicy:     "omit_sensitive",
	})
	first.Aggregates.Refs = len(first.Refs)
	first.Aggregates.Classifications = len(first.Classifications)
	second := first
	second.Refs = append([]refRecord(nil), first.Refs...)
	second.Refs[0], second.Refs[1] = second.Refs[1], second.Refs[0]
	second.Checksum = "sha256:" + strings.Repeat("f", 64)
	second.Aggregates.Refs = len(second.Refs)

	firstPayload, err := canonicalPayload(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, err := canonicalPayload(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstPayload) != string(secondPayload) {
		t.Fatalf("canonical payload changed with record order or checksum:\n%s\n%s", firstPayload, secondPayload)
	}

	sealed, err := sealManifest(first)
	if err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.Private.Head = strings.Repeat("c", 40)
	changedSealed, err := sealManifest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Checksum == changedSealed.Checksum {
		t.Fatal("retained field change did not change checksum")
	}
}

func TestManifestStrictDecode(t *testing.T) {
	dir := t.TempDir()
	sealed, err := sealManifest(testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unknown.json")
	unknownPayload := append([]byte(nil), b[:len(b)-1]...)
	unknownPayload = append(unknownPayload, []byte(",\"unknown\":true}")...)
	if err := os.WriteFile(unknown, unknownPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(unknown); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v, want strict decode rejection", err)
	}
	trailing := filepath.Join(dir, "trailing.json")
	trailingPayload := append([]byte(nil), b...)
	trailingPayload = append(trailingPayload, []byte(" {}")...)
	if err := os.WriteFile(trailing, trailingPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(trailing); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing value error = %v, want rejection", err)
	}
}

func TestSealManifestRejectsCoverageAndAggregateMismatches(t *testing.T) {
	m := testManifest(t)
	m.Aggregates.Refs++
	if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "aggregates") {
		t.Fatalf("aggregate mismatch error = %v", err)
	}
	m = testManifest(t)
	m.Classifications = nil
	m.Aggregates.Classifications = 0
	if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "classification") {
		t.Fatalf("classification coverage error = %v", err)
	}
}

func TestClassificationRestoreDispositionPairs(t *testing.T) {
	pairs := map[string]string{
		"already_forward_ported": "retain_archive",
		"candidate_public_delta": "reexpress_public",
		"private_recovery":       "preserve",
		"never_public":           "exclude_public",
		"unresolved":             "block",
	}
	for classification, disposition := range pairs {
		t.Run(classification, func(t *testing.T) {
			m := testManifest(t)
			for i := range m.Classifications {
				m.Classifications[i].Classification = classification
				m.Classifications[i].RestoreDisposition = disposition
				m.Classifications[i].RecordID = makeRecordID("classification", m.Classifications[i].TargetRecordID, classification)
			}
			if _, err := sealManifest(m); err != nil {
				t.Fatalf("valid pair rejected: %v", err)
			}
			m.Classifications[0].RestoreDisposition = "block"
			if disposition == "block" {
				m.Classifications[0].RestoreDisposition = "preserve"
			}
			if _, err := sealManifest(m); err == nil {
				t.Fatal("mismatched restore disposition was accepted")
			}
		})
	}
}

func TestSealManifestRejectsUnstableRecordIDsAndNonCanonicalPaths(t *testing.T) {
	t.Run("unstable record id", func(t *testing.T) {
		m := testManifest(t)
		m.Refs[0].RecordID = "sha256:" + strings.Repeat("f", 64)
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "stable identity") {
			t.Fatalf("unstable record ID error = %v", err)
		}
	})
	t.Run("non-canonical repository path", func(t *testing.T) {
		m := testManifest(t)
		m.Public.Root = "/private/../public"
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "paths must be absolute") {
			t.Fatalf("non-canonical repository path error = %v", err)
		}
	})
	t.Run("mapping source mismatch", func(t *testing.T) {
		m := testManifest(t)
		m.ArchiveMapping[0].Source = "/other"
		m.ArchiveMapping[0].RecordID = makeRecordID("archive_mapping", "/other", m.ArchiveMapping[0].Destination)
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "source does not match") {
			t.Fatalf("mapping source mismatch error = %v", err)
		}
	})
	t.Run("public ref", func(t *testing.T) {
		m := testManifest(t)
		m.Refs[0].RepositoryRole = "public"
		m.Refs[0].RecordID = makeRecordID("ref", m.Public.Root, m.Refs[0].RefName)
		m.Classifications[0].TargetRecordID = m.Refs[0].RecordID
		m.Classifications[0].RecordID = makeRecordID("classification", m.Refs[0].RecordID, m.Classifications[0].Classification)
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "private repository") {
			t.Fatalf("public ref error = %v", err)
		}
	})
	t.Run("non-canonical worktree common dir", func(t *testing.T) {
		m := testManifest(t)
		m.Worktrees[0].CommonDir = "/private/../private/.git"
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "common_dir") {
			t.Fatalf("non-canonical common dir error = %v", err)
		}
	})
}

func TestSealManifestRejectsInvalidGitObjectIDs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest)
	}{
		{name: "repository", mutate: func(m *manifest) { m.Public.Head = "not-an-oid" }},
		{name: "ref", mutate: func(m *manifest) { m.Refs[0].ObjectID = strings.Repeat("A", 40) }},
		{name: "worktree", mutate: func(m *manifest) { m.Worktrees[0].Head = strings.Repeat("a", 39) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := testManifest(t)
			test.mutate(&m)
			if _, err := sealManifest(m); err == nil {
				t.Fatal("invalid Git object ID was accepted")
			}
		})
	}
}

func TestSealManifestRequiresExactPresentWorktreeMappings(t *testing.T) {
	t.Run("missing present mapping", func(t *testing.T) {
		m := testManifest(t)
		m.ArchiveMapping = nil
		m.Aggregates.ArchiveMappings = 0
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "main checkouts") {
			t.Fatalf("missing mapping accepted: %v", err)
		}
	})
	t.Run("duplicate mapping", func(t *testing.T) {
		m := testManifest(t)
		duplicate := m.ArchiveMapping[0]
		duplicate.Destination = "/archive-second"
		duplicate.RecordID = makeRecordID("archive_mapping", duplicate.Source, duplicate.Destination)
		m.ArchiveMapping = append(m.ArchiveMapping, duplicate)
		m.Aggregates.ArchiveMappings++
		if _, err := sealManifest(m); err == nil || !strings.Contains(err.Error(), "multiple archive mappings") {
			t.Fatalf("duplicate mapping accepted: %v", err)
		}
	})
}

func TestSealManifestRejectsProcessOutsideMappedRootOrWrongRootID(t *testing.T) {
	for name, test := range map[string]struct{ path, rootID string }{
		"outside":    {"/outside/file", makeRecordID("process_root", "/private", "/private")},
		"wrong root": {"/private/file", makeRecordID("process_root", "/other", "/other")},
	} {
		t.Run(name, func(t *testing.T) {
			m := testManifest(t)
			record := processRecord{RootRecordID: test.rootID, PID: 99, OccupancyKind: "cwd", Path: test.path}
			record.RecordID = makeRecordID("process", record.RootRecordID, processIdentity(record))
			m.Processes = []processRecord{record}
			m.Aggregates.Processes = 1
			if _, err := sealManifest(m); err == nil {
				t.Fatal("invalid process mapping accepted")
			}
		})
	}
}

func TestWriteManifestAtomicRejectsProtectedRootsAndWrites0600(t *testing.T) {
	base := t.TempDir()
	publicRoot := filepath.Join(base, "public")
	privateRoot := filepath.Join(base, "private")
	archiveRoot := filepath.Join(base, "archive")
	for _, root := range []string{publicRoot, privateRoot, archiveRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := testManifest(t)
	m.Public.Root = publicRoot
	m.Public.CommonDir = filepath.Join(publicRoot, ".git")
	m.Private.Root = privateRoot
	m.Private.CommonDir = filepath.Join(privateRoot, ".git")
	m.Worktrees[0].Source = privateRoot
	m.Worktrees[0].CommonDir = filepath.Join(privateRoot, ".git")
	m.ArchiveMapping[0].Source = privateRoot
	m.ArchiveMapping[0].Destination = archiveRoot
	refreshTestRecordIDs(&m)
	sealed, err := sealManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifestAtomic(filepath.Join(privateRoot, "manifest.json"), sealed); err == nil {
		t.Fatal("output within private root was accepted")
	}
	evidenceRoot := filepath.Join(base, "evidence")
	if err := os.Mkdir(evidenceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(evidenceRoot, "manifest.json")
	if err := writeManifestAtomic(output, sealed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %#o, want 0600", info.Mode().Perm())
	}
	if _, err := readManifest(output); err != nil {
		t.Fatalf("atomic output did not strictly round-trip: %v", err)
	}
}

func testManifest(t *testing.T) manifest {
	t.Helper()
	privateRoot := "/private"
	worktreeID := makeRecordID("worktree", privateRoot, privateRoot)
	refID := makeRecordID("ref", privateRoot, "refs/heads/main")
	mappingID := makeRecordID("archive_mapping", privateRoot, "/archive")
	classificationID := makeRecordID("classification", refID, "private_recovery")
	worktreeClassificationID := makeRecordID("classification", worktreeID, "private_recovery")
	return manifest{
		SchemaVersion:  1,
		CapturedAt:     time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC).Format(time.RFC3339),
		Public:         repositoryRecord{Role: "public", Root: "/public", Head: strings.Repeat("1", 40), Branch: "master", CommonDir: "/public/.git", OriginRepository: "public/yhc"},
		Private:        repositoryRecord{Role: "private", Root: privateRoot, Head: strings.Repeat("2", 40), Branch: "master", CommonDir: privateRoot + "/.git", OriginRepository: "private/yhc"},
		ArchiveMapping: []archiveMappingRecord{{RecordID: mappingID, WorktreeRecordID: worktreeID, Kind: "main_checkout", Source: privateRoot, Destination: "/archive"}},
		Refs:           []refRecord{{RecordID: refID, RepositoryRole: "private", RefName: "refs/heads/main", ObjectID: strings.Repeat("2", 40)}},
		Worktrees:      []worktreeRecord{{RecordID: worktreeID, Source: privateRoot, Head: strings.Repeat("2", 40), Branch: "master", Present: true, CommonDir: privateRoot + "/.git", PorcelainBase64: ""}},
		Classifications: []classificationRecord{
			{RecordID: classificationID, TargetRecordID: refID, TargetKind: "ref", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
			{RecordID: worktreeClassificationID, TargetRecordID: worktreeID, TargetKind: "worktree", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
		},
		Aggregates: aggregateRecord{ArchiveMappings: 1, Refs: 1, Worktrees: 1, Classifications: 2},
	}
}

func refreshTestRecordIDs(m *manifest) {
	refIDs := make(map[string]string, len(m.Refs))
	for i := range m.Refs {
		source := m.Private.Root
		if m.Refs[i].RepositoryRole == m.Public.Role {
			source = m.Public.Root
		}
		oldID := m.Refs[i].RecordID
		m.Refs[i].RecordID = makeRecordID("ref", source, m.Refs[i].RefName)
		refIDs[oldID] = m.Refs[i].RecordID
	}
	worktreeIDs := make(map[string]string, len(m.Worktrees))
	for i := range m.Worktrees {
		oldID := m.Worktrees[i].RecordID
		m.Worktrees[i].RecordID = makeRecordID("worktree", m.Private.Root, m.Worktrees[i].Source)
		worktreeIDs[oldID] = m.Worktrees[i].RecordID
	}
	for i := range m.ArchiveMapping {
		if replacement, ok := worktreeIDs[m.ArchiveMapping[i].WorktreeRecordID]; ok {
			m.ArchiveMapping[i].WorktreeRecordID = replacement
		}
		m.ArchiveMapping[i].RecordID = makeRecordID("archive_mapping", m.ArchiveMapping[i].Source, m.ArchiveMapping[i].Destination)
	}
	for i := range m.Classifications {
		if replacement, ok := refIDs[m.Classifications[i].TargetRecordID]; ok {
			m.Classifications[i].TargetRecordID = replacement
		}
		if replacement, ok := worktreeIDs[m.Classifications[i].TargetRecordID]; ok {
			m.Classifications[i].TargetRecordID = replacement
		}
		m.Classifications[i].RecordID = makeRecordID("classification", m.Classifications[i].TargetRecordID, m.Classifications[i].Classification)
	}
}
