//go:build parity

package parity

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-udiff"
)

var (
	yhcBin       = flag.String("yhc-bin", "", "path to YHC binary")
	crushBin     = flag.String("crush-bin", "", "path to crush binary")
	claudeDir    = flag.String("claude-dir", "", "path to claude-code-ripe directory")
	codexBin     = flag.String("codex-bin", "", "path to Codex CLI binary")
	updateGolden = flag.Bool("update", false, "update golden files")
	reportOutput = flag.String("report-output", "", "write parity report to this path")
)

func testdataDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata")
}

func goldenDir() string {
	return filepath.Join(testdataDir(), "golden")
}

func scenariosDir() string {
	return filepath.Join(testdataDir(), "scenarios")
}

func TestYHCIdentityDefaults(t *testing.T) {
	t.Setenv("PARITY_YHC_BIN", "/test/bin/yhc")
	config := DefaultConfigs()[ProjectYHC]
	if config.Project != ProjectYHC || config.Command != "/test/bin/yhc" {
		t.Fatalf("YHC parity config = %#v", config)
	}
	if got := NormalizeBranding("Yet Hooked on Coding YHC yhc"); got != "[AGENT] [AGENT] [AGENT]" {
		t.Fatalf("normalized YHC branding = %q", got)
	}
}

func getYHCConfig(t *testing.T) BinaryConfig {
	t.Helper()
	bin := *yhcBin
	if bin == "" {
		bin = os.Getenv("PARITY_YHC_BIN")
	}
	if bin == "" {
		// Try default build path
		bin = filepath.Join(projectRoot(), "build", "linux-amd64", "yhc")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("YHC binary not found at %s (build with 'make build' or set -yhc-bin)", bin)
	}
	return BinaryConfig{
		Project: ProjectYHC,
		Command: bin,
		Args:    []string{"--yolo"},
		Env: []string{
			"TERM=xterm-256color",
			"YHC_DISABLE_MOUSE=1",
			"PROV=agenticopenai",
			"PROV_API_KEY=parity-test-key",
			"PROV_MODEL=gpt-4o",
		},
	}
}

func getCrushConfig(t *testing.T) BinaryConfig {
	t.Helper()
	bin := *crushBin
	if bin == "" {
		bin = os.Getenv("PARITY_CRUSH_BIN")
	}
	if bin == "" {
		bin = filepath.Join(projectRoot(), ".reference", "crush", "crush")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("crush binary not found at %s (set -crush-bin)", bin)
	}
	return BinaryConfig{
		Project: ProjectCrush,
		Command: bin,
		Env:     []string{"TERM=xterm-256color"},
	}
}

func getClaudeConfig(t *testing.T) BinaryConfig {
	t.Helper()
	dir := *claudeDir
	if dir == "" {
		dir = os.Getenv("PARITY_CLAUDE_DIR")
	}
	if dir == "" {
		dir = filepath.Join(projectRoot(), ".reference", "claude-code-ripe")
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts", "dev-cli.ts")); err != nil {
		t.Skipf("claude-code-ripe not found at %s (set -claude-dir)", dir)
	}
	return BinaryConfig{
		Project: ProjectClaudeCodeRipe,
		Command: "bun",
		Args:    []string{"scripts/dev-cli.ts"},
		WorkDir: dir,
		Env: []string{
			"TERM=xterm-256color",
			"DISABLE_AUTOUPDATER=1",
			"DISABLE_TELEMETRY=1",
		},
	}
}

func getCodexConfig(t *testing.T) BinaryConfig {
	t.Helper()
	return getCodexConfigWithHome(t, t.TempDir())
}

func getCodexConfigWithHome(t *testing.T, codexHome string) BinaryConfig {
	t.Helper()
	bin := *codexBin
	if bin == "" {
		bin = os.Getenv("PARITY_CODEX_BIN")
	}
	if bin == "" {
		var err error
		bin, err = exec.LookPath("codex")
		if err != nil {
			t.Skip("Codex CLI not found (set -codex-bin or PARITY_CODEX_BIN)")
		}
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("Codex CLI not found at %s (set -codex-bin)", bin)
	}
	return BinaryConfig{
		Project: ProjectCodex,
		Command: bin,
		Args: []string{
			"--no-alt-screen",
			"-c", "check_for_update_on_startup=false",
			"-C", projectRoot(),
		},
		Env: []string{
			"TERM=xterm-256color",
			"NO_COLOR=1",
			"CODEX_HOME=" + codexHome,
		},
		UnsetEnv: []string{
			"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL",
		},
	}
}

func projectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "..")
}

