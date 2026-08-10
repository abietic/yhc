//go:build !windows

package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceSessionExportFile(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("export committed but parent durability is uncertain: %w", err)
	}
	defer directory.Close() //nolint:errcheck
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("export committed but parent durability is uncertain: %w", err)
	}
	return nil
}
