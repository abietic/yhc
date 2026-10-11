package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
)

func TestPermissionRuleSettingsApplyAtIdleAndRejectOwnedTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	e := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(e.Close)
	snapshot, err := e.PermissionRuleSettings("local")
	if err != nil {
		t.Fatal(err)
	}
	changes := []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}}
	e.planMu.Lock()
	e.planActiveTurnID = "owned-turn"
	e.planMu.Unlock()
	if _, err := e.ApplyPermissionRuleSettings(context.Background(), "local", snapshot.Revision, changes); !errors.Is(err, ErrPermissionSettingsActiveTurn) {
		t.Fatal("active turn admitted settings")
	}
	if _, err := os.Stat(config.ProjectLocalConfigPath(root)); !os.IsNotExist(err) {
		t.Fatal("active rejection wrote settings")
	}
	e.planMu.Lock()
	e.planActiveTurnID = ""
	e.planState.Phase = PlanPhaseAwaitingApproval
	e.planMu.Unlock()
	if _, err := e.ApplyPermissionRuleSettings(context.Background(), "local", snapshot.Revision, changes); !errors.Is(err, ErrPermissionSettingsActiveTurn) {
		t.Fatal("pending Plan approval admitted settings")
	}
	e.planMu.Lock()
	e.planState.Phase = PlanPhaseInactive
	e.planMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ApplyPermissionRuleSettings(ctx, "local", snapshot.Revision, changes); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request admitted settings")
	}
	updated, err := e.ApplyPermissionRuleSettings(context.Background(), "local", snapshot.Revision, changes)
	if err != nil || !updated.RuntimeApplied || updated.Diagnostic != "" || e.PermissionMode() != permission.ModeDefault {
		t.Fatalf("apply=%+v err=%v", updated, err)
	}
	if action, matched := e.permissionRulesSnapshot().EvaluateMatch("Write", map[string]any{"file_path": "fixture"}); !matched || action != permission.ActionDeny {
		t.Fatal("live engine did not apply stored denial")
	}
}

func TestPermissionRuleSettingsReportCommittedReloadFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	e := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(e.Close)
	e.mu.Lock()
	e.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Edit", Action: permission.ActionDeny}})
	e.mu.Unlock()
	snapshot, err := e.PermissionRuleSettings("local")
	if err != nil {
		t.Fatal(err)
	}
	// A different canonical source becomes unreadable after engine construction.
	if err := os.MkdirAll(config.UserConfigPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := e.ApplyPermissionRuleSettings(context.Background(), "local", snapshot.Revision, []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}})
	if err != nil || result.RuntimeApplied || result.Diagnostic != "runtime_reload_failed" || result.Revision == snapshot.Revision {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	stored, err := e.PermissionRuleSettings("local")
	if err != nil || len(stored.Rules) != 1 || stored.Rules[0].Value != "Write" {
		t.Fatal("committed disk mutation not reported")
	}
	if !e.permissionRulesSnapshot().IsToolBlanketDenied("Edit") || e.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatal("failed reload replaced previous runtime policy")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionRuleSettingsDirectApplyLeavesOtherEngineCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	first := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(first.Close)
	other := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(other.Close)
	// Isolate this method's direct target from the existing background watcher.
	// Production watchers may later consume the same shared file, as with TUI.
	other.settingsWatcher.Stop()
	snapshot, err := first.PermissionRuleSettings("project")
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.ApplyPermissionRuleSettings(context.Background(), "project", snapshot.Revision, []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}})
	if err != nil || !first.permissionRulesSnapshot().IsToolBlanketDenied("Write") || other.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatal("runtime application crossed engine ownership")
	}
	other.reloadPermissionRules()
	if !other.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatal("existing watcher reload path did not consume the shared file")
	}
}
