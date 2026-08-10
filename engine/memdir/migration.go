package memdir

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	autoMemoryMigrationMaxEntries  = 4096
	autoMemoryMigrationMaxBytes    = 64 << 20
	agentMemoryMigrationMaxEntries = 8192
	agentMemoryMigrationMaxBytes   = 128 << 20
	memoryMigrationMaxFileBytes    = 8 << 20
)

// ErrMemoryMigrationUnavailable reports a scope or path selection which does
// not have a canonical-default/legacy-default pair to import.
var ErrMemoryMigrationUnavailable = errors.New("memory migration is unavailable")

// MemoryMigrationSpec returns one exact memory owner. Each invocation names a
// single legacy subtree and a single canonical destination; no specification
// scans either state root for additional artifacts.
func MemoryMigrationSpec(
	owner string,
	scope string,
	projectRoot string,
) (statemigration.ArtifactSpec, error) {
	switch owner {
	case "memory":
		if scope != "user" {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		selection, err := resolveAutoMemorySelection(projectRoot)
		if err != nil || !selection.Migratable {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		legacyProject, canonicalProject, err := memoryMigrationProjectRoots(projectRoot)
		if err != nil {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		return newMemoryTreeMigrationSpec(
			owner,
			scope,
			path.Join("projects", sanitizePath(legacyProject), autoMemDirname),
			path.Join("projects", sanitizePath(canonicalProject), autoMemDirname),
			autoMemoryMigrationMaxEntries,
			autoMemoryMigrationMaxBytes,
			false,
		), nil
	case "agent-memory":
		if scope != "user" && scope != "project" {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		if scope == "user" {
			selection, err := resolveMemoryBaseSelection()
			if err != nil || !selection.Migratable {
				return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
			}
		}
		return newMemoryTreeMigrationSpec(
			owner,
			scope,
			"agent-memory",
			"agent-memory",
			agentMemoryMigrationMaxEntries,
			agentMemoryMigrationMaxBytes,
			true,
		), nil
	case "agent-memory-local":
		if scope != "project" {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		remote, err := resolveRemoteMemorySelection()
		if err != nil || !remote.Migratable {
			return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
		}
		return newMemoryTreeMigrationSpec(
			owner,
			scope,
			"agent-memory-local",
			"agent-memory-local",
			agentMemoryMigrationMaxEntries,
			agentMemoryMigrationMaxBytes,
			true,
		), nil
	default:
		return statemigration.ArtifactSpec{}, ErrMemoryMigrationUnavailable
	}
}

func memoryMigrationProjectRoots(projectRoot string) (string, string, error) {
	if strings.TrimSpace(projectRoot) == "" || strings.ContainsRune(projectRoot, '\x00') {
		return "", "", ErrMemoryMigrationUnavailable
	}
	legacy, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", "", ErrMemoryMigrationUnavailable
	}
	roots, err := statepath.ProjectRoots(projectRoot)
	if err != nil {
		return "", "", ErrMemoryMigrationUnavailable
	}
	return filepath.Clean(legacy), filepath.Dir(roots.Canonical), nil
}

func newMemoryTreeMigrationSpec(
	owner string,
	scope string,
	sourceRel string,
	targetRel string,
	maxEntries int,
	maxBytes int64,
	agentTree bool,
) statemigration.ArtifactSpec {
	return statemigration.ArtifactSpec{
		Owner:      owner,
		Scope:      scope,
		SourceRel:  sourceRel,
		TargetRel:  targetRel,
		Kind:       statemigration.DirectoryTree,
		LegacyMode: statemigration.LegacyOwnerControlled,
		MaxFiles:   maxEntries,
		MaxBytes:   maxBytes,
		Validate: func(ctx context.Context, snapshot statemigration.Snapshot) error {
			return validateMemoryMigrationSnapshot(ctx, snapshot, agentTree)
		},
		Stage: stageMemoryMigrationSnapshot,
	}
}

func validateMemoryMigrationSnapshot(
	ctx context.Context,
	snapshot statemigration.Snapshot,
	agentTree bool,
) error {
	return snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return errors.New("memory migration interrupted")
		}
		if relative == "." {
			if !entry.IsDir() {
				return errors.New("memory migration root is invalid")
			}
			return nil
		}
		if entry.IsDir() {
			return validateMemoryMigrationDirectory(relative, agentTree)
		}
		if entry.Type()&fs.ModeType != 0 {
			return errors.New("memory migration entry is invalid")
		}
		if agentTree && !strings.Contains(relative, "/") {
			return errors.New("agent memory root file is invalid")
		}
		base := path.Base(relative)
		switch {
		case strings.EqualFold(path.Ext(base), ".md"),
			strings.EqualFold(path.Ext(base), ".log"):
			return validateMemoryMigrationText(snapshot, relative)
		case agentTree && strings.Count(relative, "/") == 1 && base == agentSnapshotSynced:
			return validateMemoryMigrationMetadata(snapshot, relative)
		default:
			return errors.New("memory migration entry is unknown")
		}
	})
}

func validateMemoryMigrationDirectory(relative string, agentTree bool) error {
	for index, segment := range strings.Split(relative, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return errors.New("memory migration directory is invalid")
		}
		if agentTree && index == 0 && sanitizeAgentType(segment) != segment {
			return errors.New("agent memory directory is invalid")
		}
	}
	return nil
}

