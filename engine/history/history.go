package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MaxHistoryItems        = 100
	MaxPastedContentLength = 1024
	historyFileName        = "history.jsonl"
	pasteStoreDir          = "paste-store"
	lockStaleTimeout       = 10 * time.Second
)

// PastedContent represents a paste entry with full content available.
type PastedContent struct {
	ID        int    `json:"id"`
	Type      string `json:"type"` // "text" or "image"
	Content   string `json:"content"`
	MediaType string `json:"mediaType,omitempty"`
	Filename  string `json:"filename,omitempty"`
}

// StoredPastedContent is the on-disk format — either inline or hash reference.
type StoredPastedContent struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`
	Content     string `json:"content,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	MediaType   string `json:"mediaType,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

// HistoryEntry is the user-facing history item with resolved content.
type HistoryEntry struct {
	Display        string                 `json:"display"`
	PastedContents map[int]*PastedContent `json:"pastedContents,omitempty"`
}

// logEntry is the on-disk JSONL format.
type logEntry struct {
	Display        string                       `json:"display"`
	PastedContents map[int]*StoredPastedContent `json:"pastedContents"`
	Timestamp      int64                        `json:"timestamp"`
	Project        string                       `json:"project"`
	SessionID      string                       `json:"sessionId,omitempty"`
}

// PasteReference is a parsed reference found in input text.
type PasteReference struct {
	ID    int
	Match string
	Index int
}

var referencePattern = regexp.MustCompile(`\[(Pasted text|Image|\.\.\.Truncated text) #(\d+)(?: \+\d+ lines)?(\.)*\]`)

// GetPastedTextRefNumLines counts newlines in pasted text (reference-compatible).
func GetPastedTextRefNumLines(text string) int {
	return strings.Count(text, "\n") + strings.Count(text, "\r") - strings.Count(text, "\r\n")
}

// FormatPastedTextRef formats a pasted text reference string.
func FormatPastedTextRef(id, numLines int) string {
	if numLines == 0 {
		return fmt.Sprintf("[Pasted text #%d]", id)
	}
	return fmt.Sprintf("[Pasted text #%d +%d lines]", id, numLines)
}

// FormatImageRef formats an image reference string.
func FormatImageRef(id int) string {
	return fmt.Sprintf("[Image #%d]", id)
}

// ParseReferences extracts all paste/image references from input text.
func ParseReferences(input string) []PasteReference {
	matches := referencePattern.FindAllStringSubmatchIndex(input, -1)
	var refs []PasteReference
	for _, loc := range matches {
		fullMatch := input[loc[0]:loc[1]]
		idStr := input[loc[4]:loc[5]]
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			continue
		}
		refs = append(refs, PasteReference{
			ID:    id,
			Match: fullMatch,
			Index: loc[0],
		})
	}
	return refs
}

// ExpandPastedTextRefs replaces [Pasted text #N] placeholders with actual content.
func ExpandPastedTextRefs(input string, pastedContents map[int]*PastedContent) string {
	refs := ParseReferences(input)
	expanded := input
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		content, ok := pastedContents[ref.ID]
		if !ok || content.Type != "text" {
			continue
		}
		expanded = expanded[:ref.Index] + content.Content + expanded[ref.Index+len(ref.Match):]
	}
	return expanded
}

// Manager manages prompt history with JSONL persistence, file locking,
// session-aware ordering, and paste store integration.
type Manager struct {
	mu                sync.Mutex
	configDir         string
	projectRoot       string
	sessionID         string
	pendingEntries    []*logEntry
	pendingPastes     map[string]string
	lastAddedEntry    *logEntry
	skippedTimestamps map[int64]bool
}

// NewManager creates a history manager.
func NewManager(configDir, projectRoot, sessionID string) *Manager {
	return &Manager{
		configDir:         configDir,
		projectRoot:       projectRoot,
		sessionID:         sessionID,
		pendingPastes:     make(map[string]string),
		skippedTimestamps: make(map[int64]bool),
	}
}

func (m *Manager) historyPath() string {
	return filepath.Join(m.configDir, historyFileName)
}

func (m *Manager) pasteStorePath() string {
	return filepath.Join(m.configDir, pasteStoreDir)
}

// Add adds a history entry. The write is deferred to Flush().
func (m *Manager) Add(entry HistoryEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	storedPasted := make(map[int]*StoredPastedContent)
	for id, content := range entry.PastedContents {
		if content.Type == "image" {
			continue
		}
		if len(content.Content) <= MaxPastedContentLength {
			storedPasted[id] = &StoredPastedContent{
				ID:        content.ID,
				Type:      content.Type,
				Content:   content.Content,
				MediaType: content.MediaType,
				Filename:  content.Filename,
			}
		} else {
			hash := hashPastedText(content.Content)
			storedPasted[id] = &StoredPastedContent{
				ID:          content.ID,
				Type:        content.Type,
				ContentHash: hash,
				MediaType:   content.MediaType,
				Filename:    content.Filename,
			}
			m.pendingPastes[hash] = content.Content
		}
	}

	le := &logEntry{
		Display:        entry.Display,
		PastedContents: storedPasted,
		Timestamp:      time.Now().UnixMilli(),
		Project:        m.projectRoot,
		SessionID:      m.sessionID,
	}
	m.pendingEntries = append(m.pendingEntries, le)
	m.lastAddedEntry = le
}

// AddSimple adds a simple text-only history entry.
func (m *Manager) AddSimple(display string) {
	m.Add(HistoryEntry{Display: display})
}

