package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// gitReader is deliberately narrower than an arbitrary command runner. The
// collector only asks it to execute the read-only argv admitted below.
type gitReader interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type gitExitError interface {
	error
	ExitCode() int
}

type repositoryInventory struct {
	Repository      repositoryRecord
	Refs            []refRecord
	ArchiveMapping  []archiveMappingRecord
	Worktrees       []worktreeRecord
	DirtyPaths      []dirtyPathRecord
	Stashes         []stashRecord
	Classifications []classificationRecord
}

type safeGitReader struct {
	reader gitReader
	roots  map[string]struct{}
}

func (r safeGitReader) run(ctx context.Context, root string, argv ...string) ([]byte, error) {
	if _, ok := r.roots[root]; !ok {
		return nil, fmt.Errorf("git root %q is not in captured worktree set", root)
	}
	if !allowedGitCommand(argv) {
		return nil, fmt.Errorf("git command is not admitted: %q", argv)
	}
	return r.reader.Run(ctx, root, argv...)
}

func allowedGitCommand(argv []string) bool {
	if len(argv) == 2 && argv[0] == "rev-parse" && (argv[1] == "--show-toplevel" || argv[1] == "--git-common-dir") {
		return true
	}
	if len(argv) == 3 && argv[0] == "rev-parse" && argv[1] == "--verify" && argv[2] == "HEAD" {
		return true
	}
	if len(argv) == 4 && argv[0] == "symbolic-ref" && argv[1] == "--quiet" && argv[2] == "--short" && argv[3] == "HEAD" {
		return true
	}
	if len(argv) == 4 && argv[0] == "remote" && argv[1] == "get-url" && argv[2] == "--all" && argv[3] == "origin" {
		return true
	}
	if len(argv) == 2 && argv[0] == "for-each-ref" && argv[1] == "--format=%(refname)%00%(objectname)%00" {
		return true
	}
	if len(argv) == 4 && argv[0] == "worktree" && argv[1] == "list" && argv[2] == "--porcelain" && argv[3] == "-z" {
		return true
	}
	if len(argv) == 4 && argv[0] == "status" && argv[1] == "--porcelain=v1" && argv[2] == "-z" && argv[3] == "--untracked-files=all" {
		return true
	}
	return len(argv) == 3 && argv[0] == "stash" && argv[1] == "list" && argv[2] == "--format=%gd%x00%H%x00%ct%x00"
}

func collectRepositoryRecord(ctx context.Context, reader gitReader, root, role string) (repositoryRecord, error) {
	if role != "public" && role != "private" {
		return repositoryRecord{}, fmt.Errorf("unknown repository role %q", role)
	}
	admitted := safeGitReader{reader: reader, roots: map[string]struct{}{root: {}}}
	actualRoot, err := admitted.run(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("resolve %s root: %w", role, err)
	}
	canonicalRoot, err := canonicalExistingPath(strings.TrimSpace(string(actualRoot)))
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("canonicalize %s root: %w", role, err)
	}
	if canonicalRoot != root {
		return repositoryRecord{}, fmt.Errorf("git root %q does not match declared root %q", canonicalRoot, root)
	}
	head, err := admitted.run(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("read %s HEAD: %w", role, err)
	}
	headID := strings.TrimSpace(string(head))
	if !validGitObjectID(headID) {
		return repositoryRecord{}, fmt.Errorf("%s HEAD is not a supported Git object ID", role)
	}
	common, err := admitted.run(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("read %s common directory: %w", role, err)
	}
	commonDir, err := canonicalGitPath(root, strings.TrimSpace(string(common)))
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("canonicalize %s common directory: %w", role, err)
	}
	remote, err := admitted.run(ctx, root, "remote", "get-url", "--all", "origin")
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("read %s origin: %w", role, err)
	}
	origin, err := normalizeOriginSet(remote)
	if err != nil {
		return repositoryRecord{}, fmt.Errorf("normalize %s origin: %w", role, err)
	}
	branch, detached, err := symbolicBranch(ctx, admitted, root)
	if err != nil {
		return repositoryRecord{}, err
	}
	return repositoryRecord{Role: role, Root: canonicalRoot, Head: headID, Branch: branch, Detached: detached, CommonDir: commonDir, OriginRepository: origin}, nil
}

