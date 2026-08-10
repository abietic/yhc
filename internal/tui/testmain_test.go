package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) {
	testStateRoot, err := os.MkdirTemp("", "yhc-tui-test-state-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create TUI test state root:", err)
		os.Exit(2)
	}
	originalResolver := resolveHistoryLocations
	var sequence atomic.Uint64
	resolveHistoryLocations = func() (historyLocations, error) {
		scope := filepath.Join(testStateRoot, strconv.FormatUint(sequence.Add(1), 10))
		projectRoot := filepath.Join(scope, "project")
		compatibilityConfigDir := filepath.Join(scope, "compatibility")
		if err := os.MkdirAll(projectRoot, 0o700); err != nil {
			return historyLocations{}, err
		}
		if err := os.MkdirAll(compatibilityConfigDir, 0o700); err != nil {
			return historyLocations{}, err
		}
		return historyLocations{
			projectRoot:            projectRoot,
			compatibilityConfigDir: compatibilityConfigDir,
		}, nil
	}

	code := m.Run()
	resolveHistoryLocations = originalResolver
	if err := os.RemoveAll(testStateRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove TUI test state root:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
