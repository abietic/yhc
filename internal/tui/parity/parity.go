//go:build parity

package parity

import "time"

// Project identifies which TUI binary is being tested.
type Project string

const (
	ProjectYHC            Project = "yhc"
	ProjectCrush          Project = "crush"
	ProjectClaudeCodeRipe Project = "claude-code-ripe"
	ProjectCodex          Project = "codex"
)

// BinaryConfig defines how to invoke a project's TUI binary.
type BinaryConfig struct {
	Project  Project
	Command  string
	Args     []string
	Env      []string
	UnsetEnv []string // inherited variables removed before Env overrides
	WorkDir  string
	BuildCmd []string // optional: command to build the binary first
}

// SupportsScenario reports the deterministic interaction surface currently
// claimed by each reference invocation. Codex runs with isolated, logged-out
// state, so only its startup surface is comparable without network/model I/O.
func SupportsScenario(project Project, scenario string) bool {
	return project != ProjectCodex || scenario == "welcome_screen"
}

// Scenario defines a TUI interaction sequence to test.
type Scenario struct {
	Name       string        `yaml:"name"`
	Width      int           `yaml:"width"`
	Height     int           `yaml:"height"`
	StableWait time.Duration `yaml:"stable_wait"`
	Steps      []Step        `yaml:"steps"`
}

// Step is a single interaction in a scenario.
type Step struct {
	Input   string        `yaml:"input,omitempty"`
	Key     string        `yaml:"key,omitempty"`
	Delay   time.Duration `yaml:"delay,omitempty"`
	WaitFor string        `yaml:"wait_for,omitempty"`
	Timeout time.Duration `yaml:"timeout,omitempty"`
	Capture string        `yaml:"capture,omitempty"`
}

// Capture holds a screen state snapshot at a point in the scenario.
type Capture struct {
	Project   Project
	Scenario  string
	CaptureID string
	Raw       string
	Plain     string
	Timestamp time.Time
	AltScreen bool
}

// ComparisonResult holds the diff between two project captures.
type ComparisonResult struct {
	Scenario  string
	CaptureID string
	ProjectA  Project
	ProjectB  Project
	Match     bool
	Diff      string
}
