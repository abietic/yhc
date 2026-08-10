package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

const acpReplayMessageTupleVersion = 1

var acpReplayMessageNamespace = uuid.MustParse(
	// Frozen from the pre-YHC URL so replay message IDs remain stable.
	"abf20d06-4522-5356-955b-7ef46f65e780",
)

type acpReplayProjection struct {
	updates []acpsdk.SessionUpdate
}

type acpReplayMessageTuple struct {
	Version       int    `json:"version"`
	SessionID     string `json:"session_id"`
	IdentityKind  string `json:"identity_kind"`
	EntryVersion  int    `json:"entry_version,omitempty"`
	EntryID       string `json:"entry_id,omitempty"`
	RecordOrdinal uint64 `json:"record_ordinal,omitempty"`
	MessageIndex  int    `json:"message_index"`
}

func buildACPReplayProjection(
	snapshot *session.SessionReplaySnapshot,
	includeAssistantMessageIDs bool,
) (*acpReplayProjection, error) {
	if snapshot == nil || strings.TrimSpace(snapshot.SessionID) == "" {
		return nil, errors.New("ACP replay snapshot is unavailable")
	}

	items := snapshot.Items()
	updates := make([]acpsdk.SessionUpdate, 0, len(items))
	seenMessageIDs := make(map[string]struct{}, len(items))
	seenToolIDs := make(map[string]struct{})
	toolWireIDs := make(map[string]string)

	for itemIndex, item := range items {
		message := item.Message
		if message == nil {
			return nil, fmt.Errorf("ACP replay item %d has no message", itemIndex)
		}
		if message.Role == schema.System {
			continue
		}
		if len(item.PromptParts) > 0 && message.Role != schema.User {
			return nil, fmt.Errorf(
				"ACP replay item %d has prompt parts on role %q",
				itemIndex,
				message.Role,
			)
		}
		if replayMessageHasRichContent(message) &&
			len(item.PromptParts) == 0 &&
			item.AssistantPresentation == nil {
			return nil, unsupportedACPInput("session.load.replay.richContent")
		}

		switch message.Role {
		case schema.User:
			messageID, err := acpReplayMessageID(snapshot, item)
			if err != nil {
				return nil, err
			}
			if err := rememberACPReplayID(
				seenMessageIDs,
				messageID,
				"message",
			); err != nil {
				return nil, err
			}
			if len(item.PromptParts) == 0 {
				updates = append(updates, acpsdk.SessionUpdate{
					UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
						Content:   acpsdk.TextBlock(message.Content),
						MessageId: stringPointer(messageID),
					},
				})
				break
			}
			for partIndex, part := range item.PromptParts {
				content, err := acpReplayPromptContent(part)
				if err != nil {
					return nil, fmt.Errorf(
						"ACP replay item %d prompt part %d is invalid: %w",
						itemIndex,
						partIndex,
						err,
					)
				}
				updates = append(updates, acpsdk.SessionUpdate{
					UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
						Content:   content,
						MessageId: stringPointer(messageID),
					},
				})
			}

		case schema.Assistant:
			messageID, err := acpReplayMessageID(snapshot, item)
			if err != nil {
				return nil, err
			}
			if err := rememberACPReplayID(
				seenMessageIDs,
				messageID,
				"message",
			); err != nil {
				return nil, err
			}
			if item.AssistantPresentation != nil {
				// Validated provider-rich output replays only its ordered
				// public text parts under the one logical assistant ID.
				// Reasoning markers, signatures, part Extra, and provider
				// metadata never produce a wire update.
				var wireMessageID *string
				if includeAssistantMessageIDs {
					wireMessageID = stringPointer(messageID)
				}
				for _, text := range item.AssistantPresentation.TextParts {
					updates = append(updates, acpsdk.SessionUpdate{
						AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
							Content:   acpsdk.TextBlock(text),
							MessageId: wireMessageID,
						},
					})
				}
			} else if message.Content != "" {
				var wireMessageID *string
				if includeAssistantMessageIDs {
					wireMessageID = stringPointer(messageID)
				}
				updates = append(updates, acpsdk.SessionUpdate{
					AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
						Content:   acpsdk.TextBlock(message.Content),
						MessageId: wireMessageID,
					},
				})
			}

			anonymous := make(map[int]struct{}, len(item.AnonymousToolCallIndexes))
			for _, callIndex := range item.AnonymousToolCallIndexes {
				anonymous[callIndex] = struct{}{}
			}
			for callIndex, call := range message.ToolCalls {
				toolID := call.ID
				if _, legacyAnonymous := anonymous[callIndex]; legacyAnonymous {
					toolID = fmt.Sprintf("%s/tool/%d", messageID, callIndex)
				}
				if strings.TrimSpace(toolID) == "" {
					return nil, fmt.Errorf(
						"ACP replay tool call at item %d index %d has no ID",
						itemIndex,
						callIndex,
					)
				}
				if err := rememberACPReplayID(
					seenToolIDs,
					toolID,
					"tool call",
				); err != nil {
					return nil, err
				}
				rawInput, err := decodeACPReplayRawInput(
					json.RawMessage(call.Function.Arguments),
				)
				if err != nil {
					return nil, fmt.Errorf(
						"ACP replay tool input at item %d index %d is invalid: %w",
						itemIndex,
						callIndex,
						err,
					)
				}
				if strings.TrimSpace(call.Function.Name) == "" {
					return nil, fmt.Errorf(
						"ACP replay tool call at item %d index %d has no name",
						itemIndex,
						callIndex,
					)
				}
				opts := []acpsdk.ToolCallStartOpt{
					acpsdk.WithStartKind(acpToolKind(call.Function.Name)),
					acpsdk.WithStartStatus(acpsdk.ToolCallStatusInProgress),
					acpsdk.WithStartRawInput(rawInput),
				}
				if locations := acpToolLocations(rawInput); len(locations) > 0 {
					opts = append(opts, acpsdk.WithStartLocations(locations))
				}
				updates = append(updates, acpsdk.StartToolCall(
					acpsdk.ToolCallId(toolID),
					call.Function.Name,
					opts...,
				))
				toolWireIDs[call.ID] = toolID
			}

		case schema.Tool:
			toolID, ok := toolWireIDs[message.ToolCallID]
			if !ok {
				return nil, fmt.Errorf(
					"ACP replay tool result at item %d has no paired wire ID",
					itemIndex,
				)
			}
			status := acpsdk.ToolCallStatusCompleted
			switch item.ToolOutcome {
			case session.SessionReplayToolOutcomeSucceeded:
			case session.SessionReplayToolOutcomeFailed,
				session.SessionReplayToolOutcomeCancelled:
				status = acpsdk.ToolCallStatusFailed
			default:
				return nil, fmt.Errorf(
					"ACP replay tool result at item %d has unknown outcome",
					itemIndex,
				)
			}
			updates = append(updates, acpsdk.UpdateToolCall(
				acpsdk.ToolCallId(toolID),
				acpsdk.WithUpdateStatus(status),
				acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
					acpsdk.ToolContent(acpsdk.TextBlock(message.Content)),
				}),
				acpsdk.WithUpdateRawOutput(message.Content),
			))

		default:
			return nil, fmt.Errorf(
				"ACP replay item %d has unsupported role %q",
				itemIndex,
				message.Role,
			)
		}
	}

	return &acpReplayProjection{updates: updates}, nil
}