func validateMemoryMigrationText(
	snapshot statemigration.Snapshot,
	relative string,
) error {
	data, err := readMemoryMigrationFile(snapshot, relative)
	if err != nil || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 ||
		len(ScanForSecrets(string(data))) != 0 {
		return errors.New("memory migration text is invalid")
	}
	return nil
}

func validateMemoryMigrationMetadata(
	snapshot statemigration.Snapshot,
	relative string,
) error {
	data, err := readMemoryMigrationFile(snapshot, relative)
	if err != nil {
		return errors.New("memory migration metadata is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') || !decoder.More() {
		return errors.New("memory migration metadata is invalid")
	}
	key, err := decoder.Token()
	if err != nil || key != "syncedFrom" {
		return errors.New("memory migration metadata is invalid")
	}
	var timestamp string
	if err := decoder.Decode(&timestamp); err != nil || strings.TrimSpace(timestamp) != timestamp {
		return errors.New("memory migration metadata is invalid")
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil || decoder.More() {
		return errors.New("memory migration metadata is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("memory migration metadata is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("memory migration metadata is invalid")
	}
	return nil
}

func readMemoryMigrationFile(
	snapshot statemigration.Snapshot,
	relative string,
) ([]byte, error) {
	reader, info, err := snapshot.Open(relative)
	if err != nil {
		return nil, errors.New("memory migration file is invalid")
	}
	defer reader.Close() //nolint:errcheck
	if info.Size() < 0 || info.Size() > memoryMigrationMaxFileBytes {
		return nil, errors.New("memory migration file is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(reader, memoryMigrationMaxFileBytes+1))
	if err != nil || int64(len(data)) != info.Size() || len(data) > memoryMigrationMaxFileBytes {
		return nil, errors.New("memory migration file is invalid")
	}
	return data, nil
}

func stageMemoryMigrationSnapshot(
	ctx context.Context,
	snapshot statemigration.Snapshot,
	stage *os.Root,
) error {
	return snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return errors.New("memory migration interrupted")
		}
		if relative == "." {
			return nil
		}
		name := filepath.FromSlash(relative)
		if entry.IsDir() {
			if err := stage.Mkdir(name, 0o700); err != nil {
				return errors.New("memory migration staging failed")
			}
			return nil
		}
		input, _, err := snapshot.Open(relative)
		if err != nil {
			return errors.New("memory migration staging failed")
		}
		output, err := stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			return errors.New("memory migration staging failed")
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil || inputCloseErr != nil || outputCloseErr != nil {
			return errors.New("memory migration staging failed")
		}
		return nil
	})
}
