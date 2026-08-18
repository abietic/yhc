package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func canonicalPayload(m manifest) ([]byte, error) {
	if err := validateManifest(m, phaseCapture); err != nil {
		return nil, err
	}
	m.Checksum = ""
	sortManifest(&m)
	return json.Marshal(m)
}

func sealManifest(m manifest) (manifest, error) {
	payload, err := canonicalPayload(m)
	if err != nil {
		return manifest{}, err
	}
	sum := sha256.Sum256(payload)
	m.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	sortManifest(&m)
	return m, nil
}

func readManifest(path string) (manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return manifest{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var m manifest
	if err := decoder.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return manifest{}, errors.New("manifest contains trailing JSON value")
		}
		return manifest{}, fmt.Errorf("decode manifest trailing value: %w", err)
	}
	if !validDigest(m.Checksum) {
		return manifest{}, errors.New("manifest checksum is invalid")
	}
	payload, err := canonicalPayload(m)
	if err != nil {
		return manifest{}, err
	}
	sum := sha256.Sum256(payload)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if m.Checksum != want {
		return manifest{}, errors.New("manifest checksum does not match canonical payload")
	}
	return m, nil
}

func writeManifestAtomic(output string, m manifest) error {
	if !filepath.IsAbs(output) {
		return errors.New("manifest output path must be absolute")
	}
	sealed, err := sealManifest(m)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return fmt.Errorf("resolve manifest output parent: %w", err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() {
		return errors.New("manifest output parent is not a directory")
	}
	if outputInfo, err := os.Lstat(output); err == nil && outputInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("manifest output may not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	resolvedOutput := filepath.Join(parent, filepath.Base(output))
	for _, root := range manifestRoots(sealed) {
		resolvedRoot, err := resolveProspectivePath(root)
		if err != nil {
			return fmt.Errorf("resolve protected root %q: %w", root, err)
		}
		if pathWithin(resolvedOutput, resolvedRoot) {
			return fmt.Errorf("manifest output is within protected root %q", root)
		}
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".cutover-recovery-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if _, err := readManifest(tempName); err != nil {
		return fmt.Errorf("strictly re-read temporary manifest: %w", err)
	}
	parentAfter, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !os.SameFile(parentInfo, parentAfter) {
		return errors.New("manifest output parent changed during write")
	}
	if err := os.Rename(tempName, resolvedOutput); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync manifest output parent: %w", err)
	}
	return nil
}

func sortManifest(m *manifest) {
	sort.Slice(m.ArchiveMapping, func(i, j int) bool { return m.ArchiveMapping[i].RecordID < m.ArchiveMapping[j].RecordID })
	sort.Slice(m.Refs, func(i, j int) bool { return m.Refs[i].RecordID < m.Refs[j].RecordID })
	sort.Slice(m.Worktrees, func(i, j int) bool { return m.Worktrees[i].RecordID < m.Worktrees[j].RecordID })
	sort.Slice(m.DirtyPaths, func(i, j int) bool { return m.DirtyPaths[i].RecordID < m.DirtyPaths[j].RecordID })
	sort.Slice(m.Stashes, func(i, j int) bool { return m.Stashes[i].RecordID < m.Stashes[j].RecordID })
	sort.Slice(m.Processes, func(i, j int) bool { return m.Processes[i].RecordID < m.Processes[j].RecordID })
	sort.Slice(m.Classifications, func(i, j int) bool { return m.Classifications[i].RecordID < m.Classifications[j].RecordID })
}

func manifestRoots(m manifest) []string {
	roots := []string{m.Public.Root, m.Private.Root}
	for _, mapping := range m.ArchiveMapping {
		roots = append(roots, mapping.Source, mapping.Destination)
	}
	return roots
}

func resolveProspectivePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	clean := filepath.Clean(path)
	for probe := clean; ; probe = filepath.Dir(probe) {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			rel, err := filepath.Rel(probe, clean)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, rel), nil
		}
		if !errors.Is(err, os.ErrNotExist) || probe == filepath.Dir(probe) {
			return "", err
		}
	}
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
