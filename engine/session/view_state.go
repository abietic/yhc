package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	persistedViewStateVersion = 1
	maxPersistedThreadViews   = 128
	maxPersistedDraftRunes    = 64 * 1024
)

// PersistedSessionViewState contains presentation-only state. Runtime queues,
// structured payloads, undo history, selections, and interactive requests are
// deliberately excluded.
type PersistedSessionViewState struct {
	Version        int                        `json:"version"`
	SessionID      string                     `json:"session_id"`
	ActiveThreadID string                     `json:"active_thread_id,omitempty"`
	Threads        []PersistedThreadViewState `json:"threads,omitempty"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

type PersistedThreadViewState struct {
	ThreadID     string `json:"thread_id"`
	Mode         string `json:"mode,omitempty"`
	Draft        string `json:"draft,omitempty"`
	CursorLine   int    `json:"cursor_line,omitempty"`
	CursorColumn int    `json:"cursor_column,omitempty"`
	InputMode    int    `json:"input_mode,omitempty"`
	ScrollItem   int    `json:"scroll_item,omitempty"`
	ScrollLine   int    `json:"scroll_line,omitempty"`
	Follow       bool   `json:"follow"`
	DetailTab    int    `json:"detail_tab,omitempty"`
}

func SessionViewStatePath(transcriptDir, sessionID string) string {
	if strings.TrimSpace(transcriptDir) == "" || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	return filepath.Join(transcriptDir, sessionID+".view.json")
}

func SaveSessionViewState(transcriptDir, sessionID string, state PersistedSessionViewState) error {
	path := SessionViewStatePath(transcriptDir, sessionID)
	if path == "" {
		return nil
	}
	state = normalizePersistedViewState(sessionID, state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session view state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-view-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func LoadSessionViewState(transcriptDir, sessionID string) (PersistedSessionViewState, error) {
	path := SessionViewStatePath(transcriptDir, sessionID)
	if path == "" {
		return PersistedSessionViewState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PersistedSessionViewState{}, nil
		}
		return PersistedSessionViewState{}, err
	}
	var state PersistedSessionViewState
	if err := json.Unmarshal(data, &state); err != nil {
		return PersistedSessionViewState{}, fmt.Errorf("decode session view state: %w", err)
	}
	if state.Version != persistedViewStateVersion || state.SessionID != sessionID {
		return PersistedSessionViewState{}, errors.New("session view state identity/version mismatch")
	}
	return normalizePersistedViewState(sessionID, state), nil
}

func normalizePersistedViewState(sessionID string, state PersistedSessionViewState) PersistedSessionViewState {
	state.Version = persistedViewStateVersion
	state.SessionID = sessionID
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	if len(state.Threads) > maxPersistedThreadViews {
		state.Threads = append([]PersistedThreadViewState(nil), state.Threads[:maxPersistedThreadViews]...)
	} else {
		state.Threads = append([]PersistedThreadViewState(nil), state.Threads...)
	}
	seen := make(map[string]struct{}, len(state.Threads))
	normalized := state.Threads[:0]
	for _, thread := range state.Threads {
		thread.ThreadID = strings.TrimSpace(thread.ThreadID)
		if thread.ThreadID == "" {
			continue
		}
		if _, duplicate := seen[thread.ThreadID]; duplicate {
			continue
		}
		seen[thread.ThreadID] = struct{}{}
		thread.Draft = truncateViewRunes(thread.Draft, maxPersistedDraftRunes)
		thread.CursorLine = max(thread.CursorLine, 0)
		thread.CursorColumn = max(thread.CursorColumn, 0)
		thread.ScrollItem = max(thread.ScrollItem, 0)
		thread.ScrollLine = max(thread.ScrollLine, 0)
		normalized = append(normalized, thread)
	}
	state.Threads = normalized
	return state
}

func truncateViewRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
