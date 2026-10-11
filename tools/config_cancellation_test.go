package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
)

// cancelAfterSecondErrContext cancels its parent immediately after the Config
// tool's final admission precheck has observed a live context. Returning that
// already-observed nil makes the boundary deterministic: the following write
// transaction must receive the same context to notice cancellation.
type cancelAfterSecondErrContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *cancelAfterSecondErrContext) Err() error {
	err := c.Context.Err()
	c.calls++
	if c.calls == 2 {
		c.cancel()
	}
	return err
}

func TestSharedConfigCancellationAfterAdmissionPrecheckDoesNotCommit(t *testing.T) {
	project, _ := sharedConfigFixture(t)
	path := engineconfig.ProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"dark"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelAfterSecondErrContext{Context: base, cancel: cancel}
	_, err := runSharedConfig(WithExecutionCWD(ctx, project), t, `{"setting":"theme","value":"light"}`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Config after admission cancellation = %v, want context cancellation", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("cancelled Config committed settings: got %s, want original bytes", after)
	}
}
