package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// BackgroundProcess represents a background process tracked by the monitor.
type BackgroundProcess struct {
	ID        string
	Command   string
	StartTime time.Time
	Output    []string
	Done      bool
	ExitCode  int
}

var (
	backgroundProcesses = make(map[string]*BackgroundProcess)
	bgMu                sync.RWMutex
)

// RegisterBackgroundProcess registers a background process for monitoring.
func RegisterBackgroundProcess(p *BackgroundProcess) {
	if p == nil || p.ID == "" {
		return
	}
	bgMu.Lock()
	defer bgMu.Unlock()
	backgroundProcesses[p.ID] = p
}

// GetBackgroundProcess returns a background process by ID.
func GetBackgroundProcess(id string) *BackgroundProcess {
	bgMu.RLock()
	defer bgMu.RUnlock()
	return backgroundProcesses[id]
}

// AppendProcessOutput appends an output line to a background process.
func AppendProcessOutput(id, line string) {
	bgMu.Lock()
	defer bgMu.Unlock()
	if p, ok := backgroundProcesses[id]; ok {
		p.Output = append(p.Output, line)
	}
}

// MonitorTool creates a tool that checks the status and output of background processes.
func MonitorTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Monitor",
			Desc: "Checks the status and recent output of background processes. Use to monitor long-running commands started with run_in_background.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"process_id": {Type: schema.String, Desc: "The ID of the background process to monitor", Required: true},
				"tail_lines": {Type: schema.Integer, Desc: "Number of recent output lines to return (default: 20)"},
			}),
		},
		Execute:           executeMonitor,
		IsConcurrencySafe: func(input map[string]any) bool { return true },
	}
}

func executeMonitor(input string) (string, error) {
	var params struct {
		ProcessID string `json:"process_id"`
		TailLines *int   `json:"tail_lines"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("monitor: invalid params: %w", err)
	}
	if params.ProcessID == "" {
		return "", fmt.Errorf("monitor: process_id is required")
	}

	tailLines := 20
	if params.TailLines != nil && *params.TailLines > 0 {
		tailLines = *params.TailLines
	}

	p := GetBackgroundProcess(params.ProcessID)
	if p == nil {
		return fmt.Sprintf("Process %s not found. No background process with this ID exists.", params.ProcessID), nil
	}

	status := "running"
	if p.Done {
		status = "completed"
	}

	var result strings.Builder
	fmt.Fprintf(&result, "Process: %s\n", p.ID)
	fmt.Fprintf(&result, "Command: %s\n", p.Command)
	fmt.Fprintf(&result, "Status: %s\n", status)
	fmt.Fprintf(&result, "Started: %s\n", p.StartTime.Format(time.RFC3339))
	if p.Done {
		fmt.Fprintf(&result, "Exit Code: %d\n", p.ExitCode)
	} else {
		fmt.Fprintf(&result, "Running for: %s\n", time.Since(p.StartTime).Truncate(time.Second))
	}

	bgMu.RLock()
	output := p.Output
	bgMu.RUnlock()

	if len(output) > 0 {
		start := 0
		if len(output) > tailLines {
			start = len(output) - tailLines
		}
		result.WriteString("\n--- Recent Output ---\n")
		for _, line := range output[start:] {
			result.WriteString(line)
			result.WriteString("\n")
		}
	} else {
		result.WriteString("\n(no output captured)\n")
	}

	return result.String(), nil
}

// SleepTool creates a tool that pauses execution for a specified duration.
func SleepTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Sleep",
			Desc: "Waits for a specified duration. Use when you need to pause before checking on a long-running process or waiting for an external event.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"duration_ms": {Type: schema.Integer, Desc: "Duration to sleep in milliseconds (max 60000)", Required: true},
			}),
		},
		Execute:           executeSleep,
		IsConcurrencySafe: func(input map[string]any) bool { return true },
	}
}

func executeSleep(input string) (string, error) {
	var params struct {
		DurationMs int `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("sleep: invalid params: %w", err)
	}
	if params.DurationMs <= 0 {
		return "", fmt.Errorf("sleep: duration_ms must be a positive integer")
	}
	if params.DurationMs > 60000 {
		return "", fmt.Errorf("sleep: duration_ms exceeds maximum of 60000ms (1 minute)")
	}

	duration := time.Duration(params.DurationMs) * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), duration+time.Second)
	defer cancel()

	select {
	case <-time.After(duration):
		return fmt.Sprintf("Slept for %dms.", params.DurationMs), nil
	case <-ctx.Done():
		return fmt.Sprintf("Sleep interrupted after waiting (requested %dms).", params.DurationMs), nil
	}
}
