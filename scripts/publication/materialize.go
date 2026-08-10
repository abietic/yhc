package main

// The publication tree is deliberately assembled from the git index rather
// than copied with filepath.Walk.  That distinction keeps ignored and local
// operational state outside a release even when it happens to sit beside an
// approved file.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const publicationManifest = "PUBLICATION_MANIFEST.json"

type ReleaseManifest struct {
	SchemaVersion    int               `json:"schema_version"`
	SourceTreeSHA256 string            `json:"source_tree_sha256"`
	TreeSHA256       string            `json:"tree_sha256"`
	FileCount        int               `json:"file_count"`
	Checks           map[string]string `json:"checks"`
	SBOMSHA256       string            `json:"sbom_sha256"`
}

type TreeSummary struct {
	SourceTreeSHA256 string
	TreeSHA256       string
	FileCount        int
	SBOMSHA256       string
}

type treeFile struct{ path, mode, digest string }

// publicationRaceHook is a deterministic test seam for replacement races.
// Production leaves it nil and all validation remains on the normal path.
var publicationRaceHook func(stage, path string)

func firePublicationRaceHook(stage, path string) {
	if publicationRaceHook != nil {
		publicationRaceHook(stage, path)
	}
}

type pinnedDirectory struct {
	path string
	root *os.Root
	info os.FileInfo
}

func openPinnedDirectory(rootPath, label string) (*pinnedDirectory, error) {
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", label, err)
	}
	before, err := os.Lstat(absolute)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s root is unsafe", label)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open %s root: %w", label, err)
	}
	pinned := &pinnedDirectory{path: absolute, root: root, info: before}
	if err := pinned.revalidate(label); err != nil {
		_ = root.Close()
		return nil, err
	}
	return pinned, nil
}

func (pinned *pinnedDirectory) revalidate(label string) error {
	opened, openErr := pinned.root.Stat(".")
	current, pathErr := os.Lstat(pinned.path)
	if openErr != nil || pathErr != nil || !opened.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(pinned.info, opened) || !os.SameFile(pinned.info, current) {
		return fmt.Errorf("%s root changed", label)
	}
	return nil
}

func (pinned *pinnedDirectory) Close() error {
	return pinned.root.Close()
}

func materialize(ctx context.Context, config Config, sourceCommit, outputPath string) error {
	snapshot, err := captureApprovedSourceSnapshot(ctx, config, sourceCommit)
	if err != nil {
		return err
	}
	payload, err := materializationPayloadSnapshot(snapshot)
	if err != nil {
		return err
	}
	inv := payload.inventory
	if !filepath.IsAbs(outputPath) || filepath.Clean(outputPath) != outputPath {
		return errors.New("publication output must be an absolute clean path")
	}
	sourcePath, err := os.Getwd()
	if err != nil {
		return err
	}
	sourcePinned, err := openPinnedDirectory(sourcePath, "publication source")
	if err != nil {
		return err
	}
	defer sourcePinned.Close()
	requested := filepath.Base(outputPath)
	requestedParent := filepath.Dir(outputPath)
	parentInfo, err := callerDirectoryInfo(requestedParent)
	// Darwin exposes /tmp as the documented /private/tmp alias.  It is the one
	// system alias accepted here; arbitrary caller-created symlink parents stay
	// unsafe because the requested parent itself must be a real directory.
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("publication caller parent is unsafe")
	}
	parentPath, err := filepath.EvalSymlinks(requestedParent)
	if err != nil {
		return fmt.Errorf("canonicalize publication parent: %w", err)
	}
	canonicalSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return fmt.Errorf("canonicalize publication source: %w", err)
	}
	canonicalOutput := filepath.Join(parentPath, requested)
	if relative, err := filepath.Rel(canonicalSource, canonicalOutput); err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("publication output must be outside source tree")
	}
	// EvalSymlinks intentionally applies only to the existing parent; the leaf
	// must remain absent and is never resolved through a caller symlink.
	if requested == "." || requested == string(filepath.Separator) || !safeSegment(requested) {
		return errors.New("unsafe publication output name")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return fmt.Errorf("open publication parent: %w", err)
	}
	defer parent.Close()
	before, err := parent.Stat(".")
	if err != nil || !before.IsDir() || !os.SameFile(parentInfo, before) {
		return errors.New("publication parent is unsafe")
	}
	if _, err := parent.Lstat(requested); !os.IsNotExist(err) {
		return errors.New("publication output already exists")
	}
	stageName, stage, err := createStage(parent)
	if err != nil {
		return err
	}
	defer stage.Close()
	promoted := false
	complete := false
	defer func() {
		if promoted && !complete {
			_ = parent.RemoveAll(requested)
			_ = syncRoot(parent)
		} else if !promoted {
			_ = parent.RemoveAll(stageName)
		}
	}()
	stageInfo, err := parent.Lstat(stageName)
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode().Perm() != 0o700 {
		return errors.New("publication staging directory is unsafe")
	}
	stageOpened, err := stage.Stat(".")
	if err != nil || !os.SameFile(stageInfo, stageOpened) {
		return errors.New("publication staging directory changed while opening")
	}
	if err := copyInventory(ctx, stage, inv.Files, payload.entries); err != nil {
		return err
	}
	if err := revalidateRootEntry(parent, stageName, stageInfo, stage); err != nil {
		return errors.New("publication staging directory changed during copy")
	}
	stagePath := filepath.Join(parentPath, stageName)
	summary, err := checkInventoryTreeWithEntries(ctx, stagePath, config, inv, false, payload.entries)
	if err != nil {
		return fmt.Errorf("validate staged publication tree: %w", err)
	}
	sourceDigest, err := sourceTreeDigest(inv.Files, payload.entries)
	if err != nil {
		return err
	}
	if summary.TreeSHA256 != sourceDigest {
		return errors.New("staged publication digest differs from source inventory")
	}
	if err := syncRoot(stage); err != nil {
		return err
	}
	if err := revalidateRootEntry(parent, stageName, stageInfo, stage); err != nil {
		return errors.New("publication staging directory changed before promotion")
	}
	if err := sourcePinned.revalidate("publication source"); err != nil {
		return err
	}
	if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	firePublicationRaceHook("before-promotion", outputPath)
	current, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, current) || !sameCallerDirectory(requestedParent, parentInfo) {
		return errors.New("publication parent changed before promotion")
	}
	staged, err := parent.Lstat(stageName)
	if err != nil || !os.SameFile(stageInfo, staged) {
		return errors.New("publication staging directory changed before promotion")
	}
	if _, err := parent.Lstat(requested); !os.IsNotExist(err) {
		return errors.New("publication output appeared before promotion")
	}
	if err := parent.Rename(stageName, requested); err != nil {
		return fmt.Errorf("promote publication tree: %w", err)
	}
	promoted = true
	firePublicationRaceHook("after-promotion", outputPath)
	if err := syncRoot(parent); err != nil {
		return err
	}
	current, err = parent.Stat(".")
	if err != nil || !os.SameFile(before, current) || !sameCallerDirectory(requestedParent, parentInfo) {
		return errors.New("publication parent changed after promotion")
	}
	if err := sourcePinned.revalidate("publication source"); err != nil {
		return err
	}
	if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
		return err
	}
	finalInfo, err := parent.Lstat(requested)
	if err != nil || !os.SameFile(stageInfo, finalInfo) {
		return errors.New("publication output changed after promotion")
	}
	complete = true
	return nil
}

