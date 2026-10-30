package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type settingsLockWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *settingsLockWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestSettingsPatchContextCancellationWhileWaitingForSharedLock(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"max_turns":4}`)
	path := ProjectConfigPath(project)
	_, revision, err := ReadSettingsDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := openWorkbenchSettingsStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.close()
	if err := holder.lock(); err != nil {
		t.Fatal(err)
	}
	defer holder.unlock()
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &settingsLockWaitContext{Context: base, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := SettingsPatchContext(ctx, project, "project", revision, map[string]json.RawMessage{"max_turns": json.RawMessage("8")})
		result <- err
	}()
	select {
	case <-ctx.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("settings patch did not enter shared-lock waiting")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled lock wait = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled patch remained blocked behind the held lock")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"max_turns":4}` {
		t.Fatalf("cancelled lock wait changed settings: %v", err)
	}
}

type settingsStagedCancellationContext struct {
	context.Context
	store               *workbenchSettingsStore
	cancel              context.CancelFunc
	returnCapturedError bool
	err                 error
}

func (c *settingsStagedCancellationContext) Err() error {
	captured := c.Context.Err()
	entries, err := os.ReadDir(c.store.path)
	if err != nil {
		c.err = err
		c.cancel()
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".settings-") && strings.HasSuffix(entry.Name(), ".tmp") {
			c.cancel()
		}
	}
	if c.returnCapturedError {
		return captured
	}
	return c.Context.Err()
}

func TestSettingsWriteContextFinalCommitCancellationBoundary(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "observed_before_replace"
		if late {
			name = "after_final_check"
		}
		t.Run(name, func(t *testing.T) {
			project := workbenchSettingsFixture(t, "", `{"max_turns":4}`)
			path := ProjectConfigPath(project)
			store, err := openWorkbenchSettingsStore(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			if err := store.lock(); err != nil {
				t.Fatal(err)
			}
			defer store.unlock()
			document, err := store.read()
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &settingsStagedCancellationContext{Context: base, store: store, cancel: cancel, returnCapturedError: late}
			// The staged temporary file is an independent oracle for the final
			// commit phase. Returning a captured nil models cancellation racing
			// immediately after that last check, not a rollback guarantee.
			err = store.writeContext(ctx, []byte(`{"max_turns":8}`), document.revision())
			if ctx.err != nil || !errors.Is(base.Err(), context.Canceled) {
				t.Fatalf("staging oracle failed: read=%v cancellation=%v", ctx.err, base.Err())
			}
			want := `{"max_turns":4}`
			if late {
				want = `{"max_turns":8}`
				if err != nil {
					t.Fatalf("late cancellation changed committed success: %v", err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("observed cancellation = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want {
				t.Fatalf("commit boundary document = %q, error = %v", data, err)
			}
			entries, err := os.ReadDir(store.path)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tmp") {
					t.Fatal("cancelled or completed write left a staging file")
				}
			}
		})
	}
}

func TestSettingsPatchContextAlreadyCancelledDoesNotCreateSettings(t *testing.T) {
	project := workbenchSettingsFixture(t, "", "")
	snapshot, err := SettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = SettingsPatchContext(ctx, project, "project", snapshot.Revision, map[string]json.RawMessage{"max_turns": json.RawMessage("8")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled patch = %v", err)
	}
	if _, err := os.Stat(ProjectConfigPath(project)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("already cancelled patch created settings: %v", err)
	}
}
