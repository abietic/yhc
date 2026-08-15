package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type dependencies struct {
	Git       gitReader
	Processes processReader
	Now       func() time.Time
}

// verifyLiveState deliberately only observes Git and process state.  Moving a
// checkout and repairing its worktree registrations remain an operator action.
func verifyLiveState(ctx context.Context, deps dependencies, frozen manifest, phase validationPhase) error {
	if phase != phasePreMove && phase != phasePostMove && phase != phasePreRollback && phase != phaseRollback {
		return fmt.Errorf("verification phase %q is not supported", phase)
	}
	if deps.Git == nil || deps.Processes == nil {
		return errors.New("verification dependencies are incomplete")
	}
	if err := validateManifest(frozen, phase); err != nil {
		return fmt.Errorf("validate frozen manifest: %w", err)
	}
	if err := validateMappingTopology(frozen.ArchiveMapping); err != nil {
		return err
	}

	main, err := mainMapping(frozen.ArchiveMapping)
	if err != nil {
		return err
	}
	currentMappings, inverse, err := phaseMappings(frozen.ArchiveMapping, phase)
	if err != nil {
		return err
	}
	if phase == phasePreMove || phase == phaseRollback {
		if err := requireAbsent(mappingDestinations(frozen.ArchiveMapping)); err != nil {
			return err
		}
	}
	if phase == phasePostMove || phase == phasePreRollback {
		if err := requireAbsent(mappingSources(frozen.ArchiveMapping)); err != nil {
			return err
		}
	}

	public, err := collectRepositoryRecord(ctx, deps.Git, frozen.Public.Root, "public")
	if err != nil {
		return fmt.Errorf("collect public repository: %w", err)
	}

	privateRoot := main.Source
	if phase == phasePostMove || phase == phasePreRollback {
		privateRoot = main.Destination
	}
	live, err := collectLivePrivateInventory(ctx, deps.Git, privateRoot)
	if err != nil {
		return fmt.Errorf("collect private repository: %w", err)
	}
	if err := normalizeInventory(&live, frozen.Private.Root, inverse, frozen.Classifications); err != nil {
		return fmt.Errorf("normalize private inventory: %w", err)
	}
	mismatches := compareInventory(frozen, live)
	if !sameRepository(frozen.Public, public) {
		mismatches = append(mismatches, "public_repository")
	}

	if phase == phasePreMove || phase == phasePreRollback {
		roots := make([]string, 0, len(currentMappings))
		for _, mapping := range currentMappings {
			roots = append(roots, mapping.Source)
		}
		occupants, err := collectProcessOccupancy(ctx, deps.Processes, roots)
		if err != nil {
			return fmt.Errorf("collect process occupancy: %w", err)
		}
		if len(occupants) != 0 {
			mismatches = append(mismatches, "process_occupancy")
		}
	}
	if len(mismatches) != 0 {
		sort.Strings(mismatches)
		return fmt.Errorf("live state mismatches: %s", strings.Join(mismatches, ","))
	}
	return nil
}

func mainMapping(mappings []archiveMappingRecord) (archiveMappingRecord, error) {
	var main archiveMappingRecord
	for _, mapping := range mappings {
		if mapping.Kind != "main_checkout" {
			continue
		}
		if main.RecordID != "" {
			return archiveMappingRecord{}, errors.New("archive mapping has multiple main checkouts")
		}
		main = mapping
	}
	if main.RecordID == "" {
		return archiveMappingRecord{}, errors.New("archive mapping has no main checkout")
	}
	return main, nil
}

func phaseMappings(mappings []archiveMappingRecord, phase validationPhase) ([]archiveMappingRecord, map[string]string, error) {
	current := make([]archiveMappingRecord, 0, len(mappings))
	inverse := make(map[string]string, len(mappings))
	for _, mapping := range mappings {
		if phase == phasePostMove || phase == phasePreRollback {
			current = append(current, archiveMappingRecord{Source: mapping.Destination, Destination: mapping.Source})
			inverse[mapping.Destination] = mapping.Source
			continue
		}
		current = append(current, archiveMappingRecord{Source: mapping.Source, Destination: mapping.Destination})
		inverse[mapping.Source] = mapping.Source
	}
	return current, inverse, nil
}

