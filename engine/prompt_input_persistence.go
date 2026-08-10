package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abietic/yhc/engine/internal/mediaimage"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/cloudwego/eino/schema"
)

func (e *QueryEngine) persistAdmittedPromptMedia(
	ctx context.Context,
	turnID string,
	input *AdmittedPromptInput,
) (promptrecord.Record, error) {
	if e == nil || input == nil || !input.requiresDurablePrompt() {
		return promptrecord.Record{}, nil
	}
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil || recorder.Path() == "" {
		return promptrecord.Record{}, newPromptAdmissionError(
			firstDurablePromptPart(input),
			promptPartKindAt(input, firstDurablePromptPart(input)),
			"media_store_unavailable",
			"",
			"",
		)
	}
	record := promptrecord.Record{
		Version: durablePromptRecordVersion(input),
		TurnID:  turnID,
		Parts:   make([]promptrecord.Part, 0, len(input.parts)),
	}
	if err := os.MkdirAll(filepath.Dir(recorder.Path()), 0o700); err != nil {
		return promptrecord.Record{}, newPromptAdmissionError(
			firstDurablePromptPart(input),
			promptPartKindAt(input, firstDurablePromptPart(input)),
			"media_store_unavailable",
			"",
			"",
		)
	}
	store := mediastore.New(recorder.Path() + ".media")
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
			info, reason := mediaimage.Inspect(
				snapshot.data,
				snapshot.mimeType,
			)
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
			info, reason := mediaimage.Inspect(
				snapshot.data,
				snapshot.mimeType,
			)
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
				"persist admitted prompt: invalid internal part",
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

func (e *QueryEngine) recordDurableUserPrompt(
	record promptrecord.Record,
	message *schema.Message,
	input *AdmittedPromptInput,
) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil {
		return durablePromptError(input, firstDurablePromptPart(input))
	}
	runtimeItemID := ""
	if message != nil {
		runtimeItemID = runtimeItemIDFromMetadata(message.Extra)
	}
	var err error
	if runtimeItemID == "" {
		err = recorder.RecordUserPrompt(record, message)
	} else {
		err = recorder.RecordRuntimeUserPrompt(
			record,
			message,
			runtimeItemID,
		)
	}
	if err != nil {
		return durablePromptError(input, firstDurablePromptPart(input))
	}
	if err := recorder.Flush(); err != nil {
		return durablePromptError(input, firstDurablePromptPart(input))
	}
	e.settleRuntimeItemsFromMessages([]*schema.Message{message})
	return nil
}

func durablePromptError(
	input *AdmittedPromptInput,
	partIndex int,
) error {
	providerName := ""
	modelName := ""
	if input != nil && input.binding != nil {
		providerName = string(input.binding.provider)
		modelName = input.binding.model
	}
	return newPromptAdmissionError(
		partIndex,
		promptPartKindAt(input, partIndex),
		"media_persistence_failed",
		providerName,
		modelName,
	)
}

func firstDurablePromptPart(input *AdmittedPromptInput) int {
	if input == nil {
		return -1
	}
	for index, part := range input.parts {
		if part.kind != promptPartText {
			return index
		}
	}
	return -1
}

func promptPartKindAt(input *AdmittedPromptInput, index int) string {
	if input == nil || index < 0 || index >= len(input.parts) {
		return "input"
	}
	return string(input.parts[index].kind)
}

func durablePromptRecordVersion(input *AdmittedPromptInput) int {
	if input == nil {
		return promptrecord.Version1
	}
	for _, part := range input.parts {
		if part.kind == promptPartResourceLink ||
			part.kind == promptPartEmbeddedText ||
			part.kind == promptPartEmbeddedBlob ||
			part.annotations != nil {
			return promptrecord.Version2
		}
	}
	return promptrecord.Version1
}
