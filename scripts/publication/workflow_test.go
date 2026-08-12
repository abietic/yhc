package main

import (
	"encoding/json"
	"os"
	"os/exec"
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

func TestMakefileWiresPublicationSecurityTools(t *testing.T) {
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
	sbomStart := strings.Index(makefile, "sbom: prepare-cyclonedx-gomod\n")
	licenseStart := strings.Index(makefile, "license-check: sbom\n")
	if sbomStart < 0 || licenseStart <= sbomStart {
		t.Fatal("Makefile lacks a bounded SBOM target")
	}
	sbomTarget := makefile[sbomStart:licenseStart]
	for _, contract := range []string{
		`@set -e;`,
		`publication materialize --config $(PUBLICATION_CONFIG)`,
		`cd "$$task_tree_parent/tree"`,
		`-output $(abspath $(PUBLICATION_SBOM_GENERATED)) .`,
	} {
		if !strings.Contains(sbomTarget, contract) {
			t.Fatalf("SBOM generation does not retain VCS-free contract %q", contract)
		}
	}
	licenseTarget := makefile[licenseStart:]
	if !strings.Contains(licenseTarget, `licenses --config $(PUBLICATION_CONFIG) --root . --sbom $(abspath $(PUBLICATION_SBOM_GENERATED)) --output $(PUBLICATION_LICENSE_REPORT)`) {
		t.Fatal("license-check must pass the generated SBOM explicitly")
	}
	if strings.Contains(sbomTarget, "normalize-sbom") || strings.Contains(sbomTarget, "sbom.cdx.json") {
		t.Fatal("SBOM target must not normalize or compare a tracked SBOM")
	}
	publicationStart := strings.Index(ci, "  publication-safety:\n")
	requiredStart := strings.Index(ci, "  required:\n")
	if publicationStart < 0 || requiredStart <= publicationStart {
		t.Fatal("CI lacks a bounded publication-safety job")
	}
	publicationJob := ci[publicationStart:requiredStart]
	if !strings.Contains(publicationJob, "fetch-depth: 0") {
		t.Fatal("publication safety must fetch the signed public baseline history")
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
		!strings.Contains(string(mustJSON(t, ruleset)), `"Required gates"`) {
		t.Fatal("master ruleset must require resolved conversations, zero approvals, and Required gates")
	}
}

func TestCIClassifiesUnreachablePushBaseAsFullTree(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/ci.yml")
	script := workflowRunBlock(t, workflow, "Resolve comparison base and change class")
	repository := t.TempDir()
	runGit(t, repository, "init", "-b", "master")
	if err := os.WriteFile(filepath.Join(repository, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write root fixture: %v", err)
	}
	runGit(t, repository, "add", "main.go")
	runGit(t, repository, "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@invalid", "commit", "-m", "root")

	scriptPath := filepath.Join(t.TempDir(), "classify.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write classification script: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "github-output")
	summaryPath := filepath.Join(t.TempDir(), "github-summary")
	cmd := exec.Command("bash", "-e", scriptPath)
	cmd.Dir = repository
	cmd.Env = append(os.Environ(),
		"EVENT_NAME=push",
		"PR_BASE_SHA=",
		"PUSH_BASE_SHA="+strings.Repeat("f", 40),
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_STEP_SUMMARY="+summaryPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("classify unreachable push base: %v\n%s", err, output)
	}

	outputs, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read classification outputs: %v", err)
	}
	emptyTree := strings.TrimSpace(gitOutputForTest(t, repository, "hash-object", "-t", "tree", "/dev/null"))
	for _, want := range []string{"base_sha=" + emptyTree, "docs_only=false", "full_tree=true"} {
		if !strings.Contains(string(outputs), want+"\n") {
			t.Fatalf("classification outputs %q lack %q", outputs, want)
		}
	}
	for _, want := range []string{
		"FULL_TREE: ${{ needs.changes.outputs.full_tree }}",
		"git diff --check \"$BASE_SHA\" HEAD -- .",
		"if [[ \"$FULL_TREE\" == \"true\" ]]; then\n            make lint",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("full-tree quality fallback lacks %q", want)
		}
	}
}

func workflowRunBlock(t *testing.T, workflow, stepName string) string {
	t.Helper()
	stepMarker := "      - name: " + stepName + "\n"
	stepStart := strings.Index(workflow, stepMarker)
	if stepStart < 0 {
		t.Fatalf("workflow lacks step %q", stepName)
	}
	remainder := workflow[stepStart+len(stepMarker):]
	runMarker := "        run: |\n"
	runStart := strings.Index(remainder, runMarker)
	if runStart < 0 {
		t.Fatalf("workflow step %q lacks run block", stepName)
	}
	remainder = remainder[runStart+len(runMarker):]
	lines := make([]string, 0)
	for _, line := range strings.Split(remainder, "\n") {
		if line == "" {
			lines = append(lines, line)
			continue
		}
		if !strings.HasPrefix(line, "          ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n")
}

func gitOutputForTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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
