package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("NODE_ENV") == "" {
		_ = os.Setenv("NODE_ENV", "test")
	}
	testStateRoot, err := os.MkdirTemp("", "yhc-engine-test-state-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create engine test state root:", err)
		os.Exit(2)
	}
	originalResolver := resolveEmptyCWDTranscriptDir
	var sequence atomic.Uint64
	resolveEmptyCWDTranscriptDir = func() string {
		return filepath.Join(testStateRoot, strconv.FormatUint(sequence.Add(1), 10), "transcripts")
	}

	code := m.Run()
	resolveEmptyCWDTranscriptDir = originalResolver
	if err := os.RemoveAll(testStateRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove engine test state root:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
