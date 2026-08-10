package tui

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type sessionPickerMode string

const (
	sessionPickerResume sessionPickerMode = "resume"
	sessionPickerFork   sessionPickerMode = "fork"
)

type sessionPickerSelection struct {
	Mode                 sessionPickerMode
	Info                 session.SessionInfo
	ConfirmLegacyStopped bool
}

func (s sessionPickerSelection) valid() bool {
	return s.Info.SessionID != "" && (s.Mode == sessionPickerResume || s.Mode == sessionPickerFork)
}

type sessionPreviewState struct {
	loading     bool
	messages    []*schema.Message
	truncated   bool
	corruptions int
	err         string
}

type resumeSessionPreviewRequestMsg struct {
	info       session.SessionInfo
	key        string
	generation uint64
}

type resumeSessionPreviewLoadedMsg struct {
	key        string
	generation uint64
	result     *transcript.RecentResult
	err        error
}

type resumeSessionTranscriptRequestMsg struct {
	info       session.SessionInfo
	key        string
	generation uint64
}

type resumeSessionTranscriptLoadedMsg struct {
	info       session.SessionInfo
	key        string
	generation uint64
	result     *transcript.LoadResult
	err        error
}

type resumeSessionActionFinishedMsg struct {
	selection sessionPickerSelection
	resumedID string
	forkedID  string
	count     int
	warnings  []string
	err       error
}

func renderSessionTranscript(info session.SessionInfo, result *transcript.LoadResult) string {
	var builder strings.Builder
	title := firstNonEmptyString(info.CustomTitle, info.Summary, info.SessionID)
	fmt.Fprintf(&builder, "Session: %s\nID: %s", sanitizeGenericHistoryText(title), info.SessionID)
	if info.CWD != "" {
		fmt.Fprintf(&builder, "\nCWD: %s", sanitizeGenericHistoryText(info.CWD))
	}
	if info.Model != "" {
		fmt.Fprintf(&builder, "\nModel: %s", sanitizeGenericHistoryText(info.Model))
	}
	builder.WriteString("\n\n")
	if result == nil || len(result.Messages) == 0 {
		builder.WriteString("No transcript messages.\n")
		return builder.String()
	}
	for _, message := range result.Messages {
		if message == nil {
			continue
		}
		label, content := sessionMessagePreview(message)
		fmt.Fprintf(&builder, "[%s]\n", label)
		content = sanitizeGenericHistoryText(content)
		if strings.TrimSpace(content) != "" {
			builder.WriteString(content)
			builder.WriteByte('\n')
		}
		if message.ReasoningContent != "" {
			builder.WriteString("Reasoning:\n")
			builder.WriteString(sanitizeGenericHistoryText(message.ReasoningContent))
			builder.WriteByte('\n')
		}
		for _, call := range message.ToolCalls {
			fmt.Fprintf(&builder, "Tool call %s: %s\n", sanitizeGenericHistoryText(call.Function.Name), sanitizeGenericHistoryText(call.Function.Arguments))
		}
		builder.WriteByte('\n')
	}
	if len(result.Corruptions) > 0 {
		fmt.Fprintf(&builder, "Recovered with %d skipped corrupt transcript line(s).\n", len(result.Corruptions))
	}
	return strings.TrimRight(builder.String(), "\n")
}

func sessionMessagePreview(message *schema.Message) (string, string) {
	switch message.Role {
	case schema.User:
		return "You", message.Content
	case schema.Assistant:
		content := message.Content
		if content == "" && message.ReasoningContent != "" {
			content = message.ReasoningContent
		}
		if content == "" && len(message.ToolCalls) > 0 {
			content = "tool call: " + message.ToolCalls[0].Function.Name
		}
		return "Assistant", content
	case schema.Tool:
		label := "Tool"
		if message.ToolName != "" {
			label = message.ToolName
		}
		return label, message.Content
	case schema.System:
		return "System", message.Content
	default:
		return string(message.Role), message.Content
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
