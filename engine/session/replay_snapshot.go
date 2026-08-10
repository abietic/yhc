package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
)

// ErrSessionReplayInvalid reports durable history that cannot be projected
// without ambiguity or silently dropping a conversation fact.
var ErrSessionReplayInvalid = errors.New("session replay snapshot is invalid")

// SessionReplayToolOutcome is the durable terminal fact carried by one tool
// result. Cancellation is reserved until the transcript persists it separately.
type SessionReplayToolOutcome string

const (
	SessionReplayToolOutcomeNone      SessionReplayToolOutcome = "none"
	SessionReplayToolOutcomeSucceeded SessionReplayToolOutcome = "succeeded"
	SessionReplayToolOutcomeFailed    SessionReplayToolOutcome = "failed"
	SessionReplayToolOutcomeCancelled SessionReplayToolOutcome = "cancelled"
)

// SessionReplayItem is one active-context message with its exact physical
// transcript identity and persisted logical assistant identity, when present.
type SessionReplayItem struct {
	Identity                 transcript.MessageEntryIdentity
	RecordOrdinal            uint64
	LogicalMessageID         string
	AnonymousToolCallIndexes []int
	Message                  *schema.Message
	PromptParts              []SessionReplayPromptPart
	AssistantPresentation    *SessionReplayAssistantPresentation
	ToolOutcome              SessionReplayToolOutcome
}

// SessionReplayPromptPartKind identifies one exact logical durable user part.
// It is neutral engine/session state; transport adapters own their wire types.
type SessionReplayPromptPartKind string

const (
	SessionReplayPromptPartText         SessionReplayPromptPartKind = "text"
	SessionReplayPromptPartImage        SessionReplayPromptPartKind = "image"
	SessionReplayPromptPartResourceLink SessionReplayPromptPartKind = "resource_link"
	SessionReplayPromptPartEmbeddedText SessionReplayPromptPartKind = "embedded_text"
	SessionReplayPromptPartEmbeddedBlob SessionReplayPromptPartKind = "embedded_blob"
)

// SessionReplayPromptPart is one closed logical user-content union recovered
// from an exact versioned prompt-record binding.
type SessionReplayPromptPart struct {
	Kind         SessionReplayPromptPartKind
	Text         *SessionReplayPromptText
	Image        *SessionReplayPromptImage
	ResourceLink *SessionReplayPromptResourceLink
	EmbeddedText *SessionReplayPromptEmbeddedText
	EmbeddedBlob *SessionReplayPromptEmbeddedBlob
}

type SessionReplayPromptText struct {
	Text string
}

type SessionReplayPromptImage struct {
	Data        string
	MIMEType    string
	Annotations *SessionReplayPromptAnnotations
}

type SessionReplayPromptResourceLink struct {
	URI         string
	Name        string
	Title       *string
	Description *string
	MIMEType    *string
	Size        *int
	Annotations *SessionReplayPromptAnnotations
}

type SessionReplayPromptEmbeddedText struct {
	URI         string
	MIMEType    *string
	Text        string
	Annotations *SessionReplayPromptAnnotations
}

type SessionReplayPromptEmbeddedBlob struct {
	URI         string
	MIMEType    string
	Data        string
	Annotations *SessionReplayPromptAnnotations
}

type SessionReplayPromptAnnotations struct {
	Audience     []string
	LastModified *string
	Priority     *float64
}

// SessionReplayAssistantPresentation is the complete public presentation of
// one validated provider-rich assistant message. A non-nil presentation marks
// a rich message even when it has no public text. Reasoning part existence,
// text, signatures, output-part Extra, and message-level provider metadata stay
// in the durable message clone and are never projected here.
type SessionReplayAssistantPresentation struct {
	TextParts []string
}

// SessionReplaySnapshot is one immutable view bound to exact transcript bytes.
// Legacy entry identities are valid only for Revision.
type SessionReplaySnapshot struct {
	SessionID string
	Revision  transcript.TranscriptRevision

	items []SessionReplayItem
}