func sameCallerDirectory(path string, expected os.FileInfo) bool {
	current, err := callerDirectoryInfo(path)
	return err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(expected, current)
}

func callerDirectoryInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err == nil && runtime.GOOS == "darwin" && path == "/tmp" && info.Mode()&os.ModeSymlink != 0 {
		return os.Stat(path)
	}
	return info, err
}

func revalidateRootEntry(parent *os.Root, name string, expected os.FileInfo, child *os.Root) error {
	current, pathErr := parent.Lstat(name)
	opened, rootErr := child.Stat(".")
	if pathErr != nil || rootErr != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !opened.IsDir() || !os.SameFile(expected, current) || !os.SameFile(expected, opened) {
		return errors.New("directory identity changed")
	}
	return nil
}

func validateSource(ctx context.Context, config Config, sourceCommit string) error {
	if len(sourceCommit) != 40 || strings.ToLower(sourceCommit) != sourceCommit {
		return errors.New("source commit must be 40 lowercase hex characters")
	}
	if _, err := hex.DecodeString(sourceCommit); err != nil {
		return errors.New("source commit must be hexadecimal")
	}
	if err := validateGitSourceRoot(ctx); err != nil {
		return err
	}
	head, err := gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != sourceCommit {
		return errors.New("source commit does not equal HEAD")
	}
	if err := gitQuiet(ctx, "merge-base", "--is-ancestor", config.Source.BaselineCommit, sourceCommit); err != nil {
		return errors.New("source commit does not descend from publication baseline")
	}
	if err := gitQuiet(ctx, "diff", "--quiet", "--cached", "HEAD", "--"); err != nil {
		return errors.New("publication source index is dirty")
	}
	if err := gitQuiet(ctx, "diff", "--quiet", "HEAD", "--"); err != nil {
		return errors.New("publication source worktree is dirty")
	}
	status, err := gitOutput(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return errors.New("publication source has dirty or untracked paths")
	}
	return nil
}

func validateGitSourceRoot(ctx context.Context) error {
	top, err := gitOutput(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("resolve publication source root")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwdInfo, cwdErr := os.Lstat(cwd)
	topInfo, topErr := os.Lstat(strings.TrimSpace(top))
	if cwdErr != nil || topErr != nil || !cwdInfo.IsDir() || !topInfo.IsDir() || cwdInfo.Mode()&os.ModeSymlink != 0 || topInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(cwdInfo, topInfo) {
		return errors.New("publication command must run at the source worktree root")
	}
	return nil
}

type sourceSnapshot struct {
	commit    string
	inventory Inventory
	entries   []trackedEntry
}

func captureApprovedSourceSnapshot(ctx context.Context, config Config, sourceCommit string) (sourceSnapshot, error) {
	if err := validateSource(ctx, config, sourceCommit); err != nil {
		return sourceSnapshot{}, err
	}
	inv, err := approvedInventory(ctx, config)
	if err != nil {
		return sourceSnapshot{}, err
	}
	entries, err := trackedEntries(ctx)
	if err != nil {
		return sourceSnapshot{}, err
	}
	digests, err := trackedBlobDigests(ctx, entries)
	if err != nil {
		return sourceSnapshot{}, err
	}
	if inv.SourceCommit != sourceCommit || len(inv.Files) != len(entries) || len(digests) != len(entries) {
		return sourceSnapshot{}, errors.New("publication source inventory changed while capturing")
	}
	for index, entry := range entries {
		if inv.Files[index].Path != entry.path || inv.Files[index].BlobSHA256 != digests[index] {
			return sourceSnapshot{}, errors.New("publication source inventory changed while capturing")
		}
	}
	snapshot := sourceSnapshot{commit: sourceCommit, inventory: inv, entries: append([]trackedEntry(nil), entries...)}
	if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
		return sourceSnapshot{}, err
	}
	return snapshot, nil
}

