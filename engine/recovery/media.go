package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/mediaimage"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
)

const (
	// MediaRecoveryVersion is the observable bounded media recovery profile.
	MediaRecoveryVersion = 1

	// One admitted embedded blob is one logical prompt part but expands to an
	// adjacent metadata/image pair in the provider message.
	maxRecoveryPromptParts = 64
)

const (
	MediaStageInitial             = "initial"
	MediaStageSelected            = "selected_route_recovery"
	MediaStageFallbackEligibility = "fallback_eligibility"
	MediaStageFallback            = "fallback"
)

const historicalImageMarkerFormat = "" +
	"[historical image omitted during media-size recovery: mime=%s detail=%s]"

var recoveryTurnIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// CurrentTurn binds recovery to one exact original current-turn message and
// its immutable ordered rich-part signature.
type CurrentTurn struct {
	TurnID    string
	Message   *schema.Message
	signature [sha256.Size]byte
}

// BoundPromptRecord pairs a validated recorder-owned prompt record with the
// exact active message object tracked by that recorder.
type BoundPromptRecord struct {
	Message *schema.Message
	Record  promptrecord.Record
}

// MediaCandidate contains a canonical historical projection and one
// attempt-local provider clone. CanonicalMessages reuses unchanged message
// objects so recorder-owned ref bindings remain authoritative.
type MediaCandidate struct {
	CanonicalMessages    []*schema.Message
	ProviderMessages     []*schema.Message
	OmittedImageCount    int
	OmittedTurnCount     int
	DerivativeImageCount int
}

// MediaError is a bounded, redacted recovery failure.
type MediaError struct {
	Stage    string
	Category string
	Err      error
}

func (e *MediaError) Error() string {
	if e == nil {
		return "media recovery failed"
	}
	stage := boundedRecoveryValue(e.Stage, MediaStageInitial)
	category := boundedRecoveryValue(e.Category, "failed")
	return fmt.Sprintf("media recovery failed: stage=%s category=%s", stage, category)
}

