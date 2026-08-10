package compact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MemoryEntry represents a single piece of session memory extracted during compaction.
type MemoryEntry struct {
	// Content is the memory text (a fact, decision, or context piece)
	Content string
	// Category classifies the memory (e.g., "decision", "fact", "preference", "context")
	Category string
	// CreatedAt is when this memory was extracted
	CreatedAt time.Time
	// Source describes where this memory came from (e.g., "compact_turn_5")
	Source string
}

// SessionMemory stores extracted memory entries that persist across compactions.
type SessionMemory struct {
	mu      sync.RWMutex
	entries []MemoryEntry
}

// NewSessionMemory creates an empty SessionMemory instance.
func NewSessionMemory() *SessionMemory {
	return &SessionMemory{
		entries: make([]MemoryEntry, 0),
	}
}

// AddEntry adds a memory entry to the session memory store.
func (m *SessionMemory) AddEntry(entry MemoryEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
}

// GetEntries returns a copy of all current memory entries.
func (m *SessionMemory) GetEntries() []MemoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]MemoryEntry, len(m.entries))
	copy(result, m.entries)
	return result
}

// FormatForReinjection formats memory entries into a system message
// suitable for inclusion in post-compact context.
func (m *SessionMemory) FormatForReinjection() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.entries) == 0 {
		return ""
	}

	// Group entries by category
	grouped := make(map[string][]MemoryEntry)
	var categories []string
	for _, entry := range m.entries {
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
	sort.Strings(categories)

	var sb strings.Builder
	sb.WriteString("[Session Memory — preserved across compaction:]\n\n")

	for i, cat := range categories {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "## %s\n", strings.Title(cat)) //nolint:staticcheck
		for _, entry := range grouped[cat] {
			fmt.Fprintf(&sb, "- %s", entry.Content)
			if entry.Source != "" {
				fmt.Fprintf(&sb, " [source: %s]", entry.Source)
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// --- Deterministic extraction (non-LLM fallback) ---

// filePathPattern matches common file path references in message content.
var filePathPattern = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])(/[a-zA-Z0-9_\-./]+\.[a-zA-Z0-9]+)`)

// preferencePatterns match explicit user preference statements.
var preferencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:i\s+)?prefer\b`),
	regexp.MustCompile(`(?i)\balways\s+(?:use|do|make|keep)\b`),
	regexp.MustCompile(`(?i)\bnever\s+(?:use|do|make|put)\b`),
}

// decisionPatterns match key decision statements.
var decisionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:i\s+)?decided\b`),
	regexp.MustCompile(`(?i)\blet'?s\s+`),
	regexp.MustCompile(`(?i)\bwe'?ll\s+`),
}

// ExtractMemoryFromMessages performs deterministic memory extraction from
// a set of messages being compacted. This is the non-LLM fallback that
// extracts obvious facts like file paths mentioned, tool names used,
// and explicit user preferences stated.
func ExtractMemoryFromMessages(messages []*schema.Message) []MemoryEntry {
	if len(messages) == 0 {
		return nil
	}

	now := time.Now()
	var entries []MemoryEntry

	// Track tool usage frequencies
	toolUsage := make(map[string]int)
	// Track file paths from tool calls
	filePathsSeen := make(map[string]bool)

	for idx, msg := range messages {
		if msg == nil {
			continue
		}
		source := fmt.Sprintf("compact_turn_%d", idx)

		// Extract tool names from assistant tool calls
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "" {
				toolUsage[tc.Function.Name]++

				// Extract file paths from Read/Edit/Write tool arguments
				if isFileToolCall(tc.Function.Name) {
					paths := extractPathsFromArgs(tc.Function.Arguments)
					for _, p := range paths {
						if !filePathsSeen[p] {
							filePathsSeen[p] = true
							entries = append(entries, MemoryEntry{
								Content:   fmt.Sprintf("File referenced: %s", p),
								Category:  "fact",
								CreatedAt: now,
								Source:    source,
							})
						}
					}
				}
			}
		}

		// Extract preferences and decisions from user messages
		if msg.Role == schema.User && msg.Content != "" {
			// Check preference patterns
			for _, pat := range preferencePatterns {
				if pat.MatchString(msg.Content) {
					// Extract the sentence containing the preference
					sentence := extractRelevantSentence(msg.Content, pat)
					if sentence != "" {
						entries = append(entries, MemoryEntry{
							Content:   sentence,
							Category:  "preference",
							CreatedAt: now,
							Source:    source,
						})
					}
					break
				}
			}

			// Check decision patterns
			for _, pat := range decisionPatterns {
				if pat.MatchString(msg.Content) {
					sentence := extractRelevantSentence(msg.Content, pat)
					if sentence != "" {
						entries = append(entries, MemoryEntry{
							Content:   sentence,
							Category:  "decision",
							CreatedAt: now,
							Source:    source,
						})
					}
					break
				}
			}
		}

		// Extract file paths mentioned in any message content
		if msg.Content != "" {
			matches := filePathPattern.FindAllStringSubmatch(msg.Content, -1)
			for _, match := range matches {
				if len(match) > 1 {
					p := match[1]
					if !filePathsSeen[p] && looksLikeRealPath(p) {
						filePathsSeen[p] = true
						entries = append(entries, MemoryEntry{
							Content:   fmt.Sprintf("File referenced: %s", p),
							Category:  "fact",
							CreatedAt: now,
							Source:    source,
						})
					}
				}
			}
		}
	}

	// Summarize frequently used tools
	if len(toolUsage) > 0 {
		type toolCount struct {
			name  string
			count int
		}
		var sorted []toolCount
		for name, count := range toolUsage {
			if count >= 2 {
				sorted = append(sorted, toolCount{name, count})
			}
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].count > sorted[j].count
		})

		if len(sorted) > 0 {
			var toolSummary []string
			for _, tc := range sorted {
				toolSummary = append(toolSummary, fmt.Sprintf("%s (%dx)", tc.name, tc.count))
			}
			entries = append(entries, MemoryEntry{
				Content:   fmt.Sprintf("Frequently used tools: %s", strings.Join(toolSummary, ", ")),
				Category:  "context",
				CreatedAt: now,
				Source:    "compact_summary",
			})
		}
	}

	return entries
}

