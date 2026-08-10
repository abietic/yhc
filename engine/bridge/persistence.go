package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PersistenceConfig configures state persistence behavior.
type PersistenceConfig struct {
	// Dir is the directory where state files are stored.
	// If empty, persistence is disabled.
	Dir string

	// Filename is the name of the state file within Dir.
	// Defaults to "bridge_state.json".
	Filename string
}

// persistedState is the JSON-serializable representation of critical state fields
// that should survive across sessions.
type persistedState struct {
	// Version is the state store version at the time of persistence.
	Version uint64 `json:"version"`

	// Timestamp is when the state was persisted.
	Timestamp time.Time `json:"timestamp"`

	// Model is the last known model.
	Model string `json:"model,omitempty"`

	// FallbackModel is the last known fallback model.
	FallbackModel string `json:"fallback_model,omitempty"`

	// SessionID is the session identifier.
	SessionID string `json:"session_id,omitempty"`

	// CWD is the working directory.
	CWD string `json:"cwd,omitempty"`

	// InputTokens is the cumulative input token count.
	InputTokens int `json:"input_tokens"`

	// OutputTokens is the cumulative output token count.
	OutputTokens int `json:"output_tokens"`

	// TurnCount is the last known turn count.
	TurnCount int `json:"turn_count"`

	// PermissionMode is the last permission mode.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// StatePersistence handles saving and loading bridge state to/from disk.
// It provides atomic writes for crash safety using write-to-temp-then-rename.
type StatePersistence struct {
	config PersistenceConfig

	mu sync.Mutex
}

// NewStatePersistence creates a new StatePersistence with the given configuration.
// If config.Dir is empty, all operations are no-ops.
func NewStatePersistence(config PersistenceConfig) *StatePersistence {
	if config.Filename == "" {
		config.Filename = "bridge_state.json"
	}
	return &StatePersistence{
		config: config,
	}
}

// Save persists critical state fields from the store to disk atomically.
// It writes to a temporary file first, then renames to the target path.
// This ensures that a crash during write does not corrupt the existing state file.
// Returns nil if persistence is disabled (empty Dir).
func (p *StatePersistence) Save(store *StateStore) error {
	if p.config.Dir == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Take a snapshot of the current state.
	snap := store.Snapshot()

	state := persistedState{
		Version:        snap.Version,
		Timestamp:      time.Now(),
		Model:          snap.GetString(FieldCurrentModel),
		FallbackModel:  snap.GetString(FieldFallbackModel),
		SessionID:      snap.GetString(FieldSessionID),
		CWD:            snap.GetString(FieldSessionCWD),
		InputTokens:    snap.GetInt(FieldInputTokens),
		OutputTokens:   snap.GetInt(FieldOutputTokens),
		TurnCount:      snap.GetInt(FieldTurnCount),
		PermissionMode: snap.GetString(FieldPermissionMode),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Ensure the directory exists.
	if err := os.MkdirAll(p.config.Dir, 0o755); err != nil {
		return fmt.Errorf("failed to create persistence directory: %w", err)
	}

	targetPath := filepath.Join(p.config.Dir, p.config.Filename)

	// Write to a temporary file in the same directory (same filesystem for atomic rename).
	tmpFile, err := os.CreateTemp(p.config.Dir, ".bridge_state_*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Write data and sync to disk.
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write state: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync state file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// Load reads persisted state from disk and applies it to the store.
// Returns nil if:
// - persistence is disabled (empty Dir)
// - no state file exists (fresh session)
// - the state file is corrupt (logged but not fatal)
//
// On corrupt data, the existing file is removed to prevent repeated failures.
func (p *StatePersistence) Load(store *StateStore) error {
	if p.config.Dir == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	targetPath := filepath.Join(p.config.Dir, p.config.Filename)

	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No persisted state — this is normal for a fresh session.
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if len(data) == 0 {
		// Empty file — treat as missing.
		return nil
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupt file — remove it and return an error describing the issue.
		_ = os.Remove(targetPath)
		return fmt.Errorf("corrupt state file (removed): %w", err)
	}

	// Apply persisted values to the store.
	batch := make(map[StateField]any)
	if state.Model != "" {
		batch[FieldCurrentModel] = state.Model
	}
	if state.FallbackModel != "" {
		batch[FieldFallbackModel] = state.FallbackModel
	}
	if state.SessionID != "" {
		batch[FieldSessionID] = state.SessionID
	}
	if state.CWD != "" {
		batch[FieldSessionCWD] = state.CWD
	}
	if state.InputTokens > 0 {
		batch[FieldInputTokens] = state.InputTokens
	}
	if state.OutputTokens > 0 {
		batch[FieldOutputTokens] = state.OutputTokens
	}
	if state.TurnCount > 0 {
		batch[FieldTurnCount] = state.TurnCount
	}
	if state.PermissionMode != "" {
		batch[FieldPermissionMode] = state.PermissionMode
	}

	if len(batch) > 0 {
		store.Update(batch)
	}

	return nil
}

// Exists returns true if a persisted state file exists on disk.
func (p *StatePersistence) Exists() bool {
	if p.config.Dir == "" {
		return false
	}
	targetPath := filepath.Join(p.config.Dir, p.config.Filename)
	_, err := os.Stat(targetPath)
	return err == nil
}

// Remove deletes the persisted state file. This is useful for cleanup after
// a session ends normally (as opposed to a crash).
func (p *StatePersistence) Remove() error {
	if p.config.Dir == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	targetPath := filepath.Join(p.config.Dir, p.config.Filename)
	err := os.Remove(targetPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Path returns the full path to the state file.
func (p *StatePersistence) Path() string {
	if p.config.Dir == "" {
		return ""
	}
	return filepath.Join(p.config.Dir, p.config.Filename)
}