// materializationPayloadSnapshot preserves full source classification and race
// validation while excluding the prior release attestation from the payload it
// describes. The manifest is regenerated only after the payload passes all
// tree checks, so copying it would create a recursive, stale attestation.
func materializationPayloadSnapshot(snapshot sourceSnapshot) (sourceSnapshot, error) {
	if len(snapshot.inventory.Files) != len(snapshot.entries) {
		return sourceSnapshot{}, errors.New("publication source inventory and index differ")
	}
	payload := sourceSnapshot{
		commit:    snapshot.commit,
		inventory: snapshot.inventory,
		entries:   make([]trackedEntry, 0, len(snapshot.entries)),
	}
	payload.inventory.Files = make([]FileDecision, 0, len(snapshot.inventory.Files))
	manifestSeen := false
	for index, file := range snapshot.inventory.Files {
		entry := snapshot.entries[index]
		if file.Path != entry.path {
			return sourceSnapshot{}, errors.New("publication source inventory and index differ")
		}
		if file.Path == publicationManifest {
			if manifestSeen {
				return sourceSnapshot{}, errors.New("publication source contains duplicate manifests")
			}
			manifestSeen = true
			continue
		}
		payload.inventory.Files = append(payload.inventory.Files, file)
		payload.entries = append(payload.entries, entry)
	}
	return payload, nil
}

func validateSourceSnapshot(ctx context.Context, config Config, snapshot sourceSnapshot) error {
	if err := validateSource(ctx, config, snapshot.commit); err != nil {
		return err
	}
	current, err := trackedEntries(ctx)
	if err != nil {
		return err
	}
	if !sameTrackedEntries(snapshot.entries, current) {
		return errors.New("publication source index changed after snapshot")
	}
	return nil
}

func sameTrackedEntries(left, right []trackedEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func gitQuiet(ctx context.Context, args ...string) error {
	return execGit(ctx, args...)
}

func execGit(ctx context.Context, args ...string) error {
	// gitOutput intentionally does not expose command output in publication errors.
	_, err := gitBytes(ctx, args...)
	return err
}

func approvedInventory(ctx context.Context, config Config) (Inventory, error) {
	inv, err := buildInventory(ctx, config)
	if err != nil {
		return Inventory{}, err
	}
	if err := checkIncludedRuleEvidence(config, inv); err != nil {
		return Inventory{}, err
	}
	for _, f := range inv.Files {
		if err := checkDecision(f); err != nil {
			return Inventory{}, err
		}
	}
	return inv, nil
}

func copyInventory(ctx context.Context, dst *os.Root, files []FileDecision, entries []trackedEntry) error {
	sourcePathInfo, err := os.Lstat(".")
	if err != nil || !sourcePathInfo.IsDir() {
		return errors.New("publication source root is unsafe")
	}
	src, err := os.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("open publication source root: %w", err)
	}
	defer src.Close()
	sourceOpened, err := src.Stat(".")
	if err != nil || !os.SameFile(sourcePathInfo, sourceOpened) {
		return errors.New("publication source root changed while opening")
	}
	destinationInfo, err := dst.Stat(".")
	if err != nil || !destinationInfo.IsDir() {
		return errors.New("publication destination root is unsafe")
	}
	modes := make(map[string]string, len(entries))
	for _, entry := range entries {
		modes[entry.path] = entry.mode
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode, ok := modes[f.Path]
		if !ok {
			return errors.New("source inventory path disappeared from index")
		}
		if err := copyOne(src, dst, f, mode); err != nil {
			return err
		}
	}
	sourceCurrent, pathErr := os.Lstat(".")
	sourceRootCurrent, rootErr := src.Stat(".")
	if pathErr != nil || rootErr != nil || !os.SameFile(sourcePathInfo, sourceCurrent) || !os.SameFile(sourcePathInfo, sourceRootCurrent) {
		return errors.New("publication source root changed during copy")
	}
	destinationCurrent, err := dst.Stat(".")
	if err != nil || !os.SameFile(destinationInfo, destinationCurrent) {
		return errors.New("publication destination root changed during copy")
	}
	return nil
}

type copiedDirectoryIdentity struct {
	parent      *os.Root
	name        string
	info        os.FileInfo
	child       *os.Root
	requireMode os.FileMode
}

