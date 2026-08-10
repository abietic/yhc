package compact

import (
	"github.com/cloudwego/eino/schema"
)

// SanitizeMediaForCompaction replaces user image, audio, video, and file
// content blocks with modality-preserving text placeholders before sending
// messages to the summary model. Binary media is not needed for generating a
// conversation summary and can leak unsupported payloads or make the
// compaction request exceed the model context window.
// Mirrors reference compact/compact.ts stripImagesFromMessages.
func SanitizeMediaForCompaction(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, len(messages))
	for i, msg := range messages {
		if msg == nil || msg.Role != schema.User {
			result[i] = msg
			continue
		}
		result[i] = sanitizeMediaBlocks(msg)
	}
	return result
}

// StripImagesFromMessages is retained for compatibility. It now delegates to
// the complete compaction media sanitizer instead of stripping only images.
func StripImagesFromMessages(messages []*schema.Message) []*schema.Message {
	return SanitizeMediaForCompaction(messages)
}

// sanitizeMediaBlocks checks MultiContent and UserInputMultiContent for binary
// media blocks and replaces them with text placeholders.
func sanitizeMediaBlocks(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}

	multiChanged := false
	var newMulti []schema.ChatMessagePart //nolint:staticcheck
	if len(msg.MultiContent) > 0 {
		newMulti = make([]schema.ChatMessagePart, 0, len(msg.MultiContent)) //nolint:staticcheck
		for _, part := range msg.MultiContent {
			if placeholder, ok := compactionMediaPlaceholder(part.Type); ok {
				multiChanged = true
				newMulti = append(newMulti, schema.ChatMessagePart{ //nolint:staticcheck
					Type: schema.ChatMessagePartTypeText,
					Text: placeholder,
				})
			} else {
				newMulti = append(newMulti, part)
			}
		}
	}

	userMultiChanged := false
	var newUserMulti []schema.MessageInputPart
	if len(msg.UserInputMultiContent) > 0 {
		newUserMulti = make([]schema.MessageInputPart, 0, len(msg.UserInputMultiContent))
		for _, part := range msg.UserInputMultiContent {
			if placeholder, ok := compactionMediaPlaceholder(part.Type); ok {
				userMultiChanged = true
				newUserMulti = append(newUserMulti, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: placeholder,
				})
			} else {
				newUserMulti = append(newUserMulti, part)
			}
		}
	}

	if !multiChanged && !userMultiChanged {
		return msg
	}

	// Clone the message with replaced content
	clone := *msg
	if multiChanged {
		clone.MultiContent = newMulti
	}
	if userMultiChanged {
		clone.UserInputMultiContent = newUserMulti
	}
	return &clone
}

func compactionMediaPlaceholder(partType schema.ChatMessagePartType) (string, bool) {
	switch partType {
	case schema.ChatMessagePartTypeImageURL:
		return "[image]", true
	case schema.ChatMessagePartTypeAudioURL:
		return "[audio]", true
	case schema.ChatMessagePartTypeVideoURL:
		return "[video]", true
	case schema.ChatMessagePartTypeFileURL:
		return "[file]", true
	default:
		return "", false
	}
}
