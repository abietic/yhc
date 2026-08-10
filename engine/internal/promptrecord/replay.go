package promptrecord

import (
	"bytes"

	"github.com/cloudwego/eino/schema"
)

// ReplayPart is one logical durable prompt part projected for a trusted
// Session consumer. The record, rather than provider-shaped message content,
// selects the variant. Media data is the already materialized canonical base64
// string and carries no source URI, path, private ref, ID, or digest.
type ReplayPart struct {
	Kind         string
	Text         *ReplayTextPart
	Image        *ReplayImagePart
	ResourceLink *ReplayResourceLinkPart
	EmbeddedText *ReplayEmbeddedTextPart
	EmbeddedBlob *ReplayEmbeddedBlobPart
}

type ReplayTextPart struct {
	Text string
}

type ReplayImagePart struct {
	Data        string
	MIMEType    string
	Annotations *Annotations
}

type ReplayResourceLinkPart struct {
	URI         string
	Name        string
	Title       *string
	Description *string
	MIMEType    *string
	Size        *int
	Annotations *Annotations
}

type ReplayEmbeddedTextPart struct {
	URI         string
	MIMEType    *string
	Text        string
	Annotations *Annotations
}

type ReplayEmbeddedBlobPart struct {
	URI         string
	MIMEType    string
	Data        string
	Annotations *Annotations
}

// ReplayParts validates the exact provider projection produced by Materialize
// against this record and returns its original logical parts. The message is a
// byte carrier only after exact record binding; its flattened text/image shape
// never selects or changes a logical kind.
func (r Record) ReplayParts(message *schema.Message) ([]ReplayPart, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if message == nil ||
		message.Role != schema.User ||
		len(message.MultiContent) != 0 ||
		len(message.AssistantGenMultiContent) != 0 ||
		len(message.ToolCalls) != 0 ||
		message.ToolCallID != "" {
		return nil, bounded("replay_projection", nil)
	}

	providerParts := message.UserInputMultiContent
	providerIndex := 0
	replayParts := make([]ReplayPart, 0, len(r.Parts))
	var content bytes.Buffer

	nextText := func(expected string) error {
		if providerIndex >= len(providerParts) ||
			!matchesReplayTextPart(providerParts[providerIndex], expected) {
			return bounded("replay_projection", nil)
		}
		providerIndex++
		return nil
	}
	nextImage := func(mimeType, detail string) (string, error) {
		if providerIndex >= len(providerParts) {
			return "", bounded("replay_projection", nil)
		}
		data, ok := replayImageData(providerParts[providerIndex], mimeType, detail)
		if !ok {
			return "", bounded("replay_projection", nil)
		}
		providerIndex++
		return data, nil
	}

	for _, part := range r.Parts {
		switch part.Kind {
		case PartText:
			if err := nextText(part.Text.Text); err != nil {
				return nil, err
			}
			content.WriteString(part.Text.Text)
			replayParts = append(replayParts, ReplayPart{
				Kind: PartText,
				Text: &ReplayTextPart{Text: part.Text.Text},
			})

		case PartImage:
			data, err := nextImage(part.Image.Ref.MIMEType, part.Image.Detail)
			if err != nil {
				return nil, err
			}
			replayParts = append(replayParts, ReplayPart{
				Kind: PartImage,
				Image: &ReplayImagePart{
					Data:        data,
					MIMEType:    part.Image.Ref.MIMEType,
					Annotations: cloneAnnotations(part.Image.Annotations),
				},
			})

		case PartResourceLink:
			rendered, err := RenderResourceLink(*part.ResourceLink)
			if err != nil {
				return nil, err
			}
			if err := nextText(rendered); err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			replayParts = append(replayParts, ReplayPart{
				Kind: PartResourceLink,
				ResourceLink: &ReplayResourceLinkPart{
					URI:         part.ResourceLink.URI,
					Name:        part.ResourceLink.Name,
					Title:       cloneString(part.ResourceLink.Title),
					Description: cloneString(part.ResourceLink.Description),
					MIMEType:    cloneString(part.ResourceLink.MIMEType),
					Size:        cloneInt(part.ResourceLink.Size),
					Annotations: cloneAnnotations(part.ResourceLink.Annotations),
				},
			})

		case PartEmbeddedText:
			rendered, err := RenderEmbeddedText(*part.EmbeddedText)
			if err != nil {
				return nil, err
			}
			if err := nextText(rendered); err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			replayParts = append(replayParts, ReplayPart{
				Kind: PartEmbeddedText,
				EmbeddedText: &ReplayEmbeddedTextPart{
					URI:         part.EmbeddedText.URI,
					MIMEType:    cloneString(part.EmbeddedText.MIMEType),
					Text:        part.EmbeddedText.Text,
					Annotations: cloneAnnotations(part.EmbeddedText.Annotations),
				},
			})

		case PartEmbeddedBlob:
			rendered, err := RenderEmbeddedBlob(*part.EmbeddedBlob)
			if err != nil {
				return nil, err
			}
			if err := nextText(rendered); err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			data, err := nextImage(
				part.EmbeddedBlob.Ref.MIMEType,
				part.EmbeddedBlob.Detail,
			)
			if err != nil {
				return nil, err
			}
			replayParts = append(replayParts, ReplayPart{
				Kind: PartEmbeddedBlob,
				EmbeddedBlob: &ReplayEmbeddedBlobPart{
					URI:         part.EmbeddedBlob.URI,
					MIMEType:    part.EmbeddedBlob.MIMEType,
					Data:        data,
					Annotations: cloneAnnotations(part.EmbeddedBlob.Annotations),
				},
			})

		default:
			return nil, bounded("replay_projection", nil)
		}
	}
	if providerIndex != len(providerParts) || message.Content != content.String() {
		return nil, bounded("replay_projection", nil)
	}
	return replayParts, nil
}

func matchesReplayTextPart(part schema.MessageInputPart, expected string) bool {
	return part.Type == schema.ChatMessagePartTypeText &&
		part.Text == expected &&
		part.Image == nil &&
		part.Audio == nil &&
		part.Video == nil &&
		part.File == nil &&
		part.ToolSearchResult == nil &&
		len(part.Extra) == 0
}

func replayImageData(
	part schema.MessageInputPart,
	mimeType string,
	detail string,
) (string, bool) {
	if part.Type != schema.ChatMessagePartTypeImageURL ||
		part.Text != "" ||
		part.Image == nil ||
		part.Image.URL != nil ||
		part.Image.Base64Data == nil ||
		*part.Image.Base64Data == "" ||
		part.Image.MIMEType != mimeType ||
		string(part.Image.Detail) != detail ||
		// Reject the deprecated nested field too; old Eino messages may still
		// populate it and replay shape validation must remain exact.
		len(part.Image.Extra) != 0 || //nolint:staticcheck
		part.Audio != nil ||
		part.Video != nil ||
		part.File != nil ||
		part.ToolSearchResult != nil ||
		len(part.Extra) != 0 {
		return "", false
	}
	return *part.Image.Base64Data, true
}