func revalidateCopiedDirectories(directories []copiedDirectoryIdentity) error {
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if err := revalidateRootEntry(directory.parent, directory.name, directory.info, directory.child); err != nil {
			return err
		}
		if directory.requireMode != 0 {
			current, err := directory.parent.Lstat(directory.name)
			if err != nil || current.Mode().Perm() != directory.requireMode.Perm() {
				return errors.New("directory mode changed")
			}
		}
	}
	return nil
}

func copyOne(src, dst *os.Root, f FileDecision, gitMode string) error {
	if err := validatePortablePath(f.Path); err != nil {
		return err
	}
	parts := strings.Split(f.Path, "/")
	from, to := src, dst
	var opened []*os.Root
	var sourceDirectories, destinationDirectories []copiedDirectoryIdentity
	destinationRoots := []*os.Root{dst}
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		info, err := from.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe source directory")
		}
		next, err := from.OpenRoot(part)
		if err != nil {
			return fmt.Errorf("open source directory: %w", err)
		}
		stat, err := next.Stat(".")
		if err != nil || !os.SameFile(info, stat) {
			_ = next.Close()
			return errors.New("source directory changed while opening")
		}
		opened = append(opened, next)
		sourceDirectories = append(sourceDirectories, copiedDirectoryIdentity{parent: from, name: part, info: info, child: next})
		from = next
		if err := to.Mkdir(part, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		di, err := to.Lstat(part)
		if err != nil || !di.IsDir() || di.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe destination directory")
		}
		dn, err := to.OpenRoot(part)
		if err != nil {
			return err
		}
		openedDirectory, err := dn.Stat(".")
		if err != nil || !os.SameFile(di, openedDirectory) {
			_ = dn.Close()
			return errors.New("destination directory changed while opening")
		}
		if err := chmodRoot(dn, 0o700); err != nil {
			_ = dn.Close()
			return err
		}
		canonical, err := to.Lstat(part)
		if err != nil || canonical.Mode().Perm() != 0o700 || !os.SameFile(di, canonical) {
			_ = dn.Close()
			return errors.New("destination directory has unsafe mode or identity")
		}
		opened = append(opened, dn)
		destinationDirectories = append(destinationDirectories, copiedDirectoryIdentity{parent: to, name: part, info: canonical, child: dn, requireMode: 0o700})
		destinationRoots = append(destinationRoots, dn)
		to = dn
	}
	name := parts[len(parts)-1]
	before, err := from.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("publication source file is not a regular file")
	}
	firePublicationRaceHook("before-source-open", f.Path)
	in, err := from.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	openedInfo, err := in.Stat()
	if err != nil || !os.SameFile(before, openedInfo) {
		return errors.New("source file changed while opening")
	}
	mode := os.FileMode(0o644)
	if gitMode == "100755" {
		mode = 0o755
	}
	out, err := to.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	outputInfo, err := out.Stat()
	if err != nil || !outputInfo.Mode().IsRegular() || outputInfo.Mode().Perm() != mode.Perm() {
		_ = out.Close()
		return errors.New("publication destination file has unsafe mode")
	}
	if err := out.Close(); err != nil {
		return err
	}
	firePublicationRaceHook("after-file-copy", f.Path)
	after, err := from.Lstat(name)
	if err != nil || !os.SameFile(before, after) {
		return errors.New("source file changed during copy")
	}
	destinationAfter, err := to.Lstat(name)
	if err != nil || !destinationAfter.Mode().IsRegular() || destinationAfter.Mode().Perm() != mode.Perm() || !os.SameFile(outputInfo, destinationAfter) {
		return errors.New("destination file changed during copy")
	}
	if err := revalidateCopiedDirectories(sourceDirectories); err != nil {
		return errors.New("source directory changed during copy")
	}
	if err := revalidateCopiedDirectories(destinationDirectories); err != nil {
		return errors.New("destination directory changed during copy")
	}
	if hex.EncodeToString(h.Sum(nil)) != f.BlobSHA256 {
		return errors.New("publication source file digest differs from inventory")
	}
	for index := len(destinationRoots) - 1; index >= 0; index-- {
		if err := syncRoot(destinationRoots[index]); err != nil {
			return err
		}
	}
	return nil
}

