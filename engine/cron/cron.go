// Package cron implements scheduled task management.
// Mirrors src/utils/cronTasks.ts and src/utils/cronScheduler.ts from the reference.
// Tasks are stored in <project>/.yhc/scheduled_tasks.json.
package cron

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abietic/yhc/internal/identity"
)

// Task represents a scheduled prompt task.
type Task struct {
	ID          string `json:"id"`
	Cron        string `json:"cron"`
	Prompt      string `json:"prompt"`
	CreatedAt   int64  `json:"createdAt"`
	LastFiredAt *int64 `json:"lastFiredAt,omitempty"`
	Recurring   bool   `json:"recurring,omitempty"`
	Permanent   bool   `json:"permanent,omitempty"`
	// Runtime-only fields (never persisted)
	Durable bool   `json:"-"`
	AgentID string `json:"-"`
}

// JitterConfig controls timing jitter for task scheduling.
type JitterConfig struct {
	MinJitterMs       int64 `json:"minJitterMs"`
	MaxJitterMs       int64 `json:"maxJitterMs"`
	RecurringMaxAgeMs int64 `json:"recurringMaxAgeMs"`
}

// DefaultJitterConfig is the default jitter configuration.
var DefaultJitterConfig = JitterConfig{
	MinJitterMs:       0,
	MaxJitterMs:       60_000,                  // 1 minute max jitter
	RecurringMaxAgeMs: 7 * 24 * 60 * 60 * 1000, // 7 days
}

const cronFileName = "scheduled_tasks.json"

var writeTasksBeforePromote = func() error { return nil }

// GetCronFilePath returns the path to the scheduled tasks file.
func GetCronFilePath(projectDir string) string {
	return filepath.Join(projectDir, identity.ProjectDirName, cronFileName)
}

// ReadTasks reads and parses .yhc/scheduled_tasks.json.
func ReadTasks(projectDir string) ([]Task, error) {
	data, exists, err := readCanonicalRegular(projectDir, cronFileName, maxCronFileBytes)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	var file struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil // malformed file returns empty
	}

	// Filter out tasks with invalid cron strings
	valid := make([]Task, 0, len(file.Tasks))
	for _, t := range file.Tasks {
		if _, err := ParseCronExpression(t.Cron); err == nil {
			valid = append(valid, t)
		}
	}
	return valid, nil
}

// WriteTasks atomically writes tasks to .yhc/scheduled_tasks.json.
func WriteTasks(projectDir string, tasks []Task) error {
	file := struct {
		Tasks []Task `json:"tasks"`
	}{Tasks: tasks}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return replaceCanonicalRegular(
		projectDir,
		cronFileName,
		".scheduled-tasks-",
		data,
		writeTasksBeforePromote,
	)
}

// CreateTask creates a new scheduled task and persists it.
func CreateTask(projectDir, cronExpr, prompt string, recurring bool) (*Task, error) {
	if _, err := ParseCronExpression(cronExpr); err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}

	task := Task{
		ID:        uuid.New().String(),
		Cron:      cronExpr,
		Prompt:    prompt,
		CreatedAt: time.Now().UnixMilli(),
		Recurring: recurring,
	}

	tasks, _ := ReadTasks(projectDir)
	tasks = append(tasks, task)
	if err := WriteTasks(projectDir, tasks); err != nil {
		return nil, err
	}
	return &task, nil
}

// RemoveTasks removes tasks by ID.
func RemoveTasks(projectDir string, ids []string) error {
	tasks, err := ReadTasks(projectDir)
	if err != nil {
		return err
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if !idSet[t.ID] {
			filtered = append(filtered, t)
		}
	}

	return WriteTasks(projectDir, filtered)
}

// MarkFired updates lastFiredAt for recurring tasks and removes one-shot tasks.
func MarkFired(projectDir, taskID string) error {
	tasks, err := ReadTasks(projectDir)
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.ID == taskID {
			if t.Recurring {
				t.LastFiredAt = &now
				filtered = append(filtered, t)
			}
			// one-shot: don't include (auto-delete)
		} else {
			filtered = append(filtered, t)
		}
	}

	return WriteTasks(projectDir, filtered)
}

// IsRecurringTaskAged returns true when a recurring task has exceeded its max age.
func IsRecurringTaskAged(t Task, nowMs, maxAgeMs int64) bool {
	if maxAgeMs == 0 {
		return false
	}
	return t.Recurring && !t.Permanent && (nowMs-t.CreatedAt >= maxAgeMs)
}

