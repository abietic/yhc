// Package session — export.go implements session export in markdown and JSON formats.
// Provides consistent output regardless of entrypoint (TUI/headless/ACP).
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
)

// ErrMediaExportUnsupported is retained for source compatibility. Export now
// represents ref-backed prompts with sanitized descriptors.
var ErrMediaExportUnsupported = errors.New("media_export_unsupported")

// ExportFormat specifies the output format for session export.
type ExportFormat int

const (
	// ExportMarkdown exports as a human-readable markdown conversation.
	ExportMarkdown ExportFormat = iota
	// ExportJSON exports as structured JSON with full metadata.
	ExportJSON
)

// ExportOptions configures session export behavior.
type ExportOptions struct {
	// SessionID is the session to export.
	SessionID string
	// Dir is the session storage directory. If empty, uses GetSessionDir with ProjectDir.
	Dir string
	// ProjectDir is used to resolve Dir when Dir is empty.
	ProjectDir string
	// Format specifies the output format (markdown or JSON).
	Format ExportFormat
	// IncludeToolCalls controls whether tool call/result messages are included.
	// When false, only user and assistant text messages are exported.
	IncludeToolCalls bool
	// IncludeMetadata controls whether session metadata is included in the export.
	// For markdown, it appears as a YAML-like header. For JSON, as a top-level field.
	IncludeMetadata bool
}

// ExportResult holds the exported session content.
type ExportResult struct {
	// Content is the formatted export string (markdown or JSON).
	Content string
	// Format is the format that was used.
	Format ExportFormat
	// MessageCount is the number of messages included in the export.
	MessageCount int
	// SessionID is the session that was exported.
	SessionID string
}

// ExportedSession is the JSON export structure for a session.
type ExportedSession struct {
	// SessionID is the unique session identifier.
	SessionID string `json:"session_id"`
	// Metadata contains session-level metadata (present when IncludeMetadata is true).
	Metadata *ExportedMetadata `json:"metadata,omitempty"`
	// Messages is the list of conversation messages.
	Messages []ExportedMessage `json:"messages"`
	// ExportedAt is when the export was generated.
	ExportedAt time.Time `json:"exported_at"`
}

