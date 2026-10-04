package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

// IndependentVerificationConfig is an opt-in, invocation-local completion gate.
// Zero MaxTurns disables it. Repairs and verifier calls share the root RunUsage.
// This is model-directed checking, not an authoritative task grader or sandbox.
type IndependentVerificationConfig struct {
	MaxTurns   int
	MaxRepairs int
}

// IndependentVerificationSummary is the bounded outward result. Detailed
// commands and tool output stay in the private session attachment.
type IndependentVerificationSummary struct {
	Attempt    int    `json:"attempt"`
	Verdict    string `json:"verdict"`
	Checks     int    `json:"checks"`
	Failed     int    `json:"failed"`
	Unverified int    `json:"unverified"`
}

var ErrIndependentVerification = errors.New("independent verification did not establish completion")

type independentVerificationCheck struct {
	Requirement string `json:"requirement"`
	ToolCallID  string `json:"tool_call_id"`
	Command     string `json:"command"`
	Expected    string `json:"expected"`
	Observed    string `json:"observed"`
	Status      string `json:"status"`
}

type independentVerificationReport struct {
	Verdict          string                         `json:"verdict"`
	CoverageComplete bool                           `json:"coverage_complete"`
	Checks           []independentVerificationCheck `json:"checks"`
	Missing          []string                       `json:"missing,omitempty"`
	Evidence         map[string]string              `json:"tool_evidence,omitempty"`
}

type independentVerificationGate struct {
	params       QueryParams
	requirements string
	repairs      int
	passed       bool
}

const independentVerificationPrompt = `You independently verify the ORIGINAL user requirements against the current workspace. Do not use the solver's claimed success, altered tests, or comments as the oracle. Inspect current artifacts and derive expected behavior from the requirements. Exercise relevant non-default settings and controlled blocked/intermediate concurrency states, not only final state. Do not inspect hidden benchmark graders or oracle solutions.
Do not modify project source, tests, configuration, or documentation, install dependencies, or run git write operations. Ordinary test/build artifacts and temporary scripts are allowed. Bash inherits existing permissions and containment; this instruction is not an OS sandbox.
Use the finite shared budget. Missing coverage, a blocked check, or uncertain expectations means PARTIAL, never PASS. A demonstrated mismatch means FAIL. Do not rewrite expectations to fit implementation.
Your FINAL response must be one JSON object, without markdown or other text: {"verdict":"PASS|FAIL|PARTIAL","coverage_complete":true|false,"checks":[{"requirement":"original requirement","tool_call_id":"actual Bash call id","command":"exact Bash command executed","expected":"contract-derived expectation","observed":"actual evidence","status":"PASS|FAIL|UNVERIFIED"}],"missing":["unverified requirements or missing context"]}. PASS requires complete coverage, no missing requirements, and at least one executable check. Every PASS/FAIL check must reference an actual Bash tool result from this verification invocation. Earlier tool calls or solver tests alone are not evidence. Tool errors are not successful checks.`

// Validate rejects unbounded automatic checking before provider dispatch.
func (cfg IndependentVerificationConfig) Validate(limits execution.RunUsageLimits) error {
	if cfg.MaxTurns < 0 || cfg.MaxTurns > 32 || cfg.MaxRepairs < 0 || cfg.MaxRepairs > 3 || (cfg.MaxTurns == 0 && cfg.MaxRepairs != 0) {
		return fmt.Errorf("independent verification requires 1..32 turns and 0..3 repairs, or both zero to disable")
	}
	if cfg.MaxTurns > 0 && limits.MaxProviderCalls <= 0 {
		return fmt.Errorf("independent verification requires a finite shared max-provider-calls limit")
	}
	return nil
}

func prepareIndependentVerification(params *QueryParams) error {
	cfg := params.IndependentVerification
	limits := execution.RunUsageLimits{}
	if params.RunUsage != nil {
		limits = params.RunUsage.Snapshot().Limits
	}
	if err := cfg.Validate(limits); err != nil {
		return err
	}
	if cfg.MaxTurns == 0 {
		return nil
	}
	if len(params.JSONSchema) > 0 {
		return fmt.Errorf("independent verification does not support synthetic structured-output completion")
	}
	if params.Deps.ProviderUsage == nil {
		params.Deps.ProviderUsage = params.RunUsage
	}
	// A paused Graph invocation needs a durable gate cursor; it is not silently
	// treated as a fresh, independently verified invocation after restart.
	if params.RuntimePermissionDecision != nil {
		return fmt.Errorf("independent verification cannot resume an interrupted Graph invocation")
	}
	var requests []string
	for _, m := range params.Messages {
		if m == nil || m.Role != schema.User || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if len(m.UserInputMultiContent) > 0 {
			return fmt.Errorf("independent verification currently supports text-only requirements")
		}
		if meta, _ := m.Extra["is_meta"].(bool); meta {
			continue
		}
		requests = append(requests, m.Content)
	}
	requirements := strings.Join(requests, "\n\n--- USER REQUEST ---\n\n")
	if requirements == "" || len(requirements) > 128*1024 {
		return fmt.Errorf("independent verification requires nonempty original text requirements of at most 128 KiB")
	}
	frozen := *params
	frozen.IndependentVerification = cfg
	frozen.independentVerification = nil
	params.independentVerification = &independentVerificationGate{params: frozen, requirements: requirements}
	return nil
}