func (e *MediaError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// BindCurrentTurn creates one immutable exact current-turn recovery binding.
func BindCurrentTurn(turnID string, message *schema.Message) (CurrentTurn, error) {
	turnID = strings.TrimSpace(turnID)
	if !recoveryTurnIDPattern.MatchString(turnID) || message == nil {
		return CurrentTurn{}, newMediaError(
			MediaStageInitial,
			"identity_unavailable",
			nil,
		)
	}
	signature, imageCount, ok := currentMessageSignature(message)
	if !ok || imageCount == 0 {
		return CurrentTurn{}, newMediaError(
			MediaStageInitial,
			"current_modalities_unavailable",
			nil,
		)
	}
	return CurrentTurn{
		TurnID:    turnID,
		Message:   message,
		signature: signature,
	}, nil
}

// BuildMediaCandidate constructs the sole selected-route recovery candidate.
// It omits only exact validated historical prompt-record images and prepares
// current-turn derivatives only in the deep provider-call clone.
func BuildMediaCandidate(
	ctx context.Context,
	messages []*schema.Message,
	current CurrentTurn,
	records []BoundPromptRecord,
) (*MediaCandidate, error) {
	canonical, omittedImages, omittedTurns, err := projectHistoricalMedia(
		ctx,
		messages,
		current,
		records,
	)
	if err != nil {
		return nil, err
	}
	providerMessages, derivativeImages, err := PrepareProviderMessages(
		ctx,
		canonical,
		current,
	)
	if err != nil {
		return nil, err
	}
	if omittedImages == 0 && derivativeImages == 0 {
		ClearProviderMessages(providerMessages)
		return nil, newMediaError(
			MediaStageInitial,
			"no_material_projection",
			nil,
		)
	}
	return &MediaCandidate{
		CanonicalMessages:    canonical,
		ProviderMessages:     providerMessages,
		OmittedImageCount:    omittedImages,
		OmittedTurnCount:     omittedTurns,
		DerivativeImageCount: derivativeImages,
	}, nil
}

// PrepareProviderMessages deep-clones one canonical active projection and
// applies Recovery Profile v1 only to the exact bound current-turn images.
func PrepareProviderMessages(
	ctx context.Context,
	messages []*schema.Message,
	current CurrentTurn,
) ([]*schema.Message, int, error) {
	currentIndex, err := validateCurrentTurn(messages, current)
	if err != nil {
		return nil, 0, err
	}
	if err := mediaContextError(ctx, MediaStageSelected); err != nil {
		return nil, 0, err
	}

	providerMessages := cloneProviderMessages(messages)
	providerCurrent := providerMessages[currentIndex]
	canonicalCurrent := messages[currentIndex]
	canonicalBytes := 0
	candidateBytes := 0
	derivativeCount := 0
	for index := range providerCurrent.UserInputMultiContent {
		providerPart := &providerCurrent.UserInputMultiContent[index]
		canonicalPart := canonicalCurrent.UserInputMultiContent[index]
		if canonicalPart.Type != schema.ChatMessagePartTypeImageURL {
			continue
		}
		if err := mediaContextError(ctx, MediaStageSelected); err != nil {
			ClearProviderMessages(providerMessages)
			return nil, 0, err
		}
		if canonicalPart.Image == nil ||
			canonicalPart.Image.Base64Data == nil ||
			providerPart.Image == nil {
			ClearProviderMessages(providerMessages)
			return nil, 0, newMediaError(
				MediaStageSelected,
				"current_modalities_stale",
				nil,
			)
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(
			*canonicalPart.Image.Base64Data,
		)
		if decodeErr != nil ||
			len(decoded) == 0 ||
			len(decoded) > mediastore.MaxBlobBytes {
			clear(decoded)
			ClearProviderMessages(providerMessages)
			return nil, 0, newMediaError(
				MediaStageSelected,
				"preparation_failed",
				nil,
			)
		}
		sourceBytes := len(decoded)
		canonicalBytes += sourceBytes
		derived, deriveErr := mediaimage.DeriveForRecovery(
			ctx,
			decoded,
			canonicalPart.Image.MIMEType,
			mediastore.MaxBlobBytes,
		)
		clear(decoded)
		if deriveErr != nil {
			ClearProviderMessages(providerMessages)
			if errors.Is(deriveErr, context.Canceled) ||
				errors.Is(deriveErr, context.DeadlineExceeded) {
				return nil, 0, newMediaError(
					MediaStageSelected,
					"canceled",
					deriveErr,
				)
			}
			return nil, 0, newMediaError(
				MediaStageSelected,
				"preparation_failed",
				nil,
			)
		}
		if len(derived.Data) == 0 {
			candidateBytes += sourceBytes
			continue
		}
		candidateBytes += len(derived.Data)
		encoded := base64.StdEncoding.EncodeToString(derived.Data)
		clear(derived.Data)
		providerPart.Image.Base64Data = &encoded
		providerPart.Image.MIMEType = derived.MIMEType
		derivativeCount++
	}
	if derivativeCount > 0 &&
		(canonicalBytes <= 0 || candidateBytes >= canonicalBytes) {
		ClearProviderMessages(providerMessages)
		return nil, 0, newMediaError(
			MediaStageSelected,
			"aggregate_not_smaller",
			nil,
		)
	}
	return providerMessages, derivativeCount, nil
}

// ClearProviderMessages drops every inline media buffer from an attempt-local
// provider clone. It must never be called with canonical messages.
func ClearProviderMessages(messages []*schema.Message) {
	for _, message := range messages {
		if message == nil {
			continue
		}
		for index := range message.UserInputMultiContent {
			part := &message.UserInputMultiContent[index]
			clearMessagePartCommonBase64(messageInputPartCommon(part))
		}
	}
}

// BoundaryMessage is the single durable active-context marker for historical
// omission. Its metadata contains counts only.
func BoundaryMessage(omittedImages, omittedTurns int) *schema.Message {
	return &schema.Message{
		Role:    schema.System,
		Content: "Media recovery projected the active conversation context.",
		Extra: map[string]any{
			"subtype":                "compact_boundary",
			"media_recovery_version": MediaRecoveryVersion,
			"recovery_reason":        "media_size_historical_omission",
			"omitted_image_count":    omittedImages,
			"omitted_turn_count":     omittedTurns,
			"current_turn_preserved": true,
		},
	}
}

// AttachmentMessage reports one bounded recovery stage without route or
// provider response details.
func AttachmentMessage(
	stage string,
	omittedImages int,
	derivativeImages int,
	fallback bool,
) *schema.Message {
	action := "current images were prepared for one bounded retry"
	if omittedImages > 0 && derivativeImages > 0 {
		action = "historical images were omitted and current images were resized for one bounded retry"
	} else if omittedImages > 0 {
		action = "historical images were omitted for one bounded retry"
	} else if derivativeImages > 0 {
		action = "current images were resized for one bounded retry"
	}
	return &schema.Message{
		Role:    schema.User,
		Content: "Media recovery: " + action + ".",
		Extra: map[string]any{
			"is_meta":                    true,
			"attachment_kind":            "media_recovery",
			"media_recovery_version":     MediaRecoveryVersion,
			"stage":                      boundedRecoveryValue(stage, MediaStageInitial),
			"omitted_image_count":        omittedImages,
			"derivative_image_count":     derivativeImages,
			"fallback_will_be_attempted": fallback,
		},
	}
}

// TerminalMessage is the sole redacted assistant projection for exhausted
// current-media recovery.
func TerminalMessage(stage string) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant,
		Content: "Image input could not be accepted after bounded media " +
			"recovery.",
		Extra: map[string]any{
			"api_error":              true,
			"error_type":             "media_size",
			"error_category":         "media_size",
			"media_recovery_version": MediaRecoveryVersion,
			"stage":                  boundedRecoveryValue(stage, MediaStageInitial),
		},
	}
}

