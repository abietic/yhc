package promptctx

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// maxContextFileSize is the maximum file size for context files (1MB).
// Larger files are truncated with a warning appended.
const maxContextFileSize = 1 << 20 // 1MB

// ContextRefresher wraps InstructionDiscovery and provides cached prompt
// assembly with change detection. It is designed to be called between turns
// in the query loop to detect when instruction files have changed and signal
// the engine to rebuild context.
//
// The refresher also manages memory files, skill descriptions, tool
// descriptions, and dynamic context additions, assembling them all into
// a coherent system prompt in the correct priority order:
//
//	system prompt -> project instructions -> memory -> skills -> tools -> dynamic context
type ContextRefresher struct {
	mu sync.RWMutex

	// discovery is the underlying instruction discovery engine.
	discovery *InstructionDiscovery

	// cwd is the current working directory for discovery.
	cwd string

	// basePrompt is the static system prompt (identity, environment, capabilities).
	basePrompt string

	// customInstructions are user-specified additional instructions.
	customInstructions string

	// --- Cached prompt state ---

	// lastRendered holds the last-assembled prompt text.
	lastRendered string
	// lastHash is the SHA-256 hash of lastRendered for quick comparison.
	lastHash string
	// lastAssemblyTime records when the last assembly occurred.
	lastAssemblyTime time.Time

	// --- Memory context ---

	// memoryDir is the directory containing memory files (e.g., .claude/memory/).
	memoryDir string
	// memoryEntries are the loaded memory file contents.
	memoryEntries []MemoryEntry

	// --- Skill context ---

	// skills are the currently loaded skill descriptions.
	skills []SkillDescription

	// --- Tool context ---

	// tools are the current tool set descriptions.
	tools []ToolInfo

	// --- Dynamic context ---

	// dynamicPaths are files added mid-session via /context add.
	dynamicPaths map[string]*DynamicContextEntry
}

// MemoryEntry represents a loaded memory file.
type MemoryEntry struct {
	// Path is the absolute path to the memory file.
	Path string
	// Content is the file's text content.
	Content string
	// ModTime is the file modification time when loaded.
	ModTime time.Time
}

// SkillDescription represents a loaded skill for inclusion in context.
type SkillDescription struct {
	// Name is the skill identifier.
	Name string
	// Description is a short summary of the skill.
	Description string
	// FilePath is the source SKILL.md path (for reference).
	FilePath string
}

// DynamicContextEntry represents a file added to context mid-session.
type DynamicContextEntry struct {
	// Path is the absolute path to the file.
	Path string
	// Content is the loaded content (possibly truncated).
	Content string
	// AddedAt records when the file was added to context.
	AddedAt time.Time
	// Truncated indicates whether the content was truncated due to size.
	Truncated bool
}

// ContextRefresherConfig holds the configuration for creating a ContextRefresher.
type ContextRefresherConfig struct {
	// CWD is the current working directory.
	CWD string
	// BasePrompt is the static system prompt text.
	BasePrompt string
	// CustomInstructions are user-specified instructions.
	CustomInstructions string
	// MemoryDir is the path to the memory directory (optional).
	MemoryDir string
	// Cache is an optional pre-existing file cache.
	Cache *ClaudeMdCache
}

// NewContextRefresher creates a new ContextRefresher with the given configuration.
func NewContextRefresher(cfg ContextRefresherConfig) *ContextRefresher {
	discovery := NewInstructionDiscovery(cfg.Cache)

	return &ContextRefresher{
		discovery:          discovery,
		cwd:                cfg.CWD,
		basePrompt:         cfg.BasePrompt,
		customInstructions: cfg.CustomInstructions,
		memoryDir:          cfg.MemoryDir,
		dynamicPaths:       make(map[string]*DynamicContextEntry),
	}
}

// Initialize performs the first discovery pass and assembles the initial prompt.
// This should be called once at session start.
func (r *ContextRefresher) Initialize() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Discover instruction files
	if r.cwd != "" {
		if _, err := r.discovery.Discover(r.cwd); err != nil {
			return fmt.Errorf("initial instruction discovery: %w", err)
		}
	}

	// Load memory files
	r.loadMemoryLocked()

	// Assemble and cache the prompt
	r.assembleLocked()

	return nil
}

