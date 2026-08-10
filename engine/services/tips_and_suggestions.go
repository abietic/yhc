package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TipCategory identifies what kind of tip to show.
type TipCategory string

const (
	TipCategoryUsage    TipCategory = "usage"
	TipCategoryFeature  TipCategory = "feature"
	TipCategoryWorkflow TipCategory = "workflow"
)

// Tip represents a user-facing tip or suggestion.
type Tip struct {
	ID          string      `json:"id"`
	Category    TipCategory `json:"category"`
	Content     string      `json:"content"`
	Priority    int         `json:"priority"`
	ShownCount  int         `json:"shownCount"`
	LastShownAt time.Time   `json:"lastShownAt,omitempty"`
}

// TipRegistry holds all available tips.
//
// Reference: src/services/tips/tipRegistry.ts
type TipRegistry struct {
	tips map[string]*Tip
}

// NewTipRegistry creates a registry with default tips.
func NewTipRegistry() *TipRegistry {
	return &TipRegistry{
		tips: map[string]*Tip{
			"compact": {ID: "compact", Category: TipCategoryUsage, Content: "Use /compact to free up context when conversations get long.", Priority: 1},
			"resume":  {ID: "resume", Category: TipCategoryUsage, Content: "Use /resume to continue a previous conversation.", Priority: 2},
			"plan":    {ID: "plan", Category: TipCategoryWorkflow, Content: "Use plan mode (EnterPlanMode) for complex tasks — explore first, then implement.", Priority: 3},
			"agent":   {ID: "agent", Category: TipCategoryFeature, Content: "Use the Agent tool to delegate subtasks to background workers.", Priority: 4},
			"history": {ID: "history", Category: TipCategoryUsage, Content: "Press Up arrow to browse your command history.", Priority: 5},
		},
	}
}

// GetTip returns a tip by ID.
func (r *TipRegistry) GetTip(id string) *Tip {
	return r.tips[id]
}

// AllTips returns all registered tips.
func (r *TipRegistry) AllTips() []*Tip {
	result := make([]*Tip, 0, len(r.tips))
	for _, t := range r.tips {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// TipHistory tracks which tips have been shown.
//
// Reference: src/services/tips/tipHistory.ts
type TipHistory struct {
	mu       sync.Mutex
	session  int
	shown    map[string]tipHistoryEntry
	filePath string
}

type tipHistoryEntry struct {
	Count       int `json:"count"`
	LastSession int `json:"last_session"`
}

type tipHistoryState struct {
	Session int                        `json:"session"`
	Shown   map[string]tipHistoryEntry `json:"shown"`
}

// NewTipHistory creates an empty history.
func NewTipHistory() *TipHistory {
	return &TipHistory{session: 1, shown: make(map[string]tipHistoryEntry)}
}

// NewPersistentTipHistory advances one project-local session and preserves
// which tip was least recently shown across process restarts.
func NewPersistentTipHistory(path string) (*TipHistory, error) {
	history := &TipHistory{filePath: path, shown: make(map[string]tipHistoryEntry)}
	data, err := os.ReadFile(path)
	if err == nil {
		var state tipHistoryState
		if json.Unmarshal(data, &state) == nil {
			history.session = state.Session
			if state.Shown != nil {
				history.shown = state.Shown
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	history.session++
	if err := history.saveLocked(); err != nil {
		return nil, err
	}
	return history, nil
}

// MarkShown records that a tip was shown.
func (h *TipHistory) MarkShown(tipID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.shown[tipID]
	entry.Count++
	entry.LastSession = h.session
	h.shown[tipID] = entry
	_ = h.saveLocked()
}

// TimesShown returns how many times a tip has been shown.
func (h *TipHistory) TimesShown(tipID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shown[tipID].Count
}

// SessionsSinceLastShown returns the rotation age for a tip.
func (h *TipHistory) SessionsSinceLastShown(tipID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.shown[tipID]
	if !ok {
		return int(^uint(0) >> 1)
	}
	return h.session - entry.LastSession
}

func (h *TipHistory) saveLocked() error {
	if h.filePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(h.filePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tipHistoryState{Session: h.session, Shown: h.shown}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.filePath, data, 0o644)
}

// TipScheduler determines which tip to show next.
//
// Reference: src/services/tips/tipScheduler.ts
type TipScheduler struct {
	registry *TipRegistry
	history  *TipHistory
}

// NewTipScheduler creates a least-shown, least-recently-shown scheduler.
func NewTipScheduler(registry *TipRegistry, history *TipHistory) *TipScheduler {
	return &TipScheduler{registry: registry, history: history}
}

// NextTip returns the next tip to show, or nil if none are due.
func (s *TipScheduler) NextTip() *Tip {
	var best *Tip
	for _, tip := range s.registry.AllTips() {
		if best == nil {
			best = tip
			continue
		}
		shown, bestShown := s.history.TimesShown(tip.ID), s.history.TimesShown(best.ID)
		age, bestAge := s.history.SessionsSinceLastShown(tip.ID), s.history.SessionsSinceLastShown(best.ID)
		if shown < bestShown ||
			(shown == bestShown && age > bestAge) ||
			(shown == bestShown && age == bestAge && tip.Priority < best.Priority) {
			best = tip
		}
	}
	return best
}

// MagicDocsModelFn calls a model for document maintenance.
type MagicDocsModelFn func(ctx context.Context, systemPrompt string, messages []string) (string, error)

// MagicDocsManager maintains markdown documents marked with MAGIC DOC headers.
// Runs periodically to update documents with new learnings from conversation.
//
// Reference: src/services/MagicDocs/magicDocs.ts (~200 lines)
type MagicDocsManager struct {
	modelFn  MagicDocsModelFn
	docPaths []string
	interval time.Duration
	cancel   context.CancelFunc
}

const magicDocHeader = "# MAGIC DOC:"

// NewMagicDocsManager creates a manager for auto-maintained documents.
func NewMagicDocsManager(modelFn MagicDocsModelFn, docPaths []string) *MagicDocsManager {
	return &MagicDocsManager{
		modelFn:  modelFn,
		docPaths: docPaths,
		interval: 5 * time.Minute,
	}
}

// IsMagicDoc checks if content starts with a MAGIC DOC header.
func IsMagicDoc(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), magicDocHeader)
}

// Start begins periodic document maintenance.
func (m *MagicDocsManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.updateDocs(ctx)
			}
		}
	}()
}

// Stop halts periodic maintenance.
func (m *MagicDocsManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *MagicDocsManager) updateDocs(ctx context.Context) {
	if m.modelFn == nil {
		return
	}
	for _, path := range m.docPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if !IsMagicDoc(content) {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		prompt := "Update this MAGIC DOC with any new information from the conversation. " +
			"Keep the existing structure. Only add genuinely new information.\n\n" +
			"Current document:\n" + content
		updated, err := m.modelFn(callCtx, prompt, nil)
		cancel()
		if err != nil || strings.TrimSpace(updated) == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(updated), magicDocHeader) {
			continue
		}
		_ = os.WriteFile(path, []byte(updated), 0o644)
	}
}

// PromptSuggester is deprecated — use PromptSuggestionService from prompt_suggestion.go.
// This type alias preserves backwards compatibility for any external callers.
type PromptSuggester = PromptSuggestionService

// NewPromptSuggester is deprecated — use NewPromptSuggestionService.
func NewPromptSuggester(modelFn PromptSuggestionModelFn) *PromptSuggester {
	return NewPromptSuggestionService(modelFn)
}