// NewMediaError creates a bounded media failure for engine integration.
func NewMediaError(stage, category string) error {
	return newMediaError(stage, category, nil)
}

func projectHistoricalMedia(
	ctx context.Context,
	messages []*schema.Message,
	current CurrentTurn,
	records []BoundPromptRecord,
) ([]*schema.Message, int, int, error) {
	currentIndex, err := validateCurrentTurn(messages, current)
	if err != nil {
		return nil, 0, 0, err
	}
	canonical := append([]*schema.Message(nil), messages...)
	if len(records) == 0 {
		return canonical, 0, 0, nil
	}

	messageIndexes := make(map[*schema.Message]int, len(messages))
	for index, message := range messages {
		if message != nil {
			messageIndexes[message] = index
		}
	}
	turnCounts := make(map[string]int, len(records))
	ordered := true
	lastIndex := -1
	for _, binding := range records {
		index, ok := messageIndexes[binding.Message]
		if !ok || index <= lastIndex {
			ordered = false
		}
		lastIndex = index
		if err := binding.Record.Validate(); err == nil {
			turnCounts[binding.Record.TurnID]++
		}
	}
	if !ordered {
		return canonical, 0, 0, nil
	}

	for _, binding := range records {
		if binding.Message != current.Message {
			continue
		}
		if err := binding.Record.Validate(); err != nil ||
			binding.Record.TurnID != current.TurnID ||
			turnCounts[current.TurnID] != 1 {
			return nil, 0, 0, newMediaError(
				MediaStageInitial,
				"current_turn_mismatch",
				nil,
			)
		}
	}

	omittedImages := 0
	omittedTurns := 0
	for _, binding := range records {
		if err := mediaContextError(ctx, MediaStageSelected); err != nil {
			return nil, 0, 0, err
		}
		index, ok := messageIndexes[binding.Message]
		if !ok ||
			index >= currentIndex ||
			binding.Message == current.Message ||
			binding.Record.TurnID == current.TurnID ||
			turnCounts[binding.Record.TurnID] != 1 {
			continue
		}
		projected, count, ok := projectHistoricalPrompt(
			binding.Message,
			binding.Record,
		)
		if !ok || count == 0 {
			continue
		}
		canonical[index] = projected
		omittedImages += count
		omittedTurns++
	}
	return canonical, omittedImages, omittedTurns, nil
}