func acpReplayMessageID(
	snapshot *session.SessionReplaySnapshot,
	item session.SessionReplayItem,
) (string, error) {
	if item.LogicalMessageID != "" {
		if _, err := uuid.Parse(item.LogicalMessageID); err != nil {
			return "", errors.New(
				"ACP replay persisted logical message ID is not a UUID",
			)
		}
		return item.LogicalMessageID, nil
	}

	tuple := acpReplayMessageTuple{
		Version:      acpReplayMessageTupleVersion,
		SessionID:    snapshot.SessionID,
		MessageIndex: item.Identity.Index,
	}
	if item.Identity.Record.IsLegacy() {
		if err := transcript.ValidateEntryCursorRevision(
			item.Identity.Record,
			snapshot.Revision,
		); err != nil {
			return "", fmt.Errorf(
				"ACP replay legacy identity revision is invalid: %w",
				err,
			)
		}
		tuple.IdentityKind = "legacy-ordinal"
		tuple.RecordOrdinal = item.RecordOrdinal
	} else {
		if item.Identity.Record.Version <= 0 ||
			strings.TrimSpace(item.Identity.Record.ID) == "" {
			return "", errors.New(
				"ACP replay persisted entry identity is incomplete",
			)
		}
		tuple.IdentityKind = "persisted-entry"
		tuple.EntryVersion = item.Identity.Record.Version
		tuple.EntryID = item.Identity.Record.ID
	}
	material, err := json.Marshal(tuple)
	if err != nil {
		return "", fmt.Errorf("encode ACP replay message identity: %w", err)
	}
	return uuid.NewSHA1(acpReplayMessageNamespace, material).String(), nil
}