func parseIndependentVerificationReport(text string) (independentVerificationReport, error) {
	var report independentVerificationReport
	if len(text) > 64*1024 {
		return report, fmt.Errorf("verification report exceeds 64 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return report, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return report, fmt.Errorf("verification report must contain exactly one JSON object")
	}
	if report.Evidence != nil {
		return report, fmt.Errorf("tool evidence is runtime-owned")
	}
	if report.Verdict != "PASS" && report.Verdict != "FAIL" && report.Verdict != "PARTIAL" {
		return report, fmt.Errorf("invalid verification verdict")
	}
	if len(report.Checks) > 128 || len(report.Missing) > 128 {
		return report, fmt.Errorf("too many verification checks")
	}
	failed := false
	for _, check := range report.Checks {
		if strings.TrimSpace(check.Requirement) == "" || strings.TrimSpace(check.Expected) == "" || strings.TrimSpace(check.Observed) == "" {
			return report, fmt.Errorf("verification check missing contract or evidence")
		}
		if check.Status != "PASS" && check.Status != "FAIL" && check.Status != "UNVERIFIED" {
			return report, fmt.Errorf("invalid check status")
		}
		if check.Status != "UNVERIFIED" && (strings.TrimSpace(check.ToolCallID) == "" || strings.TrimSpace(check.Command) == "") {
			return report, fmt.Errorf("executable check missing tool identity")
		}
		failed = failed || check.Status == "FAIL"
		if report.Verdict == "PASS" && check.Status != "PASS" {
			return report, fmt.Errorf("PASS contradicts a check")
		}
	}
	if report.Verdict == "PASS" && (!report.CoverageComplete || len(report.Checks) == 0 || len(report.Missing) != 0) {
		return report, fmt.Errorf("PASS requires complete executable coverage")
	}
	if report.Verdict == "FAIL" && !failed {
		return report, fmt.Errorf("FAIL requires a counterexample")
	}
	return report, nil
}

func verificationToolAllowed(name string) bool {
	return name == "Read" || name == "Glob" || name == "Grep" || name == "Bash"
}

func (g *independentVerificationGate) check(ctx context.Context) (independentVerificationReport, error) {
	// Construct a narrow fresh invocation rather than copying root history,
	// structured-output policy, queue/checkpoint owners, or completion hooks.
	p := QueryParams{
		RunUsage:               g.params.RunUsage,
		QuerySource:            QuerySource("independent_verification"),
		Messages:               []*schema.Message{{Role: schema.User, Content: g.requirements}},
		SystemPrompt:           &schema.Message{Role: schema.System, Content: independentVerificationPrompt},
		SessionID:              g.params.SessionID,
		ChatModel:              g.params.ChatModel,
		modelCall:              g.params.modelCall,
		modelResolver:          g.params.modelResolver,
		modelDispatchGuard:     g.params.modelDispatchGuard,
		modelCompactionGuard:   g.params.modelCompactionGuard,
		promptRouteGuard:       g.params.promptRouteGuard,
		commandEntrypoint:      g.params.commandEntrypoint,
		SkipCacheWrite:         g.params.SkipCacheWrite,
		ToolRegistry:           g.params.ToolRegistry,
		ToolExecutor:           g.params.ToolExecutor,
		CanUseTool:             g.params.CanUseTool,
		RepeatedToolCallPrompt: g.params.RepeatedToolCallPrompt,
		CancelToolInteraction:  g.params.CancelToolInteraction,
		HookExecutor:           hooks.NewExecutor(),
		ResultStorage:          g.params.ResultStorage,
		Deps:                   &QueryDeps{UUID: g.params.Deps.UUID, CallModel: g.params.Deps.CallModel, ProviderUsage: g.params.Deps.ProviderUsage},
	}
	// Tool hooks retain their parent owner and policy; completion hooks are
	// deliberately not re-run inside the checker.
	if parent := g.params.HookExecutor; parent != nil {
		p.HookExecutor.RegisterPreTool(parent.ExecutePreTool)
		p.HookExecutor.RegisterPostTool(parent.ExecutePostTool)
		p.HookExecutor.RegisterPostToolFailure(parent.ExecutePostToolFailure)
		p.HookExecutor.RegisterPermissionDenied(parent.ExecutePermissionDenied)
	}
	maxTurns := g.params.IndependentVerification.MaxTurns
	p.MaxTurns = &maxTurns
	p.ToolUseContext = clonePermissionReviewToolContext(g.params.ToolUseContext)
	if p.ToolUseContext == nil {
		p.ToolUseContext = &ToolUseContext{}
	}
	p.ToolUseContext.ReadFileState = NewFileStateCache()
	if p.ToolUseContext.Options != nil {
		var selected []*schema.ToolInfo
		for _, tool := range p.ToolUseContext.Options.Tools {
			if tool != nil && verificationToolAllowed(tool.Name) {
				selected = append(selected, tool)
			}
		}
		p.ToolUseContext.Options.Tools = selected
		p.ToolUseContext.Options.RefreshTools = nil
		p.ToolUseContext.Options.ToolChoice = ""
		p.ToolUseContext.Options.ForcedToolName = ""
		p.ToolUseContext.Options.AppendSystemPrompt = ""
	}
	parentCanUse := p.CanUseTool
	p.CanUseTool = func(ctx context.Context, name string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
		if !verificationToolAllowed(name) {
			return false, "tool excluded from independent verification"
		}
		if parentCanUse != nil {
			return parentCanUse(ctx, name, input, toolCtx)
		}
		return true, ""
	}
	var evidenceMu sync.Mutex
	commands := map[string]string{}
	results := map[string]*schema.Message{}
	parentExecutor := p.ToolExecutor
	p.ToolExecutor = func(ctx context.Context, name, input string) (string, error) {
		if !verificationToolAllowed(name) || parentExecutor == nil {
			return "", fmt.Errorf("verification tool unavailable: %s", name)
		}
		output, err := parentExecutor(ctx, name, input)
		if name == "Bash" && err == nil {
			var args struct {
				Command    string `json:"command"`
				Background bool   `json:"run_in_background"`
			}
			if json.Unmarshal([]byte(input), &args) == nil && !args.Background {
				evidenceMu.Lock()
				commands[tools.ToolUseIDFromCtx(ctx)] = args.Command
				evidenceMu.Unlock()
			}
		}
		return output, err
	}
	var final strings.Builder
	terminal := Query(ctx, p, func(e QueryEvent) {
		if e.Type == EventStreamRequestStart {
			final.Reset()
		}
		if e.Type == EventAssistant {
			m := e.AssistantMessage
			if m == nil {
				m = e.Message
			}
			if m == nil {
				return
			}
			final.WriteString(m.Content)
		}
		if e.Type == EventToolResult {
			m := e.ToolResultMessage
			if m == nil {
				m = e.Message
			}
			if m != nil {
				evidenceMu.Lock()
				results[m.ToolCallID] = m
				evidenceMu.Unlock()
			}
		}
	})
	if err := ctx.Err(); err != nil {
		return independentVerificationReport{}, err
	}
	if terminal.Err != nil {
		return independentVerificationReport{}, terminal.Err
	}
	if terminal.Reason != TerminalCompleted {
		return independentVerificationReport{}, fmt.Errorf("verifier stopped: %s", terminal.Reason)
	}
	report, err := parseIndependentVerificationReport(final.String())
	if err != nil {
		return report, err
	}
	evidenceMu.Lock()
	defer evidenceMu.Unlock()
	report.Evidence = make(map[string]string)
	evidenceBytes := 0
	for _, check := range report.Checks {
		if check.Status == "UNVERIFIED" {
			continue
		}
		result := results[check.ToolCallID]
		if result == nil || commands[check.ToolCallID] != check.Command {
			return report, fmt.Errorf("check references an unexecuted command")
		}
		if isError, _ := result.Extra["is_error"].(bool); isError {
			return report, fmt.Errorf("check references a failed tool invocation")
		}
		output := result.Content
		if len(output) > 16384 {
			output = output[:16384] + " [truncated]"
		}
		if evidenceBytes+len(output) <= 65536 {
			report.Evidence[check.ToolCallID] = output
			evidenceBytes += len(output)
		}
	}
	return report, nil
}