//nolint:unparam // callers in the composed command consume the summary.
func checkTree(ctx context.Context, config Config, rootPath string) (summary TreeSummary, returnErr error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return TreeSummary{}, err
	}
	pinned, err := openPinnedDirectory(rootPath, "publication check")
	if err != nil {
		return TreeSummary{}, err
	}
	rootPath = pinned.path
	defer func() {
		if err := pinned.revalidate("publication check"); returnErr == nil && err != nil {
			summary = TreeSummary{}
			returnErr = err
		}
		_ = pinned.Close()
	}()
	snapshot, sourceMode, err := currentApprovedSourceSnapshot(ctx, config)
	if err != nil {
		return TreeSummary{}, err
	}
	if sourceMode {
		payload, err := materializationPayloadSnapshot(snapshot)
		if err != nil {
			return TreeSummary{}, err
		}
		summary, err := checkInventoryTreeWithEntries(ctx, rootPath, config, payload.inventory, true, payload.entries)
		if err != nil {
			return TreeSummary{}, err
		}
		if err := checkExpressionClean(ctx, config, rootPath); err != nil {
			return TreeSummary{}, err
		}
		if err := verifyOptionalManifest(rootPath, summary); err != nil {
			return TreeSummary{}, err
		}
		finalSummary, err := checkInventoryTreeWithEntries(ctx, rootPath, config, payload.inventory, true, payload.entries)
		if err != nil || finalSummary != summary {
			return TreeSummary{}, errors.New("publication tree changed during checks")
		}
		if err := verifyOptionalManifest(rootPath, finalSummary); err != nil {
			return TreeSummary{}, err
		}
		if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
			return TreeSummary{}, err
		}
		return finalSummary, nil
	}
	manifest, err := readManifest(rootPath)
	if err != nil {
		return TreeSummary{}, err
	}
	if err := checkManifestShape(manifest); err != nil {
		return TreeSummary{}, err
	}
	records, sbom, err := scanTree(rootPath, config, true)
	if err != nil {
		return TreeSummary{}, err
	}
	digest := digestTree(records)
	if digest != manifest.TreeSHA256 || len(records) != manifest.FileCount || sbom != manifest.SBOMSHA256 || manifest.SourceTreeSHA256 != digest {
		return TreeSummary{}, errors.New("publication manifest does not match tree")
	}
	if err := checkExpressionClean(ctx, config, rootPath); err != nil {
		return TreeSummary{}, err
	}
	finalRecords, finalSBOM, err := scanTree(rootPath, config, true)
	if err != nil || digestTree(finalRecords) != digest || len(finalRecords) != len(records) || finalSBOM != sbom {
		return TreeSummary{}, errors.New("publication tree changed during checks")
	}
	finalManifest, err := readManifest(rootPath)
	if err != nil || checkManifestShape(finalManifest) != nil || !sameReleaseManifest(manifest, finalManifest) {
		return TreeSummary{}, errors.New("publication manifest changed during checks")
	}
	return TreeSummary{SourceTreeSHA256: manifest.SourceTreeSHA256, TreeSHA256: digest, FileCount: len(records), SBOMSHA256: sbom}, nil
}

func sameReleaseManifest(left, right ReleaseManifest) bool {
	return left.SchemaVersion == right.SchemaVersion && left.SourceTreeSHA256 == right.SourceTreeSHA256 && left.TreeSHA256 == right.TreeSHA256 && left.FileCount == right.FileCount && left.SBOMSHA256 == right.SBOMSHA256
}

func currentApprovedSourceSnapshot(ctx context.Context, config Config) (sourceSnapshot, bool, error) {
	inside, err := gitOutput(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if ctx.Err() != nil {
			return sourceSnapshot{}, false, ctx.Err()
		}
		return sourceSnapshot{}, false, nil
	}
	if strings.TrimSpace(inside) != "true" {
		return sourceSnapshot{}, false, nil
	}
	if err := validateGitSourceRoot(ctx); err != nil {
		return sourceSnapshot{}, true, err
	}
	head, err := gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return sourceSnapshot{}, true, errors.New("resolve publication source HEAD")
	}
	snapshot, err := captureApprovedSourceSnapshot(ctx, config, strings.TrimSpace(head))
	if err != nil {
		return sourceSnapshot{}, true, err
	}
	return snapshot, true, nil
}

func checkExpressionClean(ctx context.Context, config Config, rootPath string) error {
	report, err := scanExpression(ctx, config, rootPath)
	if err != nil || len(report.Findings) != 0 {
		return errors.New("publication expression check failed")
	}
	return nil
}

func verifyOptionalManifest(rootPath string, summary TreeSummary) error {
	info, err := os.Lstat(filepath.Join(rootPath, publicationManifest))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe publication manifest")
	}
	manifest, err := readManifest(rootPath)
	if err != nil {
		return err
	}
	if err := checkManifestShape(manifest); err != nil {
		return err
	}
	if manifest.SourceTreeSHA256 != summary.SourceTreeSHA256 || manifest.TreeSHA256 != summary.TreeSHA256 || manifest.FileCount != summary.FileCount || manifest.SBOMSHA256 != summary.SBOMSHA256 {
		return errors.New("publication manifest does not match source inventory")
	}
	return nil
}

func checkInventoryTreeWithEntries(ctx context.Context, root string, config Config, inv Inventory, allowManifest bool, entries []trackedEntry) (TreeSummary, error) {
	if err := ctx.Err(); err != nil {
		return TreeSummary{}, err
	}
	records, sbom, err := scanTree(root, config, allowManifest)
	if err != nil {
		return TreeSummary{}, err
	}
	want := make(map[string]FileDecision, len(inv.Files))
	for _, f := range inv.Files {
		want[f.Path] = f
	}
	if len(records) != len(want) {
		return TreeSummary{}, errors.New("publication tree file count differs from inventory")
	}
	modes := make(map[string]string, len(entries))
	for _, entry := range entries {
		modes[entry.path] = entry.mode
	}
	for _, r := range records {
		f, ok := want[r.path]
		if !ok || f.BlobSHA256 != r.digest || modes[r.path] != r.mode {
			return TreeSummary{}, errors.New("publication tree differs from inventory")
		}
	}
	d := digestTree(records)
	source, err := sourceTreeDigest(inv.Files, entries)
	if err != nil {
		return TreeSummary{}, err
	}
	return TreeSummary{SourceTreeSHA256: source, TreeSHA256: d, FileCount: len(records), SBOMSHA256: sbom}, nil
}

