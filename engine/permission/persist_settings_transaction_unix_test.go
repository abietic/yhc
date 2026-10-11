//go:build unix

package permission

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/config"
	"golang.org/x/sys/unix"
)

func TestPermissionPersistenceHonorsSharedSettingsLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, dest := range []SettingsDestination{DestUserSettings, DestProjectSettings, DestLocalSettings} {
		t.Run(string(dest), func(t *testing.T) {
			project := t.TempDir()
			path := settingsPathForDest(project, dest)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			const original = `{"max_turns":4,"permissions":{"allow":["Read"]}}`
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			guard, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".settings-write.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close() //nolint:errcheck
			if err := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(guard.Fd()), unix.LOCK_UN) //nolint:errcheck
			for _, update := range []func() error{
				func() error { return PersistPermissionRules(project, []string{"Edit"}, ActionAllow, dest) },
				func() error { return RemovePermissionRules(project, []string{"Read"}, ActionAllow, dest) },
			} {
				if err := update(); !errors.Is(err, config.ErrSettingsBusy) {
					t.Fatalf("permission writer bypassed shared settings lock: %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != original {
					t.Fatalf("locked settings changed: %v", err)
				}
			}
		})
	}
}

func TestPermissionPersistencePreservesPrivateSettingsAndMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	path := config.ProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const original = `{"max_turns":4,"opaque":{"integer":9007199254740993},"mcp_servers":{"private":{"env":{"API_KEY":"fixture-only"}}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := config.SettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.SettingsPatch(project, "project", snapshot.Revision, map[string]json.RawMessage{"max_turns": json.RawMessage("0")}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []func() error{
		func() error {
			return PersistPermissionRules(project, []string{"Read"}, ActionAllow, DestProjectSettings)
		},
		func() error {
			return RemovePermissionRules(project, []string{"Read"}, ActionAllow, DestProjectSettings)
		},
	} {
		if err := update(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("permission update exposed private settings: mode=%v err=%v", info, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]json.RawMessage
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		var opaque struct {
			Integer int64 `json:"integer"`
		}
		if err := json.Unmarshal(stored["opaque"], &opaque); err != nil || opaque.Integer != 9007199254740993 || string(stored["max_turns"]) != "0" {
			t.Fatal("permission update changed unrelated settings")
		}
		var before map[string]json.RawMessage
		if err := json.Unmarshal([]byte(original), &before); err != nil {
			t.Fatal(err)
		}
		var beforeMCP, afterMCP any
		_ = json.Unmarshal(before["mcp_servers"], &beforeMCP)
		_ = json.Unmarshal(stored["mcp_servers"], &afterMCP)
		if stringMustJSON(t, beforeMCP) != stringMustJSON(t, afterMCP) {
			t.Fatal("permission update changed private MCP settings")
		}
	}
}

func stringMustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