func collectPrivateInventory(ctx context.Context, reader gitReader, root string, input cutoverInput) (repositoryInventory, error) {
	if input.SchemaVersion != schemaVersion {
		return repositoryInventory{}, fmt.Errorf("cutover input schema_version = %d", input.SchemaVersion)
	}
	private, err := collectRepositoryRecord(ctx, reader, root, "private")
	if err != nil {
		return repositoryInventory{}, err
	}
	if private.OriginRepository != input.ExpectedPrivateRepository {
		return repositoryInventory{}, fmt.Errorf("private origin %q does not match expected private repository %q", private.OriginRepository, input.ExpectedPrivateRepository)
	}

	base := safeGitReader{reader: reader, roots: map[string]struct{}{private.Root: {}}}
	worktreeBytes, err := base.run(ctx, private.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return repositoryInventory{}, fmt.Errorf("list private worktrees: %w", err)
	}
	worktrees, err := parseWorktrees(worktreeBytes, private.Root)
	if err != nil {
		return repositoryInventory{}, err
	}
	roots := map[string]struct{}{}
	for _, worktree := range worktrees {
		roots[worktree.Source] = struct{}{}
	}
	admitted := safeGitReader{reader: reader, roots: roots}
	for i := range worktrees {
		if !worktrees[i].Present {
			continue
		}
		head, err := admitted.run(ctx, worktrees[i].Source, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return repositoryInventory{}, fmt.Errorf("read worktree %q HEAD: %w", worktrees[i].Source, err)
		}
		liveHead := strings.TrimSpace(string(head))
		if !validGitObjectID(liveHead) || liveHead != worktrees[i].Head {
			return repositoryInventory{}, fmt.Errorf("worktree %q HEAD differs from worktree porcelain", worktrees[i].Source)
		}
		branch, detached, err := symbolicBranch(ctx, admitted, worktrees[i].Source)
		if err != nil {
			return repositoryInventory{}, err
		}
		if branch != worktrees[i].Branch || detached != worktrees[i].Detached {
			return repositoryInventory{}, fmt.Errorf("worktree %q branch identity differs from worktree porcelain", worktrees[i].Source)
		}
		common, err := admitted.run(ctx, worktrees[i].Source, "rev-parse", "--git-common-dir")
		if err != nil {
			return repositoryInventory{}, fmt.Errorf("read worktree %q common directory: %w", worktrees[i].Source, err)
		}
		worktrees[i].CommonDir, err = canonicalGitPath(worktrees[i].Source, strings.TrimSpace(string(common)))
		if err != nil {
			return repositoryInventory{}, err
		}
	}

	refs, err := collectRefs(ctx, admitted, private)
	if err != nil {
		return repositoryInventory{}, err
	}
	dirty, err := collectDirtyPaths(ctx, admitted, worktrees)
	if err != nil {
		return repositoryInventory{}, err
	}
	stashes, err := collectStashes(ctx, admitted, private.Root)
	if err != nil {
		return repositoryInventory{}, err
	}
	mappings, err := collectMappings(private.Root, worktrees, input.Mappings)
	if err != nil {
		return repositoryInventory{}, err
	}
	classifications, err := classifyInventory(input, private.Root, refs, worktrees, dirty, stashes)
	if err != nil {
		return repositoryInventory{}, err
	}
	if err := applyDirtyChecksumPolicies(worktrees, dirty, classifications); err != nil {
		return repositoryInventory{}, err
	}
	return repositoryInventory{Repository: private, Refs: refs, ArchiveMapping: mappings, Worktrees: worktrees, DirtyPaths: dirty, Stashes: stashes, Classifications: classifications}, nil
}

func symbolicBranch(ctx context.Context, reader safeGitReader, root string) (string, bool, error) {
	branch, err := reader.run(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exit gitExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", true, nil
		}
		return "", false, fmt.Errorf("read symbolic branch for %q: %w", root, err)
	}
	value := strings.TrimSpace(string(branch))
	if value == "" {
		return "", false, fmt.Errorf("symbolic branch for %q is empty", root)
	}
	return value, false, nil
}