// Items returns mutation-isolated copies of the ordered replay items.
func (s *SessionReplaySnapshot) Items() []SessionReplayItem {
	if s == nil || len(s.items) == 0 {
		return nil
	}
	items := make([]SessionReplayItem, len(s.items))
	for index, item := range s.items {
		items[index] = item
		items[index].AnonymousToolCallIndexes = append(
			[]int(nil),
			item.AnonymousToolCallIndexes...,
		)
		items[index].Message = mustCloneReplayMessage(item.Message)
		items[index].PromptParts = cloneSessionReplayPromptParts(item.PromptParts)
		items[index].AssistantPresentation = cloneSessionReplayAssistantPresentation(
			item.AssistantPresentation,
		)
	}
	return items
}

// LoadSessionReplaySnapshot reads and strictly validates the same final active
// context selected by ResumeSession without opening a transcript writer.
func LoadSessionReplaySnapshot(
	ctx context.Context,
	opts ResumeOptions,
) (*SessionReplaySnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("replay snapshot canceled before load: %w", err)
	}
	sessionDir, sessionID, err := resolveSessionLocation(opts)
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	recorder := transcript.NewRecorder(sessionID, sessionDir)
	if recorder.Path() == "" {
		return nil, errors.New("session transcript path is empty")
	}
	if _, err := os.Stat(recorder.Path()); err != nil {
		return nil, fmt.Errorf("stat session transcript: %w", err)
	}
	loaded, err := recorder.LoadFullContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load transcript: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("replay snapshot canceled after load: %w", err)
	}
	if loaded == nil || loaded.Revision == "" {
		return nil, replayInvalid("transcript revision is unavailable")
	}
	if len(loaded.Corruptions) > 0 {
		first := loaded.Corruptions[0]
		return nil, replayInvalid(
			"transcript corruption at line %d: %v",
			first.Line,
			first.Err,
		)
	}
	if len(loaded.Messages) == 0 && len(loaded.LifecycleBoundaries) == 0 {
		return nil, replayInvalid("session %s has no messages", sessionID)
	}
	items, err := buildSessionReplayItems(ctx, loaded.Entries)
	if err != nil {
		return nil, err
	}
	if err := attachSessionReplayPromptParts(
		items,
		loaded.Messages,
		loaded.PromptRecords,
	); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf(
			"replay snapshot canceled during construction: %w",
			err,
		)
	}
	return &SessionReplaySnapshot{
		SessionID: sessionID,
		Revision:  loaded.Revision,
		items:     items,
	}, nil
}

type selectedReplayMessage struct {
	identity      transcript.MessageEntryIdentity
	recordOrdinal uint64
	message       *schema.Message
}

type pendingReplayToolCall struct {
	itemIndex int
	callIndex int
}