func validateCurrentTurn(
	messages []*schema.Message,
	current CurrentTurn,
) (int, error) {
	if !recoveryTurnIDPattern.MatchString(current.TurnID) ||
		current.Message == nil {
		return -1, newMediaError(
			MediaStageInitial,
			"identity_unavailable",
			nil,
		)
	}
	currentIndex := -1
	for index, message := range messages {
		if message != current.Message {
			continue
		}
		if currentIndex >= 0 {
			return -1, newMediaError(
				MediaStageInitial,
				"duplicate_current_message",
				nil,
			)
		}
		currentIndex = index
	}
	if currentIndex < 0 {
		return -1, newMediaError(
			MediaStageInitial,
			"current_message_unavailable",
			nil,
		)
	}
	signature, imageCount, ok := currentMessageSignature(current.Message)
	if !ok || imageCount == 0 || signature != current.signature {
		return -1, newMediaError(
			MediaStageInitial,
			"current_modalities_stale",
			nil,
		)
	}
	return currentIndex, nil
}

func projectHistoricalPrompt(
	message *schema.Message,
	record promptrecord.Record,
) (*schema.Message, int, bool) {
	if message == nil ||
		message.Role != schema.User ||
		len(record.Parts) == 0 {
		return nil, 0, false
	}
	expectedContent := strings.Builder{}
	projectedParts := make(
		[]schema.MessageInputPart,
		0,
		len(message.UserInputMultiContent),
	)
	imageCount := 0
	messagePartIndex := 0
	for _, recordPart := range record.Parts {
		switch recordPart.Kind {
		case promptrecord.PartText:
			if messagePartIndex >= len(message.UserInputMultiContent) {
				return nil, 0, false
			}
			messagePart := message.UserInputMultiContent[messagePartIndex]
			if recordPart.Text == nil ||
				messagePart.Type != schema.ChatMessagePartTypeText ||
				messagePart.Text != recordPart.Text.Text {
				return nil, 0, false
			}
			expectedContent.WriteString(recordPart.Text.Text)
			projectedParts = append(projectedParts, messagePart)
			messagePartIndex++
		case promptrecord.PartImage:
			if recordPart.Image == nil ||
				!appendHistoricalImageProjection(
					&projectedParts,
					message.UserInputMultiContent,
					&messagePartIndex,
					recordPart.Image.Ref,
					recordPart.Image.Detail,
				) {
				return nil, 0, false
			}
			imageCount++
		case promptrecord.PartResourceLink:
			if recordPart.ResourceLink == nil {
				return nil, 0, false
			}
			rendered, err := promptrecord.RenderResourceLink(
				*recordPart.ResourceLink,
			)
			if err != nil ||
				!appendHistoricalTextProjection(
					&projectedParts,
					message.UserInputMultiContent,
					&messagePartIndex,
					rendered,
				) {
				return nil, 0, false
			}
			expectedContent.WriteString(rendered)
		case promptrecord.PartEmbeddedText:
			if recordPart.EmbeddedText == nil {
				return nil, 0, false
			}
			rendered, err := promptrecord.RenderEmbeddedText(
				*recordPart.EmbeddedText,
			)
			if err != nil ||
				!appendHistoricalTextProjection(
					&projectedParts,
					message.UserInputMultiContent,
					&messagePartIndex,
					rendered,
				) {
				return nil, 0, false
			}
			expectedContent.WriteString(rendered)
		case promptrecord.PartEmbeddedBlob:
			if recordPart.EmbeddedBlob == nil {
				return nil, 0, false
			}
			rendered, err := promptrecord.RenderEmbeddedBlob(
				*recordPart.EmbeddedBlob,
			)
			if err != nil ||
				!appendHistoricalTextProjection(
					&projectedParts,
					message.UserInputMultiContent,
					&messagePartIndex,
					rendered,
				) ||
				!appendHistoricalImageProjection(
					&projectedParts,
					message.UserInputMultiContent,
					&messagePartIndex,
					recordPart.EmbeddedBlob.Ref,
					recordPart.EmbeddedBlob.Detail,
				) {
				return nil, 0, false
			}
			expectedContent.WriteString(rendered)
			imageCount++
		default:
			return nil, 0, false
		}
	}
	if messagePartIndex != len(message.UserInputMultiContent) ||
		message.Content != expectedContent.String() {
		return nil, 0, false
	}
	projected := *message
	projected.UserInputMultiContent = projectedParts
	return &projected, imageCount, true
}

