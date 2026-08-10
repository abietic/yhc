package compact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudwego/eino/schema"
)

// memoryFileData is the on-disk JSON representation of session memory.
type memoryFileData struct {
	Entries     []memoryFileEntry `json:"entries"`
	LastUpdated time.Time         `json:"last_updated"`
}

// memoryFileEntry is the JSON representation of a single memory entry.
type memoryFileEntry struct {
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// MemoryStore provides persistent storage for session memory entries.
// It supports adding, loading, saving, searching, and pruning memories.
type MemoryStore struct {
	entries    []MemoryEntry
	filePath   string
	lockPath   string
	maxEntries int
	loaded     bool
	mu         sync.RWMutex
}

// NewMemoryStore creates a new MemoryStore that persists to sessionDir/memory.json.
func NewMemoryStore(sessionDir string, maxEntries int) *MemoryStore {
	return &MemoryStore{
		entries:    make([]MemoryEntry, 0),
		filePath:   filepath.Join(sessionDir, "memory.json"),
		lockPath:   filepath.Join(sessionDir, ".memory.lock"),
		maxEntries: maxEntries,
	}
}

// Add adds a memory entry to the store and persists to disk.
// If adding the entry would exceed maxEntries, the oldest entry is removed.
func (s *MemoryStore) Add(entry MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	if err := s.loadLocked(); err != nil {
		return err
	}

	s.entries = append(s.entries, entry)

	// Enforce max entries by removing oldest
	if s.maxEntries > 0 && len(s.entries) > s.maxEntries {
		s.entries = s.entries[len(s.entries)-s.maxEntries:]
	}

	return s.saveLocked()
}

// AddAll adds memory entries and persists them in one write.
func (s *MemoryStore) AddAll(entries []MemoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	if err := s.loadLocked(); err != nil {
		return err
	}
	s.entries = append(s.entries, entries...)
	if s.maxEntries > 0 && len(s.entries) > s.maxEntries {
		s.entries = s.entries[len(s.entries)-s.maxEntries:]
	}
	return s.saveLocked()
}

// Load reads memory entries from the JSON file on disk.
// If the file does not exist, this is a no-op (starts with empty entries).
func (s *MemoryStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		s.entries = nil
		s.loaded = true
		return nil
	}
	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	return s.loadLocked()
}

func (s *MemoryStore) loadLocked() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.loaded = true
			return nil
		}
		return fmt.Errorf("reading memory file: %w", err)
	}

	var fileData memoryFileData
	if err := json.Unmarshal(data, &fileData); err != nil {
		return fmt.Errorf("parsing memory file: %w", err)
	}

	s.entries = make([]MemoryEntry, 0, len(fileData.Entries))
	for _, fe := range fileData.Entries {
		s.entries = append(s.entries, MemoryEntry{
			Content:   fe.Content,
			Category:  fe.Category,
			Source:    fe.Source,
			CreatedAt: fe.CreatedAt,
		})
	}
	s.loaded = true

	return nil
}

func (s *MemoryStore) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	return s.loadLocked()
}

func (s *MemoryStore) acquireFileLock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return func() {}, fmt.Errorf("creating memory lock directory: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(s.lockPath) }, nil
		}
		if !os.IsExist(err) {
			return func() {}, fmt.Errorf("acquiring memory lock: %w", err)
		}
		if info, statErr := os.Stat(s.lockPath); statErr == nil && time.Since(info.ModTime()) > 10*time.Second {
			_ = os.Remove(s.lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return func() {}, fmt.Errorf("acquiring memory lock: timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Save persists the current entries to disk in JSON format.
func (s *MemoryStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	return s.saveLocked()
}

// saveLocked writes entries to disk. Caller must hold at least a read lock.
func (s *MemoryStore) saveLocked() error {
	fileData := memoryFileData{
		Entries:     make([]memoryFileEntry, 0, len(s.entries)),
		LastUpdated: time.Now(),
	}

	for _, entry := range s.entries {
		fileData.Entries = append(fileData.Entries, memoryFileEntry{
			Content:   entry.Content,
			Category:  entry.Category,
			Source:    entry.Source,
			CreatedAt: entry.CreatedAt,
		})
	}

	data, err := json.MarshalIndent(fileData, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling memory data: %w", err)
	}

	// Ensure the directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0o644); err != nil {
		return fmt.Errorf("writing memory file: %w", err)
	}

	return nil
}

// GetAll returns a copy of all memory entries.
func (s *MemoryStore) GetAll() []MemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]MemoryEntry, len(s.entries))
	copy(result, s.entries)
	return result
}