// Refresh checks if any context sources have changed and rebuilds the prompt
// if needed. Returns true if the prompt was rebuilt, false if no changes.
// This is the main entry point for the query loop to call between turns.
func (r *ContextRefresher) Refresh() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	changed := false

	// Check instruction file changes
	instructionsChanged, err := r.discovery.Refresh(r.cwd)
	if err != nil {
		return false, fmt.Errorf("refreshing instructions: %w", err)
	}
	if instructionsChanged {
		changed = true
	}

	// Check memory file changes
	if r.memoryChanged() {
		r.loadMemoryLocked()
		changed = true
	}

	// Check dynamic context file changes
	if r.dynamicContextChanged() {
		r.reloadDynamicContextLocked()
		changed = true
	}

	if !changed {
		return false, nil
	}

	// Rebuild prompt
	r.assembleLocked()
	return true, nil
}

// GetPrompt returns the last-assembled system prompt. This is cheap to call
// and does not perform any I/O; the prompt is only rebuilt on Refresh().
func (r *ContextRefresher) GetPrompt() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastRendered
}

// HasChanged returns true if any context source has been modified since the
// last assembly. This is a cheap check suitable for polling.
func (r *ContextRefresher) HasChanged() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.discovery.HasChanged() {
		return true
	}
	if r.memoryChanged() {
		return true
	}
	if r.dynamicContextChanged() {
		return true
	}
	return false
}

// LastAssemblyTime returns when the prompt was last assembled.
func (r *ContextRefresher) LastAssemblyTime() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastAssemblyTime
}

// --- Memory context management ---

// SetMemoryDir updates the memory directory path and reloads memory files.
func (r *ContextRefresher) SetMemoryDir(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memoryDir = dir
	r.loadMemoryLocked()
}

// GetMemoryEntries returns the currently loaded memory entries.
func (r *ContextRefresher) GetMemoryEntries() []MemoryEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MemoryEntry, len(r.memoryEntries))
	copy(result, r.memoryEntries)
	return result
}

// loadMemoryLocked loads all memory files from the memory directory.
// Must be called with r.mu held.
func (r *ContextRefresher) loadMemoryLocked() {
	r.memoryEntries = nil

	if r.memoryDir == "" {
		return
	}

	entries, err := os.ReadDir(r.memoryDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Only load .md files
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		fullPath := filepath.Join(r.memoryDir, entry.Name())
		content, truncated := readContextFile(fullPath)
		if content == "" {
			continue
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		if truncated {
			content += fmt.Sprintf("\n\n[WARNING: File %s was truncated at %d bytes]", entry.Name(), maxContextFileSize)
		}

		r.memoryEntries = append(r.memoryEntries, MemoryEntry{
			Path:    fullPath,
			Content: content,
			ModTime: info.ModTime(),
		})
	}
}

// memoryChanged checks if any memory files have been modified.
// Must be called with at least r.mu.RLock held.
func (r *ContextRefresher) memoryChanged() bool {
	if r.memoryDir == "" {
		return false
	}

	// Check if directory listing has changed (new/deleted files)
	entries, err := os.ReadDir(r.memoryDir)
	if err != nil {
		// Directory gone — counts as changed if we had entries.
		return len(r.memoryEntries) > 0
	}

	mdCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			mdCount++
		}
	}
	if mdCount != len(r.memoryEntries) {
		return true
	}

	// Check modification times of known entries
	for _, mem := range r.memoryEntries {
		info, err := os.Stat(mem.Path)
		if err != nil {
			return true
		}
		if !info.ModTime().Equal(mem.ModTime) {
			return true
		}
	}

	return false
}

// --- Skill context management ---

// SetSkills updates the active skill descriptions.
func (r *ContextRefresher) SetSkills(skills []SkillDescription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills = make([]SkillDescription, len(skills))
	copy(r.skills, skills)
}

// GetSkills returns the current skill descriptions.
func (r *ContextRefresher) GetSkills() []SkillDescription {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]SkillDescription, len(r.skills))
	copy(result, r.skills)
	return result
}

// --- Tool context management ---

// SetTools updates the current tool set.
func (r *ContextRefresher) SetTools(tools []ToolInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = make([]ToolInfo, len(tools))
	copy(r.tools, tools)
}

// GetTools returns the current tool descriptions.
func (r *ContextRefresher) GetTools() []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ToolInfo, len(r.tools))
	copy(result, r.tools)
	return result
}

