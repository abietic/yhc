package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestPermissionRuleSettingsApplyDoesNotLoseDenyToStaleReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	e := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(e.Close)
	e.settingsWatcher.Stop()
	registry := tools.NewRegistry()
	registerPoolTestTool(registry, "Write")
	e.toolRegistry = registry
	e.config.ToolRegistry = registry

	loaded := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
	reloaded := make(chan struct{})
	var reloadErr error
	go func() {
		defer close(reloaded)
		reloadErr = e.reloadPermissionRulesWithLoader(func(projectRoot string) ([]permission.PermissionRule, error) {
			rules, err := permission.LoadPermissionRules(projectRoot)
			close(loaded)
			<-release
			return rules, err
		})
	}()
	applyDone := make(chan struct{})
	var applyStarted bool
	t.Cleanup(func() {
		releaseOld()
		<-reloaded
		if applyStarted {
			<-applyDone
		}
	})
	<-loaded

	snapshot, err := e.PermissionRuleSettings("local")
	if err != nil {
		t.Fatal(err)
	}
	type applyResult struct {
		result PermissionRuleSettingsResult
		err    error
	}
	var applyResultValue applyResult
	applyStarted = true
	go func() {
		result, err := e.ApplyPermissionRuleSettings(context.Background(), "local", snapshot.Revision, []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}})
		applyResultValue = applyResult{result: result, err: err}
		close(applyDone)
	}()
	if e.permissionRulesReloadMu.TryLock() {
		e.permissionRulesReloadMu.Unlock()
		// Without the reload mutex, Apply completes while this older load is
		// paused, so releasing it deterministically installs stale rules last.
		<-applyDone
		result := applyResultValue
		if result.err != nil || !result.result.RuntimeApplied {
			t.Fatalf("apply=%+v err=%v", result.result, result.err)
		}
		releaseOld()
	} else {
		releaseOld()
	}
	<-reloaded
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	<-applyDone
	result := applyResultValue
	if result.err != nil || !result.result.RuntimeApplied {
		t.Fatalf("apply=%+v err=%v", result.result, result.err)
	}
	if !e.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatal("stale reload replaced newly applied deny")
	}
	for _, tool := range e.modelVisibleTools() {
		if tool.Name == "Write" {
			t.Fatal("next model-visible tool pool retained Write after deny")
		}
	}
}

func TestPermissionRulesReloadHoldsReloadLockDuringLoad(t *testing.T) {
	e := &QueryEngine{config: QueryEngineConfig{PermissionProjectRoot: t.TempDir()}}
	err := e.reloadPermissionRulesWithLoader(func(string) ([]permission.PermissionRule, error) {
		if e.permissionRulesReloadMu.TryLock() {
			e.permissionRulesReloadMu.Unlock()
			t.Fatal("ordinary reload loaded rules without the reload mutex")
		}
		return []permission.PermissionRule{{ToolName: "Write", Action: permission.ActionDeny}}, nil
	})
	if err != nil || !e.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatalf("reload err=%v deny=%v", err, e.permissionRulesSnapshot().IsToolBlanketDenied("Write"))
	}
}

func TestPermissionRuleSettingsApplyHoldsReloadLockDuringLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	e := NewQueryEngine(QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(e.Close)
	e.settingsWatcher.Stop()
	snapshot, err := e.PermissionRuleSettings("local")
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.applyPermissionRuleSettingsWithLoader(context.Background(), "local", snapshot.Revision, []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}}, func(projectRoot string) ([]permission.PermissionRule, error) {
		if e.permissionRulesReloadMu.TryLock() {
			e.permissionRulesReloadMu.Unlock()
			t.Fatal("Apply loaded rules without the reload mutex")
		}
		return permission.LoadPermissionRules(projectRoot)
	})
	if err != nil || !result.RuntimeApplied || result.Revision == snapshot.Revision {
		t.Fatalf("apply=%+v err=%v", result, err)
	}
	if !e.permissionRulesSnapshot().IsToolBlanketDenied("Write") {
		t.Fatal("typed Apply did not install its persisted deny")
	}
}

func TestPermissionRulesReloadInstallsReturnedRulesOnLoadError(t *testing.T) {
	e := &QueryEngine{permissionRules: permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Edit", Action: permission.ActionDeny}})}
	err := e.reloadPermissionRulesWithLoader(func(string) ([]permission.PermissionRule, error) {
		return []permission.PermissionRule{{ToolName: "Write", Action: permission.ActionDeny}}, errors.New("load failed")
	})
	if err == nil {
		t.Fatal("reload hid load failure")
	}
	rules := e.permissionRulesSnapshot()
	if !rules.IsToolBlanketDenied("Write") || rules.IsToolBlanketDenied("Edit") {
		t.Fatal("ordinary reload did not replace cache with returned rules")
	}
}