func replayMessageHasRichContent(message *schema.Message) bool {
	return message != nil &&
		(len(message.MultiContent) > 0 ||
			len(message.UserInputMultiContent) > 0 ||
			len(message.AssistantGenMultiContent) > 0)
}

func acpReplayPromptContent(
	part session.SessionReplayPromptPart,
) (acpsdk.ContentBlock, error) {
	variants := 0
	for _, present := range []bool{
		part.Text != nil,
		part.Image != nil,
		part.ResourceLink != nil,
		part.EmbeddedText != nil,
		part.EmbeddedBlob != nil,
	} {
		if present {
			variants++
		}
	}
	if variants != 1 {
		return acpsdk.ContentBlock{}, errors.New("logical content union is invalid")
	}

	switch part.Kind {
	case session.SessionReplayPromptPartText:
		if part.Text == nil {
			return acpsdk.ContentBlock{}, errors.New("text payload is unavailable")
		}
		return acpsdk.TextBlock(part.Text.Text), nil

	case session.SessionReplayPromptPartImage:
		if part.Image == nil ||
			part.Image.Data == "" ||
			strings.TrimSpace(part.Image.MIMEType) == "" {
			return acpsdk.ContentBlock{}, errors.New("image payload is incomplete")
		}
		block := acpsdk.ImageBlock(part.Image.Data, part.Image.MIMEType)
		block.Image.Annotations = acpReplayAnnotations(part.Image.Annotations)
		return block, nil

	case session.SessionReplayPromptPartResourceLink:
		if part.ResourceLink == nil ||
			part.ResourceLink.URI == "" ||
			part.ResourceLink.Name == "" {
			return acpsdk.ContentBlock{}, errors.New("resource link is incomplete")
		}
		resource := part.ResourceLink
		block := acpsdk.ResourceLinkBlock(resource.Name, resource.URI)
		block.ResourceLink.Title = cloneACPReplayString(resource.Title)
		block.ResourceLink.Description = cloneACPReplayString(resource.Description)
		block.ResourceLink.MimeType = cloneACPReplayString(resource.MIMEType)
		block.ResourceLink.Size = cloneACPReplayInt(resource.Size)
		block.ResourceLink.Annotations = acpReplayAnnotations(resource.Annotations)
		return block, nil

	case session.SessionReplayPromptPartEmbeddedText:
		if part.EmbeddedText == nil || part.EmbeddedText.URI == "" {
			return acpsdk.ContentBlock{}, errors.New("embedded text is incomplete")
		}
		resource := part.EmbeddedText
		block := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
			TextResourceContents: &acpsdk.TextResourceContents{
				Uri:      resource.URI,
				MimeType: cloneACPReplayString(resource.MIMEType),
				Text:     resource.Text,
			},
		})
		block.Resource.Annotations = acpReplayAnnotations(resource.Annotations)
		return block, nil

	case session.SessionReplayPromptPartEmbeddedBlob:
		if part.EmbeddedBlob == nil ||
			part.EmbeddedBlob.URI == "" ||
			part.EmbeddedBlob.Data == "" ||
			strings.TrimSpace(part.EmbeddedBlob.MIMEType) == "" {
			return acpsdk.ContentBlock{}, errors.New("embedded blob is incomplete")
		}
		resource := part.EmbeddedBlob
		mimeType := resource.MIMEType
		block := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
			BlobResourceContents: &acpsdk.BlobResourceContents{
				Uri:      resource.URI,
				MimeType: &mimeType,
				Blob:     resource.Data,
			},
		})
		block.Resource.Annotations = acpReplayAnnotations(resource.Annotations)
		return block, nil

	default:
		return acpsdk.ContentBlock{}, errors.New("logical content kind is unsupported")
	}
}

