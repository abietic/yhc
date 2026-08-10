package hooks

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AgentHookModelFn calls a model to execute an agent-driven hook.
type AgentHookModelFn func(ctx context.Context, prompt string, messages []string) (string, error)

// ExecAgentHook executes a hook using a forked sub-agent (model-driven).
// Unlike shell hooks that run a command, agent hooks use the LLM to
// evaluate conditions and generate responses.
//
// Reference: src/utils/hooks/execAgentHook.ts (339 lines)
func ExecAgentHook(
	ctx context.Context,
	prompt string,
	messages []string,
	modelFn AgentHookModelFn,
	timeout time.Duration,
) (string, error) {
	if modelFn == nil {
		return "", nil
	}

	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return modelFn(ctx, prompt, messages)
}

// FileChangedWatcher watches file paths registered by hooks and
// triggers re-execution when files change.
//
// Reference: src/utils/hooks/fileChangedWatcher.ts (191 lines)
type FileChangedWatcher struct {
	mu        sync.Mutex
	watches   map[string]time.Time // path -> last known modtime
	callbacks map[string][]func(string)
	interval  time.Duration
	cancel    context.CancelFunc
}

// NewFileChangedWatcher creates a file watcher with the given poll interval.
func NewFileChangedWatcher(interval time.Duration) *FileChangedWatcher {
	if interval == 0 {
		interval = 2 * time.Second
	}
	return &FileChangedWatcher{
		watches:   make(map[string]time.Time),
		callbacks: make(map[string][]func(string)),
		interval:  interval,
	}
}

// Watch registers a file path to watch. The callback fires when the file changes.
func (w *FileChangedWatcher) Watch(path string, cb func(string)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	if info, err := os.Stat(absPath); err == nil {
		w.watches[absPath] = info.ModTime()
	} else {
		w.watches[absPath] = time.Time{}
	}

	w.callbacks[absPath] = append(w.callbacks[absPath], cb)
}

// Start begins polling for file changes.
func (w *FileChangedWatcher) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.check()
			}
		}
	}()
}

// Stop halts the file watcher.
func (w *FileChangedWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *FileChangedWatcher) check() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for path, lastMod := range w.watches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		currentMod := info.ModTime()
		if !currentMod.Equal(lastMod) {
			w.watches[path] = currentMod
			for _, cb := range w.callbacks[path] {
				go cb(path)
			}
		}
	}
}

// SkillImprovementHook provides post-skill execution feedback.
// After a skill is invoked, this hook captures the outcome and
// can be used to improve skill behavior over time.
//
// Reference: src/utils/hooks/skillImprovement.ts (267 lines)
type SkillImprovementHook struct {
	mu       sync.Mutex
	feedback []SkillFeedback
}

// SkillFeedback captures the outcome of a skill invocation.
type SkillFeedback struct {
	SkillName string        `json:"skillName"`
	Success   bool          `json:"success"`
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// NewSkillImprovementHook creates a skill improvement tracker.
func NewSkillImprovementHook() *SkillImprovementHook {
	return &SkillImprovementHook{}
}

// RecordFeedback records the outcome of a skill invocation.
func (h *SkillImprovementHook) RecordFeedback(fb SkillFeedback) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fb.Timestamp = time.Now()
	h.feedback = append(h.feedback, fb)
}

// GetFeedback returns all recorded feedback.
func (h *SkillImprovementHook) GetFeedback() []SkillFeedback {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]SkillFeedback, len(h.feedback))
	copy(result, h.feedback)
	return result
}

// GetSkillSuccessRate returns the success rate for a given skill.
func (h *SkillImprovementHook) GetSkillSuccessRate(skillName string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	var total, success int
	for _, fb := range h.feedback {
		if fb.SkillName == skillName {
			total++
			if fb.Success {
				success++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(success) / float64(total)
}
