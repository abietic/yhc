package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TeamRole defines the role of an agent within a team.
type TeamRole string

const (
	TeamRoleImplementer TeamRole = "implementer"
	TeamRoleReviewer    TeamRole = "reviewer"
	TeamRoleTester      TeamRole = "tester"
	TeamRoleArchitect   TeamRole = "architect"
	TeamRoleCoordinator TeamRole = "coordinator"
	TeamRoleCustom      TeamRole = "custom"
)

// TeamExecutionMode determines how team members execute.
type TeamExecutionMode string

const (
	TeamExecSequential TeamExecutionMode = "sequential"
	TeamExecParallel   TeamExecutionMode = "parallel"
)

// TeamMemberConfig describes a member to be executed as part of a team run.
type TeamMemberConfig struct {
	Role         TeamRole
	Name         string
	Task         string
	Model        string
	MaxTurns     int
	AllowedTools []string
	DependsOn    []string // Names of members that must complete before this one starts.
}

// TeamRunConfig holds the parameters for a team run.
type TeamRunConfig struct {
	TeamID        string
	Goal          string
	Members       []TeamMemberConfig
	Mode          TeamExecutionMode
	MaxDuration   time.Duration
	StopOnFailure bool // If true, stop remaining members when one fails.
}

// TeamMemberResult holds the outcome of a single team member's execution.
type TeamMemberResult struct {
	Name      string
	Role      TeamRole
	AgentID   string
	Status    string // "completed", "failed", "skipped", "cancelled"
	Result    string
	Error     error
	Duration  time.Duration
	StartedAt time.Time
}

// TeamRunResult holds the aggregate outcome of a team run.
type TeamRunResult struct {
	TeamID    string
	Goal      string
	Status    string // "completed", "partial", "failed", "cancelled"
	Members   []TeamMemberResult
	Duration  time.Duration
	StartedAt time.Time
}

// TeamContext provides shared state across team members. Members can post
// findings and read findings from other members for coordination.
type TeamContext struct {
	mu       sync.RWMutex
	teamID   string
	goal     string
	findings map[string][]string // member name -> findings
	shared   map[string]any      // shared key-value store
}

// NewTeamContext creates a new shared context for a team run.
func NewTeamContext(teamID, goal string) *TeamContext {
	return &TeamContext{
		teamID:   teamID,
		goal:     goal,
		findings: make(map[string][]string),
		shared:   make(map[string]any),
	}
}

// TeamID returns the team identifier.
func (tc *TeamContext) TeamID() string {
	return tc.teamID
}

// Goal returns the team goal.
func (tc *TeamContext) Goal() string {
	return tc.goal
}

// PostFinding records a finding from a team member.
func (tc *TeamContext) PostFinding(memberName, finding string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.findings[memberName] = append(tc.findings[memberName], finding)
}

// GetFindings returns all findings posted by a specific member.
func (tc *TeamContext) GetFindings(memberName string) []string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	findings := tc.findings[memberName]
	if len(findings) == 0 {
		return nil
	}
	out := make([]string, len(findings))
	copy(out, findings)
	return out
}

// GetAllFindings returns all findings from all members.
func (tc *TeamContext) GetAllFindings() map[string][]string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	out := make(map[string][]string, len(tc.findings))
	for name, findings := range tc.findings {
		cp := make([]string, len(findings))
		copy(cp, findings)
		out[name] = cp
	}
	return out
}

// SetShared stores a key-value pair accessible to all team members.
func (tc *TeamContext) SetShared(key string, value any) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.shared[key] = value
}

// GetShared retrieves a shared value by key.
func (tc *TeamContext) GetShared(key string) (any, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	v, ok := tc.shared[key]
	return v, ok
}