// --- Dynamic context management ---

// AddContext adds a file to the session's dynamic context. The file is read
// immediately and its content cached. Returns an error if the file cannot
// be read or is not a valid text file.
func (r *ContextRefresher) AddContext(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	content, truncated := readContextFile(absPath)
	if content == "" {
		// Check if file exists but is empty vs doesn't exist
		if _, statErr := os.Stat(absPath); statErr != nil {
			return fmt.Errorf("file not found: %s", absPath)
		}
		return fmt.Errorf("file is empty or binary: %s", absPath)
	}

	if truncated {
		content += fmt.Sprintf("\n\n[WARNING: File was truncated at %d bytes]", maxContextFileSize)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.dynamicPaths[absPath] = &DynamicContextEntry{
		Path:      absPath,
		Content:   content,
		AddedAt:   time.Now(),
		Truncated: truncated,
	}

	return nil
}

// RemoveContext removes a file from the session's dynamic context.
// Returns true if the file was found and removed, false if it was not present.
func (r *ContextRefresher) RemoveContext(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.dynamicPaths[absPath]; ok {
		delete(r.dynamicPaths, absPath)
		return true
	}
	return false
}

// ListContext returns all currently active dynamic context sources.
func (r *ContextRefresher) ListContext() []DynamicContextEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]DynamicContextEntry, 0, len(r.dynamicPaths))
	for _, entry := range r.dynamicPaths {
		entries = append(entries, *entry)
	}
	return entries
}

// ClearDynamicContext removes all dynamic context entries (used on session reset).
func (r *ContextRefresher) ClearDynamicContext() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dynamicPaths = make(map[string]*DynamicContextEntry)
}

// dynamicContextChanged checks if any dynamic context files have been modified.
// Must be called with at least r.mu.RLock held.
func (r *ContextRefresher) dynamicContextChanged() bool {
	for path := range r.dynamicPaths {
		info, err := os.Stat(path)
		if err != nil {
			// File deleted — counts as changed.
			return true
		}
		_ = info // We don't track modtime for dynamic context currently;
		// dynamic files are re-read on explicit Refresh only if needed.
		// For now, rely on size changes as a heuristic.
		entry := r.dynamicPaths[path]
		contentLen := len(entry.Content)
		if entry.Truncated {
			// If truncated, actual file could have grown — check size.
			if info.Size() > int64(contentLen) {
				return true
			}
		} else {
			if info.Size() != int64(contentLen) {
				return true
			}
		}
	}
	return false
}

// reloadDynamicContextLocked reloads all dynamic context files.
// Must be called with r.mu held.
func (r *ContextRefresher) reloadDynamicContextLocked() {
	for path, entry := range r.dynamicPaths {
		content, truncated := readContextFile(path)
		if content == "" {
			// File gone or became empty/binary — remove it.
			delete(r.dynamicPaths, path)
			continue
		}
		if truncated {
			content += fmt.Sprintf("\n\n[WARNING: File was truncated at %d bytes]", maxContextFileSize)
		}
		entry.Content = content
		entry.Truncated = truncated
	}
}

// --- Prompt assembly ---