func collectRefs(ctx context.Context, reader safeGitReader, private repositoryRecord) ([]refRecord, error) {
	b, err := reader.run(ctx, private.Root, "for-each-ref", "--format=%(refname)%00%(objectname)%00")
	if err != nil {
		return nil, err
	}
	records, err := parseNULLineRecords(b, 2, "ref")
	if err != nil {
		return nil, err
	}
	refs := make([]refRecord, 0, len(records))
	for _, fields := range records {
		name, objectID := string(fields[0]), string(fields[1])
		if name == "" || !validGitObjectID(objectID) {
			return nil, errors.New("ref output has empty name or object ID")
		}
		refs = append(refs, refRecord{RecordID: makeRecordID("ref", private.Root, name), RepositoryRole: private.Role, RefName: name, ObjectID: objectID})
	}
	return refs, nil
}

func parseWorktrees(b []byte, privateRoot string) ([]worktreeRecord, error) {
	groups := splitNULGroups(b)
	worktrees := make([]worktreeRecord, 0, len(groups))
	for _, group := range groups {
		var source, head string
		var branch string
		var detached, locked, prunable bool
		for _, field := range group {
			value := string(field)
			switch {
			case strings.HasPrefix(value, "worktree "):
				source = strings.TrimPrefix(value, "worktree ")
			case strings.HasPrefix(value, "HEAD "):
				head = strings.TrimPrefix(value, "HEAD ")
			case strings.HasPrefix(value, "branch "):
				const prefix = "branch refs/heads/"
				if !strings.HasPrefix(value, prefix) {
					return nil, fmt.Errorf("unsupported worktree branch %q", value)
				}
				branch = strings.TrimPrefix(value, prefix)
			case value == "detached":
				detached = true
			case value == "locked" || strings.HasPrefix(value, "locked "):
				locked = true
			case value == "prunable" || strings.HasPrefix(value, "prunable "):
				prunable = true
			default:
				return nil, fmt.Errorf("unknown worktree porcelain field %q", value)
			}
		}
		if source == "" {
			return nil, errors.New("worktree porcelain is missing source")
		}
		canonical, present, err := canonicalWorktreePath(source, prunable)
		if err != nil {
			return nil, err
		}
		if !present && !prunable {
			return nil, fmt.Errorf("non-prunable worktree %q is absent", canonical)
		}
		if detached {
			if branch != "" {
				return nil, errors.New("detached worktree has a branch")
			}
			branch = ""
		} else if branch == "" && (present || !prunable) {
			return nil, errors.New("attached worktree is missing branch")
		}
		if !validGitObjectID(head) {
			return nil, fmt.Errorf("worktree %q has unsupported HEAD object ID", source)
		}
		worktrees = append(worktrees, worktreeRecord{RecordID: makeRecordID("worktree", privateRoot, canonical), Source: canonical, Head: head, Branch: branch, Detached: detached, Locked: locked, Prunable: prunable, Present: present, PorcelainBase64: base64.StdEncoding.EncodeToString(joinNUL(group))})
	}
	if len(worktrees) == 0 {
		return nil, errors.New("git worktree list returned no worktrees")
	}
	return worktrees, nil
}