// BuildMemoryExtractionPrompt builds a prompt for LLM-based memory extraction.
// Used when a ChatModel is available during compaction.
func BuildMemoryExtractionPrompt(messages []*schema.Message) string {
	var sb strings.Builder

	sb.WriteString(`You are a memory extraction assistant. Your task is to extract key facts, decisions, preferences, and important context from the following conversation that should be remembered across conversation compactions.

For each memory item, output it in the following format (one per line):
[category] content

Valid categories are:
- [fact] — concrete facts like file paths, architecture details, technology choices
- [decision] — decisions made during the conversation
- [preference] — user preferences or stated ways of working
- [context] — important context that would be lost without explicit preservation

Rules:
- Extract only information that would be valuable to remember in future interactions
- Be concise — each memory should be one clear sentence
- Focus on actionable and specific information, not general discussion
- Do not extract trivial or obvious information
- Maximum 15 memory items

Conversation to extract memories from:
`)

	for _, msg := range messages {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User:
			fmt.Fprintf(&sb, "\nUser: %s\n", truncateForPrompt(msg.Content, 500))
		case schema.Assistant:
			content := msg.Content
			if content == "" && len(msg.ToolCalls) > 0 {
				var toolNames []string
				for _, tc := range msg.ToolCalls {
					if tc.Function.Name != "" {
						toolNames = append(toolNames, tc.Function.Name)
					}
				}
				content = fmt.Sprintf("[Tool calls: %s]", strings.Join(toolNames, ", "))
			}
			fmt.Fprintf(&sb, "\nAssistant: %s\n", truncateForPrompt(content, 500))
		case schema.Tool:
			fmt.Fprintf(&sb, "\nTool (%s): %s\n", msg.ToolName, truncateForPrompt(msg.Content, 200))
		}
	}

	sb.WriteString(`
Now extract the key memories from this conversation. Output only the memory items, one per line, in [category] format.`)

	return sb.String()
}

// --- helpers ---

// isFileToolCall returns true if the tool name is a file-operation tool.
func isFileToolCall(name string) bool {
	switch strings.ToLower(name) {
	case "read", "edit", "write", "glob", "grep":
		return true
	}
	return false
}

// pathArgPattern matches common JSON key patterns for file path arguments.
var pathArgPattern = regexp.MustCompile(`"(?:file_path|path|filename|file)":\s*"([^"]+)"`)

// extractPathsFromArgs extracts file paths from JSON-like tool call arguments.
func extractPathsFromArgs(args string) []string {
	matches := pathArgPattern.FindAllStringSubmatch(args, -1)
	var paths []string
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			paths = append(paths, match[1])
		}
	}
	return paths
}

// extractRelevantSentence extracts the sentence from text that matches the pattern.
func extractRelevantSentence(text string, pat *regexp.Regexp) string {
	loc := pat.FindStringIndex(text)
	if loc == nil {
		return ""
	}

	// Find sentence boundaries around the match
	start := loc[0]
	end := loc[1]

	// Walk back to sentence start
	for start > 0 && text[start-1] != '.' && text[start-1] != '\n' && text[start-1] != '!' && text[start-1] != '?' {
		start--
	}

	// Walk forward to sentence end
	for end < len(text) && text[end] != '.' && text[end] != '\n' && text[end] != '!' && text[end] != '?' {
		end++
	}
	if end < len(text) && (text[end] == '.' || text[end] == '!' || text[end] == '?') {
		end++
	}

	sentence := strings.TrimSpace(text[start:end])
	// Limit length
	if len(sentence) > 200 {
		sentence = sentence[:200] + "..."
	}
	return sentence
}

// looksLikeRealPath does a basic sanity check on extracted file paths.
func looksLikeRealPath(p string) bool {
	// Must have at least one directory separator and a file extension
	if !strings.Contains(p, "/") {
		return false
	}
	// Skip very short or suspicious paths
	if len(p) < 5 {
		return false
	}
	// Skip paths that are likely URL fragments
	if strings.Contains(p, "://") {
		return false
	}
	return true
}

// truncateForPrompt truncates content for inclusion in the extraction prompt.
func truncateForPrompt(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "..."
}