func acpReplayAnnotations(
	value *session.SessionReplayPromptAnnotations,
) *acpsdk.Annotations {
	if value == nil {
		return nil
	}
	annotations := &acpsdk.Annotations{
		Audience: make([]acpsdk.Role, len(value.Audience)),
	}
	for index, audience := range value.Audience {
		annotations.Audience[index] = acpsdk.Role(audience)
	}
	annotations.LastModified = cloneACPReplayString(value.LastModified)
	if value.Priority != nil {
		priority := *value.Priority
		annotations.Priority = &priority
	}
	return annotations
}

func cloneACPReplayString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneACPReplayInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func rememberACPReplayID(
	seen map[string]struct{},
	value string,
	kind string,
) error {
	if _, duplicate := seen[value]; duplicate {
		return fmt.Errorf("duplicate ACP replay %s ID", kind)
	}
	seen[value] = struct{}{}
	return nil
}

func decodeACPReplayRawInput(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("tool input contains trailing JSON")
	}
	return value, nil
}

func stringPointer(value string) *string {
	return &value
}

func (a *Agent) deliverACPReplay(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	projection *acpReplayProjection,
) error {
	if projection == nil {
		return errors.New("ACP replay projection is unavailable")
	}
	if a.conn == nil {
		// Direct in-process callers have no protocol transport. Production ACP
		// always sets a connection before a load request can arrive.
		return nil
	}
	for index, update := range projection.updates {
		if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
			SessionId: sessionID,
			Update:    update,
		}); err != nil {
			return fmt.Errorf(
				"deliver ACP replay update %d: %w",
				index,
				err,
			)
		}
	}
	return nil
}

func (a *Agent) publishRestoredSessionState(
	ctx context.Context,
	acpSession *Session,
) error {
	if a == nil || acpSession == nil || acpSession.Engine == nil {
		return errors.New("ACP restored session state is unavailable")
	}
	if a.conn == nil {
		return nil
	}
	if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
		SessionId: acpSession.ID,
		Update: acpsdk.SessionUpdate{
			ConfigOptionUpdate: &acpsdk.SessionConfigOptionUpdate{
				ConfigOptions: sessionConfigOptions(ctx, acpSession.Engine),
			},
		},
	}); err != nil {
		return fmt.Errorf("deliver ACP restored configuration: %w", err)
	}
	if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
		SessionId: acpSession.ID,
		Update: acpsdk.SessionUpdate{
			CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{
				CurrentModeId: acpsdk.SessionModeId(
					acpSession.Engine.PermissionMode(),
				),
			},
		},
	}); err != nil {
		return fmt.Errorf("deliver ACP restored mode: %w", err)
	}
	return nil
}