// ExportedMetadata holds session metadata in the JSON export format.
type ExportedMetadata struct {
	CreatedAt       time.Time              `json:"created_at,omitempty"`
	LastActiveAt    time.Time              `json:"last_active_at,omitempty"`
	Model           string                 `json:"model,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	GitBranch       string                 `json:"git_branch,omitempty"`
	CWD             string                 `json:"cwd,omitempty"`
	ParentSessionID string                 `json:"parent_session_id,omitempty"`
	BranchPoint     int                    `json:"branch_point,omitempty"`
	ModelBinding    ModelBindingProjection `json:"model_binding"`
	MessageCount    int                    `json:"message_count"`
	TurnCount       int                    `json:"turn_count"`
}

// ExportedMessage is a single message in the JSON export format.
type ExportedMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Parts is present only for ref-backed ordered prompts. It is a sanitized
	// presentation projection and never contains private media identity.
	Parts []ExportedPromptPart `json:"parts,omitempty"`
	// ToolCalls is present for assistant messages that invoke tools.
	ToolCalls []ExportedToolCall `json:"tool_calls,omitempty"`
}

// ExportedPromptPart is the closed sanitized projection of every prompt kind.
type ExportedPromptPart struct {
	Kind     string                   `json:"kind"`
	Text     string                   `json:"text,omitempty"`
	MIMEType string                   `json:"mime_type,omitempty"`
	Image    *ExportedImageDescriptor `json:"image,omitempty"`
}

// ExportedImageDescriptor contains public presentation metadata only.
type ExportedImageDescriptor struct {
	MIMEType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Detail    string `json:"detail"`
}

// ExportedToolCall represents a tool invocation in the export format.
type ExportedToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Argument string `json:"argument,omitempty"`
}

// ExportSession exports a session in the requested format.
// The output is consistent regardless of the calling context (TUI, headless, ACP).
func ExportSession(opts ExportOptions) (*ExportResult, error) {
	if opts.SessionID == "" {
		return nil, errors.New("session ID must not be empty")
	}

	dir := opts.Dir
	if dir == "" {
		dir = GetSessionDir(opts.ProjectDir)
	}

	// Load the full session transcript.
	rec := transcript.NewRecorder(opts.SessionID, dir)
	loadResult, err := rec.LoadRefProjection()
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if len(loadResult.Messages) == 0 {
		return nil, fmt.Errorf("session %s has no messages", opts.SessionID)
	}
	promptDescriptors, err := exportPromptDescriptors(loadResult)
	if err != nil {
		return nil, err
	}

	// Filter messages based on options.
	messages := filterMessagesForExport(loadResult.Messages, opts.IncludeToolCalls)

	// Build metadata from the load result.
	var metadata *ExportedMetadata
	if opts.IncludeMetadata {
		metadata = buildExportMetadata(loadResult, messages)
	}

	var content string
	switch opts.Format {
	case ExportJSON:
		content, err = formatJSON(
			opts.SessionID,
			metadata,
			messages,
			promptDescriptors,
			opts.IncludeToolCalls,
		)
	default: // ExportMarkdown
		content = formatMarkdown(
			opts.SessionID,
			metadata,
			messages,
			promptDescriptors,
			opts.IncludeToolCalls,
		)
	}
	if err != nil {
		return nil, err
	}

	return &ExportResult{
		Content:      content,
		Format:       opts.Format,
		MessageCount: len(messages),
		SessionID:    opts.SessionID,
	}, nil
}

func exportPromptDescriptors(
	loadResult *transcript.LoadResult,
) (map[*schema.Message]promptrecord.Descriptor, error) {
	descriptors := make(map[*schema.Message]promptrecord.Descriptor)
	if loadResult == nil {
		return descriptors, nil
	}
	for _, binding := range loadResult.PromptRecords {
		if binding.MessageIndex < 0 ||
			binding.MessageIndex >= len(loadResult.Messages) ||
			loadResult.Messages[binding.MessageIndex] == nil {
			return nil, errors.New("export prompt projection is inconsistent")
		}
		descriptor, err := binding.Record.Describe()
		if err != nil {
			return nil, err
		}
		descriptors[loadResult.Messages[binding.MessageIndex]] = descriptor
	}
	return descriptors, nil
}

// filterMessagesForExport filters messages based on export options.
func filterMessagesForExport(messages []*schema.Message, includeToolCalls bool) []*schema.Message {
	if includeToolCalls {
		// Return all non-nil messages.
		var result []*schema.Message
		for _, msg := range messages {
			if msg != nil {
				result = append(result, msg)
			}
		}
		return result
	}

	// Filter out tool-related messages and system messages.
	var result []*schema.Message
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User, schema.Assistant:
			// Include user and assistant messages, but skip assistant messages
			// that only contain tool calls with no text content.
			if msg.Role == schema.Assistant && msg.Content == "" && len(msg.ToolCalls) > 0 {
				continue
			}
			result = append(result, msg)
		case schema.System:
			// Skip system messages (compact boundaries, etc).
			continue
		case schema.Tool:
			// Skip tool result messages.
			continue
		}
	}
	return result
}

// buildExportMetadata constructs export metadata from a load result.
func buildExportMetadata(loadResult *transcript.LoadResult, messages []*schema.Message) *ExportedMetadata {
	meta := &ExportedMetadata{
		MessageCount: len(messages),
		ModelBinding: ModelBindingProjection{
			State: ModelBindingStateAbsent,
		},
	}

	// Count turns.
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.User {
			meta.TurnCount++
		}
	}

	// Extract from transcript metadata entries.
	for _, m := range loadResult.Metadata {
		switch m.Key {
		case "parent_session_id":
			meta.ParentSessionID = m.Value
		case "branch_point":
			_, _ = fmt.Sscanf(m.Value, "%d", &meta.BranchPoint)
		case "git_branch":
			meta.GitBranch = m.Value
		case "cwd":
			meta.CWD = m.Value
		}
		// Use earliest metadata timestamp as created_at approximation.
		if meta.CreatedAt.IsZero() && !m.Timestamp.IsZero() {
			meta.CreatedAt = m.Timestamp
		}
	}

	// Try full metadata.
	if full := ReadSessionMetadataFull(loadResult); full != nil {
		meta.ModelBinding = SafeModelBindingProjection(full.ModelBinding)
		if meta.Model == "" {
			meta.Model = full.Model
		}
		if meta.Provider == "" {
			meta.Provider = full.Provider
		}
		if meta.GitBranch == "" {
			meta.GitBranch = full.GitBranch
		}
		if meta.CWD == "" {
			meta.CWD = full.CWD
		}
		if !full.CreatedAt.IsZero() {
			meta.CreatedAt = full.CreatedAt
		}
		if !full.UpdatedAt.IsZero() {
			meta.LastActiveAt = full.UpdatedAt
		}
		if meta.ParentSessionID == "" {
			meta.ParentSessionID = full.ParentSessionID
		}
		if meta.BranchPoint == 0 {
			meta.BranchPoint = full.BranchPoint
		}
	}

	// Try to extract model/provider from message extras.
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Extra == nil {
			continue
		}
		if meta.Model == "" {
			if m, ok := msg.Extra["model"]; ok {
				if s, ok := m.(string); ok && s != "" {
					meta.Model = s
				}
			}
		}
		if meta.Provider == "" {
			if p, ok := msg.Extra["provider"]; ok {
				if s, ok := p.(string); ok && s != "" {
					meta.Provider = s
				}
			}
		}
		if meta.Model != "" && meta.Provider != "" {
			break
		}
	}

	return meta
}

// formatMarkdown formats the session as a markdown conversation.
func formatMarkdown(
	sessionID string,
	metadata *ExportedMetadata,
	messages []*schema.Message,
	promptDescriptors map[*schema.Message]promptrecord.Descriptor,
	includeToolCalls bool,
) string {
	var sb strings.Builder

	// Header.
	sb.WriteString("# Session: ")
	sb.WriteString(sessionID)
	sb.WriteString("\n\n")

	// Metadata block.
	if metadata != nil {
		sb.WriteString("---\n")
		if !metadata.CreatedAt.IsZero() {
			sb.WriteString("created: ")
			sb.WriteString(metadata.CreatedAt.Format(time.RFC3339))
			sb.WriteString("\n")
		}
		if metadata.Model != "" {
			sb.WriteString("model: ")
			sb.WriteString(metadata.Model)
			sb.WriteString("\n")
		}
		if metadata.Provider != "" {
			sb.WriteString("provider: ")
			sb.WriteString(metadata.Provider)
			sb.WriteString("\n")
		}
		if metadata.GitBranch != "" {
			sb.WriteString("git_branch: ")
			sb.WriteString(metadata.GitBranch)
			sb.WriteString("\n")
		}
		if metadata.CWD != "" {
			sb.WriteString("cwd: ")
			sb.WriteString(metadata.CWD)
			sb.WriteString("\n")
		}
		if metadata.ParentSessionID != "" {
			sb.WriteString("parent_session: ")
			sb.WriteString(metadata.ParentSessionID)
			sb.WriteString("\n")
		}
		sb.WriteString("model_binding_state: ")
		sb.WriteString(metadata.ModelBinding.State)
		sb.WriteString("\n")
		if metadata.ModelBinding.State == ModelBindingStateValid {
			sb.WriteString("model_binding_kind: ")
			sb.WriteString(metadata.ModelBinding.Kind)
			sb.WriteString("\n")
			sb.WriteString("model_binding_value: ")
			sb.WriteString(metadata.ModelBinding.Value)
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "messages: %d\n", metadata.MessageCount)
		fmt.Fprintf(&sb, "turns: %d\n", metadata.TurnCount)
		sb.WriteString("---\n\n")
	}

	// Messages.
	for _, msg := range messages {
		if msg == nil {
			continue
		}

		switch msg.Role {
		case schema.User:
			sb.WriteString("## User\n\n")
			if descriptor, ok := promptDescriptors[msg]; ok {
				writeMarkdownPrompt(&sb, descriptor)
			} else {
				sb.WriteString(msg.Content)
			}
			sb.WriteString("\n\n")
		case schema.Assistant:
			sb.WriteString("## Assistant\n\n")
			if msg.Content != "" {
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}
			// Include tool calls if present and requested.
			if includeToolCalls {
				for _, tc := range msg.ToolCalls {
					fmt.Fprintf(&sb, "**Tool Call:** `%s`", tc.Function.Name)
					if tc.ID != "" {
						fmt.Fprintf(&sb, " (id: %s)", tc.ID)
					}
					sb.WriteString("\n")
					if tc.Function.Arguments != "" {
						sb.WriteString("```\n")
						sb.WriteString(tc.Function.Arguments)
						sb.WriteString("\n```\n")
					}
					sb.WriteString("\n")
				}
			}
		case schema.Tool:
			sb.WriteString("## Tool Result")
			if msg.ToolCallID != "" {
				fmt.Fprintf(&sb, " (for: %s)", msg.ToolCallID)
			}
			sb.WriteString("\n\n")
			if msg.Content != "" {
				sb.WriteString("```\n")
				sb.WriteString(msg.Content)
				sb.WriteString("\n```\n\n")
			}
		case schema.System:
			sb.WriteString("## System\n\n")
			if msg.Content != "" {
				sb.WriteString("> ")
				sb.WriteString(strings.ReplaceAll(msg.Content, "\n", "\n> "))
				sb.WriteString("\n\n")
			}
		}
	}

	return sb.String()
}