func buildSessionReplayItems(
	ctx context.Context,
	entries []transcript.DurableEntry,
) ([]SessionReplayItem, error) {
	selected := make([]selectedReplayMessage, 0)
	for _, entry := range entries {
		if entry.Message != nil {
			selected = append(selected, selectedReplayMessage{
				identity: transcript.MessageEntryIdentity{
					Record: entry.Identity,
				},
				recordOrdinal: entry.Ordinal,
				message:       entry.Message,
			})
		}
		if replayLifecycleBoundary(entry.Kind) {
			selected = selected[:0]
			for index, message := range entry.Messages {
				selected = append(selected, selectedReplayMessage{
					identity: transcript.MessageEntryIdentity{
						Record: entry.Identity,
						Index:  index,
					},
					recordOrdinal: entry.Ordinal,
					message:       message,
				})
			}
		}
	}

	items := make([]SessionReplayItem, 0, len(selected))
	seenEntries := make(map[string]struct{}, len(selected))
	seenLogicalIDs := make(map[string]struct{})
	seenToolCallIDs := make(map[string]struct{})
	pendingNamed := make(map[string]pendingReplayToolCall)
	pendingAnonymous := make([]pendingReplayToolCall, 0)

	for _, selectedMessage := range selected {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf(
				"replay snapshot canceled during construction: %w",
				err,
			)
		}
		identityKey := selectedMessage.identity.Key()
		if _, duplicate := seenEntries[identityKey]; duplicate {
			return nil, replayInvalid(
				"duplicate durable message identity %q",
				identityKey,
			)
		}
		seenEntries[identityKey] = struct{}{}
		message := selectedMessage.message
		if err := validateReplayMessageShape(message, identityKey); err != nil {
			return nil, err
		}
		cloned, err := cloneReplayMessage(message)
		if err != nil {
			return nil, replayInvalid(
				"clone message %s: %v",
				identityKey,
				err,
			)
		}
		item := SessionReplayItem{
			Identity:      selectedMessage.identity,
			RecordOrdinal: selectedMessage.recordOrdinal,
			Message:       cloned,
			ToolOutcome:   SessionReplayToolOutcomeNone,
		}

		switch message.Role {
		case schema.Assistant:
			logicalID, err := replayLogicalMessageID(
				message,
				identityKey,
				seenLogicalIDs,
			)
			if err != nil {
				return nil, err
			}
			item.LogicalMessageID = logicalID
			presentation, err := replayAssistantPresentation(
				message,
				identityKey,
			)
			if err != nil {
				return nil, err
			}
			item.AssistantPresentation = presentation
			for callIndex, call := range message.ToolCalls {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf(
						"replay snapshot canceled during construction: %w",
						err,
					)
				}
				if call.ID == "" {
					item.AnonymousToolCallIndexes = append(
						item.AnonymousToolCallIndexes,
						callIndex,
					)
					pendingAnonymous = append(
						pendingAnonymous,
						pendingReplayToolCall{
							itemIndex: len(items),
							callIndex: callIndex,
						},
					)
					continue
				}
				if strings.TrimSpace(call.ID) == "" {
					return nil, replayInvalid(
						"tool call ID at %s/tool/%d is blank",
						identityKey,
						callIndex,
					)
				}
				if _, duplicate := seenToolCallIDs[call.ID]; duplicate {
					return nil, replayInvalid(
						"duplicate tool call ID %q",
						call.ID,
					)
				}
				seenToolCallIDs[call.ID] = struct{}{}
				pendingNamed[call.ID] = pendingReplayToolCall{
					itemIndex: len(items),
					callIndex: callIndex,
				}
			}
		case schema.Tool:
			outcome, err := replayToolOutcome(message, identityKey)
			if err != nil {
				return nil, err
			}
			item.ToolOutcome = outcome
			if err := settleReplayToolResult(
				message,
				identityKey,
				items,
				pendingNamed,
				&pendingAnonymous,
				seenToolCallIDs,
				cloned,
			); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	if len(pendingNamed) != 0 || len(pendingAnonymous) != 0 {
		return nil, replayInvalid(
			"unsettled tool calls: %d named, %d anonymous",
			len(pendingNamed),
			len(pendingAnonymous),
		)
	}
	return items, nil
}

func attachSessionReplayPromptParts(
	items []SessionReplayItem,
	messages []*schema.Message,
	bindings []transcript.PromptRecordBinding,
) error {
	if len(items) != len(messages) {
		return replayInvalid(
			"active message count %d does not match replay item count %d",
			len(messages),
			len(items),
		)
	}
	seen := make(map[int]struct{}, len(bindings))
	for _, binding := range bindings {
		index := binding.MessageIndex
		if index < 0 || index >= len(items) {
			return replayInvalid("prompt record message index %d is invalid", index)
		}
		if _, duplicate := seen[index]; duplicate {
			return replayInvalid("duplicate prompt record message index %d", index)
		}
		seen[index] = struct{}{}
		if !reflect.DeepEqual(items[index].Message, messages[index]) {
			return replayInvalid(
				"prompt record message index %d does not match replay selection",
				index,
			)
		}
		parts, err := binding.Record.ReplayParts(items[index].Message)
		if err != nil {
			return replayInvalid(
				"prompt record message index %d cannot be projected: %v",
				index,
				err,
			)
		}
		projected, err := sessionReplayPromptParts(parts)
		if err != nil {
			return replayInvalid(
				"prompt record message index %d has an invalid logical projection: %v",
				index,
				err,
			)
		}
		items[index].PromptParts = projected
	}
	return nil
}

