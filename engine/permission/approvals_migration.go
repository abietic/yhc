package permission

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/abietic/yhc/internal/statemigration"
)

const approvalMigrationMaxBytes = 1 << 20

// ApprovalMigrationSpec names the project-local approval file and validates
// its persistent, parameter-scoped entries before import.
func ApprovalMigrationSpec(projectRoot string) (statemigration.ArtifactSpec, error) {
	projectRoot, err := approvalProjectRoot(projectRoot)
	if err != nil {
		return statemigration.ArtifactSpec{}, errors.New("approval migration is unavailable")
	}
	return statemigration.ArtifactSpec{
		Owner:      "approvals",
		Scope:      "project",
		SourceRel:  "approvals.json",
		TargetRel:  "approvals.json",
		Kind:       statemigration.RegularFile,
		LegacyMode: statemigration.LegacyOwnerControlled,
		MaxFiles:   1,
		MaxBytes:   approvalMigrationMaxBytes,
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			if _, err := readApprovalSnapshot(snapshot, projectRoot); err != nil {
				return errors.New("approval migration data is invalid")
			}
			return nil
		},
		Stage: func(_ context.Context, snapshot statemigration.Snapshot, stage *os.Root) error {
			entries, err := readApprovalSnapshot(snapshot, projectRoot)
			if err != nil {
				return errors.New("approval migration staging failed")
			}
			data, err := marshalPersistedApprovals(entries)
			if err != nil {
				return errors.New("approval migration staging failed")
			}
			file, err := stage.OpenFile("approvals.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return errors.New("approval migration staging failed")
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				return errors.New("approval migration staging failed")
			}
			return nil
		},
	}, nil
}

func approvalProjectRoot(projectRoot string) (string, error) {
	if projectRoot == "" || !utf8.ValidString(projectRoot) || bytes.IndexByte([]byte(projectRoot), 0) >= 0 {
		return "", errors.New("approval project root is invalid")
	}
	absolute, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", errors.New("approval project root is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("approval project root is invalid")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("approval project root is invalid")
	}
	return filepath.Clean(resolved), nil
}

func readApprovalSnapshot(snapshot statemigration.Snapshot, projectRoot string) ([]persistedApproval, error) {
	reader, info, err := snapshot.Open(".")
	if err != nil || info.Size() < 0 || info.Size() > approvalMigrationMaxBytes {
		return nil, errors.New("approval migration data is invalid")
	}
	defer reader.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(reader, approvalMigrationMaxBytes+1))
	if err != nil || int64(len(data)) != info.Size() || len(data) > approvalMigrationMaxBytes {
		return nil, errors.New("approval migration data is invalid")
	}
	return parsePersistedApprovals(data, projectRoot)
}