func collectDirtyPaths(ctx context.Context, reader safeGitReader, worktrees []worktreeRecord) ([]dirtyPathRecord, error) {
	var records []dirtyPathRecord
	for _, worktree := range worktrees {
		if !worktree.Present {
			continue
		}
		b, err := reader.run(ctx, worktree.Source, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil {
			return nil, fmt.Errorf("read worktree %q status: %w", worktree.Source, err)
		}
		parsed, err := parseDirtyStatus(worktree, b)
		if err != nil {
			return nil, err
		}
		records = append(records, parsed...)
	}
	return records, nil
}

func parseDirtyStatus(worktree worktreeRecord, b []byte) ([]dirtyPathRecord, error) {
	fields := splitNUL(b)
	records := make([]dirtyPathRecord, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		if len(fields[i]) == 0 {
			continue
		}
		if len(fields[i]) < 4 || fields[i][2] != ' ' {
			return nil, fmt.Errorf("invalid porcelain status record %q", string(fields[i]))
		}
		status, relative := string(fields[i][:2]), fields[i][3:]
		if len(relative) == 0 || !safeRelativePath(relative) {
			return nil, errors.New("dirty path is empty or escapes worktree")
		}
		var original []byte
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			i++
			if i >= len(fields) || len(fields[i]) == 0 || !safeRelativePath(fields[i]) {
				return nil, errors.New("rename record is missing safe original path")
			}
			original = fields[i]
		}
		record := dirtyPathRecord{WorktreeRecordID: worktree.RecordID, StatusCode: status, RelativePathBase64: base64.StdEncoding.EncodeToString(relative), OriginalPathBase64: base64.StdEncoding.EncodeToString(original)}
		record.RecordID = makeRecordID("dirty_path", worktree.Source, dirtyPathIdentity(record))
		if err := populateDirtyMetadata(worktree.Source, relative, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func populateDirtyMetadata(root string, relative []byte, record *dirtyPathRecord) error {
	path := filepath.Join(root, filepath.FromSlash(string(relative)))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		record.FileType, record.OmissionReason = "missing", "missing"
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		record.FileType, record.OmissionReason = "symlink", "symlink"
	case info.IsDir():
		record.FileType, record.OmissionReason = "directory", "directory"
	case !info.Mode().IsRegular():
		record.FileType, record.OmissionReason = "other", "not_regular"
	default:
		record.FileType, record.OmissionReason = "regular", "omit_sensitive"
	}
	return nil
}

func applyDirtyChecksumPolicies(worktrees []worktreeRecord, dirty []dirtyPathRecord, classifications []classificationRecord) error {
	policies := make(map[string]string, len(classifications))
	for _, classification := range classifications {
		if classification.TargetKind == "dirty_path" {
			policies[classification.TargetRecordID] = classification.ChecksumPolicy
		}
	}
	roots := make(map[string]string, len(worktrees))
	for _, worktree := range worktrees {
		roots[worktree.RecordID] = worktree.Source
	}
	for i := range dirty {
		if policies[dirty[i].RecordID] != "sha256" || dirty[i].FileType != "regular" {
			continue
		}
		relative, err := base64.StdEncoding.DecodeString(dirty[i].RelativePathBase64)
		if err != nil {
			return fmt.Errorf("decode dirty path identity: %w", err)
		}
		size, digest, err := hashRegularFile(roots[dirty[i].WorktreeRecordID], relative)
		if err != nil {
			return err
		}
		dirty[i].Size, dirty[i].SHA256, dirty[i].OmissionReason = size, digest, ""
	}
	return nil
}

func hashRegularFile(root string, relative []byte) (int64, string, error) {
	path := filepath.Join(root, filepath.FromSlash(string(relative)))
	before, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !before.Mode().IsRegular() {
		return 0, "", errors.New("checksum target is not a regular file")
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return 0, "", errors.New("checksum target changed before safe open")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return 0, "", err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return 0, "", errors.New("checksum target changed during capture")
	}
	return before.Size(), "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func collectStashes(ctx context.Context, reader safeGitReader, root string) ([]stashRecord, error) {
	b, err := reader.run(ctx, root, "stash", "list", "--format=%gd%x00%H%x00%ct%x00")
	if err != nil {
		return nil, err
	}
	records, err := parseNULLineRecords(b, 3, "stash")
	if err != nil {
		return nil, err
	}
	stashes := make([]stashRecord, 0, len(records))
	for _, fields := range records {
		captured, err := strconv.ParseInt(string(fields[2]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse stash timestamp: %w", err)
		}
		ref, oid := string(fields[0]), string(fields[1])
		if ref == "" || !validGitObjectID(oid) {
			return nil, errors.New("stash output has empty identity")
		}
		stashes = append(stashes, stashRecord{RecordID: makeRecordID("stash", root, ref), RefName: ref, ObjectID: oid, CapturedUnix: captured})
	}
	return stashes, nil
}

func collectMappings(privateRoot string, worktrees []worktreeRecord, inputs []archiveMappingInput) ([]archiveMappingRecord, error) {
	bySource := make(map[string]archiveMappingInput, len(inputs))
	byDestination := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		source, err := canonicalExistingPath(input.Source)
		if err != nil {
			return nil, err
		}
		destination, err := canonicalDestinationPath(input.Destination)
		if err != nil {
			return nil, err
		}
		if input.Kind != "main_checkout" && input.Kind != "linked_worktree" {
			return nil, fmt.Errorf("unknown mapping kind %q", input.Kind)
		}
		if _, exists := bySource[source]; exists {
			return nil, fmt.Errorf("duplicate mapping source %q", source)
		}
		if _, exists := byDestination[destination]; exists {
			return nil, fmt.Errorf("duplicate or aliased mapping destination %q", destination)
		}
		byDestination[destination] = struct{}{}
		input.Source, input.Destination = source, destination
		bySource[source] = input
	}
	mappings := make([]archiveMappingRecord, 0, len(worktrees))
	for _, worktree := range worktrees {
		input, found := bySource[worktree.Source]
		if worktree.Prunable && !worktree.Present {
			if found {
				return nil, fmt.Errorf("absent prunable worktree %q may not have a mapping", worktree.Source)
			}
			continue
		}
		if !found {
			return nil, fmt.Errorf("present worktree %q is missing archive mapping", worktree.Source)
		}
		wantKind := "linked_worktree"
		if worktree.Source == privateRoot {
			wantKind = "main_checkout"
		}
		if input.Kind != wantKind {
			return nil, fmt.Errorf("worktree %q mapping kind = %q, want %q", worktree.Source, input.Kind, wantKind)
		}
		mappings = append(mappings, archiveMappingRecord{RecordID: makeRecordID("archive_mapping", worktree.Source, input.Destination), WorktreeRecordID: worktree.RecordID, Kind: input.Kind, Source: worktree.Source, Destination: input.Destination})
		delete(bySource, worktree.Source)
	}
	if len(bySource) != 0 {
		return nil, errors.New("archive mapping does not target a captured worktree")
	}
	return mappings, nil
}

type classificationTarget struct{ id, kind, source, identity string }

func classifyInventory(input cutoverInput, privateRoot string, refs []refRecord, worktrees []worktreeRecord, dirty []dirtyPathRecord, stashes []stashRecord) ([]classificationRecord, error) {
	knownDefaults := map[string]struct{}{"ref": {}, "worktree": {}, "dirty_path": {}, "stash": {}}
	if len(input.Defaults) != len(knownDefaults) {
		return nil, errors.New("classification defaults must contain exactly four kinds")
	}
	defaults := make(map[string]classificationDefault, len(input.Defaults))
	for _, d := range input.Defaults {
		if _, known := knownDefaults[d.Kind]; !known || defaults[d.Kind].Kind != "" {
			return nil, fmt.Errorf("duplicate or empty classification default %q", d.Kind)
		}
		defaults[d.Kind] = d
	}
	targets := make([]classificationTarget, 0, len(refs)+len(worktrees)+len(dirty)+len(stashes))
	for _, r := range refs {
		targets = append(targets, classificationTarget{r.RecordID, "ref", privateRoot, r.RefName})
	}
	for _, w := range worktrees {
		targets = append(targets, classificationTarget{w.RecordID, "worktree", privateRoot, w.Source})
	}
	worktreeRoots := make(map[string]string, len(worktrees))
	for _, w := range worktrees {
		worktreeRoots[w.RecordID] = w.Source
	}
	for _, d := range dirty {
		targets = append(targets, classificationTarget{d.RecordID, "dirty_path", worktreeRoots[d.WorktreeRecordID], dirtyPathIdentity(d)})
	}
	for _, s := range stashes {
		targets = append(targets, classificationTarget{s.RecordID, "stash", privateRoot, s.RefName})
	}
	for _, kind := range []string{"ref", "worktree", "dirty_path", "stash"} {
		if defaults[kind].Kind == "" {
			return nil, fmt.Errorf("missing classification default for %s", kind)
		}
	}
	used := make([]int, len(input.Rules))
	results := make([]classificationRecord, 0, len(targets))
	for _, target := range targets {
		choice := defaults[target.kind]
		matches := 0
		for i, rule := range input.Rules {
			if rule.Kind == target.kind && rule.Source == target.source && rule.Identity == target.identity {
				matches++
				if matches > 1 {
					return nil, fmt.Errorf("multiple classification overrides target %q", target.id)
				}
				used[i]++
				choice = classificationDefault{Kind: target.kind, Classification: rule.Classification, Owner: rule.Owner, RestoreDisposition: rule.RestoreDisposition, ChecksumPolicy: rule.ChecksumPolicy}
			}
		}
		result := classificationRecord{TargetRecordID: target.id, TargetKind: target.kind, Classification: choice.Classification, Owner: choice.Owner, RestoreDisposition: choice.RestoreDisposition, ChecksumPolicy: choice.ChecksumPolicy}
		result.RecordID = makeRecordID("classification", result.TargetRecordID, result.Classification)
		if err := validateClassification(result, phaseCapture); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	for i, count := range used {
		if count == 0 {
			return nil, fmt.Errorf("classification override %d matched no record", i)
		}
	}
	return results, nil
}

func normalizeOriginSet(b []byte) (string, error) {
	var value string
	for _, line := range strings.Fields(string(b)) {
		normalized, err := normalizeOrigin(line)
		if err != nil {
			return "", err
		}
		if value != "" && value != normalized {
			return "", errors.New("origin has multiple repository identities")
		}
		value = normalized
	}
	if value == "" {
		return "", errors.New("origin is empty")
	}
	return value, nil
}

func normalizeOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "https://github.com/"):
		value = strings.TrimPrefix(value, "https://github.com/")
	case strings.HasPrefix(value, "git@github.com:"):
		value = strings.TrimPrefix(value, "git@github.com:")
	case strings.HasPrefix(value, "ssh://git@github.com/"):
		value = strings.TrimPrefix(value, "ssh://git@github.com/")
	default:
		return "", fmt.Errorf("unsupported origin %q", value)
	}
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("unsupported origin %q", value)
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), nil
}

func canonicalExistingPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path is not absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
func canonicalDestinationPath(path string) (string, error) {
	return resolveProspectivePath(path)
}
func canonicalWorktreePath(path string, prunable bool) (string, bool, error) {
	if !filepath.IsAbs(path) {
		return "", false, errors.New("worktree path is not absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), true, nil
	}
	if prunable && errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(path), false, nil
	}
	return "", false, err
}
func canonicalGitPath(root, value string) (string, error) {
	if value == "" {
		return "", errors.New("git common directory is empty")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return canonicalExistingPath(value)
}
func safeRelativePath(path []byte) bool {
	raw := string(path)
	value := filepath.Clean(filepath.FromSlash(raw))
	return raw != "" && raw == filepath.ToSlash(value) && value != "." && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) && !filepath.IsAbs(value)
}
func parseNULLineRecords(output []byte, wantFields int, kind string) ([][][]byte, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != '\n' {
		return nil, fmt.Errorf("%s output is missing final newline record boundary", kind)
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	records := make([][][]byte, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 || line[len(line)-1] != 0 {
			return nil, fmt.Errorf("%s record is missing final NUL field terminator", kind)
		}
		fields := bytes.Split(line[:len(line)-1], []byte{0})
		if len(fields) != wantFields {
			return nil, fmt.Errorf("%s record has %d fields, want %d", kind, len(fields), wantFields)
		}
		for _, field := range fields {
			if len(field) == 0 || bytes.Contains(field, []byte{'\n'}) {
				return nil, fmt.Errorf("%s record has empty or multiline field", kind)
			}
		}
		records = append(records, fields)
	}
	return records, nil
}
func splitNUL(b []byte) [][]byte {
	parts := strings.Split(string(b), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	result := make([][]byte, len(parts))
	for i := range parts {
		result[i] = []byte(parts[i])
	}
	return result
}
func splitNULGroups(b []byte) [][][]byte {
	fields := splitNUL(b)
	var groups [][][]byte
	current := make([][]byte, 0)
	for _, field := range fields {
		if len(field) == 0 {
			if len(current) > 0 {
				groups = append(groups, current)
				current = nil
			}
			continue
		}
		current = append(current, field)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}
func joinNUL(fields [][]byte) []byte {
	return []byte(strings.Join(func() []string {
		values := make([]string, len(fields))
		for i := range fields {
			values[i] = string(fields[i])
		}
		return values
	}(), "\x00"))
}
