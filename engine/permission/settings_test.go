package permission

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/config"
)

func permissionSettingsFixture(t *testing.T, scope, value string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	dest := map[string]SettingsDestination{"user": DestUserSettings, "project": DestProjectSettings, "local": DestLocalSettings}[scope]
	path := settingsPathForDest(project, dest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return project, path
}

func TestRuleSettingsShareCanonicalWriterAndExcludePrivateFields(t *testing.T) {
	for _, scope := range []string{"user", "project", "local"} {
		t.Run(scope, func(t *testing.T) {
			project, path := permissionSettingsFixture(t, scope, `{"opaque":9007199254740993,"mcp_servers":{"x":{"env":{"KEY":"fixture-private"}}},"permissions":{"allow":["Read"],"custom":{"big":9007199254740993}},"permission_rules":["Bash(echo *):deny"]}`)
			snapshot, err := RuleSettingsSnapshot(project, scope)
			if err != nil || len(snapshot.Rules) != 2 || snapshot.Rules[1].Editable {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
			wire, _ := json.Marshal(snapshot)
			if strings.Contains(string(wire), "fixture-private") || strings.Contains(string(wire), "opaque") {
				t.Fatal("private configuration leaked")
			}
			updated, err := RuleSettingsPatch(project, scope, snapshot.Revision, []RuleSettingsChange{{Operation: "add", Action: ActionAllow, Rule: "Edit"}, {Operation: "remove", Action: ActionAllow, Rule: "Read"}})
			if err != nil || updated.Revision == snapshot.Revision {
				t.Fatalf("update=%v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]json.RawMessage
			_ = json.Unmarshal(data, &stored)
			if string(stored["opaque"]) != "9007199254740993" || !strings.Contains(string(stored["permissions"]), "custom") || !strings.Contains(string(stored["mcp_servers"]), "fixture-private") {
				t.Fatal("unrelated settings lost")
			}
			loaded, err := LoadPermissionRules(project)
			if err != nil {
				t.Fatal(err)
			}
			var edit, read bool
			for _, rule := range loaded {
				edit = edit || rule.ToolName == "Edit"
				read = read || rule.ToolName == "Read"
			}
			if !edit || read {
				t.Fatal("canonical loader did not consume saved rules")
			}
		})
	}
}

func TestRuleSettingsRejectMalformedSectionsWithoutOverwriting(t *testing.T) {
	for _, raw := range []string{`{"permissions":"private"}`, `{"permissions":{"allow":["Read",3]}}`, `{"permissions":{"deny":3}}`} {
		project, path := permissionSettingsFixture(t, "local", raw)
		if _, err := RuleSettingsSnapshot(project, "local"); !errors.Is(err, config.ErrSettingsInvalid) {
			t.Fatalf("malformed read=%v", err)
		}
		_, revision, err := config.ReadSettingsDocument(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RuleSettingsPatch(project, "local", revision, []RuleSettingsChange{{Operation: "add", Action: ActionAllow, Rule: "Edit"}}); !errors.Is(err, config.ErrSettingsInvalid) {
			t.Fatal("malformed section overwritten")
		}
		data, _ := os.ReadFile(path)
		if string(data) != raw {
			t.Fatal("rejected write changed bytes")
		}
	}
}

func TestRuleSettingsConflictWithTUIAndSharedDefaultsIsAtomic(t *testing.T) {
	project, path := permissionSettingsFixture(t, "project", `{"max_turns":4,"permissions":{"allow":["Read"]}}`)
	snapshot, err := RuleSettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	if err := PersistPermissionRules(project, []string{"Bash(go test *)"}, ActionAsk, DestProjectSettings); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := RuleSettingsPatch(project, "project", snapshot.Revision, []RuleSettingsChange{{Operation: "remove", Action: ActionAllow, Rule: "Read"}}); !errors.Is(err, config.ErrSettingsConflict) {
		t.Fatal("ignored TUI writer")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("conflicting update changed bytes")
	}
	snapshot, err = RuleSettingsSnapshot(project, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.SettingsPatch(project, "project", snapshot.Revision, map[string]json.RawMessage{"max_turns": json.RawMessage("0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := RuleSettingsPatch(project, "project", snapshot.Revision, []RuleSettingsChange{{Operation: "add", Action: ActionDeny, Rule: "Write"}}); !errors.Is(err, config.ErrSettingsConflict) {
		t.Fatal("ignored defaults writer")
	}
}

func TestRuleSettingsRejectInvalidBatchBeforeAnyMutation(t *testing.T) {
	project, path := permissionSettingsFixture(t, "local", `{"permissions":{"allow":["Read"]}}`)
	snapshot, err := RuleSettingsSnapshot(project, "local")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, bad := range []RuleSettingsChange{{Operation: "replace", Action: ActionAllow, Rule: "Edit"}, {Operation: "add", Action: "bypass", Rule: "Edit"}, {Operation: "add", Action: ActionAllow, Rule: "\x00"}} {
		if _, err := RuleSettingsPatch(project, "local", snapshot.Revision, []RuleSettingsChange{{Operation: "remove", Action: ActionAllow, Rule: "Read"}, bad}); !errors.Is(err, config.ErrSettingsInvalid) {
			t.Fatal("invalid batch admitted")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("partial update")
		}
	}
	if _, err := RuleSettingsSnapshot(project, "arbitrary"); !errors.Is(err, config.ErrSettingsInvalid) {
		t.Fatal("unknown scope admitted")
	}
	if _, err := RuleSettingsSnapshot("", "local"); !errors.Is(err, config.ErrSettingsInvalid) {
		t.Fatal("unowned workspace admitted")
	}
}
