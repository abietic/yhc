package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// CronJob represents a scheduled cron-like task.
type CronJob struct {
	ID          string    `json:"id"`
	Schedule    string    `json:"schedule"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	LastRun     time.Time `json:"last_run,omitempty"`
	NextRun     time.Time `json:"next_run"`
	RunCount    int       `json:"run_count"`
	Active      bool      `json:"active"`
}

// WakeupTimer represents a one-time wakeup timer.
type WakeupTimer struct {
	ID      string
	Message string
	FireAt  time.Time
	Fired   bool
	cancel  context.CancelFunc
}

var (
	cronJobs = make(map[string]*CronJob)
	cronMu   sync.RWMutex

	wakeupTimers = make(map[string]*WakeupTimer)
	wakeupMu     sync.RWMutex
)

// ScheduleCronTool creates a tool for managing cron-like scheduled tasks.
func ScheduleCronTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ScheduleCron",
			Desc: "Create, list, or delete scheduled cron-like tasks that run commands at specified intervals.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"action":      {Type: schema.String, Desc: "Action: 'create', 'list', 'delete'", Required: true},
				"id":          {Type: schema.String, Desc: "ID of the cron job (for delete)"},
				"schedule":    {Type: schema.String, Desc: "Cron schedule expression (e.g., '*/5 * * * *' for every 5 min)"},
				"command":     {Type: schema.String, Desc: "Command to execute on schedule"},
				"description": {Type: schema.String, Desc: "Human-readable description of what this cron does"},
			}),
		},
		Execute: executeScheduleCron,
	}
}

// ScheduleWakeupTool creates a tool for scheduling one-time wakeup timers.
func ScheduleWakeupTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ScheduleWakeup",
			Desc: "Schedule a one-time wakeup after a specified delay. The agent will be notified when the timer fires.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"delay_seconds": {Type: schema.Integer, Desc: "Seconds to wait before waking up", Required: true},
				"message":       {Type: schema.String, Desc: "Message to deliver when the timer fires", Required: true},
			}),
		},
		Execute:           executeScheduleWakeup,
		IsConcurrencySafe: func(input map[string]any) bool { return true },
	}
}

func executeScheduleCron(input string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		ID          string `json:"id"`
		Schedule    string `json:"schedule"`
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("schedule_cron: invalid params: %w", err)
	}
	if params.Action == "" {
		return "", fmt.Errorf("schedule_cron: action is required")
	}

	switch params.Action {
	case "create":
		return cronCreate(params.Schedule, params.Command, params.Description)
	case "list":
		return cronList()
	case "delete":
		return cronDelete(params.ID)
	default:
		return "", fmt.Errorf("schedule_cron: unknown action %q (must be 'create', 'list', or 'delete')", params.Action)
	}
}

func cronCreate(schedule, command, description string) (string, error) {
	if schedule == "" {
		return "", fmt.Errorf("schedule_cron: schedule is required for create action")
	}
	if command == "" {
		return "", fmt.Errorf("schedule_cron: command is required for create action")
	}
	if err := ParseCronSchedule(schedule); err != nil {
		return "", fmt.Errorf("schedule_cron: invalid schedule: %w", err)
	}

	id := fmt.Sprintf("cron_%d", time.Now().UnixNano())
	now := time.Now()

	job := &CronJob{
		ID:          id,
		Schedule:    schedule,
		Command:     command,
		Description: description,
		CreatedAt:   now,
		NextRun:     now.Add(estimateInterval(schedule)),
		Active:      true,
	}

	cronMu.Lock()
	cronJobs[id] = job
	cronMu.Unlock()

	var result strings.Builder
	fmt.Fprintf(&result, "Cron job created successfully.\n")
	fmt.Fprintf(&result, "  ID:          %s\n", job.ID)
	fmt.Fprintf(&result, "  Schedule:    %s\n", job.Schedule)
	fmt.Fprintf(&result, "  Command:     %s\n", job.Command)
	if job.Description != "" {
		fmt.Fprintf(&result, "  Description: %s\n", job.Description)
	}
	fmt.Fprintf(&result, "  Next Run:    %s\n", job.NextRun.Format(time.RFC3339))
	return result.String(), nil
}

func cronList() (string, error) {
	cronMu.RLock()
	defer cronMu.RUnlock()

	if len(cronJobs) == 0 {
		return "No cron jobs scheduled.", nil
	}

	var result strings.Builder
	fmt.Fprintf(&result, "Scheduled Cron Jobs (%d total):\n\n", len(cronJobs))

	for _, job := range cronJobs {
		status := "active"
		if !job.Active {
			status = "inactive"
		}
		fmt.Fprintf(&result, "  [%s] %s\n", job.ID, status)
		fmt.Fprintf(&result, "    Schedule:    %s\n", job.Schedule)
		fmt.Fprintf(&result, "    Command:     %s\n", job.Command)
		if job.Description != "" {
			fmt.Fprintf(&result, "    Description: %s\n", job.Description)
		}
		fmt.Fprintf(&result, "    Created:     %s\n", job.CreatedAt.Format(time.RFC3339))
		if !job.LastRun.IsZero() {
			fmt.Fprintf(&result, "    Last Run:    %s\n", job.LastRun.Format(time.RFC3339))
		}
		fmt.Fprintf(&result, "    Next Run:    %s\n", job.NextRun.Format(time.RFC3339))
		fmt.Fprintf(&result, "    Run Count:   %d\n", job.RunCount)
		result.WriteString("\n")
	}

	return result.String(), nil
}

func cronDelete(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("schedule_cron: id is required for delete action")
	}

	cronMu.Lock()
	defer cronMu.Unlock()

	if _, ok := cronJobs[id]; !ok {
		return fmt.Sprintf("Cron job %q not found.", id), nil
	}

	delete(cronJobs, id)
	return fmt.Sprintf("Cron job %q deleted successfully.", id), nil
}

func executeScheduleWakeup(input string) (string, error) {
	var params struct {
		DelaySeconds int    `json:"delay_seconds"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("schedule_wakeup: invalid params: %w", err)
	}
	if params.DelaySeconds <= 0 {
		return "", fmt.Errorf("schedule_wakeup: delay_seconds must be a positive integer")
	}
	if params.DelaySeconds > 3600 {
		return "", fmt.Errorf("schedule_wakeup: delay_seconds exceeds maximum of 3600 (1 hour)")
	}
	if params.Message == "" {
		return "", fmt.Errorf("schedule_wakeup: message is required")
	}

	id := fmt.Sprintf("wakeup_%d", time.Now().UnixNano())
	fireAt := time.Now().Add(time.Duration(params.DelaySeconds) * time.Second)

	ctx, cancel := context.WithCancel(context.Background())

	timer := &WakeupTimer{
		ID:      id,
		Message: params.Message,
		FireAt:  fireAt,
		Fired:   false,
		cancel:  cancel,
	}

	wakeupMu.Lock()
	wakeupTimers[id] = timer
	wakeupMu.Unlock()

	// Start the background timer goroutine.
	go func() {
		defer cancel()
		delay := time.Duration(params.DelaySeconds) * time.Second
		select {
		case <-time.After(delay):
			wakeupMu.Lock()
			timer.Fired = true
			wakeupMu.Unlock()
		case <-ctx.Done():
			// Timer was cancelled.
		}
	}()

	var result strings.Builder
	fmt.Fprintf(&result, "Wakeup timer scheduled.\n")
	fmt.Fprintf(&result, "  ID:      %s\n", id)
	fmt.Fprintf(&result, "  Delay:   %d seconds\n", params.DelaySeconds)
	fmt.Fprintf(&result, "  Fire At: %s\n", fireAt.Format(time.RFC3339))
	fmt.Fprintf(&result, "  Message: %s\n", params.Message)
	return result.String(), nil
}