// RemoveLast undoes the most recent Add call (for interrupt rewind).
func (m *Manager) RemoveLast() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lastAddedEntry == nil {
		return
	}
	entry := m.lastAddedEntry
	m.lastAddedEntry = nil

	for i := len(m.pendingEntries) - 1; i >= 0; i-- {
		if m.pendingEntries[i] == entry {
			m.pendingEntries = append(m.pendingEntries[:i], m.pendingEntries[i+1:]...)
			return
		}
	}
	m.skippedTimestamps[entry.Timestamp] = true
}

// ClearPending resets the pending buffer and skip set.
func (m *Manager) ClearPending() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingEntries = nil
	m.lastAddedEntry = nil
	m.pendingPastes = make(map[string]string)
	m.skippedTimestamps = make(map[int64]bool)
}

// GetHistory returns history entries for the current project with current
// session entries first (prevents interleaving in concurrent sessions).
func (m *Manager) GetHistory() ([]HistoryEntry, error) {
	m.mu.Lock()
	pending := make([]*logEntry, len(m.pendingEntries))
	copy(pending, m.pendingEntries)
	skipped := make(map[int64]bool, len(m.skippedTimestamps))
	for k, v := range m.skippedTimestamps {
		skipped[k] = v
	}
	m.mu.Unlock()

	var currentSession []*logEntry
	var otherSessions []*logEntry

	for i := len(pending) - 1; i >= 0; i-- {
		e := pending[i]
		if e.Project != m.projectRoot {
			continue
		}
		if e.SessionID == m.sessionID {
			currentSession = append(currentSession, e)
		} else {
			otherSessions = append(otherSessions, e)
		}
	}

	diskEntries, err := m.readLogEntries()
	if err != nil {
		return nil, err
	}

	for _, e := range diskEntries {
		if e.Project != m.projectRoot {
			continue
		}
		if e.SessionID == m.sessionID && skipped[e.Timestamp] {
			continue
		}
		if e.SessionID == m.sessionID {
			currentSession = append(currentSession, e)
		} else {
			otherSessions = append(otherSessions, e)
		}
		if len(currentSession)+len(otherSessions) >= MaxHistoryItems {
			break
		}
	}

	var result []HistoryEntry
	for _, e := range currentSession {
		he := m.resolveLogEntry(e)
		result = append(result, he)
		if len(result) >= MaxHistoryItems {
			return result, nil
		}
	}
	for _, e := range otherSessions {
		if len(result) >= MaxHistoryItems {
			break
		}
		he := m.resolveLogEntry(e)
		result = append(result, he)
	}
	return result, nil
}

// GetDisplayHistory returns just the display strings for TUI up-arrow,
// newest first, deduped, project-scoped.
func (m *Manager) GetDisplayHistory() []string {
	entries, err := m.GetHistory()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, e := range entries {
		if seen[e.Display] {
			continue
		}
		seen[e.Display] = true
		result = append(result, e.Display)
	}
	return result
}

// Flush writes all pending entries to disk. Called on process exit.
func (m *Manager) Flush() error {
	m.mu.Lock()
	entries := m.pendingEntries
	pastes := m.pendingPastes
	m.pendingEntries = nil
	m.pendingPastes = make(map[string]string)
	m.mu.Unlock()

	for hash, content := range pastes {
		m.storePastedText(hash, content)
	}
	if len(entries) == 0 {
		return nil
	}
	return m.writeEntries(entries)
}

func (m *Manager) writeEntries(entries []*logEntry) error {
	path := m.historyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	lockPath := path + ".lock"
	unlock, err := acquireFileLock(lockPath, lockStaleTimeout)
	if err != nil {
		return fmt.Errorf("history lock: %w", err)
	}
	defer unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		_, _ = f.Write(data)
		_, _ = f.WriteString("\n")
	}
	return nil
}

func (m *Manager) readLogEntries() ([]*logEntry, error) {
	path := m.historyPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	var all []*logEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		all = append(all, &e)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp > all[j].Timestamp
	})
	return all, nil
}

func (m *Manager) resolveLogEntry(e *logEntry) HistoryEntry {
	he := HistoryEntry{
		Display:        e.Display,
		PastedContents: make(map[int]*PastedContent),
	}
	for id, stored := range e.PastedContents {
		resolved := m.resolveStoredContent(stored)
		if resolved != nil {
			he.PastedContents[id] = resolved
		}
	}
	return he
}

func (m *Manager) resolveStoredContent(stored *StoredPastedContent) *PastedContent {
	if stored.Content != "" {
		return &PastedContent{
			ID:        stored.ID,
			Type:      stored.Type,
			Content:   stored.Content,
			MediaType: stored.MediaType,
			Filename:  stored.Filename,
		}
	}
	if stored.ContentHash != "" {
		content, err := m.retrievePastedText(stored.ContentHash)
		if err == nil && content != "" {
			return &PastedContent{
				ID:        stored.ID,
				Type:      stored.Type,
				Content:   content,
				MediaType: stored.MediaType,
				Filename:  stored.Filename,
			}
		}
	}
	return nil
}

// Paste store operations

func hashPastedText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func (m *Manager) storePastedText(hash, content string) {
	dir := m.pasteStorePath()
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, hash)
	_ = os.WriteFile(path, []byte(content), 0o600)
}

func (m *Manager) retrievePastedText(hash string) (string, error) {
	path := filepath.Join(m.pasteStorePath(), hash)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// File locking (simple lockfile implementation)

func acquireFileLock(lockPath string, staleTimeout time.Duration) (func(), error) {
	for retries := 0; retries < 3; retries++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			continue
		}
		if time.Since(info.ModTime()) > staleTimeout {
			_ = os.Remove(lockPath)
			continue
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(lockPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, nil
	}
	_ = f.Close()
	return func() { _ = os.Remove(lockPath) }, nil
}