func writeReleaseManifest(ctx context.Context, config Config, rootPath, outputPath string) (manifest ReleaseManifest, returnErr error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return ReleaseManifest{}, err
	}
	pinned, err := openPinnedDirectory(rootPath, "publication manifest operation")
	if err != nil {
		return ReleaseManifest{}, err
	}
	rootPath = pinned.path
	defer func() {
		if err := pinned.revalidate("publication manifest operation"); returnErr == nil && err != nil {
			manifest = ReleaseManifest{}
			returnErr = err
		}
		_ = pinned.Close()
	}()
	want := filepath.Join(rootPath, publicationManifest)
	if !filepath.IsAbs(outputPath) || filepath.Clean(outputPath) != outputPath || outputPath != want {
		return ReleaseManifest{}, errors.New("manifest output must be the publication root manifest")
	}
	snapshot, sourceMode, err := currentApprovedSourceSnapshot(ctx, config)
	if err != nil {
		return ReleaseManifest{}, err
	}
	if !sourceMode {
		return ReleaseManifest{}, errors.New("publication manifest requires clean source inventory")
	}
	payload, err := materializationPayloadSnapshot(snapshot)
	if err != nil {
		return ReleaseManifest{}, err
	}
	summary, err := checkInventoryTreeWithEntries(ctx, rootPath, config, payload.inventory, true, payload.entries)
	if err != nil {
		return ReleaseManifest{}, err
	}
	if err := checkExpressionClean(ctx, config, rootPath); err != nil {
		return ReleaseManifest{}, err
	}
	finalSummary, err := checkInventoryTreeWithEntries(ctx, rootPath, config, payload.inventory, true, payload.entries)
	if err != nil || finalSummary != summary {
		return ReleaseManifest{}, errors.New("publication tree changed during manifest checks")
	}
	if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
		return ReleaseManifest{}, err
	}
	m := ReleaseManifest{SchemaVersion: 1, SourceTreeSHA256: summary.SourceTreeSHA256, TreeSHA256: summary.TreeSHA256, FileCount: summary.FileCount, Checks: map[string]string{"policy": "pass", "tree": "pass", "expression": "pass", "sbom": "pass"}, SBOMSHA256: summary.SBOMSHA256}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return ReleaseManifest{}, err
	}
	b = append(b, '\n')
	if err := writeManifestAtomic(rootPath, b); err != nil {
		return ReleaseManifest{}, err
	}
	if err := validateSourceSnapshot(ctx, config, snapshot); err != nil {
		return ReleaseManifest{}, err
	}
	return m, nil
}

func scanTree(rootPath string, config Config, allowManifest bool) ([]treeFile, string, error) {
	pinned, err := openPinnedDirectory(rootPath, "publication tree")
	if err != nil {
		return nil, "", err
	}
	defer pinned.Close()
	root := pinned.root
	if pinned.info.Mode().Perm() != 0o700 {
		return nil, "", errors.New("publication tree root has noncanonical mode")
	}
	var files []treeFile
	var decisions []FileDecision
	var mappingBytes []byte
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		listing, err := dir.Open(".")
		if err != nil {
			return err
		}
		entries, err := listing.ReadDir(-1)
		closeErr := listing.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			full := name
			if prefix != "" {
				full = prefix + "/" + name
			}
			if name == ".git" || name == ".reference" || name == ".eino-agent" || name == ".yhc" || name == ".claude" {
				return errors.New("forbidden publication root")
			}
			if err := validatePortablePath(full); err != nil {
				return err
			}
			info, err := dir.Lstat(name)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("publication tree contains symlink")
			}
			if info.IsDir() {
				if info.Mode().Perm() != 0o700 {
					return errors.New("publication directory has noncanonical mode")
				}
				child, err := dir.OpenRoot(name)
				if err != nil {
					return err
				}
				stat, err := child.Stat(".")
				if err != nil || !os.SameFile(info, stat) {
					_ = child.Close()
					return errors.New("publication directory changed")
				}
				err = walk(child, full)
				firePublicationRaceHook("after-tree-directory-walk", full)
				current, checkErr := dir.Lstat(name)
				_ = child.Close()
				if err != nil {
					return err
				}
				if checkErr != nil || !os.SameFile(info, current) {
					return errors.New("publication directory changed during walk")
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return errors.New("publication tree contains special file")
			}
			if full == config.Mappings.Manifest && info.Size() > maximumMappingBytes {
				return errors.New("publication mapping manifest is too large")
			}
			if full == publicationManifest && allowManifest {
				if info.Mode().Perm() != 0o644 {
					return errors.New("publication manifest has noncanonical mode")
				}
				continue
			}
			rule, err := matchRule(config.Rules, full)
			if err != nil {
				return err
			}
			decisions = append(decisions, FileDecision{Path: full, RuleID: rule.ID, Class: rule.Class, Decision: rule.Decision, License: rule.License})
			in, err := dir.Open(name)
			if err != nil {
				return err
			}
			opened, err := in.Stat()
			if err != nil || !os.SameFile(info, opened) {
				_ = in.Close()
				return errors.New("publication file changed")
			}
			h := sha256.New()
			var copied bytes.Buffer
			writer := io.Writer(h)
			if full == config.Mappings.Manifest {
				writer = io.MultiWriter(h, &copied)
			}
			_, err = io.Copy(writer, in)
			_ = in.Close()
			if err != nil {
				return err
			}
			firePublicationRaceHook("after-tree-file-read", full)
			current, err := dir.Lstat(name)
			if err != nil || !os.SameFile(info, current) {
				return errors.New("publication file changed during read")
			}
			mode := "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			if info.Mode().Perm() != 0o644 && info.Mode().Perm() != 0o755 {
				return errors.New("publication tree has noncanonical mode")
			}
			if full == config.Mappings.Manifest {
				mappingBytes = append([]byte(nil), copied.Bytes()...)
			}
			files = append(files, treeFile{full, mode, hex.EncodeToString(h.Sum(nil))})
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, "", err
	}
	firePublicationRaceHook("after-tree-root-walk", pinned.path)
	if err := pinned.revalidate("publication tree"); err != nil {
		return nil, "", err
	}
	if err := collisionFree(files); err != nil {
		return nil, "", err
	}
	candidates := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		candidates = append(candidates, decision.Path)
	}
	if mappingBytes == nil {
		return nil, "", errors.New("publication mapping manifest is missing from tree")
	}
	mappings, err := parsePublicationMappings(mappingBytes, candidates)
	if err != nil {
		return nil, "", err
	}
	if err := revalidateTreeMappingBytes(root, config.Mappings.Manifest, mappingBytes); err != nil {
		return nil, "", err
	}
	for index := range decisions {
		decisions[index].Mapped = mappings.mapped(decisions[index].Path)
		if err := checkDecision(decisions[index]); err != nil {
			return nil, "", err
		}
	}
	if err := checkIncludedRuleEvidence(config, Inventory{Files: decisions}); err != nil {
		return nil, "", err
	}
	sbom := ""
	for _, f := range files {
		if f.path == config.Dependencies.SBOM {
			sbom = f.digest
		}
	}
	if sbom == "" {
		return nil, "", errors.New("publication SBOM is missing or unapproved")
	}
	return files, sbom, nil
}