// ParseCronSchedule validates a cron expression (5-field format).
// Fields: minute hour day-of-month month day-of-week
func ParseCronSchedule(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	fieldNames := []string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	maxValues := []int{59, 23, 31, 12, 7}
	minValues := []int{0, 0, 1, 1, 0}

	for i, field := range fields {
		if err := validateCronField(field, minValues[i], maxValues[i]); err != nil {
			return fmt.Errorf("invalid %s field %q: %w", fieldNames[i], field, err)
		}
	}
	return nil
}

func validateCronField(field string, min, max int) error {
	// Handle wildcard
	if field == "*" {
		return nil
	}

	// Handle step values (e.g., */5 or 1-10/2)
	parts := strings.SplitN(field, "/", 2)
	base := parts[0]
	if len(parts) == 2 {
		step, err := strconv.Atoi(parts[1])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step value")
		}
	}

	// Handle range (e.g., 1-5)
	if base == "*" {
		return nil
	}

	// Handle comma-separated values (e.g., 1,3,5)
	values := strings.Split(base, ",")
	for _, v := range values {
		// Check if it's a range
		if strings.Contains(v, "-") {
			rangeParts := strings.SplitN(v, "-", 2)
			start, err := strconv.Atoi(rangeParts[0])
			if err != nil {
				return fmt.Errorf("invalid range start %q", rangeParts[0])
			}
			end, err := strconv.Atoi(rangeParts[1])
			if err != nil {
				return fmt.Errorf("invalid range end %q", rangeParts[1])
			}
			if start < min || start > max || end < min || end > max {
				return fmt.Errorf("value out of range [%d-%d]", min, max)
			}
			if start > end {
				return fmt.Errorf("range start %d > end %d", start, end)
			}
		} else {
			num, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid value %q", v)
			}
			if num < min || num > max {
				return fmt.Errorf("value %d out of range [%d-%d]", num, min, max)
			}
		}
	}
	return nil
}

// estimateInterval provides a rough estimate of the cron interval for NextRun calculation.
func estimateInterval(schedule string) time.Duration {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return time.Hour
	}

	// Check minute field for step pattern (e.g., */5)
	if strings.HasPrefix(fields[0], "*/") {
		if step, err := strconv.Atoi(fields[0][2:]); err == nil && step > 0 {
			return time.Duration(step) * time.Minute
		}
	}

	// Check if minute is specific and hour is wildcard
	if fields[0] != "*" && fields[1] == "*" {
		return time.Hour
	}

	// Check if minute and hour are specific
	if fields[0] != "*" && fields[1] != "*" {
		return 24 * time.Hour
	}

	// Default: assume every minute
	return time.Minute
}
