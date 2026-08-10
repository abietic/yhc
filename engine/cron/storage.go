package cron

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const maxCronFileBytes int64 = 4 << 20

func openCanonicalCronStore(
	projectDir string,
	create bool,
) (*statemigration.CanonicalStore, bool, error) {
	roots, err := statepath.ProjectRoots(projectDir)
	if err != nil {
		return nil, false, errors.New("cron state root is unsafe")
	}
	store, exists, err := statemigration.OpenCanonicalStore(roots.Canonical, ".", create)
	if err != nil {
		return nil, false, errors.New("cron state root is unsafe")
	}
	return store, exists, nil
}

func readCanonicalRegular(
	projectDir string,
	name string,
	maxBytes int64,
) ([]byte, bool, error) {
	store, exists, err := openCanonicalCronStore(projectDir, false)
	if err != nil || !exists {
		return nil, false, err
	}
	defer store.Close() //nolint:errcheck
	data, _, fileExists, err := readStoreRegular(store, name, maxBytes)
	return data, fileExists, err
}

func readStoreRegular(
	store *statemigration.CanonicalStore,
	name string,
	maxBytes int64,
) ([]byte, os.FileInfo, bool, error) {
	file, opened, exists, err := store.OpenRegular(name, os.O_RDONLY, false)
	if err != nil || !exists {
		return nil, nil, false, err
	}
	defer file.Close() //nolint:errcheck
	if opened.Size() < 0 || opened.Size() > maxBytes {
		return nil, nil, false, errors.New("cron state file is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) != opened.Size() {
		return nil, nil, false, errors.New("cron state file is unsafe")
	}
	if err := store.ValidateRegular(name, opened); err != nil {
		return nil, nil, false, errors.New("cron state file is unsafe")
	}
	return data, opened, true, nil
}

func replaceCanonicalRegular(
	projectDir string,
	target string,
	tempPrefix string,
	data []byte,
	beforePromote func() error,
) error {
	store, _, err := openCanonicalCronStore(projectDir, true)
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck
	temporary, expected, err := stageCanonicalRegular(store, tempPrefix, data)
	if err != nil {
		return err
	}
	defer store.RemoveRegularIfSame(temporary, expected)
	if beforePromote != nil {
		if err := beforePromote(); err != nil {
			return errors.New("cron state promotion interrupted")
		}
	}
	if err := store.PromoteRegular(temporary, target, expected); err != nil {
		return errors.New("cron state promotion failed")
	}
	return nil
}

func stageCanonicalRegular(
	store *statemigration.CanonicalStore,
	prefix string,
	data []byte,
) (string, os.FileInfo, error) {
	for attempts := 0; attempts < 16; attempts++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, errors.New("cron state staging failed")
		}
		name := prefix + hex.EncodeToString(token[:]) + ".tmp"
		file, created, exists, err := store.OpenRegular(name, os.O_WRONLY, true)
		if err != nil {
			continue
		}
		if !exists {
			return "", nil, errors.New("cron state staging failed")
		}
		cleanup := func(expected os.FileInfo) {
			_ = file.Close()
			store.RemoveRegularIfSame(name, expected)
		}
		if len(data) > 0 {
			written, writeErr := file.Write(data)
			if writeErr != nil || written != len(data) {
				cleanup(created)
				return "", nil, errors.New("cron state staging failed")
			}
		}
		if err := file.Sync(); err != nil {
			cleanup(created)
			return "", nil, errors.New("cron state staging failed")
		}
		expected, err := file.Stat()
		if err != nil {
			cleanup(created)
			return "", nil, errors.New("cron state staging failed")
		}
		if err := file.Close(); err != nil {
			store.RemoveRegularIfSame(name, expected)
			return "", nil, errors.New("cron state staging failed")
		}
		if err := store.ValidateRegular(name, expected); err != nil {
			store.RemoveRegularIfSame(name, expected)
			return "", nil, errors.New("cron state staging failed")
		}
		return name, expected, nil
	}
	return "", nil, errors.New("cron state staging failed")
}