// NextFireAt computes when a task should next fire (ms since epoch).
func NextFireAt(t Task, config JitterConfig) int64 {
	anchor := t.CreatedAt
	if t.LastFiredAt != nil {
		anchor = *t.LastFiredAt
	}

	expr, err := ParseCronExpression(t.Cron)
	if err != nil {
		return 0
	}

	anchorTime := time.UnixMilli(anchor)
	nextRun := ComputeNextCronRun(expr, anchorTime)
	nextMs := nextRun.UnixMilli()

	// Add jitter
	jitter := config.MinJitterMs
	if config.MaxJitterMs > config.MinJitterMs {
		jitter += rand.Int63n(config.MaxJitterMs - config.MinJitterMs)
	}

	return nextMs + jitter
}

// FindMissedTasks returns tasks that should have fired between their anchor and now.
func FindMissedTasks(tasks []Task, nowMs int64) []Task {
	var missed []Task
	for _, t := range tasks {
		anchor := t.CreatedAt
		if t.LastFiredAt != nil {
			anchor = *t.LastFiredAt
		}
		expr, err := ParseCronExpression(t.Cron)
		if err != nil {
			continue
		}
		anchorTime := time.UnixMilli(anchor)
		nextRun := ComputeNextCronRun(expr, anchorTime)
		if nextRun.UnixMilli() <= nowMs {
			missed = append(missed, t)
		}
	}
	return missed
}

// HasTasks returns true if the cron file exists and has tasks.
func HasTasks(projectDir string) bool {
	data, exists, err := readCanonicalRegular(projectDir, cronFileName, maxCronFileBytes)
	if err != nil || !exists {
		return false
	}
	return strings.Contains(string(data), `"id"`)
}

// CronField represents a parsed cron field.
type CronField struct {
	Values []int
}

// CronExpression represents a parsed 5-field cron expression.
type CronExpression struct {
	Minute   CronField
	Hour     CronField
	DayMonth CronField
	Month    CronField
	DayWeek  CronField
}

// ParseCronExpression parses a 5-field cron expression string.
func ParseCronExpression(expr string) (*CronExpression, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	dayMonth, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dayWeek, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}

	return &CronExpression{
		Minute:   minute,
		Hour:     hour,
		DayMonth: dayMonth,
		Month:    month,
		DayWeek:  dayWeek,
	}, nil
}

