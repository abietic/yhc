package keybindings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	keybindingsMigrationFile     = "keybindings.json"
	keybindingsMigrationMaxBytes = 1 << 20
)

// ResolveUserConfigDir resolves the canonical user config directory while
// preserving exact canonical or legacy environment overrides.
func ResolveUserConfigDir(home string) (string, error) {
	roots, err := statepath.UserRoots(home)
	if err != nil {
		return "", errors.New("user config root is invalid")
	}
	selection, err := statepath.ResolveOverride(
		identity.RuntimeEnvConfigDir.Pair(),
		roots,
	)
	if err != nil {
		return "", errors.New("user config root is invalid")
	}
	return selection.Effective, nil
}

// UserMigrationSpec returns the exact user keybindings artifact owner.
func UserMigrationSpec() statemigration.ArtifactSpec {
	return statemigration.ArtifactSpec{
		Owner:     "keybindings",
		Scope:     "user",
		SourceRel: keybindingsMigrationFile,
		TargetRel: keybindingsMigrationFile,
		Kind:      statemigration.RegularFile,
		MaxFiles:  1,
		MaxBytes:  keybindingsMigrationMaxBytes,
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			return validateKeybindingsMigrationSnapshot(snapshot)
		},
		Stage: stageKeybindingsMigration,
	}
}

func validateKeybindingsMigrationSnapshot(snapshot statemigration.Snapshot) error {
	data, err := readKeybindingsMigrationSnapshot(snapshot)
	if err != nil {
		return err
	}
	_, err = validatedMigrationBindings(data)
	return err
}

func stageKeybindingsMigration(
	_ context.Context,
	snapshot statemigration.Snapshot,
	stage *os.Root,
) error {
	data, err := readKeybindingsMigrationSnapshot(snapshot)
	if err != nil {
		return err
	}
	bindings, err := validatedMigrationBindings(data)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(struct {
		Bindings []userBindingBlock `json:"bindings"`
	}{Bindings: bindings}, "", "  ")
	if err != nil {
		return errors.New("keybindings migration schema is invalid")
	}
	encoded = append(encoded, '\n')
	output, err := stage.OpenFile(
		keybindingsMigrationFile,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("keybindings migration staging failed")
	}
	_, writeErr := output.Write(encoded)
	closeErr := output.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("keybindings migration staging failed")
	}
	return nil
}

func validatedMigrationBindings(data []byte) ([]userBindingBlock, error) {
	bindings, issues, err := parseAndValidateUserBindings(data)
	if err != nil || HasValidationErrors(issues) {
		return nil, errors.New("keybindings migration schema is invalid")
	}
	return bindings, nil
}

func readKeybindingsMigrationSnapshot(
	snapshot statemigration.Snapshot,
) ([]byte, error) {
	reader, _, err := snapshot.Open(".")
	if err != nil {
		return nil, errors.New("keybindings migration source is invalid")
	}
	defer reader.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(reader, keybindingsMigrationMaxBytes+1))
	if err != nil || len(data) > keybindingsMigrationMaxBytes {
		return nil, errors.New("keybindings migration source is invalid")
	}
	return data, nil
}