func validateMappingTopology(mappings []archiveMappingRecord) error {
	if len(mappings) == 0 {
		return errors.New("archive mapping is empty")
	}
	sources := mappingSources(mappings)
	destinations := mappingDestinations(mappings)
	if err := requireDisjointRoots("source", sources); err != nil {
		return err
	}
	if err := requireDisjointRoots("destination", destinations); err != nil {
		return err
	}
	for _, source := range sources {
		for _, destination := range destinations {
			if overlappingPaths(source, destination) {
				return fmt.Errorf("archive mapping source %q overlaps destination %q", source, destination)
			}
		}
	}
	return nil
}

func mappingSources(mappings []archiveMappingRecord) []string {
	result := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		result = append(result, mapping.Source)
	}
	return result
}

func mappingDestinations(mappings []archiveMappingRecord) []string {
	result := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		result = append(result, mapping.Destination)
	}
	return result
}

func requireDisjointRoots(kind string, roots []string) error {
	for i, root := range roots {
		for j := 0; j < i; j++ {
			if overlappingPaths(root, roots[j]) {
				return fmt.Errorf("archive mapping %s roots overlap: %q and %q", kind, roots[j], root)
			}
		}
	}
	return nil
}

func overlappingPaths(left, right string) bool {
	return isWithin(left, right) || isWithin(right, left)
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func requireAbsent(paths []string) error {
	for _, path := range paths {
		resolved, err := resolveProspectivePath(path)
		if err != nil {
			return fmt.Errorf("resolve prospective collision path %q: %w", path, err)
		}
		if resolved != path {
			return fmt.Errorf("prospective collision path %q drifted to %q", path, resolved)
		}
		_, err = os.Lstat(path)
		if err == nil {
			return fmt.Errorf("path collision at %q", path)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect collision path %q: %w", path, err)
		}
	}
	return nil
}

func collectLivePrivateInventory(ctx context.Context, reader gitReader, root string) (repositoryInventory, error) {
	private, err := collectRepositoryRecord(ctx, reader, root, "private")
	if err != nil {
		return repositoryInventory{}, err
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
	roots := make(map[string]struct{}, len(worktrees))
	for _, worktree := range worktrees {
		if worktree.Present {
			roots[worktree.Source] = struct{}{}
		}
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
		if value := strings.TrimSpace(string(head)); value != worktrees[i].Head {
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
	return repositoryInventory{Repository: private, Refs: refs, Worktrees: worktrees, DirtyPaths: dirty, Stashes: stashes}, nil
}

func normalizeInventory(inventory *repositoryInventory, frozenRoot string, inverse map[string]string, classifications []classificationRecord) error {
	inventory.Repository.Root = mapPath(inventory.Repository.Root, inverse)
	inventory.Repository.CommonDir = mapPath(inventory.Repository.CommonDir, inverse)
	for i := range inventory.Refs {
		inventory.Refs[i].RecordID = makeRecordID("ref", frozenRoot, inventory.Refs[i].RefName)
	}
	currentToFrozen := make(map[string]string, len(inventory.Worktrees))
	frozenToCurrentRoot := make(map[string]string, len(inventory.Worktrees))
	for i := range inventory.Worktrees {
		worktree := &inventory.Worktrees[i]
		currentID := worktree.RecordID
		currentRoot := worktree.Source
		porcelain, err := normalizeWorktreePorcelain(worktree.PorcelainBase64, inverse)
		if err != nil {
			return err
		}
		worktree.Source = mapPath(worktree.Source, inverse)
		worktree.CommonDir = mapPath(worktree.CommonDir, inverse)
		worktree.RecordID = makeRecordID("worktree", frozenRoot, worktree.Source)
		worktree.PorcelainBase64 = porcelain
		currentToFrozen[currentID] = worktree.RecordID
		frozenToCurrentRoot[worktree.RecordID] = currentRoot
	}
	policies := make(map[string]string, len(classifications))
	for _, classification := range classifications {
		if classification.TargetKind == "dirty_path" {
			policies[classification.TargetRecordID] = classification.ChecksumPolicy
		}
	}
	for i := range inventory.DirtyPaths {
		dirty := &inventory.DirtyPaths[i]
		frozenWorktreeID, ok := currentToFrozen[dirty.WorktreeRecordID]
		if !ok {
			continue
		}
		dirty.WorktreeRecordID = frozenWorktreeID
		for _, worktree := range inventory.Worktrees {
			if worktree.RecordID == frozenWorktreeID {
				dirty.RecordID = makeRecordID("dirty_path", worktree.Source, dirtyPathIdentity(*dirty))
				break
			}
		}
		if policies[dirty.RecordID] == "sha256" && dirty.FileType == "regular" {
			relative, err := base64.StdEncoding.DecodeString(dirty.RelativePathBase64)
			if err != nil {
				return fmt.Errorf("decode dirty path identity: %w", err)
			}
			size, digest, err := hashRegularFile(frozenToCurrentRoot[frozenWorktreeID], relative)
			if err != nil {
				return err
			}
			dirty.Size, dirty.SHA256, dirty.OmissionReason = size, digest, ""
		}
	}
	for i := range inventory.Stashes {
		inventory.Stashes[i].RecordID = makeRecordID("stash", frozenRoot, inventory.Stashes[i].RefName)
	}
	return nil
}

func normalizeWorktreePorcelain(encoded string, inverse map[string]string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode worktree porcelain: %w", err)
	}
	fields := splitNUL(raw)
	found := false
	for i, field := range fields {
		const prefix = "worktree "
		if !strings.HasPrefix(string(field), prefix) {
			continue
		}
		if found {
			return "", errors.New("worktree porcelain has multiple roots")
		}
		found = true
		fields[i] = []byte(prefix + mapPath(strings.TrimPrefix(string(field), prefix), inverse))
	}
	if !found {
		return "", errors.New("worktree porcelain has no root")
	}
	return base64.StdEncoding.EncodeToString(joinNUL(fields)), nil
}

func mapPath(path string, inverse map[string]string) string {
	best := ""
	for current := range inverse {
		if isWithin(path, current) && len(current) > len(best) {
			best = current
		}
	}
	if best == "" {
		return path
	}
	rel, err := filepath.Rel(best, path)
	if err != nil {
		return path
	}
	return filepath.Join(inverse[best], rel)
}

func sameRepository(expected, actual repositoryRecord) bool { return expected == actual }

func compareInventory(frozen manifest, live repositoryInventory) []string {
	var mismatches []string
	if !sameRepository(frozen.Private, live.Repository) {
		mismatches = append(mismatches, "private_repository")
	}
	if !sameRecords(frozen.Refs, live.Refs, func(record refRecord) string { return record.RecordID }) {
		mismatches = append(mismatches, "refs")
	}
	if !sameRecords(frozen.Worktrees, live.Worktrees, func(record worktreeRecord) string { return record.RecordID }) {
		mismatches = append(mismatches, "worktrees")
	}
	if !sameRecords(frozen.DirtyPaths, live.DirtyPaths, func(record dirtyPathRecord) string { return record.RecordID }) {
		mismatches = append(mismatches, "dirty_paths")
	}
	if !sameRecords(frozen.Stashes, live.Stashes, func(record stashRecord) string { return record.RecordID }) {
		mismatches = append(mismatches, "stashes")
	}
	return mismatches
}

func sameRecords[T comparable](expected, actual []T, id func(T) string) bool {
	left := make(map[string]T, len(expected))
	for _, record := range expected {
		key := id(record)
		if _, exists := left[key]; exists {
			return false
		}
		left[key] = record
	}
	if len(left) != len(actual) {
		return false
	}
	for _, record := range actual {
		key := id(record)
		want, exists := left[key]
		if !exists || want != record {
			return false
		}
		delete(left, key)
	}
	return len(left) == 0
}

func processIDs(records []processRecord) string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.RecordID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