// TestYHCWelcomeScreen tests YHC's welcome screen rendering
// against its own golden file.
func TestYHCWelcomeScreen(t *testing.T) {
	cfg := getYHCConfig(t)
	s, err := LoadScenario(filepath.Join(scenariosDir(), "welcome_screen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	captures, err := RunScenario(ctx, s, cfg)
	if err != nil {
		t.Fatalf("scenario failed: %v", err)
	}

	for _, cap := range captures {
		if *updateGolden {
			if err := UpdateGolden(cap, goldenDir()); err != nil {
				t.Fatalf("update golden: %v", err)
			}
			t.Logf("Updated golden: %s/%s", cap.Scenario, cap.CaptureID)
			continue
		}
		ok, diff := CompareToGolden(cap, goldenDir())
		if !ok {
			t.Errorf("golden mismatch %s/%s:\n%s", cap.Scenario, cap.CaptureID, diff)
		}
	}
}

// TestYHCPromptInput tests text input and deletion rendering.
func TestYHCPromptInput(t *testing.T) {
	cfg := getYHCConfig(t)
	s, err := LoadScenario(filepath.Join(scenariosDir(), "prompt_input.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	captures, err := RunScenario(ctx, s, cfg)
	if err != nil {
		t.Fatalf("scenario failed: %v", err)
	}

	for _, cap := range captures {
		if *updateGolden {
			if err := UpdateGolden(cap, goldenDir()); err != nil {
				t.Fatalf("update golden: %v", err)
			}
			t.Logf("Updated golden: %s/%s", cap.Scenario, cap.CaptureID)
			continue
		}
		ok, diff := CompareToGolden(cap, goldenDir())
		if !ok {
			t.Errorf("golden mismatch %s/%s:\n%s", cap.Scenario, cap.CaptureID, diff)
		}
	}
}

// TestWelcomeScreenParity compares welcome screens across all available projects.
func TestWelcomeScreenParity(t *testing.T) {
	configs := make(map[Project]BinaryConfig)

	// Add whatever projects are available
	if cfg := getYHCConfig(t); cfg.Command != "" {
		configs[ProjectYHC] = cfg
	}
	if cfg := getCrushConfig(t); cfg.Command != "" {
		configs[ProjectCrush] = cfg
	}
	if cfg := getClaudeConfig(t); cfg.Command != "" {
		configs[ProjectClaudeCodeRipe] = cfg
	}
	if cfg := getCodexConfig(t); cfg.Command != "" {
		configs[ProjectCodex] = cfg
	}

	if len(configs) < 2 {
		t.Skip("need at least 2 project binaries for parity comparison")
	}

	s, err := LoadScenario(filepath.Join(scenariosDir(), "welcome_screen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, err := RunParityCheck(ctx, s, configs)
	if err != nil {
		t.Fatalf("parity check failed: %v", err)
	}

	for _, r := range results {
		if !r.Match {
			t.Logf("Parity gap %s vs %s at %s/%s:\n%s",
				r.ProjectA, r.ProjectB, r.Scenario, r.CaptureID, r.Diff)
		}
	}
}

// TestGenerateParityReport runs all scenarios and generates a markdown report.
func TestGenerateParityReport(t *testing.T) {
	output := *reportOutput
	if output == "" {
		output = os.Getenv("PARITY_REPORT_OUTPUT")
	}
	if output == "" {
		t.Skip("no -report-output or PARITY_REPORT_OUTPUT set")
	}

	configs := make(map[Project]BinaryConfig)
	configs[ProjectYHC] = getYHCConfig(t)

	// Optionally add other projects
	if cfg := getCrushConfig(t); cfg.Command != "" {
		if _, err := os.Stat(cfg.Command); err == nil {
			configs[ProjectCrush] = cfg
		}
	}
	if cfg := getClaudeConfig(t); cfg.Command != "" {
		if _, err := os.Stat(filepath.Join(cfg.WorkDir, "scripts", "dev-cli.ts")); err == nil {
			configs[ProjectClaudeCodeRipe] = cfg
		}
	}
	if cfg := getCodexConfig(t); cfg.Command != "" {
		configs[ProjectCodex] = cfg
	}

	scenarios, err := LoadScenariosDir(scenariosDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var allResults []ComparisonResult
	for _, s := range scenarios {
		results, err := RunParityCheck(ctx, s, configs)
		if err != nil {
			t.Logf("scenario %s failed: %v", s.Name, err)
			continue
		}
		allResults = append(allResults, results...)
	}

	report := GenerateReport(allResults)
	if err := os.WriteFile(output, []byte(report), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("Report written to %s", output)
}

// TestClaudeCodeParity is the primary parity test: YHC vs claude-code-ripe.
// This runs all scenarios comparing only these two projects.
func TestClaudeCodeParity(t *testing.T) {
	yhcCfg := getYHCConfig(t)
	claudeCfg := getClaudeConfig(t)

	configs := map[Project]BinaryConfig{
		ProjectYHC:            yhcCfg,
		ProjectClaudeCodeRipe: claudeCfg,
	}

	scenarios, err := LoadScenariosDir(scenariosDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var gaps []ComparisonResult
	for _, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			results, err := RunParityCheck(ctx, s, configs)
			if err != nil {
				t.Fatalf("scenario %s failed: %v", s.Name, err)
			}
			for _, r := range results {
				if !r.Match {
					gaps = append(gaps, r)
					t.Logf("GAP [%s/%s]: YHC vs claude-code-ripe\n%s",
						r.Scenario, r.CaptureID, r.Diff)
				}
			}
		})
	}

	if len(gaps) > 0 {
		t.Logf("\n=== PARITY SUMMARY: %d gaps found across %d scenarios ===", len(gaps), len(scenarios))
		for _, g := range gaps {
			t.Logf("  - %s/%s", g.Scenario, g.CaptureID)
		}
	}
}

// TestCodexInvocationDeterministic proves that the reference can be invoked
// without inheriting user auth, update checks, or mutable global Codex state.
// Only the logged-out startup surface is claimed until a deterministic local
// app-server/model fixture is added.
func TestCodexInvocationDeterministic(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join(scenariosDir(), "welcome_screen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	homes := []string{t.TempDir(), t.TempDir()}
	outputs := make([]string, len(homes))
	for index, home := range homes {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		captures, runErr := RunScenario(ctx, scenario, getCodexConfigWithHome(t, home))
		cancel()
		if runErr != nil {
			t.Fatalf("Codex deterministic run %d: %v", index+1, runErr)
		}
		if len(captures) != 1 {
			t.Fatalf("Codex deterministic run %d captures = %d, want 1", index+1, len(captures))
		}
		normalized := NormalizeForCompare(captures[0].Plain)
		normalized = strings.ReplaceAll(normalized, home, "[CODEX_HOME]")
		outputs[index] = normalized
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("isolated Codex startup is not deterministic:\n%s",
			udiff.Unified("codex-run-1", "codex-run-2", outputs[0], outputs[1]))
	}
	if !strings.Contains(strings.ToLower(outputs[0]), "codex") {
		t.Fatalf("isolated Codex startup did not render an identifiable surface:\n%s", outputs[0])
	}
}

func TestCodexParityEnvironmentRemovesInheritedAuth(t *testing.T) {
	cfg := getCodexConfig(t)
	environment := parityEnvironment([]string{
		"PATH=/usr/bin", "OPENAI_API_KEY=real-secret", "CODEX_API_KEY=other-secret",
	}, cfg.Env, cfg.UnsetEnv)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "real-secret") || strings.Contains(joined, "other-secret") {
		t.Fatalf("Codex parity environment retained inherited auth:\n%s", joined)
	}
	if !strings.Contains(joined, "CODEX_HOME=") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("Codex parity environment lost required isolated/base values:\n%s", joined)
	}
	if !SupportsScenario(ProjectCodex, "welcome_screen") || SupportsScenario(ProjectCodex, "prompt_input") {
		t.Fatal("Codex deterministic scenario boundary changed without an explicit fixture")
	}
}

// TestCaptureSingle runs a single scenario against one project and prints the
// screen output. Useful for debugging: go test -tags=parity -run=TestCaptureSingle -v
func TestCaptureSingle(t *testing.T) {
	project := os.Getenv("PARITY_PROJECT")
	scenarioName := os.Getenv("PARITY_SCENARIO")
	if scenarioName == "" {
		scenarioName = "welcome_screen"
	}

	var cfg BinaryConfig
	switch Project(project) {
	case ProjectClaudeCodeRipe:
		cfg = getClaudeConfig(t)
	case ProjectCrush:
		cfg = getCrushConfig(t)
	case ProjectCodex:
		cfg = getCodexConfig(t)
	default:
		cfg = getYHCConfig(t)
	}

	s, err := LoadScenario(filepath.Join(scenariosDir(), scenarioName+".yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	captures, err := RunScenario(ctx, s, cfg)
	if err != nil {
		t.Fatalf("scenario failed: %v", err)
	}

	for _, cap := range captures {
		t.Logf("\n=== %s / %s [%s] (alt=%v) ===\n%s\n===END===",
			cap.Scenario, cap.CaptureID, cap.Project, cap.AltScreen, cap.Plain)
	}
}