// ComputeNextCronRun finds the next time the cron expression matches after the given time.
func ComputeNextCronRun(expr *CronExpression, after time.Time) time.Time {
	t := after.Add(time.Minute).Truncate(time.Minute)

	for i := 0; i < 525960; i++ { // ~1 year of minutes
		if cronMatches(expr, t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return after.Add(365 * 24 * time.Hour) // fallback: 1 year from now
}

// CronToHuman converts a cron expression to a human-readable schedule description.
func CronToHuman(expr string) string {
	parsed, err := ParseCronExpression(expr)
	if err != nil {
		return expr
	}

	// Simple common cases
	allMinutes := len(parsed.Minute.Values) == 60
	allHours := len(parsed.Hour.Values) == 24
	allDays := len(parsed.DayMonth.Values) == 31
	allMonths := len(parsed.Month.Values) == 12
	allWeekdays := len(parsed.DayWeek.Values) == 7

	if allMinutes && allHours && allDays && allMonths && allWeekdays {
		return "every minute"
	}
	if !allMinutes && allHours && allDays && allMonths && allWeekdays {
		if len(parsed.Minute.Values) == 1 && parsed.Minute.Values[0] == 0 {
			return "every hour"
		}
	}
	if len(parsed.Minute.Values) == 1 && len(parsed.Hour.Values) == 1 && allDays && allMonths && allWeekdays {
		return fmt.Sprintf("daily at %02d:%02d", parsed.Hour.Values[0], parsed.Minute.Values[0])
	}

	return "on schedule: " + expr
}

func cronMatches(expr *CronExpression, t time.Time) bool {
	if !fieldContains(expr.Minute, t.Minute()) {
		return false
	}
	if !fieldContains(expr.Hour, t.Hour()) {
		return false
	}
	if !fieldContains(expr.Month, int(t.Month())) {
		return false
	}
	dayOfMonthMatch := fieldContains(expr.DayMonth, t.Day())
	dayOfWeekMatch := fieldContains(expr.DayWeek, int(t.Weekday()))
	// Standard cron: if both day-of-month and day-of-week are restricted, either can match
	allDays := len(expr.DayMonth.Values) == 31
	allWeekdays := len(expr.DayWeek.Values) == 7
	if allDays {
		return dayOfWeekMatch
	}
	if allWeekdays {
		return dayOfMonthMatch
	}
	return dayOfMonthMatch || dayOfWeekMatch
}

func fieldContains(f CronField, val int) bool {
	for _, v := range f.Values {
		if v == val {
			return true
		}
	}
	return false
}

func parseCronField(field string, min, max int) (CronField, error) {
	if field == "*" {
		values := make([]int, 0, max-min+1)
		for i := min; i <= max; i++ {
			values = append(values, i)
		}
		return CronField{Values: values}, nil
	}

	var allValues []int
	parts := strings.Split(field, ",")
	for _, part := range parts {
		// Handle step: */5 or 1-10/2
		if strings.Contains(part, "/") {
			stepParts := strings.SplitN(part, "/", 2)
			step, err := strconv.Atoi(stepParts[1])
			if err != nil || step <= 0 {
				return CronField{}, fmt.Errorf("invalid step: %s", part)
			}
			rangeStart, rangeEnd := min, max
			if stepParts[0] != "*" {
				rangeParts := strings.SplitN(stepParts[0], "-", 2)
				rangeStart, err = strconv.Atoi(rangeParts[0])
				if err != nil {
					return CronField{}, fmt.Errorf("invalid range start: %s", part)
				}
				if len(rangeParts) == 2 {
					rangeEnd, err = strconv.Atoi(rangeParts[1])
					if err != nil {
						return CronField{}, fmt.Errorf("invalid range end: %s", part)
					}
				}
			}
			for i := rangeStart; i <= rangeEnd; i += step {
				allValues = append(allValues, i)
			}
		} else if strings.Contains(part, "-") {
			// Handle range: 1-5
			rangeParts := strings.SplitN(part, "-", 2)
			start, err := strconv.Atoi(rangeParts[0])
			if err != nil {
				return CronField{}, fmt.Errorf("invalid range start: %s", part)
			}
			end, err := strconv.Atoi(rangeParts[1])
			if err != nil {
				return CronField{}, fmt.Errorf("invalid range end: %s", part)
			}
			for i := start; i <= end; i++ {
				allValues = append(allValues, i)
			}
		} else {
			val, err := strconv.Atoi(part)
			if err != nil {
				return CronField{}, fmt.Errorf("invalid value: %s", part)
			}
			if val < min || val > max {
				return CronField{}, fmt.Errorf("value %d out of range [%d,%d]", val, min, max)
			}
			allValues = append(allValues, val)
		}
	}

	return CronField{Values: allValues}, nil
}

// Scheduler manages periodic checking and firing of cron tasks.
type Scheduler struct {
	mu         sync.Mutex
	projectDir string
	onFire     func(task Task)
	config     JitterConfig
	stopCh     chan struct{}
	running    bool
	// Session-only tasks (not persisted)
	sessionTasks []Task
}

// NewScheduler creates a new cron scheduler for the given project directory.
func NewScheduler(projectDir string, onFire func(task Task)) *Scheduler {
	return &Scheduler{
		projectDir: projectDir,
		onFire:     onFire,
		config:     DefaultJitterConfig,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the scheduler's check loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go s.loop()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopCh)
}

// AddSessionTask adds a runtime-only task (not persisted to disk).
func (s *Scheduler) AddSessionTask(t Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Durable = false
	s.sessionTasks = append(s.sessionTasks, t)
}

// RemoveSessionTasks removes session-only tasks by ID.
func (s *Scheduler) RemoveSessionTasks(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	filtered := make([]Task, 0, len(s.sessionTasks))
	for _, t := range s.sessionTasks {
		if !idSet[t.ID] {
			filtered = append(filtered, t)
		}
	}
	s.sessionTasks = filtered
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.check()
		}
	}
}

func (s *Scheduler) check() {
	nowMs := time.Now().UnixMilli()

	// Check file-persisted tasks
	tasks, _ := ReadTasks(s.projectDir)
	for _, t := range tasks {
		if s.shouldFire(t, nowMs) {
			s.fire(t)
		}
	}

	// Check session-only tasks
	s.mu.Lock()
	sessionTasks := make([]Task, len(s.sessionTasks))
	copy(sessionTasks, s.sessionTasks)
	s.mu.Unlock()

	for _, t := range sessionTasks {
		if s.shouldFire(t, nowMs) {
			s.fire(t)
		}
	}
}

func (s *Scheduler) shouldFire(t Task, nowMs int64) bool {
	// Check if aged out
	if IsRecurringTaskAged(t, nowMs, s.config.RecurringMaxAgeMs) {
		_ = RemoveTasks(s.projectDir, []string{t.ID})
		return false
	}

	nextFire := NextFireAt(t, JitterConfig{}) // no jitter for check
	return nextFire <= nowMs
}

func (s *Scheduler) fire(t Task) {
	if s.onFire != nil {
		s.onFire(t)
	}
	if t.Durable || t.ID != "" {
		_ = MarkFired(s.projectDir, t.ID)
	}
}
