package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionDecision represents a persisted permission decision (allow-always or deny-always)
// that survives across sessions. Mirrors the reference's addRules + persistence behavior
// in PermissionUpdate.ts and permissionsLoader.ts.
type SessionDecision struct {
	ToolName     string           `json:"tool_name"`
	InputPattern string           `json:"input_pattern,omitempty"`
	Action       PermissionAction `json:"action"`
	Scope        DecisionScope    `json:"scope"`
	CreatedAt    time.Time        `json:"created_at"`
	Reason       string           `json:"reason,omitempty"`
}

// DecisionScope determines where a persisted decision applies.
type DecisionScope string

const (
	// ScopeProject means the decision only applies to the current project.
	ScopeProject DecisionScope = "project"
	// ScopeGlobal means the decision applies across all projects.
	ScopeGlobal DecisionScope = "global"
)

// SessionStore manages persisted permission decisions across sessions.
// It supports both project-scoped and global-scoped decisions.
// Thread-safe for concurrent access.
//
// Mirrors the reference's permission rule persistence:
// - Project decisions go to <projectDir>/.claude/settings.local.json
// - Global decisions go to ~/.claude/settings.json
// - Both use the "permissions" key with allow/deny/ask arrays
type SessionStore struct {
	decisions []SessionDecision
	mu        sync.RWMutex
}

// NewSessionStore creates a new empty session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{}
}

// Add records a permission decision for persistence.
func (s *SessionStore) Add(decision SessionDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for existing decision on same tool+input and update it.
	for i, existing := range s.decisions {
		if existing.ToolName == decision.ToolName &&
			existing.InputPattern == decision.InputPattern &&
			existing.Scope == decision.Scope {
			s.decisions[i] = decision
			return
		}
	}

	s.decisions = append(s.decisions, decision)
}

// Remove removes a specific decision matching tool+input+scope.
func (s *SessionStore) Remove(toolName, inputPattern string, scope DecisionScope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, d := range s.decisions {
		if d.ToolName == toolName && d.InputPattern == inputPattern && d.Scope == scope {
			s.decisions = append(s.decisions[:i], s.decisions[i+1:]...)
			return true
		}
	}
	return false
}

// Clear removes all persisted decisions, optionally filtered by scope.
// If scope is empty string, clears all decisions regardless of scope.
func (s *SessionStore) Clear(scope DecisionScope) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if scope == "" {
		s.decisions = nil
		return
	}

	filtered := make([]SessionDecision, 0, len(s.decisions))
	for _, d := range s.decisions {
		if d.Scope != scope {
			filtered = append(filtered, d)
		}
	}
	s.decisions = filtered
}

// Lookup finds a persisted decision matching the given tool invocation.
// Returns the decision and true if found, zero value and false otherwise.
// Project-scope decisions take precedence over global-scope decisions.
func (s *SessionStore) Lookup(toolName string, toolInput map[string]any) (SessionDecision, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inputStr := extractInputString(toolName, toolInput)

	var globalMatch *SessionDecision
	for i := range s.decisions {
		d := &s.decisions[i]
		if d.ToolName != toolName {
			continue
		}

		// Check input pattern match
		if d.InputPattern == "" {
			// Tool-wide decision
			if d.Scope == ScopeProject {
				return *d, true
			}
			if globalMatch == nil {
				globalMatch = d
			}
			continue
		}

		if matchPattern(d.InputPattern, inputStr) {
			if d.Scope == ScopeProject {
				return *d, true
			}
			if globalMatch == nil {
				globalMatch = d
			}
		}
	}

	if globalMatch != nil {
		return *globalMatch, true
	}
	return SessionDecision{}, false
}

// List returns a snapshot of all persisted decisions.
func (s *SessionStore) List() []SessionDecision {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.decisions) == 0 {
		return nil
	}
	result := make([]SessionDecision, len(s.decisions))
	copy(result, s.decisions)
	return result
}

// ToRules converts persisted decisions into PermissionRule objects suitable
// for injection into the RulesEngine. This bridges the session store with
// the rule evaluation system.
func (s *SessionStore) ToRules() []PermissionRule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rules := make([]PermissionRule, 0, len(s.decisions))
	for _, d := range s.decisions {
		source := SourceLocal
		if d.Scope == ScopeGlobal {
			source = SourceUser
		}
		rules = append(rules, PermissionRule{
			ToolName:     d.ToolName,
			InputPattern: d.InputPattern,
			Action:       d.Action,
			Source:       source,
		})
	}
	return rules
}

// persistedSessionFile is the JSON structure for the session decisions file.
type persistedSessionFile struct {
	Version   int               `json:"version"`
	Decisions []SessionDecision `json:"decisions"`
}

const sessionFileVersion = 1

// SaveTo persists all decisions to a JSON file.
// Creates parent directories as needed. Handles atomic write.
func (s *SessionStore) SaveTo(path string) error {
	s.mu.RLock()
	decisions := make([]SessionDecision, len(s.decisions))
	copy(decisions, s.decisions)
	s.mu.RUnlock()

	file := persistedSessionFile{
		Version:   sessionFileVersion,
		Decisions: decisions,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session decisions: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	return atomicWriteFile(path, data)
}

// LoadFrom reads persisted decisions from a JSON file.
// Returns nil error if the file does not exist or is corrupt (lenient behavior).
// On corrupt files, starts fresh rather than failing — matches reference lenient parsing.
func (s *SessionStore) LoadFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read session store: %w", err)
	}

	// Handle empty file gracefully.
	if len(data) == 0 {
		return nil
	}

	var file persistedSessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		// Lenient: start fresh on corrupt JSON, matching reference behavior.
		return nil
	}

	// Validate version — future versions are accepted (forward compat).
	if file.Version < 1 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Merge loaded decisions with any already in memory.
	for _, d := range file.Decisions {
		// Validate required fields.
		if d.ToolName == "" || (d.Action != ActionAllow && d.Action != ActionDeny && d.Action != ActionAsk) {
			continue
		}
		s.decisions = append(s.decisions, d)
	}

	return nil
}

// SessionStorePath returns the default path for the session store file
// within a project directory. This file is intended to be git-ignored.
func SessionStorePath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "permission_decisions.json")
}

// GlobalSessionStorePath returns the path for global (cross-project) decisions.
func GlobalSessionStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "permission_decisions.json")
}