// assembleLocked rebuilds the cached prompt from all sources.
// Must be called with r.mu held.
func (r *ContextRefresher) assembleLocked() {
	var sections []string

	// 1. Base system prompt
	if trimmed := strings.TrimSpace(r.basePrompt); trimmed != "" {
		sections = append(sections, trimmed)
	}

	// 2. Project instructions (from InstructionDiscovery)
	instructionFiles := r.discovery.GetActiveFiles()
	if len(instructionFiles) > 0 {
		var instrParts []string
		for _, f := range instructionFiles {
			trimmed := strings.TrimSpace(f.Content)
			if trimmed == "" {
				continue
			}
			desc := sourceDescription(f.Source)
			instrParts = append(instrParts, fmt.Sprintf("Contents of %s%s:\n\n%s", f.Path, desc, trimmed))
		}
		if len(instrParts) > 0 {
			sections = append(sections, strings.Join(instrParts, "\n\n"))
		}
	}

	// 3. Memory context
	if len(r.memoryEntries) > 0 {
		var memParts []string
		memParts = append(memParts, "# Memory\n\nThe following memory entries are available from prior context:")
		for _, mem := range r.memoryEntries {
			trimmed := strings.TrimSpace(mem.Content)
			if trimmed != "" {
				memParts = append(memParts, fmt.Sprintf("## %s\n\n%s", filepath.Base(mem.Path), trimmed))
			}
		}
		if len(memParts) > 1 {
			sections = append(sections, strings.Join(memParts, "\n\n"))
		}
	}

	// 4. Skill descriptions
	if len(r.skills) > 0 {
		var skillParts []string
		skillParts = append(skillParts, "# Skills\n\nThe following skills are available:")
		for _, skill := range r.skills {
			entry := fmt.Sprintf("- **%s**", skill.Name)
			if skill.Description != "" {
				entry += ": " + skill.Description
			}
			skillParts = append(skillParts, entry)
		}
		sections = append(sections, strings.Join(skillParts, "\n"))
	}

	// 5. Tool descriptions
	if len(r.tools) > 0 {
		toolDesc := BuildToolDescriptions(r.tools)
		if trimmed := strings.TrimSpace(toolDesc); trimmed != "" {
			sections = append(sections, trimmed)
		}
	}

	// 6. Dynamic context
	if len(r.dynamicPaths) > 0 {
		var dynParts []string
		dynParts = append(dynParts, "# Additional Context\n\nThe following files have been added to the session context:")
		for _, entry := range r.dynamicPaths {
			trimmed := strings.TrimSpace(entry.Content)
			if trimmed != "" {
				dynParts = append(dynParts, fmt.Sprintf("## %s\n\n%s", filepath.Base(entry.Path), trimmed))
			}
		}
		if len(dynParts) > 1 {
			sections = append(sections, strings.Join(dynParts, "\n\n"))
		}
	}

	// 7. Custom instructions (highest priority — last)
	if trimmed := strings.TrimSpace(r.customInstructions); trimmed != "" {
		sections = append(sections, "# Custom Instructions\n\n"+trimmed)
	}

	rendered := strings.Join(sections, "\n\n")
	hash := computeHash(rendered)

	r.lastRendered = rendered
	r.lastHash = hash
	r.lastAssemblyTime = time.Now()
}

// --- File reading utilities ---

// readContextFile reads a file suitable for inclusion in context.
// It skips binary files, handles very large files by truncation, and
// handles various error conditions gracefully.
// Returns the content and whether it was truncated.
func readContextFile(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	// Skip directories
	if info.IsDir() {
		return "", false
	}

	// Skip symlinks that point to themselves (loop detection done via Lstat)
	linfo, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		// Resolve to check for loops
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			// Symlink loop or broken symlink — skip.
			return "", false
		}
		// If resolved path equals the original path, that's fine.
		// If it resolves to a directory, skip.
		resolvedInfo, err := os.Stat(resolved)
		if err != nil {
			return "", false
		}
		if resolvedInfo.IsDir() {
			return "", false
		}
	}

	// Skip empty files
	if info.Size() == 0 {
		return "", false
	}

	// Read file content (potentially truncated)
	truncated := false
	var data []byte

	if info.Size() > maxContextFileSize {
		// Read only the first maxContextFileSize bytes
		f, err := os.Open(path)
		if err != nil {
			return "", false
		}
		defer f.Close() //nolint:errcheck

		data = make([]byte, maxContextFileSize)
		n, err := f.Read(data)
		if err != nil && n == 0 {
			return "", false
		}
		data = data[:n]
		truncated = true
	} else {
		data, err = os.ReadFile(path)
		if err != nil {
			return "", false
		}
	}

	// Skip binary files: check if content is valid UTF-8 and doesn't contain
	// too many non-printable characters.
	if !isTextContent(data) {
		return "", false
	}

	return string(data), truncated
}

// isTextContent checks whether data appears to be text (not binary).
// It checks for valid UTF-8 and absence of null bytes in the first 8KB.
func isTextContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	// Check first 8KB for binary indicators
	checkLen := len(data)
	if checkLen > 8192 {
		checkLen = 8192
	}
	sample := data[:checkLen]

	// Null bytes are a strong indicator of binary content
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}

	// Check if it's valid UTF-8
	if !utf8.Valid(sample) {
		return false
	}

	return true
}

// computeHash returns a hex SHA-256 hash of the content.
func computeHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}