// GetRecent returns the n most recent memory entries.
// If n exceeds the total number of entries, all entries are returned.
func (s *MemoryStore) GetRecent(n int) []MemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 {
		return nil
	}

	total := len(s.entries)
	if n > total {
		n = total
	}

	result := make([]MemoryEntry, n)
	copy(result, s.entries[total-n:])
	return result
}

// Search performs a simple case-insensitive keyword search across memory entries.
// Returns entries whose content or category contains the query string.
func (s *MemoryStore) Search(query string) []MemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if query == "" {
		return nil
	}

	queryLower := strings.ToLower(query)
	var results []MemoryEntry

	for _, entry := range s.entries {
		contentLower := strings.ToLower(entry.Content)
		categoryLower := strings.ToLower(entry.Category)

		if strings.Contains(contentLower, queryLower) || strings.Contains(categoryLower, queryLower) {
			results = append(results, entry)
		}
	}

	return results
}

// GetRelevant ranks memories against a query, then uses recency as a stable
// fallback and tie-breaker. Relevant older entries therefore outrank recent
// unrelated entries without making empty-query retrieval nondeterministic.
func (s *MemoryStore) GetRelevant(query string, limit int) []MemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || len(s.entries) == 0 {
		return nil
	}
	type rankedEntry struct {
		entry MemoryEntry
		score int
		index int
	}
	query = strings.TrimSpace(strings.ToLower(query))
	terms := memoryQueryTerms(query)
	ranked := make([]rankedEntry, 0, len(s.entries))
	for index, entry := range s.entries {
		content := strings.ToLower(entry.Content)
		metadata := strings.ToLower(entry.Category + " " + entry.Source)
		score := 0
		if query != "" && strings.Contains(content, query) {
			score += 1000
		}
		for _, term := range terms {
			if strings.Contains(content, term) {
				score += 100
			} else if strings.Contains(metadata, term) {
				score += 40
			}
		}
		ranked = append(ranked, rankedEntry{entry: entry, score: score, index: index})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if !ranked[i].entry.CreatedAt.Equal(ranked[j].entry.CreatedAt) {
			return ranked[i].entry.CreatedAt.After(ranked[j].entry.CreatedAt)
		}
		return ranked[i].index > ranked[j].index
	})
	if limit > len(ranked) {
		limit = len(ranked)
	}
	result := make([]MemoryEntry, limit)
	for index := range limit {
		result[index] = ranked[index].entry
	}
	return result
}

func memoryQueryTerms(query string) []string {
	parts := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(parts))
	terms := make([]string, 0, len(parts))
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	for _, part := range parts {
		add(part)
		runes := []rune(part)
		containsCJK := false
		for _, r := range runes {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				containsCJK = true
				break
			}
		}
		if containsCJK {
			for index := 0; index+1 < len(runes); index++ {
				add(string(runes[index : index+2]))
			}
		}
	}
	return terms
}

// Prune removes entries older than maxAge from the store.
func (s *MemoryStore) Prune(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	var kept []MemoryEntry

	for _, entry := range s.entries {
		if entry.CreatedAt.After(cutoff) {
			kept = append(kept, entry)
		}
	}

	s.entries = kept
}

// BuildContext formats all memory entries into a string suitable for
// injection into a system prompt. Groups entries by category.
func (s *MemoryStore) BuildContext() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.entries) == 0 {
		return ""
	}

	// Group entries by category
	grouped := make(map[string][]MemoryEntry)
	var categories []string
	for _, entry := range s.entries {
		cat := entry.Category
		if cat == "" {
			cat = "general"
		}
		if _, exists := grouped[cat]; !exists {
			categories = append(categories, cat)
		}
		grouped[cat] = append(grouped[cat], entry)
	}

	// Sort categories for deterministic output
	sortStrings(categories)

	var sb strings.Builder
	sb.WriteString("[Session Memory]\n\n")

	for i, cat := range categories {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "## %s\n", cat)
		for _, entry := range grouped[cat] {
			fmt.Fprintf(&sb, "- %s\n", entry.Content)
		}
	}

	return sb.String()
}

// CompactSessionMemory extracts memories from messages and adds them to the store.
// It uses ExtractMemoryFromMessages for deterministic extraction, adds all
// extracted entries to the store, and persists the result to disk.
func CompactSessionMemory(messages []*schema.Message, store *MemoryStore) error {
	entries := ExtractMemoryFromMessages(messages)
	if len(entries) == 0 {
		return nil
	}

	for _, entry := range entries {
		if err := store.Add(entry); err != nil {
			return fmt.Errorf("adding memory entry: %w", err)
		}
	}

	return nil
}

// sortStrings sorts a slice of strings in place (avoids importing sort for this small helper).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
