package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const schemaVersion = 1

type manifest struct {
	SchemaVersion   int                    `json:"schema_version"`
	CapturedAt      string                 `json:"captured_at"`
	Public          repositoryRecord       `json:"public"`
	Private         repositoryRecord       `json:"private"`
	ArchiveMapping  []archiveMappingRecord `json:"archive_mapping"`
	Refs            []refRecord            `json:"refs"`
	Worktrees       []worktreeRecord       `json:"worktrees"`
	DirtyPaths      []dirtyPathRecord      `json:"dirty_paths"`
	Stashes         []stashRecord          `json:"stashes"`
	Processes       []processRecord        `json:"processes"`
	Classifications []classificationRecord `json:"classifications"`
	Aggregates      aggregateRecord        `json:"aggregates"`
	Checksum        string                 `json:"checksum"`
}

type repositoryRecord struct {
	Role             string `json:"role"`
	Root             string `json:"root"`
	Head             string `json:"head"`
	Branch           string `json:"branch,omitempty"`
	Detached         bool   `json:"detached"`
	CommonDir        string `json:"common_dir"`
	OriginRepository string `json:"origin_repository"`
}

type archiveMappingRecord struct {
	RecordID         string `json:"record_id"`
	WorktreeRecordID string `json:"worktree_record_id"`
	Kind             string `json:"kind"`
	Source           string `json:"source"`
	Destination      string `json:"destination"`
}

type refRecord struct {
	RecordID       string `json:"record_id"`
	RepositoryRole string `json:"repository_role"`
	RefName        string `json:"ref_name"`
	ObjectID       string `json:"object_id"`
}

type worktreeRecord struct {
	RecordID        string `json:"record_id"`
	Source          string `json:"source"`
	Head            string `json:"head"`
	Branch          string `json:"branch,omitempty"`
	Detached        bool   `json:"detached"`
	Locked          bool   `json:"locked"`
	Prunable        bool   `json:"prunable"`
	Present         bool   `json:"present"`
	CommonDir       string `json:"common_dir"`
	PorcelainBase64 string `json:"porcelain_base64"`
}

type dirtyPathRecord struct {
	RecordID           string `json:"record_id"`
	WorktreeRecordID   string `json:"worktree_record_id"`
	StatusCode         string `json:"status_code"`
	RelativePathBase64 string `json:"relative_path_base64"`
	OriginalPathBase64 string `json:"original_path_base64,omitempty"`
	FileType           string `json:"file_type"`
	Size               int64  `json:"size,omitempty"`
	SHA256             string `json:"sha256,omitempty"`
	OmissionReason     string `json:"omission_reason,omitempty"`
}

type stashRecord struct {
	RecordID     string `json:"record_id"`
	RefName      string `json:"ref_name"`
	ObjectID     string `json:"object_id"`
	CapturedUnix int64  `json:"captured_unix"`
}

type processRecord struct {
	RecordID      string `json:"record_id"`
	RootRecordID  string `json:"root_record_id"`
	PID           int    `json:"pid"`
	OccupancyKind string `json:"occupancy_kind"`
	Path          string `json:"path"`
}

