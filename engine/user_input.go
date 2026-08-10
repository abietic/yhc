package engine

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

const maxPromptResourceDescriptorBytes = 16 * 1024

// PromptInput is an ordered, transport-neutral user prompt. Entry-point
// adapters convert their protocol blocks into this type before submitting the
// rendered provider fallback to QueryEngine.
type PromptInput struct {
	Blocks []PromptInputBlock
}

// PromptInputBlock is a closed union. Exactly one variant must be set.
type PromptInputBlock struct {
	Text         *string
	ResourceLink *PromptResourceLink
}

// PromptResourceLink preserves the portable metadata of a referenced
// resource. QueryEngine never dereferences URI or expands filesystem/network
// authority from this value.
type PromptResourceLink struct {
	URI         string
	Name        string
	Title       *string
	Description *string
	MIMEType    *string
	Size        *int
	Annotations *PromptResourceAnnotations
}

// PromptResourceAnnotations contains the portable ACP/MCP annotation fields.
// Reserved protocol extension metadata is intentionally not model-visible.
type PromptResourceAnnotations struct {
	Audience     []string `json:"audience,omitempty"`
	LastModified *string  `json:"lastModified,omitempty"`
	Priority     *float64 `json:"priority,omitempty"`
}

// PromptInputValidationError identifies a rejected block without exposing its
// content.
type PromptInputValidationError struct {
	BlockIndex int
	ReasonCode string
}

func (e *PromptInputValidationError) Error() string {
	return fmt.Sprintf(
		"user prompt validation: block=%d reason=%s",
		e.BlockIndex,
		e.ReasonCode,
	)
}

type promptResourceDescriptor struct {
	Type        string                     `json:"type"`
	URI         string                     `json:"uri"`
	Name        string                     `json:"name"`
	Title       *string                    `json:"title,omitempty"`
	Description *string                    `json:"description,omitempty"`
	MIMEType    *string                    `json:"mimeType,omitempty"`
	Size        *int                       `json:"size,omitempty"`
	Annotations *PromptResourceAnnotations `json:"annotations,omitempty"`
}

// Render returns the deterministic provider fallback consumed by the existing
// string query boundary. Text bytes are unchanged; non-empty blocks are joined
// with the same single-newline rule used by the pre-P23 ACP adapter.
func (input PromptInput) Render() (string, error) {
	parts := make([]string, 0, len(input.Blocks))
	for index, block := range input.Blocks {
		switch {
		case block.Text != nil && block.ResourceLink == nil:
			parts = append(parts, *block.Text)
		case block.Text == nil && block.ResourceLink != nil:
			rendered, err := renderPromptResourceLink(index, *block.ResourceLink)
			if err != nil {
				return "", err
			}
			parts = append(parts, rendered)
		default:
			return "", &PromptInputValidationError{
				BlockIndex: index,
				ReasonCode: "invalid_block_union",
			}
		}
	}
	return joinPromptInputParts(parts), nil
}

func renderPromptResourceLink(
	index int,
	resource PromptResourceLink,
) (string, error) {
	if resource.URI == "" {
		return "", &PromptInputValidationError{
			BlockIndex: index,
			ReasonCode: "resource_uri_required",
		}
	}
	if resource.Name == "" {
		return "", &PromptInputValidationError{
			BlockIndex: index,
			ReasonCode: "resource_name_required",
		}
	}
	if resource.Size != nil && *resource.Size < 0 {
		return "", &PromptInputValidationError{
			BlockIndex: index,
			ReasonCode: "resource_size_negative",
		}
	}

	descriptor, err := json.Marshal(promptResourceDescriptor{
		Type:        "resource_link",
		URI:         resource.URI,
		Name:        resource.Name,
		Title:       resource.Title,
		Description: resource.Description,
		MIMEType:    resource.MIMEType,
		Size:        resource.Size,
		Annotations: resource.Annotations,
	})
	if err != nil {
		return "", &PromptInputValidationError{
			BlockIndex: index,
			ReasonCode: "resource_metadata_invalid",
		}
	}
	rendered := "<resource_link>" + string(descriptor) + "</resource_link>"
	if len(rendered) > maxPromptResourceDescriptorBytes {
		return "", &PromptInputValidationError{
			BlockIndex: index,
			ReasonCode: "resource_descriptor_too_large",
		}
	}
	return rendered, nil
}

func joinPromptInputParts(parts []string) string {
	var result string
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i > 0 && result != "" {
			result += "\n"
		}
		result += part
	}
	return result
}

// UserImageValidationError identifies a rejected user image without exposing its content.
type UserImageValidationError struct {
	ImageIndex int
	ReasonCode string
}

func (e *UserImageValidationError) Error() string {
	return fmt.Sprintf("user image validation: image=%d reason=%s", e.ImageIndex, e.ReasonCode)
}

func newUserMessage(prompt string, extra map[string]any, images []UserImage) *schema.Message {
	message := &schema.Message{
		Role:    schema.User,
		Content: prompt,
		Extra:   cloneMessageExtra(extra),
	}
	if len(images) == 0 {
		return message
	}

	parts := make([]schema.MessageInputPart, 0, len(images)+1)
	if prompt != "" {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: prompt,
		})
	}
	for _, image := range images {
		data := image.Base64Data
		mediaType, _ := normalizedUserImageMIME(image.MIMEType)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &data,
					MIMEType:   mediaType,
				},
			},
		})
	}
	if len(parts) > 0 {
		message.UserInputMultiContent = parts
	}
	return message
}