func appendHistoricalTextProjection(
	projectedParts *[]schema.MessageInputPart,
	messageParts []schema.MessageInputPart,
	messagePartIndex *int,
	expected string,
) bool {
	if *messagePartIndex >= len(messageParts) {
		return false
	}
	messagePart := messageParts[*messagePartIndex]
	if messagePart.Type != schema.ChatMessagePartTypeText ||
		messagePart.Text != expected {
		return false
	}
	*projectedParts = append(*projectedParts, messagePart)
	*messagePartIndex++
	return true
}

func appendHistoricalImageProjection(
	projectedParts *[]schema.MessageInputPart,
	messageParts []schema.MessageInputPart,
	messagePartIndex *int,
	ref mediastore.Ref,
	detail string,
) bool {
	if *messagePartIndex >= len(messageParts) {
		return false
	}
	messagePart := messageParts[*messagePartIndex]
	if ref.Validate() != nil ||
		messagePart.Type != schema.ChatMessagePartTypeImageURL ||
		messagePart.Image == nil ||
		messagePart.Image.Base64Data == nil ||
		*messagePart.Image.Base64Data == "" ||
		messagePart.Image.MIMEType != ref.MIMEType ||
		string(messagePart.Image.Detail) != detail ||
		!validRecoveryMIME(ref.MIMEType) ||
		!validRecoveryDetail(detail) {
		return false
	}
	*projectedParts = append(*projectedParts, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: fmt.Sprintf(
			historicalImageMarkerFormat,
			ref.MIMEType,
			detail,
		),
	})
	*messagePartIndex++
	return true
}

func currentMessageSignature(
	message *schema.Message,
) ([sha256.Size]byte, int, bool) {
	var zero [sha256.Size]byte
	if message == nil ||
		message.Role != schema.User ||
		len(message.UserInputMultiContent) == 0 ||
		len(message.UserInputMultiContent) > maxRecoveryPromptParts {
		return zero, 0, false
	}
	hash := sha256.New()
	writeRecoveryHashString(hash, string(message.Role))
	writeRecoveryHashString(hash, message.Content)
	imageCount := 0
	for _, part := range message.UserInputMultiContent {
		writeRecoveryHashString(hash, string(part.Type))
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			writeRecoveryHashString(hash, part.Text)
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil ||
				part.Image.Base64Data == nil ||
				*part.Image.Base64Data == "" ||
				part.Image.URL != nil ||
				!validRecoveryMIME(part.Image.MIMEType) ||
				!validRecoveryDetail(string(part.Image.Detail)) {
				return zero, 0, false
			}
			writeRecoveryHashString(hash, part.Image.MIMEType)
			writeRecoveryHashString(hash, string(part.Image.Detail))
			writeRecoveryHashString(hash, *part.Image.Base64Data)
			imageCount++
		default:
			return zero, 0, false
		}
	}
	copy(zero[:], hash.Sum(nil))
	return zero, imageCount, true
}

func writeRecoveryHashString(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}

func cloneProviderMessages(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, len(messages))
	for index, message := range messages {
		cloned[index] = cloneProviderMessage(message)
	}
	return cloned
}

func cloneProviderMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	cloned := *message
	cloned.Extra = cloneRecoveryMap(message.Extra)
	if len(message.MultiContent) > 0 {
		cloned.MultiContent = append( //nolint:staticcheck
			[]schema.ChatMessagePart(nil), //nolint:staticcheck
			message.MultiContent...,
		)
		for index := range cloned.MultiContent { //nolint:staticcheck
			part := &cloned.MultiContent[index] //nolint:staticcheck
			if part.ImageURL != nil {
				imageURL := *part.ImageURL
				imageURL.Extra = cloneRecoveryMap(part.ImageURL.Extra)
				part.ImageURL = &imageURL
			}
			if part.AudioURL != nil {
				audioURL := *part.AudioURL
				audioURL.Extra = cloneRecoveryMap(part.AudioURL.Extra)
				part.AudioURL = &audioURL
			}
			if part.VideoURL != nil {
				videoURL := *part.VideoURL
				videoURL.Extra = cloneRecoveryMap(part.VideoURL.Extra)
				part.VideoURL = &videoURL
			}
			if part.FileURL != nil {
				fileURL := *part.FileURL
				fileURL.Extra = cloneRecoveryMap(part.FileURL.Extra)
				part.FileURL = &fileURL
			}
		}
	}
	if len(message.UserInputMultiContent) > 0 {
		cloned.UserInputMultiContent = make(
			[]schema.MessageInputPart,
			len(message.UserInputMultiContent),
		)
		for index, part := range message.UserInputMultiContent {
			cloned.UserInputMultiContent[index] = cloneInputPart(part)
		}
	}
	if len(message.AssistantGenMultiContent) > 0 {
		cloned.AssistantGenMultiContent = append(
			[]schema.MessageOutputPart(nil),
			message.AssistantGenMultiContent...,
		)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		for index := range cloned.ToolCalls {
			if message.ToolCalls[index].Index != nil {
				value := *message.ToolCalls[index].Index
				cloned.ToolCalls[index].Index = &value
			}
			cloned.ToolCalls[index].Extra = cloneRecoveryMap(
				message.ToolCalls[index].Extra,
			)
		}
	}
	if message.ResponseMeta != nil {
		meta := *message.ResponseMeta
		if message.ResponseMeta.Usage != nil {
			usage := *message.ResponseMeta.Usage
			meta.Usage = &usage
		}
		cloned.ResponseMeta = &meta
	}
	return &cloned
}

func cloneInputPart(part schema.MessageInputPart) schema.MessageInputPart {
	cloned := part
	cloned.Extra = cloneRecoveryMap(part.Extra)
	if part.Image != nil {
		imagePart := *part.Image
		imagePart.MessagePartCommon = cloneMessagePartCommon(
			part.Image.MessagePartCommon,
		)
		cloned.Image = &imagePart
	}
	if part.Audio != nil {
		audioPart := *part.Audio
		audioPart.MessagePartCommon = cloneMessagePartCommon(
			part.Audio.MessagePartCommon,
		)
		cloned.Audio = &audioPart
	}
	if part.Video != nil {
		videoPart := *part.Video
		videoPart.MessagePartCommon = cloneMessagePartCommon(
			part.Video.MessagePartCommon,
		)
		cloned.Video = &videoPart
	}
	if part.File != nil {
		filePart := *part.File
		filePart.MessagePartCommon = cloneMessagePartCommon(
			part.File.MessagePartCommon,
		)
		cloned.File = &filePart
	}
	return cloned
}

func cloneMessagePartCommon(
	common schema.MessagePartCommon,
) schema.MessagePartCommon {
	cloned := common
	if common.URL != nil {
		value := *common.URL
		cloned.URL = &value
	}
	if common.Base64Data != nil {
		value := *common.Base64Data
		cloned.Base64Data = &value
	}
	cloned.Extra = cloneRecoveryMap(common.Extra) //nolint:staticcheck
	return cloned
}

func cloneRecoveryMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func messageInputPartCommon(
	part *schema.MessageInputPart,
) *schema.MessagePartCommon {
	if part == nil {
		return nil
	}
	switch {
	case part.Image != nil:
		return &part.Image.MessagePartCommon
	case part.Audio != nil:
		return &part.Audio.MessagePartCommon
	case part.Video != nil:
		return &part.Video.MessagePartCommon
	case part.File != nil:
		return &part.File.MessagePartCommon
	default:
		return nil
	}
}

func clearMessagePartCommonBase64(common *schema.MessagePartCommon) {
	if common == nil || common.Base64Data == nil {
		return
	}
	*common.Base64Data = ""
	common.Base64Data = nil
}

func validRecoveryMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validRecoveryDetail(value string) bool {
	switch value {
	case "auto", "low", "high":
		return true
	default:
		return false
	}
}

func mediaContextError(ctx context.Context, stage string) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return newMediaError(stage, "canceled", err)
	}
	return nil
}

func newMediaError(stage, category string, err error) error {
	return &MediaError{
		Stage:    boundedRecoveryValue(stage, MediaStageInitial),
		Category: boundedRecoveryValue(category, "failed"),
		Err:      err,
	}
}

func boundedRecoveryValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return fallback
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' ||
			char >= '0' && char <= '9' ||
			char == '_' || char == '-' {
			continue
		}
		return fallback
	}
	return value
}
