package compact

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	// maxFileStateReinjection limits how many files are included in the
	// file-state reinjection message to avoid context bloat.
	maxFileStateReinjection = 10
)

// PostCompactState holds metrics and context produced by the post-compact pipeline.
// Mirrors the telemetry shape from the reference postCompactCleanup.ts.
type PostCompactState struct {
	// OriginalMessageCount is the number of messages before compaction.
	OriginalMessageCount int
	// CompactedMessageCount is the number of messages after cleanup.
	CompactedMessageCount int
	// TokensSaved estimates how many tokens were saved by compaction.
	TokensSaved int
	// FilesReferenced lists file paths mentioned in compacted messages.
	FilesReferenced []string
	// ActiveFiles lists files currently being worked on (candidates for reinjection).
	ActiveFiles []string
}

// PostCompactCleanup ensures post-compacted messages maintain valid structure:
//   - Removes orphaned tool results without corresponding tool calls
//   - Ensures the conversation starts with a proper role (user or system)
//   - Validates no duplicate message IDs
//   - Preserves valid assistant+tool result pairs
//
// Mirrors reference postCompactCleanup.ts runPostCompactCleanup behavior.
func PostCompactCleanup(messages []*schema.Message, state *PostCompactState) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	// Step 1: Collect all tool call IDs from assistant messages
	validToolCallIDs := make(map[string]bool)
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					validToolCallIDs[tc.ID] = true
				}
			}
		}
	}

	// Step 2: Remove orphaned tool results (tool messages whose ToolCallID
	// doesn't correspond to any known tool call in the conversation)
	var cleaned []*schema.Message
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			if !validToolCallIDs[msg.ToolCallID] {
				// Orphaned tool result — skip it
				continue
			}
		}
		cleaned = append(cleaned, msg)
	}

	// Step 3: Deduplicate by message ID (Extra["message_id"] or Extra["id"])
	seen := make(map[string]bool)
	var deduped []*schema.Message
	for _, msg := range cleaned {
		msgID := getMessageExtraID(msg)
		if msgID != "" {
			if seen[msgID] {
				continue
			}
			seen[msgID] = true
		}
		deduped = append(deduped, msg)
	}

	// Step 4: Ensure conversation starts with a proper role.
	// The API requires the first message to be user or system. If it starts
	// with assistant or tool, we need to handle that.
	result := ensureProperStart(deduped)

	// Update state
	if state != nil {
		state.CompactedMessageCount = len(result)
	}

	return result
}

// ensureProperStart ensures messages begin with a user or system message.
// If the first message is an assistant or tool message, a synthetic user
// marker is prepended (same pattern as TruncateHeadForPTLRetry).
func ensureProperStart(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	first := messages[0]
	if first.Role == schema.User || first.Role == schema.System {
		return messages
	}

	// Prepend a synthetic user context marker
	marker := &schema.Message{
		Role:    schema.User,
		Content: "[Conversation continued from previous context]",
		Extra: map[string]any{
			"subtype": "post_compact_marker",
			"isMeta":  true,
		},
	}
	return append([]*schema.Message{marker}, messages...)
}

// getMessageExtraID extracts a unique message ID from Extra metadata.
func getMessageExtraID(msg *schema.Message) string {
	if msg == nil || msg.Extra == nil {
		return ""
	}
	if id, ok := msg.Extra["message_id"].(string); ok && id != "" {
		return id
	}
	if id, ok := msg.Extra["id"].(string); ok && id != "" {
		return id
	}
	return ""
}

// fileToolCallPattern matches tool names that operate on files.
var fileToolNames = map[string]bool{
	"read":  true,
	"write": true,
	"edit":  true,
	"glob":  true,
	"grep":  true,
	"Read":  true,
	"Write": true,
	"Edit":  true,
	"Glob":  true,
	"Grep":  true,
}

// fileArgPathRe matches file_path or path JSON keys in tool call arguments.
var fileArgPathRe = regexp.MustCompile(`"(?:file_path|path|filename|file|old_string_file_path)":\s*"([^"]+)"`)

// ExtractActiveFiles scans messages for file paths that were read/written/edited
// and returns a deduplicated list sorted by recency (most recently referenced first).
// This identifies the "active working set" of files the model was operating on.
// Mirrors the file-state tracking in the reference createPostCompactFileAttachments.
func ExtractActiveFiles(messages []*schema.Message) []string {
	pathIndex := make(map[string]int) // path → most recent index

	for idx, msg := range messages {
		if msg == nil {
			continue
		}

		// Extract from tool calls (assistant messages calling file tools)
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if !fileToolNames[tc.Function.Name] {
					continue
				}
				paths := extractFilePathsFromArgs(tc.Function.Arguments)
				for _, p := range paths {
					normalized := normalizePath(p)
					if normalized != "" {
						pathIndex[normalized] = idx
					}
				}
			}
		}

		// Also scan message content for explicit file path references
		if msg.Content != "" {
			matches := filePathPattern.FindAllStringSubmatch(msg.Content, -1)
			for _, match := range matches {
				if len(match) > 1 && looksLikeRealPath(match[1]) {
					normalized := normalizePath(match[1])
					if normalized != "" {
						pathIndex[normalized] = idx
					}
				}
			}
		}
	}

	if len(pathIndex) == 0 {
		return nil
	}

	// Sort by recency (higher index = more recent = first in result)
	type pathEntry struct {
		path  string
		index int
	}
	entries := make([]pathEntry, 0, len(pathIndex))
	for p, idx := range pathIndex {
		entries = append(entries, pathEntry{path: p, index: idx})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].index > entries[j].index
	})

	result := make([]string, len(entries))
	for i, e := range entries {
		result[i] = e.path
	}
	return result
}

