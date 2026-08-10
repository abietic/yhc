package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/compact"
)

// SessionMemoryModelFn calls a model for session memory extraction.
type SessionMemoryModelFn func(ctx context.Context, systemPrompt string, messages []string) (string, error)

// SessionMemoryService periodically writes markdown session notes.
// Triggered based on tool call thresholds during the session.
//
// Reference: src/services/SessionMemory/sessionMemory.ts (~200 lines)
type SessionMemoryService struct {
	mu              sync.Mutex
	updateMu        sync.Mutex
	modelFn         SessionMemoryModelFn
	memoryDir       string
	sessionID       string
	toolCallCount   int
	initThreshold   int
	updateThreshold int
	initialized     bool
	lastUpdateCount int
	lastContent     string
}

// NewSessionMemoryService creates a session memory service.
func NewSessionMemoryService(modelFn SessionMemoryModelFn, memoryDir, sessionID string) *SessionMemoryService {
	return &SessionMemoryService{
		modelFn:         modelFn,
		memoryDir:       memoryDir,
		sessionID:       sessionID,
		initThreshold:   5,
		updateThreshold: 10,
	}
}

// RecordToolCall increments the tool call count and triggers update if threshold met.
func (s *SessionMemoryService) RecordToolCall(ctx context.Context, messages []string) {
	if s.recordToolCall() {
		s.update(ctx, messages)
	}
}

func (s *SessionMemoryService) recordToolCall() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCallCount++
	count := s.toolCallCount
	if !s.initialized && count >= s.initThreshold {
		s.initialized = true
		s.lastUpdateCount = count
		return true
	}
	if s.initialized && count-s.lastUpdateCount >= s.updateThreshold {
		s.lastUpdateCount = count
		return true
	}
	return false
}

func (s *SessionMemoryService) update(ctx context.Context, messages []string) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.modelFn == nil {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	prompt := `Extract key information from this coding session for future reference.
Write a brief markdown summary covering:
- What the user is working on (project/feature/bug)
- Key decisions made
- Current state/progress
- Any important context for resuming later

Keep it concise (5-15 lines). Update previous notes if provided.`

	s.mu.Lock()
	if s.lastContent != "" {
		prompt += "\n\nPrevious notes:\n" + s.lastContent
	}
	s.mu.Unlock()

	result, err := s.modelFn(callCtx, prompt, messages)
	if err != nil || strings.TrimSpace(result) == "" {
		return
	}

	s.mu.Lock()
	s.lastContent = result
	s.mu.Unlock()

	if s.memoryDir != "" {
		_ = os.MkdirAll(s.memoryDir, 0o755)
		path := filepath.Join(s.memoryDir, "session-memory-"+s.sessionID+".md")
		_ = os.WriteFile(path, []byte(result), 0o644)
	}
}

// GetContent returns the current session memory content.
func (s *SessionMemoryService) GetContent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastContent
}

// ExtractMemoriesModelFn calls a model for durable memory extraction.
type ExtractMemoriesModelFn func(ctx context.Context, prompt string, messages []string) (string, error)

// ExtractMemoriesService extracts durable memories at end of query loop.
// Writes to ~/.claude/projects/<path>/memory/ directory.
//
// Reference: src/services/extractMemories/extractMemories.ts (~200 lines)
type ExtractMemoriesService struct {
	modelFn     ExtractMemoriesModelFn
	memoryDir   string
	sessionID   string
	memoryStore *compact.MemoryStore
}

// NewExtractMemoriesService creates a memory extraction service.
func NewExtractMemoriesService(modelFn ExtractMemoriesModelFn, memoryDir string) *ExtractMemoriesService {
	return &ExtractMemoriesService{modelFn: modelFn, memoryDir: memoryDir}
}

// SetMemoryStore routes extracted durable memories into the project store.
func (s *ExtractMemoriesService) SetMemoryStore(store *compact.MemoryStore, sessionID string) {
	s.memoryStore = store
	s.sessionID = sessionID
}

