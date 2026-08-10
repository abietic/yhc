package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/abietic/yhc/engine/internal/mediaimage"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/cloudwego/eino/schema"
)

// runtimePromptWriter is an engine-owned capability. Durable ref prompts must
// be bound to the exact coordinator and item ID that published them.
type runtimePromptWriter struct{}

var errRuntimePromptAlreadySubmitting = errors.New(
	"durable prompt is already submitting",
)

type runtimeUserPromptWire struct {
	Display        string               `json:"display,omitempty"`
	Prompt         string               `json:"prompt,omitempty"`
	Images         []UserImage          `json:"images,omitempty"`
	PromptEnvelope *promptrecord.Record `json:"prompt_envelope,omitempty"`
}

type runtimeUserPromptRawWire struct {
	Display        string          `json:"display,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	Images         []UserImage     `json:"images,omitempty"`
	PromptEnvelope json.RawMessage `json:"prompt_envelope,omitempty"`
}

func (p RuntimeUserPrompt) MarshalJSON() ([]byte, error) {
	if p.durablePrompt == nil {
		return json.Marshal(runtimeUserPromptWire{
			Display: p.Display,
			Prompt:  p.Prompt,
			Images:  p.Images,
		})
	}
	if strings.TrimSpace(p.Prompt) != "" || len(p.Images) != 0 {
		return nil, fmt.Errorf("runtime user prompt mixes durable and inline media")
	}
	if err := p.durablePrompt.Validate(); err != nil {
		return nil, fmt.Errorf("runtime user prompt has invalid durable envelope")
	}
	record := p.durablePrompt.Clone()
	return json.Marshal(runtimeUserPromptWire{
		Display:        p.Display,
		PromptEnvelope: &record,
	})
}

func (p *RuntimeUserPrompt) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("runtime user prompt is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire runtimeUserPromptRawWire
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("runtime user prompt decode failed")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("runtime user prompt decode failed")
	}

	*p = RuntimeUserPrompt{
		Display: wire.Display,
		Prompt:  wire.Prompt,
		Images:  append([]UserImage(nil), wire.Images...),
	}
	if len(wire.PromptEnvelope) == 0 {
		return nil
	}
	if strings.TrimSpace(wire.Prompt) != "" || len(wire.Images) != 0 {
		return fmt.Errorf("runtime user prompt mixes durable and inline media")
	}
	var record promptrecord.Record
	if err := json.Unmarshal(wire.PromptEnvelope, &record); err != nil {
		return fmt.Errorf("runtime user prompt has invalid durable envelope")
	}
	p.Prompt = ""
	p.Images = nil
	p.durablePrompt = &record
	return nil
}

func (c *RuntimeInputCoordinator) enqueueDurableUserPrompt(
	item RuntimeItem,
	record promptrecord.Record,
	maxPending int,
) (RuntimeItem, error) {
	if c == nil || c.path == "" || c.mediaStore == nil {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: durable prompt writer is unavailable",
		)
	}
	if item.Kind != RuntimeItemUserPrompt || item.UserPrompt == nil {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: durable prompt requires a user item",
		)
	}
	record = record.Clone()
	item = cloneRuntimeItem(item)
	item.UserPrompt.Prompt = ""
	item.UserPrompt.Images = nil
	item.UserPrompt.durablePrompt = &record
	item.UserPrompt.materializedInput = nil
	item.UserPrompt.writer = c.promptWriter
	item.UserPrompt.writerItemID = item.ID
	accepted, err := c.EnqueueBounded(item, maxPending)
	if err != nil {
		return RuntimeItem{}, fmt.Errorf(
			"durable queued prompt ledger commit failed",
		)
	}
	return accepted, nil
}

func buildDurableRuntimePromptFromAdmitted(
	ctx context.Context,
	store *mediastore.Store,
	turnID string,
	input *AdmittedPromptInput,
) (promptrecord.Record, error) {
	if store == nil || store.Root() == "" || input == nil ||
		input.hasImages() && input.store == nil {
		return promptrecord.Record{}, fmt.Errorf(
			"durable queued prompt store unavailable",
		)
	}
	record := promptrecord.Record{
		Version: durablePromptRecordVersion(input),
		TurnID:  strings.TrimSpace(turnID),
		Parts:   make([]promptrecord.Part, 0, len(input.parts)),
	}
	for index, part := range input.parts {
		switch part.kind {
		case promptPartText:
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: part.text},
			})
		case promptPartImage:
			snapshot, err := input.store.snapshotPromptMedia(part.mediaRef)
			if err != nil {
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			info, reason := mediaimage.Inspect(snapshot.data, snapshot.mimeType)
			if reason != "" {
				clear(snapshot.data)
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			ref, err := store.Put(ctx, snapshot.data, mediastore.Metadata{
				MIMEType: snapshot.mimeType,
				Width:    info.Width,
				Height:   info.Height,
				Kind:     "prompt_image",
			})
			clear(snapshot.data)
			if err != nil {
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind: promptrecord.PartImage,
				Image: &promptrecord.ImagePart{
					Ref:         ref,
					Detail:      string(snapshot.detail),
					Annotations: part.annotations,
				},
			})
		case promptPartResourceLink:
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind:         promptrecord.PartResourceLink,
				ResourceLink: part.resourceLink,
			})
		case promptPartEmbeddedText:
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind:         promptrecord.PartEmbeddedText,
				EmbeddedText: part.embeddedText,
			})
		case promptPartEmbeddedBlob:
			snapshot, err := input.store.snapshotPromptMedia(part.mediaRef)
			if err != nil {
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			info, reason := mediaimage.Inspect(snapshot.data, snapshot.mimeType)
			if reason != "" {
				clear(snapshot.data)
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			ref, err := store.Put(ctx, snapshot.data, mediastore.Metadata{
				MIMEType: snapshot.mimeType,
				Width:    info.Width,
				Height:   info.Height,
				Kind:     "prompt_image",
			})
			clear(snapshot.data)
			if err != nil {
				return promptrecord.Record{}, durablePromptError(input, index)
			}
			embedded := *part.embeddedBlob
			embedded.Ref = ref
			embedded.MIMEType = snapshot.mimeType
			embedded.Detail = string(snapshot.detail)
			record.Parts = append(record.Parts, promptrecord.Part{
				Kind:         promptrecord.PartEmbeddedBlob,
				EmbeddedBlob: &embedded,
			})
		default:
			return promptrecord.Record{}, fmt.Errorf(
				"durable queued prompt has invalid admitted part",
			)
		}
	}
	if err := record.Validate(); err != nil {
		return promptrecord.Record{}, durablePromptError(
			input,
			firstDurablePromptPart(input),
		)
	}
	return record, nil
}

func (c *RuntimeInputCoordinator) materializeRuntimePrompt(
	item RuntimeItem,
) (RuntimeItem, error) {
	item = cloneRuntimeItem(item)
	if item.UserPrompt == nil || item.UserPrompt.durablePrompt == nil {
		return item, nil
	}
	if c == nil || c.mediaStore == nil {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: durable prompt media is unavailable",
		)
	}
	input, err := materializeDurableRuntimePrompt(
		context.Background(),
		c.mediaStore,
		*item.UserPrompt.durablePrompt,
	)
	if err != nil {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: durable prompt materialization failed",
		)
	}
	item.UserPrompt.materializedInput = &input
	return item, nil
}

func materializeDurableRuntimePrompt(
	ctx context.Context,
	store *mediastore.Store,
	record promptrecord.Record,
) (UntrustedPromptInput, error) {
	if err := record.Validate(); err != nil {
		return UntrustedPromptInput{}, err
	}
	parts := make([]UntrustedPromptPart, 0, len(record.Parts))
	for _, part := range record.Parts {
		switch part.Kind {
		case promptrecord.PartText:
			parts = append(parts, NewPromptTextPart(part.Text.Text))
		case promptrecord.PartImage:
			data, err := store.Resolve(ctx, part.Image.Ref)
			if err != nil {
				return UntrustedPromptInput{}, err
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			clear(data)
			parts = append(parts, NewPromptImagePartWithAnnotations(
				encoded,
				part.Image.Ref.MIMEType,
				PromptImageDetail(part.Image.Detail),
				enginePromptAnnotations(part.Image.Annotations),
			))
		case promptrecord.PartResourceLink:
			parts = append(parts, NewPromptResourceLinkPart(
				enginePromptResourceLink(*part.ResourceLink),
			))
		case promptrecord.PartEmbeddedText:
			parts = append(parts, NewPromptEmbeddedTextPart(
				enginePromptEmbeddedText(*part.EmbeddedText),
			))
		case promptrecord.PartEmbeddedBlob:
			data, err := store.Resolve(ctx, part.EmbeddedBlob.Ref)
			if err != nil {
				return UntrustedPromptInput{}, err
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			clear(data)
			parts = append(parts, NewPromptEmbeddedBlobPart(
				enginePromptEmbeddedBlob(*part.EmbeddedBlob, encoded),
			))
		default:
			return UntrustedPromptInput{}, fmt.Errorf(
				"durable queued prompt has invalid materialized part",
			)
		}
	}
	return NewUntrustedPromptInput(parts...), nil
}

func (c *RuntimeInputCoordinator) processingDurableRuntimePrompt(
	item RuntimeItem,
) (RuntimeItem, error) {
	if c == nil || item.UserPrompt == nil ||
		item.UserPrompt.durablePrompt == nil {
		return RuntimeItem{}, fmt.Errorf("runtime item is not a durable prompt")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.submitting[item.ID]; exists {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: %w",
			errRuntimePromptAlreadySubmitting,
		)
	}
	for _, current := range c.items {
		if current.ID != item.ID ||
			current.Kind != RuntimeItemUserPrompt ||
			current.State != RuntimeItemProcessing ||
			current.UserPrompt == nil ||
			current.UserPrompt.durablePrompt == nil ||
			current.UserPrompt.durablePrompt.TurnID !=
				item.UserPrompt.durablePrompt.TurnID {
			continue
		}
		materialized, err := c.materializeRuntimePrompt(current)
		if err != nil {
			return RuntimeItem{}, err
		}
		c.submitting[item.ID] = struct{}{}
		return materialized, nil
	}
	return RuntimeItem{}, fmt.Errorf(
		"runtime input coordinator: durable prompt is not the claimed item",
	)
}

func (c *RuntimeInputCoordinator) durablePromptRecord(
	id string,
) (promptrecord.Record, bool) {
	if c == nil || strings.TrimSpace(id) == "" {
		return promptrecord.Record{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range c.items {
		if item.ID == strings.TrimSpace(id) &&
			item.Kind == RuntimeItemUserPrompt &&
			item.State == RuntimeItemProcessing &&
			item.UserPrompt != nil &&
			item.UserPrompt.durablePrompt != nil {
			return item.UserPrompt.durablePrompt.Clone(), true
		}
	}
	return promptrecord.Record{}, false
}

func durablePromptText(record promptrecord.Record) string {
	var builder strings.Builder
	for _, part := range record.Parts {
		if part.Kind == promptrecord.PartText && part.Text != nil {
			builder.WriteString(part.Text.Text)
		}
	}
	return builder.String()
}

func runtimeItemDurablePrompt(item *RuntimeItem) promptrecord.Record {
	if item == nil ||
		item.UserPrompt == nil ||
		item.UserPrompt.durablePrompt == nil {
		return promptrecord.Record{}
	}
	return item.UserPrompt.durablePrompt.Clone()
}

func cloneUntrustedPromptInput(input UntrustedPromptInput) UntrustedPromptInput {
	parts := make([]UntrustedPromptPart, 0, len(input.Parts))
	for _, part := range input.Parts {
		switch typed := part.(type) {
		case untrustedPromptTextPart:
			parts = append(parts, NewPromptTextPart(typed.text))
		case untrustedPromptImagePart:
			parts = append(parts, NewPromptImagePartWithAnnotations(
				typed.base64Data,
				typed.mimeType,
				typed.detail,
				typed.annotations,
			))
		case untrustedPromptResourceLinkPart:
			parts = append(parts, NewPromptResourceLinkPart(typed.resource))
		case untrustedPromptEmbeddedTextPart:
			parts = append(parts, NewPromptEmbeddedTextPart(typed.resource))
		case untrustedPromptEmbeddedBlobPart:
			parts = append(parts, NewPromptEmbeddedBlobPart(typed.resource))
		default:
			parts = append(parts, part)
		}
	}
	return UntrustedPromptInput{
		Version: input.Version,
		Parts:   parts,
	}
}

func runtimeItemUserImages(item RuntimeItem) []UserImage {
	if item.UserPrompt == nil {
		return nil
	}
	if item.UserPrompt.materializedInput == nil {
		return append([]UserImage(nil), item.UserPrompt.Images...)
	}
	return untrustedPromptImages(*item.UserPrompt.materializedInput)
}

func untrustedPromptImages(input UntrustedPromptInput) []UserImage {
	images := make([]UserImage, 0)
	for _, part := range input.Parts {
		switch typed := part.(type) {
		case untrustedPromptImagePart:
			images = append(images, UserImage{
				MIMEType:   typed.mimeType,
				Base64Data: typed.base64Data,
			})
		case untrustedPromptEmbeddedBlobPart:
			images = append(images, UserImage{
				MIMEType:   typed.resource.MIMEType,
				Base64Data: typed.resource.Base64Data,
			})
		}
	}
	return images
}

func untrustedPromptMessage(
	input UntrustedPromptInput,
	extra map[string]any,
) *schema.Message {
	message := &schema.Message{
		Role:  schema.User,
		Extra: cloneMessageExtra(extra),
	}
	parts := make([]schema.MessageInputPart, 0, len(input.Parts))
	for _, part := range input.Parts {
		switch typed := part.(type) {
		case untrustedPromptTextPart:
			message.Content += typed.text
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: typed.text,
			})
		case untrustedPromptImagePart:
			data := typed.base64Data
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &data,
						MIMEType:   typed.mimeType,
					},
					Detail: schema.ImageURLDetail(typed.detail),
				},
			})
		case untrustedPromptResourceLinkPart:
			rendered, err := promptrecord.RenderResourceLink(
				promptRecordResourceLink(typed.resource),
			)
			if err == nil {
				message.Content += rendered
				parts = append(parts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: rendered,
				})
			}
		case untrustedPromptEmbeddedTextPart:
			rendered, err := promptrecord.RenderEmbeddedText(
				promptRecordEmbeddedText(typed.resource),
			)
			if err == nil {
				message.Content += rendered
				parts = append(parts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: rendered,
				})
			}
		case untrustedPromptEmbeddedBlobPart:
			rendered, err := promptrecord.RenderEmbeddedBlob(
				promptRecordEmbeddedBlob(
					typed.resource,
					typed.resource.Detail,
				),
			)
			if err == nil {
				message.Content += rendered
				parts = append(parts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: rendered,
				})
			}
			data := typed.resource.Base64Data
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &data,
						MIMEType:   typed.resource.MIMEType,
					},
					Detail: schema.ImageURLDetail(typed.resource.Detail),
				},
			})
		}
	}
	message.UserInputMultiContent = parts
	return message
}

func durablePromptWithAdmittedText(
	record promptrecord.Record,
	input *AdmittedPromptInput,
) (promptrecord.Record, error) {
	if input == nil || len(record.Parts) != len(input.parts) {
		return promptrecord.Record{}, fmt.Errorf(
			"durable queued prompt shape changed after admission",
		)
	}
	updated := record.Clone()
	for index, admittedPart := range input.parts {
		recordPart := &updated.Parts[index]
		switch admittedPart.kind {
		case promptPartText:
			if recordPart.Kind != promptrecord.PartText ||
				recordPart.Text == nil ||
				recordPart.Image != nil {
				return promptrecord.Record{}, fmt.Errorf(
					"durable queued prompt text shape changed after admission",
				)
			}
			recordPart.Text.Text = admittedPart.text
		case promptPartImage:
			if recordPart.Kind != promptrecord.PartImage ||
				recordPart.Image == nil ||
				recordPart.Text != nil ||
				recordPart.Image.Ref.MIMEType != admittedPart.mimeType ||
				recordPart.Image.Detail != string(admittedPart.detail) {
				return promptrecord.Record{}, fmt.Errorf(
					"durable queued prompt image shape changed after admission",
				)
			}
		case promptPartResourceLink:
			if recordPart.Kind != promptrecord.PartResourceLink ||
				recordPart.ResourceLink == nil ||
				recordPart.Text != nil ||
				recordPart.Image != nil ||
				recordPart.EmbeddedText != nil ||
				recordPart.EmbeddedBlob != nil {
				return promptrecord.Record{}, fmt.Errorf(
					"durable queued prompt resource-link shape changed after admission",
				)
			}
		case promptPartEmbeddedText:
			if recordPart.Kind != promptrecord.PartEmbeddedText ||
				recordPart.EmbeddedText == nil ||
				recordPart.Text != nil ||
				recordPart.Image != nil ||
				recordPart.ResourceLink != nil ||
				recordPart.EmbeddedBlob != nil {
				return promptrecord.Record{}, fmt.Errorf(
					"durable queued prompt embedded-text shape changed after admission",
				)
			}
		case promptPartEmbeddedBlob:
			if recordPart.Kind != promptrecord.PartEmbeddedBlob ||
				recordPart.EmbeddedBlob == nil ||
				recordPart.Text != nil ||
				recordPart.Image != nil ||
				recordPart.ResourceLink != nil ||
				recordPart.EmbeddedText != nil ||
				recordPart.EmbeddedBlob.Ref.MIMEType != admittedPart.mimeType ||
				recordPart.EmbeddedBlob.Detail != string(admittedPart.detail) {
				return promptrecord.Record{}, fmt.Errorf(
					"durable queued prompt embedded-blob shape changed after admission",
				)
			}
		default:
			return promptrecord.Record{}, fmt.Errorf(
				"durable queued prompt has invalid admitted part",
			)
		}
	}
	if err := updated.Validate(); err != nil {
		return promptrecord.Record{}, fmt.Errorf(
			"durable queued prompt is invalid after admission",
		)
	}
	return updated, nil
}

func enginePromptAnnotations(
	value *promptrecord.Annotations,
) *PromptResourceAnnotations {
	if value == nil {
		return nil
	}
	return &PromptResourceAnnotations{
		Audience:     append([]string(nil), value.Audience...),
		LastModified: cloneStringPointer(value.LastModified),
		Priority:     cloneFloatPointer(value.Priority),
	}
}

func enginePromptResourceLink(
	value promptrecord.ResourceLinkPart,
) PromptResourceLink {
	return PromptResourceLink{
		URI:         value.URI,
		Name:        value.Name,
		Title:       cloneStringPointer(value.Title),
		Description: cloneStringPointer(value.Description),
		MIMEType:    cloneStringPointer(value.MIMEType),
		Size:        cloneIntPointer(value.Size),
		Annotations: enginePromptAnnotations(value.Annotations),
	}
}

func enginePromptEmbeddedText(
	value promptrecord.EmbeddedTextPart,
) PromptEmbeddedTextResource {
	return PromptEmbeddedTextResource{
		URI:         value.URI,
		MIMEType:    cloneStringPointer(value.MIMEType),
		Text:        value.Text,
		Annotations: enginePromptAnnotations(value.Annotations),
	}
}

func enginePromptEmbeddedBlob(
	value promptrecord.EmbeddedBlobPart,
	base64Data string,
) PromptEmbeddedBlobResource {
	return PromptEmbeddedBlobResource{
		URI:         value.URI,
		MIMEType:    value.MIMEType,
		Base64Data:  base64Data,
		Detail:      PromptImageDetail(value.Detail),
		Annotations: enginePromptAnnotations(value.Annotations),
	}
}