// extractFilePathsFromArgs extracts file paths from JSON-formatted tool arguments.
func extractFilePathsFromArgs(args string) []string {
	matches := fileArgPathRe.FindAllStringSubmatch(args, -1)
	var paths []string
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			paths = append(paths, match[1])
		}
	}
	return paths
}

// BuildFileStateReinjection builds a system message describing the current
// state of active files (existence, size, last modified). This gives the model
// awareness of the working set after compaction without re-reading full content.
// Limits to top N files (maxFileStateReinjection) to avoid context bloat.
// Mirrors the file attachment logic in reference createPostCompactFileAttachments.
func BuildFileStateReinjection(files []string, projectDir string) (*schema.Message, error) {
	if len(files) == 0 {
		return nil, nil
	}

	// Limit to top N files
	limit := maxFileStateReinjection
	if len(files) < limit {
		limit = len(files)
	}
	candidates := files[:limit]

	var parts []string
	for _, filePath := range candidates {
		// Resolve relative paths against project dir
		absPath := filePath
		if !filepath.IsAbs(filePath) && projectDir != "" {
			absPath = filepath.Join(projectDir, filePath)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			// File doesn't exist or can't be accessed — note it
			parts = append(parts, fmt.Sprintf("- %s (not found or inaccessible)", filePath))
			continue
		}

		size := info.Size()
		modTime := info.ModTime().Format(time.RFC3339)
		sizeStr := formatFileSize(size)

		parts = append(parts, fmt.Sprintf("- %s (%s, modified %s)", filePath, sizeStr, modTime))
	}

	if len(parts) == 0 {
		return nil, nil
	}

	content := "[Active file states after compaction — these files were recently worked on:]\n\n" +
		strings.Join(parts, "\n")

	msg := &schema.Message{
		Role:    schema.User,
		Content: content,
		Extra: map[string]any{
			"subtype": "attachment",
			"type":    "post_compact_file_state",
			"isMeta":  true,
		},
	}
	return msg, nil
}

// ApplyPostCompact orchestrates the full post-compact pipeline:
//  1. Extract active files from the pre-compact messages
//  2. Run PostCompactCleanup on the new messages
//  3. Build file state reinjection message
//  4. Insert reinjection at the right position
//
// Returns the final message list and state.
// Mirrors the combined behavior of reference compact.ts buildPostCompactMessages
// and postCompactCleanup.ts runPostCompactCleanup.
func ApplyPostCompact(messages []*schema.Message, projectDir string) ([]*schema.Message, *PostCompactState) {
	state := &PostCompactState{
		OriginalMessageCount: len(messages),
	}

	// Step 1: Extract active files from messages before cleanup
	activeFiles := ExtractActiveFiles(messages)
	state.ActiveFiles = activeFiles

	// Also extract all files referenced (superset including non-active)
	state.FilesReferenced = extractAllFilePaths(messages)

	// Step 2: Estimate pre-compact tokens for the savings calculation
	preTokens := EstimateTokenCount(messages)

	// Step 3: Run structural cleanup
	cleaned := PostCompactCleanup(messages, state)

	// Step 4: Build file state reinjection
	reinjection, err := BuildFileStateReinjection(activeFiles, projectDir)
	if err != nil {
		// Non-fatal: proceed without reinjection
		reinjection = nil
	}

	// Step 5: Insert reinjection at the right position.
	// The reinjection goes after the compact boundary and summary messages
	// but before other content, so the model sees it as context.
	var result []*schema.Message
	if reinjection != nil {
		// Find insertion point: after boundary marker and summary, before other messages.
		insertIdx := findReinjectionInsertIndex(cleaned)
		result = make([]*schema.Message, 0, len(cleaned)+1)
		result = append(result, cleaned[:insertIdx]...)
		result = append(result, reinjection)
		result = append(result, cleaned[insertIdx:]...)
	} else {
		result = cleaned
	}

	// Step 6: Calculate tokens saved
	postTokens := EstimateTokenCount(result)
	state.TokensSaved = preTokens - postTokens
	if state.TokensSaved < 0 {
		state.TokensSaved = 0
	}
	state.CompactedMessageCount = len(result)

	return result, state
}

// findReinjectionInsertIndex determines where to insert the file state
// reinjection message. It should go after compact boundary markers and
// summary messages, but before regular conversation messages.
func findReinjectionInsertIndex(messages []*schema.Message) int {
	idx := 0
	for i, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			subtype, _ := msg.Extra["subtype"].(string)
			if subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "post_compact_marker" {
				idx = i + 1
				continue
			}
		}
		// Stop at the first non-boundary/non-summary message
		break
	}
	return idx
}

// extractAllFilePaths collects all unique file paths referenced in messages.
func extractAllFilePaths(messages []*schema.Message) []string {
	pathSet := make(map[string]bool)

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		// From tool calls
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if fileToolNames[tc.Function.Name] {
					for _, p := range extractFilePathsFromArgs(tc.Function.Arguments) {
						pathSet[normalizePath(p)] = true
					}
				}
			}
		}

		// From content
		if msg.Content != "" {
			matches := filePathPattern.FindAllStringSubmatch(msg.Content, -1)
			for _, match := range matches {
				if len(match) > 1 && looksLikeRealPath(match[1]) {
					pathSet[normalizePath(match[1])] = true
				}
			}
		}
	}

	if len(pathSet) == 0 {
		return nil
	}

	result := make([]string, 0, len(pathSet))
	for p := range pathSet {
		if p != "" {
			result = append(result, p)
		}
	}
	sort.Strings(result)
	return result
}

// formatFileSize returns a human-readable file size string.
func formatFileSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