// replayAssistantPresentation validates one provider-rich assistant message
// and derives its immutable public text presentation. Only text and
// reasoning output parts are accepted, each as a closed union, and the
// concatenated text bytes must equal Message.Content exactly. Reasoning
// payloads, signatures, output-part Extra, and message-level provider
// metadata remain in the durable clone and are never projected.
func replayAssistantPresentation(
	message *schema.Message,
	identityKey string,
) (*SessionReplayAssistantPresentation, error) {
	if len(message.AssistantGenMultiContent) == 0 {
		return nil, nil
	}
	if len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 {
		return nil, replayInvalid(
			"replay item %s assistant output failed validation: assistant_mixed_content_unions",
			identityKey,
		)
	}
	presentation := &SessionReplayAssistantPresentation{TextParts: make(
		[]string,
		0,
		len(message.AssistantGenMultiContent),
	)}
	var public strings.Builder
	for index, part := range message.AssistantGenMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			if part.Image != nil ||
				part.Audio != nil ||
				part.Video != nil ||
				part.Reasoning != nil {
				return nil, replayInvalid(
					"replay item %s assistant part %d failed validation: assistant_text_mixed_payload",
					identityKey,
					index,
				)
			}
			public.WriteString(part.Text)
			presentation.TextParts = append(presentation.TextParts, part.Text)
		case schema.ChatMessagePartTypeReasoning:
			if part.Reasoning == nil {
				return nil, replayInvalid(
					"replay item %s assistant part %d failed validation: assistant_reasoning_missing_payload",
					identityKey,
					index,
				)
			}
			if part.Image != nil ||
				part.Audio != nil ||
				part.Video != nil ||
				part.Text != "" {
				return nil, replayInvalid(
					"replay item %s assistant part %d failed validation: assistant_reasoning_mixed_payload",
					identityKey,
					index,
				)
			}
		default:
			return nil, replayInvalid(
				"replay item %s assistant part %d failed validation: assistant_output_unsupported_type",
				identityKey,
				index,
			)
		}
	}
	if public.String() != message.Content {
		return nil, replayInvalid(
			"replay item %s assistant output failed validation: assistant_public_text_mismatch",
			identityKey,
		)
	}
	return presentation, nil
}

func sessionReplayPromptParts(
	parts []promptrecord.ReplayPart,
) ([]SessionReplayPromptPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	projected := make([]SessionReplayPromptPart, 0, len(parts))
	for _, part := range parts {
		next := SessionReplayPromptPart{}
		switch part.Kind {
		case promptrecord.PartText:
			if part.Text == nil {
				return nil, errors.New("text payload is unavailable")
			}
			next.Kind = SessionReplayPromptPartText
			next.Text = &SessionReplayPromptText{Text: part.Text.Text}
		case promptrecord.PartImage:
			if part.Image == nil {
				return nil, errors.New("image payload is unavailable")
			}
			next.Kind = SessionReplayPromptPartImage
			next.Image = &SessionReplayPromptImage{
				Data:        part.Image.Data,
				MIMEType:    part.Image.MIMEType,
				Annotations: sessionReplayPromptAnnotations(part.Image.Annotations),
			}
		case promptrecord.PartResourceLink:
			if part.ResourceLink == nil {
				return nil, errors.New("resource-link payload is unavailable")
			}
			next.Kind = SessionReplayPromptPartResourceLink
			next.ResourceLink = &SessionReplayPromptResourceLink{
				URI:         part.ResourceLink.URI,
				Name:        part.ResourceLink.Name,
				Title:       cloneSessionReplayString(part.ResourceLink.Title),
				Description: cloneSessionReplayString(part.ResourceLink.Description),
				MIMEType:    cloneSessionReplayString(part.ResourceLink.MIMEType),
				Size:        cloneSessionReplayInt(part.ResourceLink.Size),
				Annotations: sessionReplayPromptAnnotations(
					part.ResourceLink.Annotations,
				),
			}
		case promptrecord.PartEmbeddedText:
			if part.EmbeddedText == nil {
				return nil, errors.New("embedded-text payload is unavailable")
			}
			next.Kind = SessionReplayPromptPartEmbeddedText
			next.EmbeddedText = &SessionReplayPromptEmbeddedText{
				URI:      part.EmbeddedText.URI,
				MIMEType: cloneSessionReplayString(part.EmbeddedText.MIMEType),
				Text:     part.EmbeddedText.Text,
				Annotations: sessionReplayPromptAnnotations(
					part.EmbeddedText.Annotations,
				),
			}
		case promptrecord.PartEmbeddedBlob:
			if part.EmbeddedBlob == nil {
				return nil, errors.New("embedded-blob payload is unavailable")
			}
			next.Kind = SessionReplayPromptPartEmbeddedBlob
			next.EmbeddedBlob = &SessionReplayPromptEmbeddedBlob{
				URI:      part.EmbeddedBlob.URI,
				MIMEType: part.EmbeddedBlob.MIMEType,
				Data:     part.EmbeddedBlob.Data,
				Annotations: sessionReplayPromptAnnotations(
					part.EmbeddedBlob.Annotations,
				),
			}
		default:
			return nil, fmt.Errorf(
				"unsupported logical content kind %q",
				part.Kind,
			)
		}
		projected = append(projected, next)
	}
	return projected, nil
}

