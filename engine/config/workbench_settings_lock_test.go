package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkbenchSettingsAdvisoryLockHolder(t *testing.T) {
	if os.Getenv("YHC_SETTINGS_LOCK_HOLDER") != "1" {
		return
	}
	store, err := openWorkbenchSettingsStore(ProjectConfigPath(os.Getenv("YHC_SETTINGS_LOCK_PROJECT")), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.lock(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("settings-lock-acquired")
	var input [1]byte
	_, _ = os.Stdin.Read(input[:])
}

func TestWorkbenchSettingsAdvisoryLockRecoversAfterProcessIsKilled(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"max_turns":4}`)
	snapshot, err := SettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWorkbenchSettingsAdvisoryLockHolder$")
	child.Env = append(os.Environ(), "YHC_SETTINGS_LOCK_HOLDER=1", "YHC_SETTINGS_LOCK_PROJECT="+project)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close() //nolint:errcheck
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(output).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	select {
	case line := <-ready:
		if line != "settings-lock-acquired" {
			t.Fatalf("child did not acquire lock: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child to acquire settings lock")
	}
	guardPath := filepath.Join(filepath.Dir(ProjectConfigPath(project)), ".settings-write.lock")
	guard, err := os.Stat(guardPath)
	if err != nil {
		t.Fatalf("inspect guard before crash: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("lock holder unexpectedly exited successfully")
	}
	updated, err := SettingsPatch(project, "project", snapshot.Revision, map[string]json.RawMessage{
		"max_turns": json.RawMessage("8"),
	})
	if err != nil || string(updated.Fields["max_turns"].EffectiveValue) != "8" {
		t.Fatalf("settings remained blocked after holder crash: snapshot=%+v err=%v stderr=%s", updated, err, stderr.String())
	}
	after, err := os.Stat(guardPath)
	if err != nil || !os.SameFile(guard, after) {
		t.Fatalf("recovery replaced or removed guard inode: %v", err)
	}
}

func TestWorkbenchSettingsAdvisoryLockKeepsGuardAcrossSaves(t *testing.T) {
	project := workbenchSettingsFixture(t, "", "")
	var original os.FileInfo
	for _, raw := range []string{"3", "5"} {
		snapshot, err := SettingsSnapshot(project, "project")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := SettingsPatch(project, "project", snapshot.Revision, map[string]json.RawMessage{"max_turns": json.RawMessage(raw)}); err != nil {
			t.Fatal(err)
		}
		guard, err := os.Stat(filepath.Join(filepath.Dir(ProjectConfigPath(project)), ".settings-write.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if original != nil && !os.SameFile(original, guard) {
			t.Fatal("settings save replaced the guard inode and split waiter admission")
		}
		original = guard
	}
}

func TestWorkbenchSettingsAdvisoryLockRejectsReplacedGuardBeforeWrite(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"max_turns":4}`)
	store, err := openWorkbenchSettingsStore(ProjectConfigPath(project), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.lock(); err != nil {
		t.Fatal(err)
	}
	document, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.root.Rename(".settings-write.lock", ".previous-settings-lock"); err != nil {
		t.Fatal(err)
	}
	guard, err := store.root.OpenFile(".settings-write.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = guard.Close()
	if err := store.write([]byte(`{"max_turns":99}`), document.revision()); !errors.Is(err, ErrSettingsUnsafePath) {
		t.Fatalf("settings write ignored replaced guard: %v", err)
	}
	saved, err := os.ReadFile(ProjectConfigPath(project))
	if err != nil || string(saved) != `{"max_turns":4}` {
		t.Fatalf("settings changed after guard replacement: %v", err)
	}
}