// Summary returns a formatted summary of all team findings for injection
// into subsequent member prompts.
func (tc *TeamContext) Summary() string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	if len(tc.findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Team Context\n\n")
	fmt.Fprintf(&sb, "Goal: %s\n\n", tc.goal)
	for name, findings := range tc.findings {
		fmt.Fprintf(&sb, "### Findings from %s:\n", name)
		for _, f := range findings {
			fmt.Fprintf(&sb, "- %s\n", f)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// TeamRunner coordinates multiple sub-agents working on related tasks.
// It manages role assignment, shared context, and execution ordering.
type TeamRunner struct {
	agentRunner *AgentRunner
	mu          sync.RWMutex
	activeRuns  map[string]*teamRunState
}

type teamRunState struct {
	config  TeamRunConfig
	context *TeamContext
	cancel  context.CancelFunc
	done    chan struct{}
	result  *TeamRunResult
}

// NewTeamRunner creates a TeamRunner backed by the given AgentRunner.
func NewTeamRunner(runner *AgentRunner) *TeamRunner {
	return &TeamRunner{
		agentRunner: runner,
		activeRuns:  make(map[string]*teamRunState),
	}
}

// RunTeam executes a team of agents according to the given configuration.
// It blocks until all members complete, the context is cancelled, or
// MaxDuration elapses. Member failures are handled gracefully: one agent
// failing does not crash the team unless StopOnFailure is set.
func (tr *TeamRunner) RunTeam(ctx context.Context, config TeamRunConfig) (*TeamRunResult, error) {
	if tr == nil || tr.agentRunner == nil {
		return nil, fmt.Errorf("team_runner: runner not configured")
	}
	if len(config.Members) == 0 {
		return nil, fmt.Errorf("team_runner: at least one team member is required")
	}

	teamCtx := NewTeamContext(config.TeamID, config.Goal)

	// Apply max duration if set.
	var cancel context.CancelFunc
	if config.MaxDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, config.MaxDuration)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}

	state := &teamRunState{
		config:  config,
		context: teamCtx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	tr.mu.Lock()
	tr.activeRuns[config.TeamID] = state
	tr.mu.Unlock()

	startedAt := time.Now()
	var results []TeamMemberResult

	switch config.Mode {
	case TeamExecParallel:
		results = tr.runParallel(ctx, config, teamCtx)
	default:
		// Default to sequential
		results = tr.runSequential(ctx, config, teamCtx)
	}

	// Check if the context was externally cancelled BEFORE we call our own cancel.
	externalCancel := ctx.Err() != nil

	cancel()

	// Determine overall status.
	status := "completed"
	failCount := 0
	cancelCount := 0
	for _, r := range results {
		if r.Status == "failed" {
			failCount++
		}
		if r.Status == "cancelled" {
			cancelCount++
		}
	}
	if externalCancel && cancelCount > 0 {
		status = "cancelled"
	} else if failCount == len(results) {
		status = "failed"
	} else if failCount > 0 {
		status = "partial"
	}

	teamResult := &TeamRunResult{
		TeamID:    config.TeamID,
		Goal:      config.Goal,
		Status:    status,
		Members:   results,
		Duration:  time.Since(startedAt),
		StartedAt: startedAt,
	}

	state.result = teamResult
	close(state.done)

	tr.mu.Lock()
	delete(tr.activeRuns, config.TeamID)
	tr.mu.Unlock()

	return teamResult, nil
}

// GetTeamContext returns the shared context for an active team run.
func (tr *TeamRunner) GetTeamContext(teamID string) (*TeamContext, bool) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	state, ok := tr.activeRuns[teamID]
	if !ok {
		return nil, false
	}
	return state.context, true
}

// CancelTeam cancels an active team run.
func (tr *TeamRunner) CancelTeam(teamID string) error {
	tr.mu.RLock()
	state, ok := tr.activeRuns[teamID]
	tr.mu.RUnlock()
	if !ok {
		return fmt.Errorf("team_runner: team %q not found or already completed", teamID)
	}
	state.cancel()
	return nil
}

func (tr *TeamRunner) runSequential(ctx context.Context, config TeamRunConfig, teamCtx *TeamContext) []TeamMemberResult {
	results := make([]TeamMemberResult, 0, len(config.Members))
	completed := make(map[string]bool)

	for _, member := range config.Members {
		// Check context before starting each member.
		if ctx.Err() != nil {
			results = append(results, TeamMemberResult{
				Name:   member.Name,
				Role:   member.Role,
				Status: "cancelled",
			})
			continue
		}

		// Check dependencies.
		if !tr.dependenciesMet(member.DependsOn, completed) {
			results = append(results, TeamMemberResult{
				Name:   member.Name,
				Role:   member.Role,
				Status: "skipped",
				Error:  fmt.Errorf("dependencies not met: %v", member.DependsOn),
			})
			continue
		}

		result := tr.executeMember(ctx, member, teamCtx)
		results = append(results, result)

		if result.Status == "completed" {
			completed[member.Name] = true
		}

		// If StopOnFailure and this member failed, cancel remaining.
		if config.StopOnFailure && result.Status == "failed" {
			// Mark remaining as cancelled.
			for i := len(results); i < len(config.Members); i++ {
				results = append(results, TeamMemberResult{
					Name:   config.Members[i].Name,
					Role:   config.Members[i].Role,
					Status: "cancelled",
					Error:  fmt.Errorf("stopped due to failure of %q", member.Name),
				})
			}
			break
		}
	}
	return results
}

func (tr *TeamRunner) runParallel(ctx context.Context, config TeamRunConfig, teamCtx *TeamContext) []TeamMemberResult {
	results := make([]TeamMemberResult, len(config.Members))
	var wg sync.WaitGroup

	// For StopOnFailure, we use a shared cancel.
	var parallelCancel context.CancelFunc
	parallelCtx := ctx
	if config.StopOnFailure {
		parallelCtx, parallelCancel = context.WithCancel(ctx)
		defer parallelCancel()
	}

	for i, member := range config.Members {
		wg.Add(1)
		go func(idx int, m TeamMemberConfig) {
			defer wg.Done()

			// Wait for dependencies in parallel mode.
			if len(m.DependsOn) > 0 {
				// In parallel mode, dependencies are best-effort: we wait briefly
				// but don't block indefinitely. This is a simplification.
				results[idx] = TeamMemberResult{
					Name:   m.Name,
					Role:   m.Role,
					Status: "skipped",
					Error:  fmt.Errorf("parallel mode does not support DependsOn; use sequential mode for ordered execution"),
				}
				return
			}

			result := tr.executeMember(parallelCtx, m, teamCtx)
			results[idx] = result

			if config.StopOnFailure && result.Status == "failed" && parallelCancel != nil {
				parallelCancel()
			}
		}(i, member)
	}

	wg.Wait()
	return results
}

func (tr *TeamRunner) executeMember(ctx context.Context, member TeamMemberConfig, teamCtx *TeamContext) TeamMemberResult {
	startedAt := time.Now()

	// Build the task prompt with team context injected.
	taskPrompt := tr.buildMemberPrompt(member, teamCtx)

	opts := AgentExecOptions{
		Task:         taskPrompt,
		Description:  fmt.Sprintf("[%s] %s", member.Role, member.Name),
		Name:         member.Name,
		Model:        member.Model,
		MaxTurns:     member.MaxTurns,
		AllowedTools: member.AllowedTools,
		TeamName:     teamCtx.TeamID(),
	}

	result, err := RunAgent(ctx, tr.agentRunner, opts)
	duration := time.Since(startedAt)

	if err != nil {
		// Check if this was a context cancellation (team cancelled).
		if ctx.Err() != nil {
			return TeamMemberResult{
				Name:      member.Name,
				Role:      member.Role,
				Status:    "cancelled",
				Error:     ctx.Err(),
				Duration:  duration,
				StartedAt: startedAt,
			}
		}
		return TeamMemberResult{
			Name:      member.Name,
			Role:      member.Role,
			Status:    "failed",
			Error:     err,
			Duration:  duration,
			StartedAt: startedAt,
		}
	}

	// Post the result as a finding for subsequent members.
	if result != nil && result.Result != "" {
		teamCtx.PostFinding(member.Name, result.Result)
	}

	agentID := ""
	var resultText string
	if result != nil {
		agentID = opts.AgentID
		resultText = result.Result
	}

	return TeamMemberResult{
		Name:      member.Name,
		Role:      member.Role,
		AgentID:   agentID,
		Status:    "completed",
		Result:    resultText,
		Duration:  duration,
		StartedAt: startedAt,
	}
}

func (tr *TeamRunner) buildMemberPrompt(member TeamMemberConfig, teamCtx *TeamContext) string {
	var sb strings.Builder

	// Role header.
	fmt.Fprintf(&sb, "You are acting as a %s in a team working toward this goal: %s\n\n", member.Role, teamCtx.Goal())

	// Include previous findings if available.
	summary := teamCtx.Summary()
	if summary != "" {
		sb.WriteString(summary)
		sb.WriteString("\n")
	}

	// The actual task.
	sb.WriteString("## Your Task\n\n")
	sb.WriteString(member.Task)

	return sb.String()
}

func (tr *TeamRunner) dependenciesMet(deps []string, completed map[string]bool) bool {
	for _, dep := range deps {
		if !completed[dep] {
			return false
		}
	}
	return true
}