type classificationRecord struct {
	RecordID           string `json:"record_id"`
	TargetRecordID     string `json:"target_record_id"`
	TargetKind         string `json:"target_kind"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type aggregateRecord struct {
	ArchiveMappings int `json:"archive_mappings"`
	Refs            int `json:"refs"`
	Worktrees       int `json:"worktrees"`
	DirtyPaths      int `json:"dirty_paths"`
	Stashes         int `json:"stashes"`
	Processes       int `json:"processes"`
	Classifications int `json:"classifications"`
}

type cutoverInput struct {
	SchemaVersion             int                     `json:"schema_version"`
	ExpectedPublicRepository  string                  `json:"expected_public_repository"`
	ExpectedPrivateRepository string                  `json:"expected_private_repository"`
	Mappings                  []archiveMappingInput   `json:"mappings"`
	Defaults                  []classificationDefault `json:"defaults"`
	Rules                     []classificationRule    `json:"rules"`
}

type archiveMappingInput struct {
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type classificationDefault struct {
	Kind               string `json:"kind"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type classificationRule struct {
	Kind               string `json:"kind"`
	Source             string `json:"source"`
	Identity           string `json:"identity"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type validationPhase string

const (
	phaseCapture     validationPhase = "capture"
	phasePreMove     validationPhase = "pre-move"
	phasePostMove    validationPhase = "post-move"
	phasePreRollback validationPhase = "pre-rollback"
	phaseRollback    validationPhase = "rollback"
)

func makeRecordID(kind, source, identity string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + source + "\x00" + identity))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateManifest(m manifest, phase validationPhase) error {
	if m.SchemaVersion != schemaVersion {
		return fmt.Errorf("schema_version = %d, want %d", m.SchemaVersion, schemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, m.CapturedAt); err != nil {
		return fmt.Errorf("captured_at: %w", err)
	}
	if err := validateRepository(m.Public, "public"); err != nil {
		return err
	}
	if err := validateRepository(m.Private, "private"); err != nil {
		return err
	}
	if err := validateAggregates(m); err != nil {
		return err
	}
	if err := validateCanonicalPaths(m); err != nil {
		return err
	}
	if err := validateStableRecordIDs(m); err != nil {
		return err
	}
	targets := make(map[string]string, len(m.Refs)+len(m.Worktrees)+len(m.DirtyPaths)+len(m.Stashes))
	if err := addRecords(targets, m.Refs, "ref", func(v refRecord) string { return v.RecordID }); err != nil {
		return err
	}
	if err := addRecords(targets, m.Worktrees, "worktree", func(v worktreeRecord) string { return v.RecordID }); err != nil {
		return err
	}
	if err := addRecords(targets, m.DirtyPaths, "dirty_path", func(v dirtyPathRecord) string { return v.RecordID }); err != nil {
		return err
	}
	if err := addRecords(targets, m.Stashes, "stash", func(v stashRecord) string { return v.RecordID }); err != nil {
		return err
	}
	if err := validateMappings(m); err != nil {
		return err
	}
	if err := validateClassifications(m.Classifications, targets, phase); err != nil {
		return err
	}
	if err := validateProcesses(m.Processes, m.ArchiveMapping); err != nil {
		return err
	}
	return nil
}

func validateRepository(r repositoryRecord, expectedRole string) error {
	if r.Role != expectedRole {
		return fmt.Errorf("%s.role = %q", expectedRole, r.Role)
	}
	if !canonicalAbsolutePath(r.Root) || !canonicalAbsolutePath(r.CommonDir) {
		return fmt.Errorf("%s repository paths must be absolute", expectedRole)
	}
	if !validGitObjectID(r.Head) || r.OriginRepository == "" {
		return fmt.Errorf("%s repository identity is incomplete", expectedRole)
	}
	if r.Detached == (r.Branch != "") {
		return fmt.Errorf("%s repository detached/branch identity is inconsistent", expectedRole)
	}
	return nil
}

func validateCanonicalPaths(m manifest) error {
	for _, mapping := range m.ArchiveMapping {
		if !canonicalAbsolutePath(mapping.Source) || !canonicalAbsolutePath(mapping.Destination) {
			return fmt.Errorf("archive mapping %q paths must be canonical absolute paths", mapping.RecordID)
		}
	}
	for _, worktree := range m.Worktrees {
		if !canonicalAbsolutePath(worktree.Source) {
			return fmt.Errorf("worktree %q source must be a canonical absolute path", worktree.RecordID)
		}
		if worktree.Present {
			if !canonicalAbsolutePath(worktree.CommonDir) {
				return fmt.Errorf("worktree %q common_dir must be a canonical absolute path", worktree.RecordID)
			}
		} else if worktree.CommonDir != "" {
			return fmt.Errorf("absent worktree %q may not claim a common_dir", worktree.RecordID)
		}
	}
	for _, process := range m.Processes {
		if !canonicalAbsolutePath(process.Path) {
			return fmt.Errorf("process %q path must be a canonical absolute path", process.RecordID)
		}
	}
	return nil
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validateStableRecordIDs(m manifest) error {
	worktreeSources := make(map[string]string, len(m.Worktrees))
	for _, record := range m.Refs {
		if record.RepositoryRole != m.Private.Role {
			return fmt.Errorf("ref %q must belong to the private repository", record.RefName)
		}
		if !validGitObjectID(record.ObjectID) {
			return fmt.Errorf("ref %q has an invalid Git object ID", record.RefName)
		}
		if err := requireRecordID(record.RecordID, makeRecordID("ref", m.Private.Root, record.RefName), "ref"); err != nil {
			return err
		}
	}
	for _, record := range m.Worktrees {
		if !validGitObjectID(record.Head) {
			return fmt.Errorf("worktree %q has an invalid Git object ID", record.Source)
		}
		if err := requireRecordID(record.RecordID, makeRecordID("worktree", m.Private.Root, record.Source), "worktree"); err != nil {
			return err
		}
		worktreeSources[record.RecordID] = record.Source
	}
	for _, record := range m.DirtyPaths {
		source, ok := worktreeSources[record.WorktreeRecordID]
		if !ok {
			return fmt.Errorf("dirty path %q references unknown worktree %q", record.RecordID, record.WorktreeRecordID)
		}
		if err := requireRecordID(record.RecordID, makeRecordID("dirty_path", source, dirtyPathIdentity(record)), "dirty path"); err != nil {
			return err
		}
	}
	for _, record := range m.Stashes {
		if !validGitObjectID(record.ObjectID) {
			return fmt.Errorf("stash %q has an invalid Git object ID", record.RefName)
		}
		if err := requireRecordID(record.RecordID, makeRecordID("stash", m.Private.Root, record.RefName), "stash"); err != nil {
			return err
		}
	}
	for _, record := range m.ArchiveMapping {
		if err := requireRecordID(record.RecordID, makeRecordID("archive_mapping", record.Source, record.Destination), "archive mapping"); err != nil {
			return err
		}
	}
	for _, record := range m.Processes {
		if err := requireRecordID(record.RecordID, makeRecordID("process", record.RootRecordID, processIdentity(record)), "process"); err != nil {
			return err
		}
	}
	for _, record := range m.Classifications {
		if err := requireRecordID(record.RecordID, makeRecordID("classification", record.TargetRecordID, record.Classification), "classification"); err != nil {
			return err
		}
	}
	return nil
}

func dirtyPathIdentity(record dirtyPathRecord) string {
	return strings.Join([]string{record.StatusCode, record.RelativePathBase64, record.OriginalPathBase64}, "\x1f")
}

func processIdentity(record processRecord) string {
	encodedPath := base64.StdEncoding.EncodeToString([]byte(record.Path))
	return strings.Join([]string{strconv.Itoa(record.PID), record.OccupancyKind, encodedPath}, "\x1f")
}

func requireRecordID(have, want, kind string) error {
	if have != want {
		return fmt.Errorf("%s record_id %q does not match stable identity", kind, have)
	}
	return nil
}

func validateAggregates(m manifest) error {
	actual := aggregateRecord{len(m.ArchiveMapping), len(m.Refs), len(m.Worktrees), len(m.DirtyPaths), len(m.Stashes), len(m.Processes), len(m.Classifications)}
	if m.Aggregates != actual {
		return fmt.Errorf("aggregates mismatch: have %+v, want %+v", m.Aggregates, actual)
	}
	return nil
}

func addRecords[T any](seen map[string]string, records []T, kind string, id func(T) string) error {
	for _, record := range records {
		recordID := id(record)
		if !validDigest(recordID) {
			return fmt.Errorf("%s has invalid record_id %q", kind, recordID)
		}
		if previous, exists := seen[recordID]; exists {
			return fmt.Errorf("duplicate record_id %q for %s and %s", recordID, previous, kind)
		}
		seen[recordID] = kind
	}
	return nil
}

func validateMappings(m manifest) error {
	seen := make(map[string]struct{}, len(m.ArchiveMapping))
	worktrees := make(map[string]worktreeRecord, len(m.Worktrees))
	mapped := make(map[string]archiveMappingRecord, len(m.ArchiveMapping))
	mainCount := 0
	for _, worktree := range m.Worktrees {
		worktrees[worktree.RecordID] = worktree
	}
	for _, mapping := range m.ArchiveMapping {
		if !validDigest(mapping.RecordID) || mapping.WorktreeRecordID == "" || !canonicalAbsolutePath(mapping.Source) || !canonicalAbsolutePath(mapping.Destination) {
			return fmt.Errorf("invalid archive mapping %q", mapping.RecordID)
		}
		if mapping.Kind != "main_checkout" && mapping.Kind != "linked_worktree" {
			return fmt.Errorf("archive mapping %q has unknown kind %q", mapping.RecordID, mapping.Kind)
		}
		if _, exists := seen[mapping.RecordID]; exists {
			return fmt.Errorf("duplicate archive mapping %q", mapping.RecordID)
		}
		seen[mapping.RecordID] = struct{}{}
		worktree, exists := worktrees[mapping.WorktreeRecordID]
		if !exists {
			return fmt.Errorf("archive mapping %q references unknown worktree %q", mapping.RecordID, mapping.WorktreeRecordID)
		}
		if worktree.Prunable && !worktree.Present {
			return fmt.Errorf("archive mapping %q targets absent prunable worktree", mapping.RecordID)
		}
		if !worktree.Present {
			return fmt.Errorf("archive mapping %q targets absent worktree", mapping.RecordID)
		}
		if _, exists := mapped[mapping.WorktreeRecordID]; exists {
			return fmt.Errorf("multiple archive mappings target worktree %q", mapping.WorktreeRecordID)
		}
		if mapping.Source != worktree.Source {
			return fmt.Errorf("archive mapping %q source does not match worktree %q", mapping.RecordID, mapping.WorktreeRecordID)
		}
		if mapping.Kind == "main_checkout" {
			mainCount++
			if mapping.Source != m.Private.Root {
				return fmt.Errorf("main checkout mapping source %q does not match private root", mapping.Source)
			}
		} else if mapping.Source == m.Private.Root {
			return errors.New("private root must use main_checkout mapping")
		}
		mapped[mapping.WorktreeRecordID] = mapping
	}
	if mainCount != 1 {
		return fmt.Errorf("archive mapping has %d main checkouts, want exactly one", mainCount)
	}
	for _, worktree := range m.Worktrees {
		_, exists := mapped[worktree.RecordID]
		if worktree.Present && !exists {
			return fmt.Errorf("present worktree %q is missing archive mapping", worktree.RecordID)
		}
		if !worktree.Present && exists {
			return fmt.Errorf("absent worktree %q has archive mapping", worktree.RecordID)
		}
	}
	return nil
}

func validateClassifications(classifications []classificationRecord, targets map[string]string, phase validationPhase) error {
	seen := make(map[string]struct{}, len(classifications))
	covered := make(map[string]struct{}, len(targets))
	for _, classification := range classifications {
		if !validDigest(classification.RecordID) {
			return fmt.Errorf("classification has invalid record_id %q", classification.RecordID)
		}
		if _, exists := seen[classification.RecordID]; exists {
			return fmt.Errorf("duplicate classification record_id %q", classification.RecordID)
		}
		seen[classification.RecordID] = struct{}{}
		kind, exists := targets[classification.TargetRecordID]
		if !exists || kind != classification.TargetKind {
			return fmt.Errorf("classification %q has invalid target %q", classification.RecordID, classification.TargetRecordID)
		}
		if _, exists := covered[classification.TargetRecordID]; exists {
			return fmt.Errorf("multiple classifications target %q", classification.TargetRecordID)
		}
		covered[classification.TargetRecordID] = struct{}{}
		if err := validateClassification(classification, phase); err != nil {
			return err
		}
	}
	if len(covered) != len(targets) {
		missing := make([]string, 0, len(targets)-len(covered))
		for id := range targets {
			if _, ok := covered[id]; !ok {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("classification coverage missing %s", strings.Join(missing, ","))
	}
	return nil
}

func validateClassification(c classificationRecord, phase validationPhase) error {
	if c.Owner == "" {
		return fmt.Errorf("classification %q has empty owner", c.RecordID)
	}
	wantDisposition := map[string]string{
		"already_forward_ported": "retain_archive",
		"candidate_public_delta": "reexpress_public",
		"private_recovery":       "preserve",
		"never_public":           "exclude_public",
		"unresolved":             "block",
	}[c.Classification]
	if wantDisposition == "" {
		return fmt.Errorf("classification %q is unknown", c.Classification)
	}
	if c.RestoreDisposition != wantDisposition {
		return fmt.Errorf("classification %q requires restore_disposition %q", c.Classification, wantDisposition)
	}
	if c.ChecksumPolicy != "sha256" && c.ChecksumPolicy != "omit_sensitive" {
		return fmt.Errorf("classification %q has unknown checksum_policy %q", c.RecordID, c.ChecksumPolicy)
	}
	if phase == phasePreMove && c.Classification == "unresolved" {
		return fmt.Errorf("unresolved classification %q blocks pre-move", c.RecordID)
	}
	return nil
}

func validateProcesses(processes []processRecord, mappings []archiveMappingRecord) error {
	seen := make(map[string]struct{}, len(processes))
	for _, process := range processes {
		if !validDigest(process.RecordID) || process.RootRecordID == "" || process.PID <= 0 || !filepath.IsAbs(process.Path) {
			return fmt.Errorf("invalid process record %q", process.RecordID)
		}
		if process.OccupancyKind != "cwd" && process.OccupancyKind != "open_file" {
			return fmt.Errorf("process %q has unknown occupancy_kind %q", process.RecordID, process.OccupancyKind)
		}
		if _, exists := seen[process.RecordID]; exists {
			return fmt.Errorf("duplicate process record %q", process.RecordID)
		}
		seen[process.RecordID] = struct{}{}
		matches := 0
		for _, mapping := range mappings {
			if !isWithin(process.Path, mapping.Source) {
				continue
			}
			matches++
			wantRootID := makeRecordID("process_root", mapping.Source, mapping.Source)
			if process.RootRecordID != wantRootID {
				return fmt.Errorf("process %q has wrong root record ID", process.RecordID)
			}
		}
		if matches != 1 {
			return fmt.Errorf("process %q path must belong to exactly one mapped source", process.RecordID)
		}
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && value == strings.ToLower(value)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, runeValue := range value {
		if !(runeValue >= '0' && runeValue <= '9' || runeValue >= 'a' && runeValue <= 'f') {
			return false
		}
	}
	return true
}
