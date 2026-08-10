package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const immutableAction = `uses:\s+[^\s@]+@[0-9a-f]{40}(?:\s|$)`

func TestPublicWorkflowUsesImmutableActionsAndMinimalPermissions(t *testing.T) {
	workflows := readWorkflowFiles(t)
	ci := workflows[".github/workflows/ci.yml"]
	codeQL := workflows[".github/workflows/codeql.yml"]
	if ci == "" || codeQL == "" {
		t.Fatal("public workflows must include CI and CodeQL")
	}
	for path, workflow := range workflows {
		for _, line := range strings.Split(workflow, "\n") {
			if !strings.Contains(line, "uses:") {
				continue
			}
			if !regexp.MustCompile(immutableAction).MatchString(line) {
				t.Fatalf("%s uses a mutable action reference: %s", path, line)
			}
		}
		if strings.Contains(workflow, "permissions: write-all") {
			t.Fatalf("%s grants write-all permissions", path)
		}
	}
	if !strings.Contains(ci, "permissions:\n  contents: read") {
		t.Fatal("CI must use a read-only default token")
	}
	if !strings.Contains(codeQL, "permissions:\n  contents: read\n  security-events: write") {
		t.Fatal("CodeQL must use only contents: read and security-events: write")
	}
}

func TestPublicWorkflowRejectsPullRequestTargetAndForkSecrets(t *testing.T) {
	workflows := readWorkflowFiles(t)
	for path, workflow := range workflows {
		if strings.Contains(workflow, "pull_request_target") {
			t.Fatalf("%s must not use pull_request_target", path)
		}
		if strings.Contains(workflow, "secrets.") || strings.Contains(workflow, "secrets[") {
			t.Fatalf("%s must not expose secrets to fork-controlled code", path)
		}
		if strings.Contains(workflow, "build/publication") {
			t.Fatalf("%s must not publish raw publication reports or scanner artifacts", path)
		}
	}
	codeQL := workflows[".github/workflows/codeql.yml"]
	for _, trigger := range []string{"pull_request:", "push:", "branches: [master]", "schedule:", "cron:"} {
		if !strings.Contains(codeQL, trigger) {
			t.Fatalf("CodeQL workflow lacks %q", trigger)
		}
	}
}

func readWorkflowFiles(t *testing.T) map[string]string {
	t.Helper()
	directory := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read workflow directory: %v", err)
	}
	workflows := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".yml" && extension != ".yaml" {
			continue
		}
		path := filepath.ToSlash(filepath.Join(".github", "workflows", entry.Name()))
		workflows[path] = readRepositoryFile(t, path)
	}
	if len(workflows) == 0 {
		t.Fatal("repository has no GitHub workflows")
	}
	return workflows
}

func TestRequiredGateDependsOnPublicationSafety(t *testing.T) {
	ci := readRepositoryFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(ci, "name: Publication safety") {
		t.Fatal("CI lacks the publication safety job")
	}
	if !strings.Contains(ci, "run: make publication-safety") {
		t.Fatal("publication safety must run the publication safety Make target")
	}
	makefile := readRepositoryFile(t, "Makefile")
	for _, target := range []string{
		"$(MAKE) publication-check-policy",
		"$(MAKE) publication-scan-expression PUBLICATION_ROOT=.",
		"$(MAKE) vuln-check",
		"$(MAKE) license-check",
		"$(MAKE) sbom",
		"$(MAKE) secret-check PUBLICATION_ROOT=.",
	} {
		if !strings.Contains(makefile, target) {
			t.Fatalf("publication safety does not run %q", target)
		}
	}
	for _, contract := range []string{
		`if [[ -e "$$task_scan_root/.git" ]]`,
		`publication materialize --config $(PUBLICATION_CONFIG)`,
		`"$$task_tree_parent/tree"`,
		`"$$task_scan_root"`,
	} {
		if !strings.Contains(makefile, contract) {
			t.Fatalf("source secret-check does not retain %q", contract)
		}
	}
	required := ci[strings.Index(ci, "  required:\n"):]
	if !strings.Contains(required, "- publication-safety") || !strings.Contains(required, "PUBLICATION_SAFETY_RESULT") {
		t.Fatal("Required gates must depend on and enforce publication safety")
	}
	treeStart := strings.Index(makefile, "verify-publication-tree:")
	if treeStart < 0 {
		t.Fatal("Makefile lacks verify-publication-tree")
	}
	treeTarget := makefile[treeStart:]
	secretIndex := strings.Index(treeTarget, "$(MAKE) secret-check PUBLICATION_ROOT=.")
	vulnerabilityIndex := strings.Index(treeTarget, "$(MAKE) vuln-check")
	if secretIndex < 0 || vulnerabilityIndex < 0 || secretIndex > vulnerabilityIndex {
		t.Fatal("materialized-tree secret scan must run before generated vulnerability evidence")
	}
}

func TestRepositoryDesiredStateMatchesApprovedRules(t *testing.T) {
	settings := map[string]any{}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, ".github/repository-settings.json")), &settings); err != nil {
		t.Fatalf("decode repository settings: %v", err)
	}
	for key, want := range map[string]any{
		"default_branch":         "master",
		"allow_squash_merge":     true,
		"allow_merge_commit":     false,
		"allow_rebase_merge":     false,
		"delete_branch_on_merge": true,
	} {
		if settings[key] != want {
			t.Fatalf("repository setting %q = %#v, want %#v", key, settings[key], want)
		}
	}
	security, ok := settings["security_and_analysis"].(map[string]any)
	if !ok || len(security) < 3 {
		t.Fatal("repository desired state must enable security analysis features")
	}

	ruleset := map[string]any{}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, ".github/rulesets/master.json")), &ruleset); err != nil {
		t.Fatalf("decode master ruleset: %v", err)
	}
	rules, ok := ruleset["rules"].([]any)
	if !ok {
		t.Fatal("master ruleset lacks rules")
	}
	wantTypes := map[string]bool{"deletion": false, "non_fast_forward": false, "required_linear_history": false, "pull_request": false, "required_status_checks": false}
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if ruleType, ok := rule["type"].(string); ok {
			if _, wanted := wantTypes[ruleType]; wanted {
				wantTypes[ruleType] = true
			}
		}
	}
	for ruleType, found := range wantTypes {
		if !found {
			t.Fatalf("master ruleset lacks %s", ruleType)
		}
	}
	if !strings.Contains(string(mustJSON(t, ruleset)), `"required_approving_review_count":0`) ||
		!strings.Contains(string(mustJSON(t, ruleset)), `"required_review_thread_resolution":true`) ||
		!strings.Contains(string(mustJSON(t, ruleset)), `"CI / Required gates"`) {
		t.Fatal("master ruleset must require resolved conversations, zero approvals, and CI / Required gates")
	}
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
	return encoded
}
