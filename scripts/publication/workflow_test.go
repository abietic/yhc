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

func TestCodeQLAnalyzesGoAndDesktopJavaScript(t *testing.T) {
	codeQL := readWorkflowFiles(t)[".github/workflows/codeql.yml"]
	for _, contract := range []string{
		"name: Analyze ${{ matrix.language }}",
		"fail-fast: false",
		"- language: go\n            build-mode: autobuild",
		"- language: javascript-typescript\n            build-mode: none",
		"languages: ${{ matrix.language }}",
		"build-mode: ${{ matrix.build-mode }}",
		"category: '/language:${{ matrix.language }}'",
		"github/codeql-action/init@c4dd10e44af883a891fe31ced449bcb4a6728b9b # v3.37.6",
		"github/codeql-action/analyze@c4dd10e44af883a891fe31ced449bcb4a6728b9b # v3.37.6",
	} {
		if !strings.Contains(codeQL, contract) {
			t.Fatalf("CodeQL workflow lacks multi-language contract %q", contract)
		}
	}
	if strings.Contains(codeQL, "github/codeql-action/autobuild@") {
		t.Fatal("CodeQL build modes must be owned by the init matrix")
	}
}

func TestDesktopNativePackageMatrixUsesExactUnpackedLifecycleTargets(t *testing.T) {
	ci := readWorkflowFiles(t)[".github/workflows/ci.yml"]
	start := strings.Index(ci, "  desktop-native-packages:\n")
	requiredStart := strings.Index(ci, "  required:\n")
	if start < 0 || requiredStart <= start {
		t.Fatal("CI lacks a bounded native Desktop package job")
	}
	job := ci[start:requiredStart]
	for _, contract := range []string{
		"name: Native Desktop package (${{ matrix.platform }})",
		"fail-fast: false",
		"- platform: macos-intel\n            runner: macos-15-intel\n            target: desktop-unpacked-window-reopen-smoke-darwin-amd64",
		"- platform: macos-arm64\n            runner: macos-15\n            target: desktop-unpacked-window-reopen-smoke-darwin-arm64",
		"- platform: windows-x64\n            runner: windows-2025\n            target: desktop-unpacked-lifecycle-smoke-windows-amd64",
		"runs-on: ${{ matrix.runner }}",
		"CSC_IDENTITY_AUTO_DISCOVERY: 'false'",
		"if: runner.os == 'Windows'",
		"choco install make --version=4.4.1 --yes --no-progress",
		"run: make SHELL=bash ${{ matrix.target }}",
	} {
		if !strings.Contains(job, contract) {
			t.Fatalf("native Desktop package job lacks %q", contract)
		}
	}
	if strings.Contains(job, "upload-artifact") || strings.Contains(job, "--publish") {
		t.Fatal("native Desktop package smoke must not upload or publish unsigned output")
	}
	if strings.Contains(job, "npm ci --ignore-scripts") || strings.Contains(job, "npm --prefix desktop rebuild electron") {
		t.Fatal("native Desktop package job must let the canonical Make target install its locked runtime once")
	}

	root := filepath.Join("..", "..")
	makefileBytes, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)
	for _, contract := range []string{
		"desktop-package-smoke-darwin-amd64: desktop-stage-darwin-amd64 desktop-install\n\tnpm --prefix desktop run package -- --mac --x64",
		"desktop-unpacked-lifecycle-smoke-darwin-amd64: desktop-package-smoke-darwin-amd64\n\tnode desktop/scripts/unpacked_lifecycle_smoke.cjs --app desktop/dist/mac/YHC.app/Contents/MacOS/YHC",
		"desktop-unpacked-window-reopen-smoke-darwin-amd64: desktop-package-smoke-darwin-amd64\n\tnode desktop/scripts/unpacked_lifecycle_smoke.cjs --app desktop/dist/mac/YHC.app/Contents/MacOS/YHC --reopen-window",
		"desktop-package-smoke-darwin-arm64: desktop-stage-darwin-arm64 desktop-install\n\tnpm --prefix desktop run package -- --mac --arm64",
		"desktop-unpacked-lifecycle-smoke-darwin-arm64: desktop-package-smoke-darwin-arm64\n\tnode desktop/scripts/unpacked_lifecycle_smoke.cjs --app desktop/dist/mac-arm64/YHC.app/Contents/MacOS/YHC",
		"desktop-unpacked-window-reopen-smoke-darwin-arm64: desktop-package-smoke-darwin-arm64\n\tnode desktop/scripts/unpacked_lifecycle_smoke.cjs --app desktop/dist/mac-arm64/YHC.app/Contents/MacOS/YHC --reopen-window",
		"desktop-package-smoke-windows-amd64: desktop-stage-windows-amd64 desktop-install\n\tnpm --prefix desktop run package -- --win --x64",
		"desktop-unpacked-lifecycle-smoke-windows-amd64: desktop-package-smoke-windows-amd64\n\tnode desktop/scripts/unpacked_lifecycle_smoke.cjs --app desktop/dist/win-unpacked/YHC.exe",
	} {
		if !strings.Contains(makefile, contract) {
			t.Fatalf("native Desktop package target lacks unpacked contract %q", contract)
		}
	}
	packageBytes, err := os.ReadFile(filepath.Join(root, "desktop", "package.json"))
	if err != nil {
		t.Fatalf("read Desktop package manifest: %v", err)
	}
	if !strings.Contains(string(packageBytes), `"package": "electron-builder --dir"`) {
		t.Fatal("native Desktop package smokes must resolve through electron-builder --dir")
	}

	required := ci[requiredStart:]
	for _, contract := range []string{
		"- desktop-native-packages",
		"DESKTOP_NATIVE_PACKAGES_RESULT: ${{ needs.desktop-native-packages.result }}",
		`"$DESKTOP_NATIVE_PACKAGES_RESULT" != "skipped"`,
		`"$DESKTOP_NATIVE_PACKAGES_RESULT" != "success"`,
	} {
		if !strings.Contains(required, contract) {
			t.Fatalf("required gate lacks native Desktop package contract %q", contract)
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
		`task_sbom_root="."`,
		`if [[ -e "$$task_sbom_root/.git" ]]`,
		`git rev-parse HEAD`,
		`publication materialize --config $(PUBLICATION_CONFIG)`,
		`task_sbom_root="$$task_tree_parent/tree"`,
		`cd "$$task_sbom_root"`,
		`-output $(abspath $(PUBLICATION_SBOM_GENERATED)) .`,
	} {
		if !strings.Contains(sbomTarget, contract) {
			t.Fatalf("SBOM generation does not retain Git-source and detached-tree contract %q", contract)
		}
	}
	conditionalIndex := strings.Index(sbomTarget, `if [[ -e "$$task_sbom_root/.git" ]]`)
	gitHeadIndex := strings.Index(sbomTarget, `git rev-parse HEAD`)
	materializeIndex := strings.Index(sbomTarget, `publication materialize --config $(PUBLICATION_CONFIG)`)
	rootSwitchIndex := strings.Index(sbomTarget, `task_sbom_root="$$task_tree_parent/tree"`)
	conditionalEndOffset := strings.Index(sbomTarget[rootSwitchIndex:], `fi;`)
	directScanIndex := strings.Index(sbomTarget, `cd "$$task_sbom_root"`)
	if conditionalIndex > gitHeadIndex || gitHeadIndex > materializeIndex || materializeIndex > rootSwitchIndex || conditionalEndOffset < 0 || rootSwitchIndex+conditionalEndOffset > directScanIndex {
		t.Fatal("SBOM Git materialization must remain bounded before the detached-tree scan")
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

func TestMakefileSBOMSupportsGitSourceAndMaterializedTree(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	makefilePath, err := filepath.Abs(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("resolve Makefile: %v", err)
	}

	for _, test := range []struct {
		name            string
		gitFile         bool
		wantGit         bool
		wantMaterialize bool
		wantScanMode    string
	}{
		{name: "materialized tree", wantScanMode: "direct"},
		{name: "linked worktree", gitFile: true, wantGit: true, wantMaterialize: true, wantScanMode: "materialized"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.Mkdir(binDir, 0o700); err != nil {
				t.Fatalf("create mock bin: %v", err)
			}
			if test.gitFile {
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: mock\n"), 0o600); err != nil {
					t.Fatalf("write linked-worktree marker: %v", err)
				}
			}

			gitMarker := filepath.Join(root, "git-called")
			materializeMarker := filepath.Join(root, "materialize-called")
			materializeRootMarker := filepath.Join(root, "materialize-root")
			scanModeMarker := filepath.Join(root, "scan-mode")
			writeExecutable(t, binDir, "git", `#!/bin/sh
set -eu
printf 'called\n' > "$YHC_TEST_GIT_MARKER"
[ "$YHC_TEST_ALLOW_GIT" = "1" ] || exit 91
[ "${1-}" = "rev-parse" ] && [ "${2-}" = "HEAD" ] || exit 92
printf '0123456789abcdef0123456789abcdef01234567\n'
`)
			fakeGo := writeExecutable(t, binDir, "go", `#!/bin/sh
set -eu
if [ "${1-}" = "version" ] && [ "${2-}" = "-m" ]; then
	printf '\tmod\tgithub.com/CycloneDX/cyclonedx-gomod\tv1.10.0\n'
	exit 0
fi
[ "${1-}" = "run" ] || exit 93
[ "$YHC_TEST_ALLOW_MATERIALIZE" = "1" ] || exit 94
shift
[ "${1-}" = "./scripts/publication" ] || exit 95
shift
[ "${1-}" = "materialize" ] || exit 96
shift
output=""
source_commit=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--source-commit)
			shift
			source_commit="${1-}"
			;;
		--output)
			shift
			output="${1-}"
			;;
	esac
	shift
done
[ "$source_commit" = "0123456789abcdef0123456789abcdef01234567" ] || exit 97
[ -n "$output" ] || exit 98
mkdir -p "$output"
: > "$output/.materialized"
printf 'called\n' > "$YHC_TEST_MATERIALIZE_MARKER"
printf '%s\n' "$output" > "$YHC_TEST_MATERIALIZE_ROOT_MARKER"
`)
			fakeCycloneDX := writeExecutable(t, binDir, "cyclonedx-gomod", `#!/bin/sh
set -eu
output=""
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-output" ]; then
		shift
		output="${1-}"
		break
	fi
	shift
done
[ -n "$output" ] || exit 96
if [ -f .materialized ]; then
	printf 'materialized\n' > "$YHC_TEST_SCAN_MODE_MARKER"
else
	printf 'direct\n' > "$YHC_TEST_SCAN_MODE_MARKER"
fi
printf '{}\n' > "$output"
`)

			command := exec.Command(makePath, "-f", makefilePath, "sbom", "GO="+fakeGo, "CYCLONEDX_GOMOD="+fakeCycloneDX)
			command.Dir = root
			command.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YHC_TEST_GIT_MARKER="+gitMarker,
				"YHC_TEST_MATERIALIZE_MARKER="+materializeMarker,
				"YHC_TEST_MATERIALIZE_ROOT_MARKER="+materializeRootMarker,
				"YHC_TEST_SCAN_MODE_MARKER="+scanModeMarker,
				"YHC_TEST_ALLOW_GIT="+boolAtom(test.wantGit),
				"YHC_TEST_ALLOW_MATERIALIZE="+boolAtom(test.wantMaterialize),
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("run SBOM target: %v\n%s", err, output)
			}
			assertMarker(t, gitMarker, test.wantGit)
			assertMarker(t, materializeMarker, test.wantMaterialize)
			assertMarker(t, materializeRootMarker, test.wantMaterialize)
			if test.wantMaterialize {
				materializeRoot, err := os.ReadFile(materializeRootMarker)
				if err != nil {
					t.Fatalf("read materialized root: %v", err)
				}
				if _, err := os.Stat(filepath.Dir(strings.TrimSpace(string(materializeRoot)))); !os.IsNotExist(err) {
					t.Fatalf("temporary materialized parent was not removed: %v", err)
				}
			}
			scanMode, err := os.ReadFile(scanModeMarker)
			if err != nil {
				t.Fatalf("read scan mode: %v", err)
			}
			if got := strings.TrimSpace(string(scanMode)); got != test.wantScanMode {
				t.Fatalf("scan mode = %q, want %q", got, test.wantScanMode)
			}
			if info, err := os.Stat(filepath.Join(root, "build", "publication", "sbom.cdx.json")); err != nil || info.Size() == 0 {
				t.Fatalf("generated SBOM missing or empty: %v", err)
			}
		})
	}
}

func writeExecutable(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}

func boolAtom(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func assertMarker(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("expected marker %s: %v", filepath.Base(path), err)
	}
	if !want && err == nil {
		t.Fatalf("unexpected marker %s", filepath.Base(path))
	}
	if !want && err != nil && !os.IsNotExist(err) {
		t.Fatalf("inspect marker %s: %v", filepath.Base(path), err)
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