func cloneSessionReplayAssistantPresentation(
	presentation *SessionReplayAssistantPresentation,
) *SessionReplayAssistantPresentation {
	if presentation == nil {
		return nil
	}
	cloned := &SessionReplayAssistantPresentation{}
	if len(presentation.TextParts) != 0 {
		cloned.TextParts = append([]string(nil), presentation.TextParts...)
	}
	return cloned
}

func cloneSessionReplayPromptParts(
	parts []SessionReplayPromptPart,
) []SessionReplayPromptPart {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]SessionReplayPromptPart, len(parts))
	for index, part := range parts {
		cloned[index] = part
		if part.Text != nil {
			value := *part.Text
			cloned[index].Text = &value
		}
		if part.Image != nil {
			value := *part.Image
			value.Annotations = cloneSessionReplayAnnotations(part.Image.Annotations)
			cloned[index].Image = &value
		}
		if part.ResourceLink != nil {
			value := *part.ResourceLink
			value.Title = cloneSessionReplayString(part.ResourceLink.Title)
			value.Description = cloneSessionReplayString(part.ResourceLink.Description)
			value.MIMEType = cloneSessionReplayString(part.ResourceLink.MIMEType)
			value.Size = cloneSessionReplayInt(part.ResourceLink.Size)
			value.Annotations = cloneSessionReplayAnnotations(
				part.ResourceLink.Annotations,
			)
			cloned[index].ResourceLink = &value
		}
		if part.EmbeddedText != nil {
			value := *part.EmbeddedText
			value.MIMEType = cloneSessionReplayString(part.EmbeddedText.MIMEType)
			value.Annotations = cloneSessionReplayAnnotations(
				part.EmbeddedText.Annotations,
			)
			cloned[index].EmbeddedText = &value
		}
		if part.EmbeddedBlob != nil {
			value := *part.EmbeddedBlob
			value.Annotations = cloneSessionReplayAnnotations(
				part.EmbeddedBlob.Annotations,
			)
			cloned[index].EmbeddedBlob = &value
		}
	}
	return cloned
}

func sessionReplayPromptAnnotations(
	value *promptrecord.Annotations,
) *SessionReplayPromptAnnotations {
	if value == nil {
		return nil
	}
	return &SessionReplayPromptAnnotations{
		Audience:     append([]string(nil), value.Audience...),
		LastModified: cloneSessionReplayString(value.LastModified),
		Priority:     cloneSessionReplayFloat(value.Priority),
	}
}

func cloneSessionReplayAnnotations(
	value *SessionReplayPromptAnnotations,
) *SessionReplayPromptAnnotations {
	if value == nil {
		return nil
	}
	return &SessionReplayPromptAnnotations{
		Audience:     append([]string(nil), value.Audience...),
		LastModified: cloneSessionReplayString(value.LastModified),
		Priority:     cloneSessionReplayFloat(value.Priority),
	}
}