// Extract runs memory extraction on the conversation.
func (s *ExtractMemoriesService) Extract(ctx context.Context, messages []string) error {
	if s.modelFn == nil || len(messages) < 3 {
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	prompt := `Extract any durable learnings from this coding session that would be useful
for future sessions. Focus on:
- Project-specific patterns or conventions discovered
- User preferences observed
- Important architectural decisions
- Recurring issues or solutions

Return a concise list of key-value learnings. If nothing notable, return empty.`

	result, err := s.modelFn(callCtx, prompt, messages)
	if err != nil || strings.TrimSpace(result) == "" {
		return err
	}

	if s.memoryDir != "" {
		_ = os.MkdirAll(s.memoryDir, 0o755)
		path := filepath.Join(s.memoryDir, "auto-"+time.Now().Format("2006-01-02")+".md")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck
		_, _ = f.WriteString("\n---\n" + result + "\n")
	}
	if s.memoryStore != nil {
		entries := make([]compact.MemoryEntry, 0, 10)
		for _, line := range strings.Split(result, "\n") {
			content := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if content == "" {
				continue
			}
			entries = append(entries, compact.MemoryEntry{
				Content: content, Category: "llm_extraction",
				Source: "session_" + s.sessionID, CreatedAt: time.Now(),
			})
			if len(entries) == 10 {
				break
			}
		}
		if err := s.memoryStore.AddAll(entries); err != nil {
			return err
		}
	}
	return nil
}

// AutoDreamService implements automatic memory consolidation.
// Time-gated: runs only after 24h since last dream, and after 5+ sessions.
//
// Reference: src/services/autoDream/autoDream.ts (~200 lines)
type AutoDreamService struct {
	mu            sync.Mutex
	modelFn       SessionMemoryModelFn
	transcriptDir string
	memoryDir     string
	sessionID     string
	memoryStore   *compact.MemoryStore
	minHoursSince int
	minSessions   int
	scanInterval  time.Duration
	lastScanAt    time.Time
	sessionCount  int
	now           func() time.Time
}

type autoDreamState struct {
	LastConsolidatedAt time.Time `json:"last_consolidated_at"`
}

// NewAutoDreamService creates an auto-dream service.
func NewAutoDreamService(modelFn SessionMemoryModelFn, memoryDir string) *AutoDreamService {
	return &AutoDreamService{
		modelFn: modelFn, memoryDir: memoryDir,
		minHoursSince: 24, minSessions: 5,
		scanInterval: 10 * time.Minute, now: time.Now,
	}
}

func newAutoDreamService(modelFn SessionMemoryModelFn, transcriptDir, memoryDir, sessionID string, store *compact.MemoryStore) *AutoDreamService {
	service := NewAutoDreamService(modelFn, memoryDir)
	service.transcriptDir = transcriptDir
	service.sessionID = sessionID
	service.memoryStore = store
	return service
}

// RecordSession increments the session count and checks if dream should trigger.
func (s *AutoDreamService) RecordSession() {
	s.mu.Lock()
	s.sessionCount++
	s.mu.Unlock()
}

// ShouldDream returns true if conditions are met for consolidation.
func (s *AutoDreamService) ShouldDream() bool {
	sessions, err := s.eligibleSessions(false)
	return err == nil && len(sessions) >= s.minSessions
}

// RunDream executes memory consolidation across past sessions.
func (s *AutoDreamService) RunDream(ctx context.Context, pastSessionSummaries []string) error {
	if s.modelFn == nil || !s.ShouldDream() {
		return nil
	}
	return s.runDream(ctx, pastSessionSummaries)
}

// RunIfDue checks persistent transcript evidence and performs one exclusive
// consolidation when the time and session gates pass.
func (s *AutoDreamService) RunIfDue(ctx context.Context) error {
	sessions, err := s.eligibleSessions(true)
	if err != nil || len(sessions) < s.minSessions || s.modelFn == nil {
		return err
	}
	release, acquired, err := s.acquireLock()
	if err != nil || !acquired {
		return err
	}
	defer release()
	summaries := make([]string, 0, len(sessions))
	for _, sessionID := range sessions {
		summary, readErr := s.readSessionSummary(sessionID)
		if readErr == nil && strings.TrimSpace(summary) != "" {
			summaries = append(summaries, summary)
		}
	}
	if len(summaries) == 0 {
		return nil
	}
	return s.runDream(ctx, summaries)
}

func (s *AutoDreamService) runDream(ctx context.Context, pastSessionSummaries []string) error {
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	prompt := `Consolidate memories from multiple past sessions into a coherent
summary. Identify patterns, recurring themes, and important context.
Remove duplicates and outdated information.`

	result, err := s.modelFn(callCtx, prompt, pastSessionSummaries)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result) == "" {
		return nil
	}

	s.mu.Lock()
	s.sessionCount = 0
	s.mu.Unlock()
	now := s.now()

	if s.memoryDir != "" {
		_ = os.MkdirAll(s.memoryDir, 0o755)
		path := filepath.Join(s.memoryDir, "consolidated-"+now.Format("2006-01-02")+".md")
		if err := os.WriteFile(path, []byte(result), 0o644); err != nil {
			return err
		}
	}
	if s.memoryStore != nil {
		if err := s.memoryStore.Add(compact.MemoryEntry{
			Content: result, Category: "dream_consolidation",
			Source: "auto_dream_session_" + s.sessionID, CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	if s.transcriptDir != "" {
		if err := s.writeState(autoDreamState{LastConsolidatedAt: now.UTC()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *AutoDreamService) eligibleSessions(throttle bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transcriptDir == "" {
		if s.sessionCount < s.minSessions {
			return nil, nil
		}
		return make([]string, s.sessionCount), nil
	}
	now := s.now()
	state, err := s.readState()
	if err != nil {
		return nil, err
	}
	if !state.LastConsolidatedAt.IsZero() && now.Sub(state.LastConsolidatedAt) < time.Duration(s.minHoursSince)*time.Hour {
		return nil, nil
	}
	if throttle && !s.lastScanAt.IsZero() && now.Sub(s.lastScanAt) < s.scanInterval {
		return nil, nil
	}
	if throttle {
		s.lastScanAt = now
	}
	entries, err := os.ReadDir(s.transcriptDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sessions := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if id == s.sessionID {
			continue
		}
		info, statErr := entry.Info()
		if statErr == nil && info.ModTime().After(state.LastConsolidatedAt) {
			sessions = append(sessions, id)
		}
	}
	sort.Strings(sessions)
	return sessions, nil
}

func (s *AutoDreamService) statePath() string {
	return filepath.Join(s.transcriptDir, ".auto-dream-state.json")
}

func (s *AutoDreamService) readState() (autoDreamState, error) {
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return autoDreamState{}, nil
	}
	if err != nil {
		return autoDreamState{}, err
	}
	var state autoDreamState
	if err := json.Unmarshal(data, &state); err != nil {
		return autoDreamState{}, fmt.Errorf("parse auto-dream state: %w", err)
	}
	return state, nil
}

func (s *AutoDreamService) writeState(state autoDreamState) error {
	if err := os.MkdirAll(s.transcriptDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.statePath(), data, 0o644)
}

func (s *AutoDreamService) acquireLock() (func(), bool, error) {
	if err := os.MkdirAll(s.transcriptDir, 0o700); err != nil {
		return func() {}, false, err
	}
	path := filepath.Join(s.transcriptDir, ".auto-dream.lock")
	open := func() (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	file, err := open()
	if errors.Is(err, os.ErrExist) {
		if info, statErr := os.Stat(path); statErr == nil && s.now().Sub(info.ModTime()) > 5*time.Minute {
			_ = os.Remove(path)
			file, err = open()
		}
	}
	if errors.Is(err, os.ErrExist) {
		return func() {}, false, nil
	}
	if err != nil {
		return func() {}, false, err
	}
	_, _ = file.WriteString(s.sessionID)
	_ = file.Close()
	return func() { _ = os.Remove(path) }, true, nil
}

func (s *AutoDreamService) readSessionSummary(sessionID string) (string, error) {
	path := filepath.Join(s.transcriptDir, sessionID, "session-memory-"+sessionID+".md")
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	path = filepath.Join(s.transcriptDir, sessionID+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	const maxSummaryBytes = 8000
	if info.Size() > maxSummaryBytes {
		_, _ = file.Seek(-maxSummaryBytes, io.SeekEnd)
	}
	data, err = io.ReadAll(io.LimitReader(file, maxSummaryBytes))
	return string(data), err
}

// BackgroundServices manages the lifecycle of all background services.
// It starts with the engine and stops on engine close.
type BackgroundServices struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	memory         *SessionMemoryService
	extract        *ExtractMemoriesService
	dream          *AutoDreamService
	running        bool
	memoryWorker   bool
	memoryPending  []string
	extractWorker  bool
	extractPending []string
	dreamWorker    bool
	dreamPending   bool
}

// BackgroundServicesConfig holds configuration for background services.
type BackgroundServicesConfig struct {
	ModelFn       SessionMemoryModelFn
	MemoryDir     string
	TranscriptDir string
	SessionID     string
	MemoryStore   *compact.MemoryStore
}

// NewBackgroundServices creates a new background services manager.
func NewBackgroundServices(cfg BackgroundServicesConfig) *BackgroundServices {
	ctx, cancel := context.WithCancel(context.Background())
	bs := &BackgroundServices{
		ctx:    ctx,
		cancel: cancel,
	}
	if cfg.ModelFn != nil {
		bs.memory = NewSessionMemoryService(cfg.ModelFn, cfg.MemoryDir, cfg.SessionID)
		sharedMemoryDir := filepath.Join(cfg.TranscriptDir, "memory")
		bs.extract = NewExtractMemoriesService(func(ctx context.Context, prompt string, messages []string) (string, error) {
			return cfg.ModelFn(ctx, prompt, messages)
		}, sharedMemoryDir)
		bs.extract.SetMemoryStore(cfg.MemoryStore, cfg.SessionID)
		bs.dream = newAutoDreamService(cfg.ModelFn, cfg.TranscriptDir, sharedMemoryDir, cfg.SessionID, cfg.MemoryStore)
	}
	return bs
}

// Start begins background service operation.
func (bs *BackgroundServices) Start() {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.running {
		return
	}
	bs.running = true
}

// Stop shuts down all background services.
func (bs *BackgroundServices) Stop() {
	_ = bs.Shutdown(context.Background())
}

// Shutdown cancels and joins all engine-owned background jobs.
func (bs *BackgroundServices) Shutdown(ctx context.Context) error {
	bs.mu.Lock()
	if !bs.running {
		bs.mu.Unlock()
		return nil
	}
	bs.running = false
	bs.memoryPending = nil
	bs.extractPending = nil
	bs.dreamPending = false
	bs.cancel()
	bs.mu.Unlock()
	done := make(chan struct{})
	go func() {
		bs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecordToolCall notifies the session memory service of a tool execution.
func (bs *BackgroundServices) RecordToolCall(messages []string) {
	bs.mu.Lock()
	mem := bs.memory
	running := bs.running
	bs.mu.Unlock()
	if !running || mem == nil || !mem.recordToolCall() {
		return
	}
	bs.queueMemoryUpdate(messages)
}

// RecordTurn schedules durable extraction and auto-dream checks at a natural
// turn boundary. Both jobs are coalesced and owned by the engine lifecycle.
func (bs *BackgroundServices) RecordTurn(messages []string) {
	bs.queueExtraction(messages)
	bs.queueDream()
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func (bs *BackgroundServices) queueMemoryUpdate(messages []string) {
	bs.mu.Lock()
	if !bs.running || bs.memory == nil {
		bs.mu.Unlock()
		return
	}
	bs.memoryPending = cloneStrings(messages)
	if bs.memoryWorker {
		bs.mu.Unlock()
		return
	}
	bs.memoryWorker = true
	bs.wg.Add(1)
	bs.mu.Unlock()
	go bs.runMemoryUpdates()
}

func (bs *BackgroundServices) runMemoryUpdates() {
	defer bs.wg.Done()
	for {
		bs.mu.Lock()
		if !bs.running || len(bs.memoryPending) == 0 {
			bs.memoryWorker = false
			bs.mu.Unlock()
			return
		}
		messages := bs.memoryPending
		bs.memoryPending = nil
		memory := bs.memory
		bs.mu.Unlock()
		memory.update(bs.ctx, messages)
	}
}

func (bs *BackgroundServices) queueExtraction(messages []string) {
	bs.mu.Lock()
	if !bs.running || bs.extract == nil || len(messages) < 3 {
		bs.mu.Unlock()
		return
	}
	bs.extractPending = cloneStrings(messages)
	if bs.extractWorker {
		bs.mu.Unlock()
		return
	}
	bs.extractWorker = true
	bs.wg.Add(1)
	bs.mu.Unlock()
	go bs.runExtractions()
}

func (bs *BackgroundServices) runExtractions() {
	defer bs.wg.Done()
	for {
		bs.mu.Lock()
		if !bs.running || len(bs.extractPending) == 0 {
			bs.extractWorker = false
			bs.mu.Unlock()
			return
		}
		messages := bs.extractPending
		bs.extractPending = nil
		extract := bs.extract
		bs.mu.Unlock()
		_ = extract.Extract(bs.ctx, messages)
	}
}

func (bs *BackgroundServices) queueDream() {
	bs.mu.Lock()
	if !bs.running || bs.dream == nil {
		bs.mu.Unlock()
		return
	}
	bs.dreamPending = true
	if bs.dreamWorker {
		bs.mu.Unlock()
		return
	}
	bs.dreamWorker = true
	bs.wg.Add(1)
	bs.mu.Unlock()
	go bs.runDreams()
}

func (bs *BackgroundServices) runDreams() {
	defer bs.wg.Done()
	for {
		bs.mu.Lock()
		if !bs.running || !bs.dreamPending {
			bs.dreamWorker = false
			bs.mu.Unlock()
			return
		}
		bs.dreamPending = false
		dream := bs.dream
		bs.mu.Unlock()
		_ = dream.RunIfDue(bs.ctx)
	}
}