func writeMarkdownPrompt(
	sb *strings.Builder,
	descriptor promptrecord.Descriptor,
) {
	for index, part := range descriptor.Parts {
		if index > 0 {
			sb.WriteString("\n\n")
		}
		if part.Kind == promptrecord.PartText {
			sb.WriteString(part.Text)
			continue
		}
		if part.Image == nil {
			fmt.Fprintf(sb, "[%s", part.Kind)
			if part.MIMEType != "" {
				fmt.Fprintf(sb, ": %s", part.MIMEType)
			}
			sb.WriteString("]")
			continue
		}
		label := part.Kind
		if label == promptrecord.PartImage {
			label = "image"
		}
		fmt.Fprintf(
			sb,
			"[%s: %s, %dx%d, %d bytes, detail=%s]",
			label,
			part.Image.MIMEType,
			part.Image.Width,
			part.Image.Height,
			part.Image.SizeBytes,
			part.Image.Detail,
		)
	}
}

// formatJSON formats the session as structured JSON.
func formatJSON(
	sessionID string,
	metadata *ExportedMetadata,
	messages []*schema.Message,
	promptDescriptors map[*schema.Message]promptrecord.Descriptor,
	includeToolCalls bool,
) (string, error) {
	exported := ExportedSession{
		SessionID:  sessionID,
		Metadata:   metadata,
		ExportedAt: time.Now().UTC(),
	}

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		em := ExportedMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		if descriptor, ok := promptDescriptors[msg]; ok {
			em.Parts = exportedPromptParts(descriptor)
			em.Content = exportedPromptText(descriptor)
		}

		if includeToolCalls {
			for _, tc := range msg.ToolCalls {
				em.ToolCalls = append(em.ToolCalls, ExportedToolCall{
					ID:       tc.ID,
					Name:     tc.Function.Name,
					Argument: tc.Function.Arguments,
				})
			}
		}

		exported.Messages = append(exported.Messages, em)
	}

	data, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal export: %w", err)
	}

	return string(data), nil
}

func exportedPromptParts(
	descriptor promptrecord.Descriptor,
) []ExportedPromptPart {
	parts := make([]ExportedPromptPart, 0, len(descriptor.Parts))
	for _, part := range descriptor.Parts {
		exported := ExportedPromptPart{
			Kind:     part.Kind,
			Text:     part.Text,
			MIMEType: part.MIMEType,
		}
		if part.Image != nil {
			exported.Image = &ExportedImageDescriptor{
				MIMEType:  part.Image.MIMEType,
				SizeBytes: part.Image.SizeBytes,
				Width:     part.Image.Width,
				Height:    part.Image.Height,
				Detail:    part.Image.Detail,
			}
		}
		parts = append(parts, exported)
	}
	return parts
}

func exportedPromptText(descriptor promptrecord.Descriptor) string {
	var text strings.Builder
	for _, part := range descriptor.Parts {
		if part.Kind == promptrecord.PartText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