func cloneSessionReplayString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSessionReplayInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSessionReplayFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateReplayMessageShape(message *schema.Message, identityKey string) error {
	if message == nil {
		return replayInvalid("replay item %s has a nil message", identityKey)
	}
	switch message.Role {
	case schema.User, schema.Assistant, schema.Tool, schema.System:
	default:
		return replayInvalid(
			"replay item %s has unknown role %q",
			identityKey,
			message.Role,
		)
	}
	if message.Role != schema.Assistant && len(message.ToolCalls) != 0 {
		return replayInvalid(
			"replay item %s has tool calls on role %q",
			identityKey,
			message.Role,
		)
	}
	if message.Role != schema.Tool && message.ToolCallID != "" {
		return replayInvalid(
			"replay item %s has a tool result ID on role %q",
			identityKey,
			message.Role,
		)
	}
	return nil
}

func replayLogicalMessageID(
	message *schema.Message,
	identityKey string,
	seen map[string]struct{},
) (string, error) {
	raw, present := message.Extra["message_id"]
	if !present {
		return "", nil
	}
	logicalID, ok := raw.(string)
	if !ok || strings.TrimSpace(logicalID) == "" {
		return "", replayInvalid(
			"replay item %s has a malformed logical message_id",
			identityKey,
		)
	}
	if _, duplicate := seen[logicalID]; duplicate {
		return "", replayInvalid(
			"duplicate assistant logical message_id %q",
			logicalID,
		)
	}
	seen[logicalID] = struct{}{}
	return logicalID, nil
}

func replayToolOutcome(
	message *schema.Message,
	identityKey string,
) (SessionReplayToolOutcome, error) {
	raw, present := message.Extra["is_error"]
	if !present {
		return SessionReplayToolOutcomeSucceeded, nil
	}
	failed, ok := raw.(bool)
	if !ok {
		return "", replayInvalid(
			"replay item %s has a non-boolean is_error fact",
			identityKey,
		)
	}
	if failed {
		return SessionReplayToolOutcomeFailed, nil
	}
	return SessionReplayToolOutcomeSucceeded, nil
}

func settleReplayToolResult(
	message *schema.Message,
	identityKey string,
	items []SessionReplayItem,
	pendingNamed map[string]pendingReplayToolCall,
	pendingAnonymous *[]pendingReplayToolCall,
	seenToolCallIDs map[string]struct{},
	clonedResult *schema.Message,
) error {
	if message.ToolCallID != "" {
		if strings.TrimSpace(message.ToolCallID) == "" {
			return replayInvalid(
				"tool result %s has a blank tool call ID",
				identityKey,
			)
		}
		if _, found := pendingNamed[message.ToolCallID]; !found {
			return replayInvalid(
				"orphan tool result %s for tool call ID %q",
				identityKey,
				message.ToolCallID,
			)
		}
		delete(pendingNamed, message.ToolCallID)
		return nil
	}
	if len(*pendingAnonymous) != 1 {
		return replayInvalid(
			"tool result %s has %d anonymous pending calls",
			identityKey,
			len(*pendingAnonymous),
		)
	}
	pending := (*pendingAnonymous)[0]
	*pendingAnonymous = (*pendingAnonymous)[:0]
	resolvedID := fmt.Sprintf(
		"%s/tool/%d",
		items[pending.itemIndex].Identity.Key(),
		pending.callIndex,
	)
	if _, duplicate := seenToolCallIDs[resolvedID]; duplicate {
		return replayInvalid("duplicate tool call ID %q", resolvedID)
	}
	seenToolCallIDs[resolvedID] = struct{}{}
	items[pending.itemIndex].Message.ToolCalls[pending.callIndex].ID = resolvedID
	clonedResult.ToolCallID = resolvedID
	return nil
}

func replayLifecycleBoundary(kind string) bool {
	switch transcript.LifecycleBoundaryKind(kind) {
	case transcript.LifecycleSessionStart,
		transcript.LifecycleReset,
		transcript.LifecycleCompact,
		transcript.LifecycleCheckpoint:
		return true
	default:
		return false
	}
}

func cloneReplayMessage(message *schema.Message) (*schema.Message, error) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var cloned schema.Message
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func mustCloneReplayMessage(message *schema.Message) *schema.Message {
	cloned, err := cloneReplayMessage(message)
	if err != nil {
		panic(fmt.Sprintf("clone immutable replay item: %v", err))
	}
	return cloned
}

func replayInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSessionReplayInvalid, fmt.Sprintf(format, args...))
}
