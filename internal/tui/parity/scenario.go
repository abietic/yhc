//go:build parity

package parity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadScenario loads a scenario definition from a YAML file.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}

	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse scenario %s: %w", path, err)
	}

	if s.Width == 0 {
		s.Width = 100
	}
	if s.Height == 0 {
		s.Height = 30
	}
	if s.StableWait == 0 {
		s.StableWait = 500 * time.Millisecond
	}

	return &s, nil
}

// LoadScenariosDir loads all .yaml scenario files from a directory.
func LoadScenariosDir(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var scenarios []*Scenario
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		s, err := LoadScenario(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, s)
	}
	return scenarios, nil
}

// RunScenario executes a scenario against a single project binary.
func RunScenario(ctx context.Context, s *Scenario, cfg BinaryConfig) ([]*Capture, error) {
	driver := NewDriver(cfg, s.Width, s.Height)

	if err := driver.Start(ctx); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Project, err)
	}
	defer driver.Stop()

	var captures []*Capture

	for _, step := range s.Steps {
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}

		if step.WaitFor != "" {
			timeout := step.Timeout
			if timeout == 0 {
				timeout = 10 * time.Second
			}
			if err := driver.WaitForPattern(step.WaitFor, timeout); err != nil {
				return captures, fmt.Errorf("step wait_for %q: %w", step.WaitFor, err)
			}
		}

		if step.Input != "" {
			if err := driver.SendText(step.Input); err != nil {
				return captures, fmt.Errorf("step input %q: %w", step.Input, err)
			}
		}

		if step.Key != "" {
			if err := driver.SendKey(step.Key); err != nil {
				return captures, fmt.Errorf("step key %q: %w", step.Key, err)
			}
		}

		if step.Capture != "" {
			// Wait for screen to stabilize before capturing
			if err := driver.WaitForStable(5*time.Second, s.StableWait); err != nil {
				// Non-fatal: capture anyway with whatever is on screen
			}
			cap := driver.CaptureScreen(s.Name, step.Capture, cfg.Project)
			captures = append(captures, cap)
		}
	}

	return captures, nil
}

// RunParityCheck executes a scenario against all configured projects
// and returns pairwise comparison results.
func RunParityCheck(ctx context.Context, s *Scenario, configs map[Project]BinaryConfig) ([]ComparisonResult, error) {
	allCaptures := make(map[string]map[Project]*Capture) // captureID → project → capture

	for project, cfg := range configs {
		if !SupportsScenario(project, s.Name) {
			continue
		}
		captures, err := RunScenario(ctx, s, cfg)
		if err != nil {
			return nil, fmt.Errorf("run scenario %q on %s: %w", s.Name, project, err)
		}
		for _, cap := range captures {
			if allCaptures[cap.CaptureID] == nil {
				allCaptures[cap.CaptureID] = make(map[Project]*Capture)
			}
			allCaptures[cap.CaptureID][project] = cap
		}
	}

	var results []ComparisonResult
	captureIDs := make([]string, 0, len(allCaptures))
	for captureID := range allCaptures {
		captureIDs = append(captureIDs, captureID)
	}
	sort.Strings(captureIDs)
	for _, captureID := range captureIDs {
		results = append(results, ComparePairwise(allCaptures[captureID])...)
	}
	return results, nil
}

// DefaultConfigs returns BinaryConfig for all four projects using defaults.
// Override paths via environment variables:
//   - PARITY_YHC_BIN: path to YHC binary
//   - PARITY_CRUSH_BIN: path to crush binary
//   - PARITY_CLAUDE_DIR: path to claude-code-ripe directory
//   - PARITY_CODEX_BIN: path to Codex CLI binary
func DefaultConfigs() map[Project]BinaryConfig {
	yhcBin := envOr("PARITY_YHC_BIN", "./build/linux-amd64/yhc")
	crushBin := envOr("PARITY_CRUSH_BIN", filepath.Join(".reference", "crush", "crush"))
	claudeDir := envOr("PARITY_CLAUDE_DIR", filepath.Join(".reference", "claude-code-ripe"))
	codexBin := envOr("PARITY_CODEX_BIN", "codex")
	codexHome := envOr("PARITY_CODEX_HOME", filepath.Join(os.TempDir(), fmt.Sprintf("yhc-codex-parity-%d", os.Getpid())))
	_ = os.MkdirAll(codexHome, 0o700)

	return map[Project]BinaryConfig{
		ProjectYHC: {
			Project: ProjectYHC,
			Command: yhcBin,
			Args:    []string{"--model", "mock", "--yolo"},
			Env:     []string{"TERM=xterm-256color"},
		},
		ProjectCrush: {
			Project: ProjectCrush,
			Command: crushBin,
			Args:    []string{},
			Env:     []string{"TERM=xterm-256color"},
		},
		ProjectClaudeCodeRipe: {
			Project: ProjectClaudeCodeRipe,
			Command: "bun",
			Args:    []string{"scripts/dev-cli.ts"},
			WorkDir: claudeDir,
			Env: []string{
				"TERM=xterm-256color",
				"DISABLE_AUTOUPDATER=1",
				"DISABLE_TELEMETRY=1",
			},
		},
		ProjectCodex: {
			Project: ProjectCodex,
			Command: codexBin,
			Args: []string{
				"--no-alt-screen", "-c", "check_for_update_on_startup=false",
			},
			Env:      []string{"TERM=xterm-256color", "NO_COLOR=1", "CODEX_HOME=" + codexHome},
			UnsetEnv: []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