func revalidateTreeMappingBytes(root *os.Root, name string, expected []byte) error {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("publication mapping manifest changed")
	}
	f, err := root.Open(name)
	if err != nil {
		return errors.New("publication mapping manifest changed")
	}
	opened, statErr := f.Stat()
	contents, readErr := io.ReadAll(f)
	closeErr := f.Close()
	after, afterErr := root.Lstat(name)
	if statErr != nil || readErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) || !bytes.Equal(expected, contents) {
		return errors.New("publication mapping manifest changed")
	}
	return nil
}

func sourceTreeDigest(files []FileDecision, entries []trackedEntry) (string, error) {
	modes := make(map[string]string, len(entries))
	for _, entry := range entries {
		modes[entry.path] = entry.mode
	}
	r := make([]treeFile, 0, len(files))
	for _, f := range files {
		mode, ok := modes[f.Path]
		if !ok {
			return "", errors.New("source inventory path disappeared from index")
		}
		r = append(r, treeFile{f.Path, mode, f.BlobSHA256})
	}
	return digestTree(r), nil
}

func digestTree(files []treeFile) string {
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	h := sha256.New()
	for _, f := range files {
		_, _ = h.Write([]byte(f.path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.mode))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.digest))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func collisionFree(files []treeFile) error {
	seen := map[string]string{}
	fold := cases.Fold()
	for _, f := range files {
		k := fold.String(norm.NFC.String(f.path))
		if old, ok := seen[k]; ok && old != f.path {
			return errors.New("publication paths collide")
		}
		seen[k] = f.path
	}
	return nil
}

func validatePortablePath(p string) error {
	if err := validateRepositoryPath(p); err != nil {
		return err
	}
	for _, s := range strings.Split(p, "/") {
		if !safeSegment(s) {
			return errors.New("unsafe publication path")
		}
	}
	return nil
}

func safeSegment(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return false
	}
	for _, r := range s {
		if strings.ContainsRune(`<>:"\\|?*`, r) {
			return false
		}
	}
	u := strings.ToUpper(strings.Split(s, ".")[0])
	if u == "CON" || u == "PRN" || u == "AUX" || u == "NUL" || (strings.HasPrefix(u, "COM") && len(u) == 4 && u[3] >= '1' && u[3] <= '9') || (strings.HasPrefix(u, "LPT") && len(u) == 4 && u[3] >= '1' && u[3] <= '9') {
		return false
	}
	return true
}

func createStage(parent *os.Root) (string, *os.Root, error) {
	for i := 0; i < 128; i++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, err
		}
		n := ".publication-stage-" + hex.EncodeToString(token[:])
		if err := parent.Mkdir(n, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", nil, err
		}
		r, err := parent.OpenRoot(n)
		if err != nil {
			_ = parent.RemoveAll(n)
			return "", nil, err
		}
		if err := chmodRoot(r, 0o700); err != nil {
			_ = r.Close()
			_ = parent.RemoveAll(n)
			return "", nil, err
		}
		info, pathErr := parent.Lstat(n)
		opened, rootErr := r.Stat(".")
		if pathErr != nil || rootErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !os.SameFile(info, opened) {
			_ = r.Close()
			_ = parent.RemoveAll(n)
			return "", nil, errors.New("publication staging directory is unsafe")
		}
		if err := syncRoot(parent); err != nil {
			_ = r.Close()
			_ = parent.RemoveAll(n)
			return "", nil, err
		}
		return n, r, nil
	}
	return "", nil, errors.New("exhausted publication stage names")
}

func chmodRoot(root *os.Root, mode os.FileMode) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func readManifest(rootPath string) (ReleaseManifest, error) {
	pinned, err := openPinnedDirectory(rootPath, "publication manifest")
	if err != nil {
		return ReleaseManifest{}, err
	}
	defer pinned.Close()
	root := pinned.root
	base := publicationManifest
	before, err := root.Lstat(base)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0o644 {
		return ReleaseManifest{}, errors.New("unsafe publication manifest")
	}
	f, err := root.Open(base)
	if err != nil {
		return ReleaseManifest{}, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = f.Close()
		return ReleaseManifest{}, errors.New("publication manifest changed while opening")
	}
	b, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return ReleaseManifest{}, err
	}
	firePublicationRaceHook("after-manifest-read", pinned.path)
	after, err := root.Lstat(base)
	if err != nil || !os.SameFile(before, after) {
		return ReleaseManifest{}, errors.New("publication manifest changed while reading")
	}
	if err := pinned.revalidate("publication manifest"); err != nil {
		return ReleaseManifest{}, err
	}
	var m ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ReleaseManifest{}, errors.New("invalid publication manifest")
	}
	canonical, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return ReleaseManifest{}, err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(b, canonical) {
		return ReleaseManifest{}, errors.New("noncanonical publication manifest")
	}
	return m, nil
}

func checkManifestShape(m ReleaseManifest) error {
	if m.SchemaVersion != 1 || !validSHA256Digest(m.SourceTreeSHA256) || !validSHA256Digest(m.TreeSHA256) || !validSHA256Digest(m.SBOMSHA256) || m.SourceTreeSHA256 != m.TreeSHA256 || m.FileCount < 0 {
		return errors.New("invalid publication manifest")
	}
	want := map[string]string{"policy": "pass", "tree": "pass", "expression": "pass", "sbom": "pass"}
	if len(m.Checks) != len(want) {
		return errors.New("invalid publication manifest checks")
	}
	for k, v := range want {
		if m.Checks[k] != v {
			return errors.New("invalid publication manifest checks")
		}
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeManifestAtomic(rootPath string, b []byte) error {
	pinned, err := openPinnedDirectory(rootPath, "publication manifest output")
	if err != nil {
		return err
	}
	defer pinned.Close()
	r := pinned.root
	old, err := r.Lstat(publicationManifest)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && (!old.Mode().IsRegular() || old.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe publication manifest target")
	}
	oldExists := err == nil
	if oldExists {
		manifest, readErr := readManifest(rootPath)
		if readErr != nil {
			return readErr
		}
		if err := checkManifestShape(manifest); err != nil {
			return err
		}
		current, currentErr := r.Lstat(publicationManifest)
		if currentErr != nil || !os.SameFile(old, current) {
			return errors.New("publication manifest changed before replacement")
		}
	}
	n, f, err := createManifestTemp(r)
	if err != nil {
		return err
	}
	temporaryInfo, statErr := f.Stat()
	if statErr != nil || !temporaryInfo.Mode().IsRegular() || temporaryInfo.Mode().Perm() != 0o644 {
		_ = f.Close()
		_ = r.Remove(n)
		return errors.New("publication manifest staging file is unsafe")
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = r.Remove(n)
		}
	}()
	if _, err = f.Write(b); err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	staged, err := r.Lstat(n)
	if err != nil || !staged.Mode().IsRegular() || staged.Mode().Perm() != 0o644 || !os.SameFile(temporaryInfo, staged) {
		return errors.New("publication manifest staging file changed")
	}
	firePublicationRaceHook("before-manifest-promotion", pinned.path)
	if err := pinned.revalidate("publication manifest output"); err != nil {
		return err
	}
	current, currentErr := r.Lstat(publicationManifest)
	if oldExists {
		if currentErr != nil || !os.SameFile(old, current) {
			return errors.New("publication manifest changed before promotion")
		}
	} else if !os.IsNotExist(currentErr) {
		return errors.New("publication manifest appeared before promotion")
	}
	if err = r.Rename(n, publicationManifest); err != nil {
		return err
	}
	promoted = true
	final, err := r.Lstat(publicationManifest)
	if err != nil || !final.Mode().IsRegular() || final.Mode().Perm() != 0o644 || !os.SameFile(temporaryInfo, final) {
		return errors.New("publication manifest changed after promotion")
	}
	if err := syncRoot(r); err != nil {
		return err
	}
	return pinned.revalidate("publication manifest output")
}

func createManifestTemp(root *os.Root) (string, *os.File, error) {
	for attempt := 0; attempt < 128; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, err
		}
		name := ".publication-manifest-" + hex.EncodeToString(token[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if err := file.Chmod(0o644); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return "", nil, err
		}
		return name, file, nil
	}
	return "", nil, errors.New("exhausted publication manifest staging names")
}
