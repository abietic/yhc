package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/abietic/yhc/engine/internal/promptrecord"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/cloudwego/eino/schema"
)

const UntrustedPromptInputVersion1 = 1

const maxPromptInputParts = 32

// UntrustedPromptInput is the versioned, ordered public prompt boundary.
// Parts are immutable values created by the constructors in this package.
type UntrustedPromptInput struct {
	Version int
	Parts   []UntrustedPromptPart
}

// NewUntrustedPromptInput creates a version-1 ordered prompt snapshot.
func NewUntrustedPromptInput(parts ...UntrustedPromptPart) UntrustedPromptInput {
	return UntrustedPromptInput{
		Version: UntrustedPromptInputVersion1,
		Parts:   append([]UntrustedPromptPart(nil), parts...),
	}
}

// UntrustedPromptPart is a closed union of literal text, image, ResourceLink,
// and embedded-resource parts.
type UntrustedPromptPart interface {
	promptInputPart()
}

type untrustedPromptTextPart struct {
	text string
}

func (untrustedPromptTextPart) promptInputPart() {}

type untrustedPromptImagePart struct {
	base64Data  string
	mimeType    string
	detail      PromptImageDetail
	annotations *PromptResourceAnnotations
}

func (untrustedPromptImagePart) promptInputPart() {}

type untrustedPromptResourceLinkPart struct {
	resource PromptResourceLink
}

func (untrustedPromptResourceLinkPart) promptInputPart() {}

type untrustedPromptEmbeddedTextPart struct {
	resource PromptEmbeddedTextResource
}

func (untrustedPromptEmbeddedTextPart) promptInputPart() {}

type untrustedPromptEmbeddedBlobPart struct {
	resource PromptEmbeddedBlobResource
}

func (untrustedPromptEmbeddedBlobPart) promptInputPart() {}

// NewPromptTextPart creates one immutable text part.
func NewPromptTextPart(text string) UntrustedPromptPart {
	return untrustedPromptTextPart{text: text}
}

// PromptImageDetail is the provider-neutral image detail hint.
type PromptImageDetail string

const (
	PromptImageDetailAuto PromptImageDetail = "auto"
	PromptImageDetailLow  PromptImageDetail = "low"
	PromptImageDetailHigh PromptImageDetail = "high"
)

// NewPromptImagePart creates one immutable inline image part. Base64 data must
// not contain a data-URL prefix.
func NewPromptImagePart(
	base64Data string,
	mimeType string,
	detail PromptImageDetail,
) UntrustedPromptPart {
	return NewPromptImagePartWithAnnotations(
		base64Data,
		mimeType,
		detail,
		nil,
	)
}

// NewPromptImagePartWithAnnotations creates one immutable inline image part
// with bounded standard protocol annotations.
func NewPromptImagePartWithAnnotations(
	base64Data string,
	mimeType string,
	detail PromptImageDetail,
	annotations *PromptResourceAnnotations,
) UntrustedPromptPart {
	return untrustedPromptImagePart{
		base64Data:  base64Data,
		mimeType:    mimeType,
		detail:      detail,
		annotations: clonePromptAnnotations(annotations),
	}
}

// PromptEmbeddedTextResource is one client-supplied embedded text resource.
// URI is metadata only and never grants filesystem or network authority.
type PromptEmbeddedTextResource struct {
	URI         string
	MIMEType    *string
	Text        string
	Annotations *PromptResourceAnnotations
}

// PromptEmbeddedBlobResource is one client-supplied embedded safe-raster
// resource. Base64Data contains raw base64 without a data-URL prefix.
type PromptEmbeddedBlobResource struct {
	URI         string
	MIMEType    string
	Base64Data  string
	Detail      PromptImageDetail
	Annotations *PromptResourceAnnotations
}

// NewPromptResourceLinkPart creates one immutable no-fetch ResourceLink part.
func NewPromptResourceLinkPart(resource PromptResourceLink) UntrustedPromptPart {
	return untrustedPromptResourceLinkPart{
		resource: clonePromptResourceLink(resource),
	}
}

// NewPromptEmbeddedTextPart creates one immutable embedded text part.
func NewPromptEmbeddedTextPart(
	resource PromptEmbeddedTextResource,
) UntrustedPromptPart {
	return untrustedPromptEmbeddedTextPart{
		resource: clonePromptEmbeddedText(resource),
	}
}

// NewPromptEmbeddedBlobPart creates one immutable embedded safe-raster part.
func NewPromptEmbeddedBlobPart(
	resource PromptEmbeddedBlobResource,
) UntrustedPromptPart {
	return untrustedPromptEmbeddedBlobPart{
		resource: clonePromptEmbeddedBlob(resource),
	}
}

// ValidateUntrustedPromptInputMetadata validates the closed ordered union and
// bounded client-supplied metadata without selecting a provider route,
// decoding media, or mutating a Session. Protocol adapters use it before
// Session lookup to preserve malformed-input precedence.
func ValidateUntrustedPromptInputMetadata(
	input UntrustedPromptInput,
) error {
	if input.Version != UntrustedPromptInputVersion1 {
		return newPromptAdmissionError(
			-1,
			"input",
			"unsupported_version",
			"",
			"",
		)
	}
	if len(input.Parts) > maxPromptInputParts {
		return newPromptAdmissionError(
			maxPromptInputParts,
			"input",
			"too_many_parts",
			"",
			"",
		)
	}
	for index, part := range input.Parts {
		switch typed := part.(type) {
		case untrustedPromptTextPart:
		case untrustedPromptImagePart:
			if typed.base64Data == "" {
				return newPromptAdmissionError(
					index,
					string(promptPartImage),
					"invalid_base64_data",
					"",
					"",
				)
			}
			if strings.TrimSpace(typed.mimeType) == "" {
				return newPromptAdmissionError(
					index,
					string(promptPartImage),
					"unsupported_mime_type",
					"",
					"",
				)
			}
			detail := typed.detail
			if detail == "" {
				detail = PromptImageDetailAuto
			}
			if !validPromptImageDetail(detail) {
				return newPromptAdmissionError(
					index,
					string(promptPartImage),
					"invalid_detail",
					"",
					"",
				)
			}
			if _, err := promptRecordAnnotations(typed.annotations); err != nil {
				return newPromptAdmissionError(
					index,
					string(promptPartImage),
					promptRecordReason(err, "annotation_invalid"),
					"",
					"",
				)
			}
		case untrustedPromptResourceLinkPart:
			if _, err := promptrecord.RenderResourceLink(
				promptRecordResourceLink(typed.resource),
			); err != nil {
				return newPromptAdmissionError(
					index,
					string(promptPartResourceLink),
					promptRecordReason(err, "resource_metadata_invalid"),
					"",
					"",
				)
			}
		case untrustedPromptEmbeddedTextPart:
			if _, err := promptrecord.RenderEmbeddedText(
				promptRecordEmbeddedText(typed.resource),
			); err != nil {
				return newPromptAdmissionError(
					index,
					string(promptPartEmbeddedText),
					promptRecordReason(err, "embedded_metadata_invalid"),
					"",
					"",
				)
			}
		case untrustedPromptEmbeddedBlobPart:
			if typed.resource.Base64Data == "" {
				return newPromptAdmissionError(
					index,
					string(promptPartEmbeddedBlob),
					"invalid_base64_data",
					"",
					"",
				)
			}
			detail := typed.resource.Detail
			if detail == "" {
				detail = PromptImageDetailAuto
			}
			if !validPromptImageDetail(detail) {
				return newPromptAdmissionError(
					index,
					string(promptPartEmbeddedBlob),
					"invalid_detail",
					"",
					"",
				)
			}
			if _, err := promptrecord.RenderEmbeddedBlob(
				promptRecordEmbeddedBlob(typed.resource, detail),
			); err != nil {
				return newPromptAdmissionError(
					index,
					string(promptPartEmbeddedBlob),
					promptRecordReason(err, "embedded_metadata_invalid"),
					"",
					"",
				)
			}
		default:
			return newPromptAdmissionError(
				index,
				"unknown",
				"invalid_part_union",
				"",
				"",
			)
		}
	}
	return nil
}

// AdmittedPromptInput is an engine-owned immutable prompt snapshot. Its fields
// intentionally remain private so callers cannot mint or alter admitted media.
type AdmittedPromptInput struct {
	parts   []admittedPromptPart
	store   *turnMediaStore
	binding *promptRouteBinding
}

type admittedPromptPart struct {
	kind         promptPartKind
	text         string
	mediaRef     MediaRef
	mimeType     string
	detail       PromptImageDetail
	byteCount    int
	annotations  *promptrecord.Annotations
	resourceLink *promptrecord.ResourceLinkPart
	embeddedText *promptrecord.EmbeddedTextPart
	embeddedBlob *promptrecord.EmbeddedBlobPart
}

type promptPartKind string

const (
	promptPartText         promptPartKind = "text"
	promptPartImage        promptPartKind = "image"
	promptPartResourceLink promptPartKind = "resource_link"
	promptPartEmbeddedText promptPartKind = "embedded_text"
	promptPartEmbeddedBlob promptPartKind = "embedded_blob"
)

// MediaRef is a turn-local, generation-bound opaque media capability.
type MediaRef struct {
	storeID    string
	mediaID    string
	generation uint64
}

// PromptInputAdmissionError identifies a rejected part without exposing prompt
// text, media bytes, references, filenames, paths, or provider response bodies.
type PromptInputAdmissionError struct {
	PartIndex  int
	PartKind   string
	ReasonCode string
	Provider   string
	Model      string
}

func (e *PromptInputAdmissionError) Error() string {
	if e == nil {
		return "prompt input admission failed"
	}
	message := fmt.Sprintf(
		"prompt input admission: part=%d kind=%s reason=%s",
		e.PartIndex,
		e.PartKind,
		e.ReasonCode,
	)
	if providerName := boundedRouteIdentity(e.Provider); providerName != "" {
		message += " provider=" + providerName
	}
	if modelName := boundedRouteIdentity(e.Model); modelName != "" {
		message += " model=" + modelName
	}
	return message
}

// PromptCapabilityStatus is a three-state route capability decision.
type PromptCapabilityStatus string

const (
	PromptCapabilitySupported   PromptCapabilityStatus = "supported"
	PromptCapabilityUnsupported PromptCapabilityStatus = "unsupported"
	PromptCapabilityUnknown     PromptCapabilityStatus = "unknown"
)

// PromptCapabilityDecision binds a capability result to its source identity.
type PromptCapabilityDecision struct {
	Status PromptCapabilityStatus
	Source string
}

// PromptCapabilityResolver resolves image-input support for one exact provider
// route. Production roots install DefaultPromptCapabilityResolver explicitly.
type PromptCapabilityResolver interface {
	ResolvePromptCapability(
		provider.Provider,
		string,
	) PromptCapabilityDecision
}

// PromptCapabilityResolverFunc adapts deterministic fixtures and custom model
// inventories to PromptCapabilityResolver.
type PromptCapabilityResolverFunc func(
	provider.Provider,
	string,
) PromptCapabilityDecision

func (f PromptCapabilityResolverFunc) ResolvePromptCapability(
	providerID provider.Provider,
	modelID string,
) PromptCapabilityDecision {
	return f(providerID, modelID)
}

type defaultPromptCapabilityResolver struct{}

// DefaultPromptCapabilityResolver returns the project-owned exact model
// inventory used by standard CLI, TUI, and ACP composition roots.
func DefaultPromptCapabilityResolver() PromptCapabilityResolver {
	return defaultPromptCapabilityResolver{}
}

func (defaultPromptCapabilityResolver) ResolvePromptCapability(
	providerID provider.Provider,
	modelID string,
) PromptCapabilityDecision {
	const source = "default-model-registry-v1"
	entry := modelcaps.DefaultRegistry().Lookup(modelID)
	if entry == nil {
		return PromptCapabilityDecision{
			Status: PromptCapabilityUnknown,
			Source: source,
		}
	}
	entryProvider, err := normalizeRegistryProvider(entry.Provider)
	if err != nil || entryProvider != providerID {
		return PromptCapabilityDecision{
			Status: PromptCapabilityUnknown,
			Source: source,
		}
	}
	status := PromptCapabilityUnsupported
	if entry.SupportsMedia {
		status = PromptCapabilitySupported
	}
	return PromptCapabilityDecision{Status: status, Source: source}
}

func selectedPromptCapabilityDecision(
	modelResolver ModelResolver,
	fallback PromptCapabilityResolver,
	selector string,
	resolved provider.ResolvedConfig,
) PromptCapabilityDecision {
	if inventory, ok := modelResolver.(runtimeModelInventory); ok {
		entry, err := inventory.ResolveInventorySelector(selector)
		if err == nil &&
			strings.TrimSpace(entry.ProfileID) != "" &&
			!strings.HasPrefix(
				strings.ToLower(strings.TrimSpace(entry.Selector)),
				"legacy:",
			) &&
			entry.Provider == string(resolved.Provider) &&
			entry.APIModel == resolved.Model {
			source := "profile-metadata:" + strings.TrimSpace(
				entry.Metadata.Images.Source,
			)
			if entry.Metadata.Images.Source == "" ||
				entry.Metadata.Images.Source == "unknown" {
				return PromptCapabilityDecision{
					Status: PromptCapabilityUnknown,
					Source: source,
				}
			}
			status := PromptCapabilityUnsupported
			if entry.Metadata.Images.Value {
				status = PromptCapabilitySupported
			}
			return PromptCapabilityDecision{Status: status, Source: source}
		}
	}
	if fallback == nil {
		return PromptCapabilityDecision{Status: PromptCapabilityUnknown}
	}
	return fallback.ResolvePromptCapability(
		resolved.Provider,
		resolved.Model,
	)
}

func normalizeRegistryProvider(raw string) (provider.Provider, error) {
	if strings.EqualFold(strings.TrimSpace(raw), "ByteDance") {
		return provider.ProviderAgenticArk, nil
	}
	return provider.NormalizeProvider(provider.Provider(raw))
}

// ProviderMediaPreparer is the sole lowering boundary from opaque MediaRef
// values to provider-ready inline media. External packages cannot implement it.
type ProviderMediaPreparer interface {
	preparePromptMedia(MediaRef) (preparedPromptMedia, error)
}

type preparedPromptMedia struct {
	base64Data string
	mimeType   string
	detail     PromptImageDetail
}

type storedPromptMedia struct {
	data     []byte
	mimeType string
	detail   PromptImageDetail
}

type promptMediaSnapshot struct {
	data     []byte
	mimeType string
	detail   PromptImageDetail
}

type turnMediaStore struct {
	mu         sync.Mutex
	storeID    string
	generation uint64
	media      map[string]*storedPromptMedia
	destroyed  bool
}

func newTurnMediaStore() (*turnMediaStore, error) {
	storeID, err := newPromptMediaToken()
	if err != nil {
		return nil, err
	}
	return &turnMediaStore{
		storeID:    storeID,
		generation: 1,
		media:      make(map[string]*storedPromptMedia),
	}, nil
}

func (s *turnMediaStore) add(
	data []byte,
	mimeType string,
	detail PromptImageDetail,
) (MediaRef, error) {
	mediaID, err := newPromptMediaToken()
	if err != nil {
		return MediaRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media[mediaID] = &storedPromptMedia{
		data:     append([]byte(nil), data...),
		mimeType: mimeType,
		detail:   detail,
	}
	return MediaRef{
		storeID:    s.storeID,
		mediaID:    mediaID,
		generation: s.generation,
	}, nil
}

func (s *turnMediaStore) preparePromptMedia(
	ref MediaRef,
) (preparedPromptMedia, error) {
	if s == nil {
		return preparedPromptMedia{}, fmt.Errorf("prompt media store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed ||
		ref.storeID != s.storeID ||
		ref.generation != s.generation {
		return preparedPromptMedia{}, fmt.Errorf("prompt media reference expired")
	}
	media := s.media[ref.mediaID]
	if media == nil {
		return preparedPromptMedia{}, fmt.Errorf("prompt media reference unavailable")
	}
	return preparedPromptMedia{
		base64Data: base64.StdEncoding.EncodeToString(media.data),
		mimeType:   media.mimeType,
		detail:     media.detail,
	}, nil
}

func (s *turnMediaStore) matchesPromptMedia(
	ref MediaRef,
	mimeType string,
	detail PromptImageDetail,
	byteCount int,
) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed ||
		ref.storeID != s.storeID ||
		ref.generation != s.generation {
		return false
	}
	media := s.media[ref.mediaID]
	return media != nil &&
		media.mimeType == mimeType &&
		media.detail == detail &&
		len(media.data) == byteCount
}

func (s *turnMediaStore) snapshotPromptMedia(
	ref MediaRef,
) (promptMediaSnapshot, error) {
	if s == nil {
		return promptMediaSnapshot{}, fmt.Errorf("prompt media store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed ||
		ref.storeID != s.storeID ||
		ref.generation != s.generation {
		return promptMediaSnapshot{}, fmt.Errorf("prompt media reference expired")
	}
	media := s.media[ref.mediaID]
	if media == nil {
		return promptMediaSnapshot{}, fmt.Errorf("prompt media reference unavailable")
	}
	return promptMediaSnapshot{
		data:     append([]byte(nil), media.data...),
		mimeType: media.mimeType,
		detail:   media.detail,
	}, nil
}

func (s *turnMediaStore) destroy() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return
	}
	for key, media := range s.media {
		if media != nil {
			clear(media.data)
			media.data = nil
		}
		delete(s.media, key)
	}
	s.generation++
	s.destroyed = true
}

type promptRouteBinding struct {
	baseGeneration        uint64
	expectedGeneration    uint64
	baseModelSpec         string
	requestedModelSpec    string
	selectedModelSpec     string
	applyModelOverride    bool
	provider              provider.Provider
	model                 string
	capabilitySource      string
	firstImagePart        int
	firstImageKind        promptPartKind
	admittedPartSignature string
}

func (e *QueryEngine) admitPromptInput(
	ctx context.Context,
	input UntrustedPromptInput,
	modelOverride string,
) (*AdmittedPromptInput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newPromptAdmissionError(-1, "input", "canceled", "", "")
	}
	if input.Version != UntrustedPromptInputVersion1 {
		return nil, newPromptAdmissionError(
			-1,
			"input",
			"unsupported_version",
			"",
			"",
		)
	}

	parts := append([]UntrustedPromptPart(nil), input.Parts...)
	if len(parts) > maxPromptInputParts {
		return nil, newPromptAdmissionError(
			maxPromptInputParts,
			"input",
			"too_many_parts",
			"",
			"",
		)
	}
	admitted := &AdmittedPromptInput{
		parts: make([]admittedPromptPart, 0, len(parts)),
	}
	images := make([]UserImage, 0)
	imagePartIndexes := make([]int, 0)
	imageDetails := make([]PromptImageDetail, 0)
	imagePartKinds := make([]promptPartKind, 0)
	for index, part := range parts {
		switch typed := part.(type) {
		case untrustedPromptTextPart:
			admitted.parts = append(admitted.parts, admittedPromptPart{
				kind: promptPartText,
				text: typed.text,
			})
		case untrustedPromptImagePart:
			detail := typed.detail
			if detail == "" {
				detail = PromptImageDetailAuto
			}
			if !validPromptImageDetail(detail) {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartImage),
					"invalid_detail",
					"",
					"",
				)
			}
			annotations, err := promptRecordAnnotations(typed.annotations)
			if err != nil {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartImage),
					promptRecordReason(err, "annotation_invalid"),
					"",
					"",
				)
			}
			admitted.parts = append(admitted.parts, admittedPromptPart{
				kind:        promptPartImage,
				mimeType:    typed.mimeType,
				detail:      detail,
				annotations: annotations,
			})
			images = append(images, UserImage{
				MIMEType:   typed.mimeType,
				Base64Data: typed.base64Data,
			})
			imagePartIndexes = append(imagePartIndexes, index)
			imageDetails = append(imageDetails, detail)
			imagePartKinds = append(imagePartKinds, promptPartImage)
		case untrustedPromptResourceLinkPart:
			resource := promptRecordResourceLink(typed.resource)
			rendered, err := promptrecord.RenderResourceLink(resource)
			if err != nil {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartResourceLink),
					promptRecordReason(err, "resource_metadata_invalid"),
					"",
					"",
				)
			}
			admitted.parts = append(admitted.parts, admittedPromptPart{
				kind:         promptPartResourceLink,
				text:         rendered,
				resourceLink: &resource,
			})
		case untrustedPromptEmbeddedTextPart:
			resource := promptRecordEmbeddedText(typed.resource)
			rendered, err := promptrecord.RenderEmbeddedText(resource)
			if err != nil {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartEmbeddedText),
					promptRecordReason(err, "embedded_metadata_invalid"),
					"",
					"",
				)
			}
			admitted.parts = append(admitted.parts, admittedPromptPart{
				kind:         promptPartEmbeddedText,
				text:         rendered,
				embeddedText: &resource,
			})
		case untrustedPromptEmbeddedBlobPart:
			detail := typed.resource.Detail
			if detail == "" {
				detail = PromptImageDetailAuto
			}
			if !validPromptImageDetail(detail) {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartEmbeddedBlob),
					"invalid_detail",
					"",
					"",
				)
			}
			resource := promptRecordEmbeddedBlob(typed.resource, detail)
			rendered, err := promptrecord.RenderEmbeddedBlob(resource)
			if err != nil {
				return nil, newPromptAdmissionError(
					index,
					string(promptPartEmbeddedBlob),
					promptRecordReason(err, "embedded_metadata_invalid"),
					"",
					"",
				)
			}
			admitted.parts = append(admitted.parts, admittedPromptPart{
				kind:         promptPartEmbeddedBlob,
				text:         rendered,
				mimeType:     typed.resource.MIMEType,
				detail:       detail,
				annotations:  resource.Annotations,
				embeddedBlob: &resource,
			})
			images = append(images, UserImage{
				MIMEType:   typed.resource.MIMEType,
				Base64Data: typed.resource.Base64Data,
			})
			imagePartIndexes = append(imagePartIndexes, index)
			imageDetails = append(imageDetails, detail)
			imagePartKinds = append(imagePartKinds, promptPartEmbeddedBlob)
		default:
			return nil, newPromptAdmissionError(
				index,
				"unknown",
				"invalid_part_union",
				"",
				"",
			)
		}
	}
	if len(images) == 0 {
		return admitted, nil
	}
	if err := validateUserImages(images); err != nil {
		var imageErr *UserImageValidationError
		if !errors.As(err, &imageErr) ||
			imageErr.ImageIndex < 0 ||
			imageErr.ImageIndex >= len(imagePartIndexes) {
			return nil, newPromptAdmissionError(
				imagePartIndexes[0],
				string(imagePartKinds[0]),
				"invalid_image",
				"",
				"",
			)
		}
		return nil, newPromptAdmissionError(
			imagePartIndexes[imageErr.ImageIndex],
			string(imagePartKinds[imageErr.ImageIndex]),
			imageErr.ReasonCode,
			"",
			"",
		)
	}

	e.planMu.Lock()
	e.mu.Lock()
	baseGeneration := e.promptRouteGeneration
	baseModelSpec := e.config.Model
	requestedModelSpec := strings.TrimSpace(modelOverride)
	if requestedModelSpec == "" {
		requestedModelSpec = baseModelSpec
	}
	modelResolver := e.config.ModelResolver
	capabilityResolver := e.config.PromptCapabilityResolver
	closed := e.promptRouteClosed
	permissionMode := e.config.PermissionMode
	routeMessages := append([]*schema.Message(nil), e.messages...)
	e.mu.Unlock()
	if permissionMode == "" {
		permissionMode = permission.ModeDefault
	}
	if planPhaseRequiresContainment(e.planState.Phase) {
		permissionMode = permission.ModePlan
	}
	selectedModelSpec := getRuntimeMainLoopModel(
		&ToolUseOptions{
			MainLoopModel:  requestedModelSpec,
			PermissionMode: permissionMode,
		},
		routeMessages,
	)
	e.planMu.Unlock()
	firstImagePart := imagePartIndexes[0]
	firstImageKind := imagePartKinds[0]
	if closed {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"engine_closed",
			"",
			"",
		)
	}
	if strings.TrimSpace(selectedModelSpec) == "" || modelResolver == nil {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"route_unknown",
			"",
			selectedModelSpec,
		)
	}
	resolved, err := modelResolver.ResolveModel(selectedModelSpec)
	if err != nil ||
		strings.TrimSpace(string(resolved.Provider)) == "" ||
		strings.TrimSpace(resolved.Model) == "" {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"route_unknown",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	decision := selectedPromptCapabilityDecision(
		modelResolver,
		capabilityResolver,
		selectedModelSpec,
		resolved,
	)
	source := boundedRouteIdentity(decision.Source)
	if source == "" ||
		decision.Status != PromptCapabilitySupported &&
			decision.Status != PromptCapabilityUnsupported {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"capability_unknown",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	if decision.Status != PromptCapabilitySupported {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"capability_unsupported",
			string(resolved.Provider),
			resolved.Model,
		)
	}

	if err := ctx.Err(); err != nil {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"canceled",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	store, err := newTurnMediaStore()
	if err != nil {
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"media_store_unavailable",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	imageIndex := 0
	for partIndex := range admitted.parts {
		if admitted.parts[partIndex].kind != promptPartImage &&
			admitted.parts[partIndex].kind != promptPartEmbeddedBlob {
			continue
		}
		decoded, reason := decodeUserImageBase64(images[imageIndex].Base64Data)
		if reason != "" {
			store.destroy()
			return nil, newPromptAdmissionError(
				imagePartIndexes[imageIndex],
				string(imagePartKinds[imageIndex]),
				reason,
				string(resolved.Provider),
				resolved.Model,
			)
		}
		mimeType, _ := normalizedUserImageMIME(images[imageIndex].MIMEType)
		byteCount := len(decoded)
		ref, addErr := store.add(
			decoded,
			mimeType,
			imageDetails[imageIndex],
		)
		clear(decoded)
		if addErr != nil {
			store.destroy()
			return nil, newPromptAdmissionError(
				imagePartIndexes[imageIndex],
				string(imagePartKinds[imageIndex]),
				"media_store_unavailable",
				string(resolved.Provider),
				resolved.Model,
			)
		}
		admitted.parts[partIndex].mediaRef = ref
		admitted.parts[partIndex].mimeType = mimeType
		admitted.parts[partIndex].detail = imageDetails[imageIndex]
		admitted.parts[partIndex].byteCount = byteCount
		if admitted.parts[partIndex].embeddedBlob != nil {
			admitted.parts[partIndex].embeddedBlob.MIMEType = mimeType
		}
		imageIndex++
	}
	binding := &promptRouteBinding{
		baseGeneration:        baseGeneration,
		expectedGeneration:    baseGeneration,
		baseModelSpec:         baseModelSpec,
		requestedModelSpec:    requestedModelSpec,
		selectedModelSpec:     selectedModelSpec,
		applyModelOverride:    strings.TrimSpace(modelOverride) != "",
		provider:              resolved.Provider,
		model:                 resolved.Model,
		capabilitySource:      source,
		firstImagePart:        firstImagePart,
		firstImageKind:        firstImageKind,
		admittedPartSignature: promptPartSignature(admitted.parts),
	}
	admitted.store = store
	admitted.binding = binding

	e.mu.Lock()
	if e.promptRouteClosed ||
		e.promptRouteGeneration != baseGeneration ||
		e.config.Model != baseModelSpec {
		e.mu.Unlock()
		store.destroy()
		return nil, newPromptAdmissionError(
			firstImagePart,
			string(firstImageKind),
			"route_stale",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	if e.activePromptMedia == nil {
		e.activePromptMedia = make(map[*turnMediaStore]struct{})
	}
	e.activePromptMedia[store] = struct{}{}
	e.mu.Unlock()
	return admitted, nil
}

func (e *QueryEngine) activateAdmittedPrompt(
	input *AdmittedPromptInput,
	planTurn planTurnSnapshot,
) error {
	if input == nil || input.binding == nil {
		return nil
	}
	binding := input.binding
	e.mu.Lock()
	if e.promptRouteClosed ||
		e.promptRouteGeneration != binding.baseGeneration ||
		e.config.Model != binding.baseModelSpec {
		e.mu.Unlock()
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"route_stale",
			string(binding.provider),
			binding.model,
		)
	}
	selectedModelSpec := getRuntimeMainLoopModel(
		&ToolUseOptions{
			MainLoopModel:  binding.requestedModelSpec,
			PermissionMode: planTurn.Mode,
		},
		e.messages,
	)
	if selectedModelSpec != binding.selectedModelSpec {
		e.mu.Unlock()
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"route_stale",
			string(binding.provider),
			binding.model,
		)
	}
	if binding.applyModelOverride &&
		e.config.Model != binding.requestedModelSpec {
		e.config.Model = binding.requestedModelSpec
		e.deprecationWarning = modelcaps.CheckDeprecation(binding.model)
		if e.subagentExecutor != nil {
			e.subagentExecutor.ParentModelName = binding.requestedModelSpec
		}
		e.promptRouteGeneration++
	}
	binding.expectedGeneration = e.promptRouteGeneration
	e.mu.Unlock()
	return e.checkAdmittedPromptRoute(input, binding.selectedModelSpec)
}

func (e *QueryEngine) checkAdmittedPromptRoute(
	input *AdmittedPromptInput,
	currentModel string,
) error {
	if input == nil || input.binding == nil {
		return nil
	}
	return e.checkAdmittedPromptBinding(
		input,
		input.binding,
		currentModel,
	)
}

func (e *QueryEngine) bindAdmittedPromptRecoveryRoute(
	input *AdmittedPromptInput,
	currentModel string,
) (*promptRouteBinding, error) {
	if input == nil || input.binding == nil {
		return nil, newPromptAdmissionError(
			-1,
			"media",
			"route_unknown",
			"",
			"",
		)
	}
	currentModel = strings.TrimSpace(currentModel)
	e.mu.Lock()
	generation := e.promptRouteGeneration
	closed := e.promptRouteClosed
	modelResolver := e.config.ModelResolver
	capabilityResolver := e.config.PromptCapabilityResolver
	baseModelSpec := e.config.Model
	e.mu.Unlock()
	if closed || currentModel == "" || modelResolver == nil {
		return nil, newPromptAdmissionError(
			input.binding.firstImagePart,
			string(input.binding.firstImageKind),
			"route_unknown",
			"",
			"",
		)
	}
	resolved, err := modelResolver.ResolveModel(currentModel)
	if err != nil ||
		strings.TrimSpace(string(resolved.Provider)) == "" ||
		strings.TrimSpace(resolved.Model) == "" {
		return nil, newPromptAdmissionError(
			input.binding.firstImagePart,
			string(input.binding.firstImageKind),
			"route_unknown",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	decision := selectedPromptCapabilityDecision(
		modelResolver,
		capabilityResolver,
		currentModel,
		resolved,
	)
	source := boundedRouteIdentity(decision.Source)
	if decision.Status != PromptCapabilitySupported || source == "" {
		return nil, newPromptAdmissionError(
			input.binding.firstImagePart,
			string(input.binding.firstImageKind),
			"capability_unknown",
			string(resolved.Provider),
			resolved.Model,
		)
	}
	binding := &promptRouteBinding{
		baseGeneration:        generation,
		expectedGeneration:    generation,
		baseModelSpec:         baseModelSpec,
		requestedModelSpec:    currentModel,
		selectedModelSpec:     currentModel,
		provider:              resolved.Provider,
		model:                 resolved.Model,
		capabilitySource:      source,
		firstImagePart:        input.binding.firstImagePart,
		firstImageKind:        input.binding.firstImageKind,
		admittedPartSignature: promptPartSignature(input.parts),
	}
	if err := e.checkAdmittedPromptBinding(
		input,
		binding,
		currentModel,
	); err != nil {
		return nil, err
	}
	return binding, nil
}

func (e *QueryEngine) checkAdmittedPromptBinding(
	input *AdmittedPromptInput,
	binding *promptRouteBinding,
	currentModel string,
) error {
	if input == nil || binding == nil {
		return newPromptAdmissionError(
			-1,
			"media",
			"route_unknown",
			"",
			"",
		)
	}
	e.mu.Lock()
	generation := e.promptRouteGeneration
	closed := e.promptRouteClosed
	modelResolver := e.config.ModelResolver
	capabilityResolver := e.config.PromptCapabilityResolver
	e.mu.Unlock()
	if closed ||
		generation != binding.expectedGeneration ||
		strings.TrimSpace(currentModel) != binding.selectedModelSpec {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"route_stale",
			string(binding.provider),
			binding.model,
		)
	}
	if modelResolver == nil {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"capability_unknown",
			string(binding.provider),
			binding.model,
		)
	}
	resolved, err := modelResolver.ResolveModel(currentModel)
	if err != nil ||
		resolved.Provider != binding.provider ||
		resolved.Model != binding.model {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"route_stale",
			string(binding.provider),
			binding.model,
		)
	}
	decision := selectedPromptCapabilityDecision(
		modelResolver,
		capabilityResolver,
		currentModel,
		resolved,
	)
	if decision.Status != PromptCapabilitySupported ||
		boundedRouteIdentity(decision.Source) != binding.capabilitySource {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"capability_stale",
			string(binding.provider),
			binding.model,
		)
	}
	if binding.admittedPartSignature != promptPartSignature(input.parts) {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"media_binding_stale",
			string(binding.provider),
			binding.model,
		)
	}
	for index, part := range input.parts {
		if part.kind != promptPartImage &&
			part.kind != promptPartEmbeddedBlob {
			continue
		}
		if !input.store.matchesPromptMedia(
			part.mediaRef,
			part.mimeType,
			part.detail,
			part.byteCount,
		) {
			return newPromptAdmissionError(
				index,
				string(part.kind),
				"media_binding_stale",
				string(binding.provider),
				binding.model,
			)
		}
	}
	e.mu.Lock()
	stillCurrent := !e.promptRouteClosed &&
		e.promptRouteGeneration == binding.expectedGeneration
	e.mu.Unlock()
	if !stillCurrent {
		return newPromptAdmissionError(
			binding.firstImagePart,
			string(binding.firstImageKind),
			"route_stale",
			string(binding.provider),
			binding.model,
		)
	}
	return nil
}

func (e *QueryEngine) releaseAdmittedPrompt(input *AdmittedPromptInput) {
	if input == nil || input.store == nil {
		return
	}
	store := input.store
	e.mu.Lock()
	delete(e.activePromptMedia, store)
	e.mu.Unlock()
	store.destroy()
}

func (input *AdmittedPromptInput) textForHook() string {
	if input == nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range input.parts {
		if part.kind == promptPartText {
			builder.WriteString(part.text)
		}
	}
	return builder.String()
}

func (input *AdmittedPromptInput) modelText() string {
	if input == nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range input.parts {
		switch part.kind {
		case promptPartText, promptPartResourceLink,
			promptPartEmbeddedText, promptPartEmbeddedBlob:
			builder.WriteString(part.text)
		}
	}
	return builder.String()
}

func (input *AdmittedPromptInput) hasImages() bool {
	if input == nil {
		return false
	}
	for _, part := range input.parts {
		if part.kind == promptPartImage ||
			part.kind == promptPartEmbeddedBlob {
			return true
		}
	}
	return false
}

func (input *AdmittedPromptInput) requiresDurablePrompt() bool {
	if input == nil {
		return false
	}
	for _, part := range input.parts {
		if part.kind != promptPartText {
			return true
		}
	}
	return false
}

func (input *AdmittedPromptInput) withHookRewrite(
	updatedPrompt string,
) (*AdmittedPromptInput, error) {
	if input == nil || updatedPrompt == input.textForHook() {
		return input, nil
	}
	textIndex := -1
	for index, part := range input.parts {
		if part.kind != promptPartText {
			continue
		}
		if textIndex >= 0 {
			return nil, newPromptAdmissionError(
				index,
				string(promptPartText),
				"ambiguous_hook_rewrite",
				"",
				"",
			)
		}
		textIndex = index
	}
	if textIndex < 0 {
		return nil, newPromptAdmissionError(
			-1,
			string(promptPartText),
			"ambiguous_hook_rewrite",
			"",
			"",
		)
	}
	cloned := &AdmittedPromptInput{
		parts:   append([]admittedPromptPart(nil), input.parts...),
		store:   input.store,
		binding: input.binding,
	}
	cloned.parts[textIndex].text = updatedPrompt
	return cloned, nil
}

func (input *AdmittedPromptInput) message(
	extra map[string]any,
) (*schema.Message, error) {
	message := &schema.Message{
		Role:    schema.User,
		Content: input.modelText(),
		Extra:   cloneMessageExtra(extra),
	}
	parts := make([]schema.MessageInputPart, 0, len(input.parts)+1)
	for index, part := range input.parts {
		switch part.kind {
		case promptPartText, promptPartResourceLink, promptPartEmbeddedText:
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: part.text,
			})
		case promptPartImage, promptPartEmbeddedBlob:
			if part.kind == promptPartEmbeddedBlob {
				parts = append(parts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: part.text,
				})
			}
			prepared, err := input.store.preparePromptMedia(part.mediaRef)
			if err != nil {
				return nil, newPromptAdmissionError(
					index,
					string(part.kind),
					"media_binding_stale",
					"",
					"",
				)
			}
			data := prepared.base64Data
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &data,
						MIMEType:   prepared.mimeType,
					},
					Detail: schema.ImageURLDetail(prepared.detail),
				},
			})
		default:
			return nil, newPromptAdmissionError(
				index,
				"unknown",
				"invalid_admitted_part",
				"",
				"",
			)
		}
	}
	message.UserInputMultiContent = parts
	return message, nil
}

func validPromptImageDetail(detail PromptImageDetail) bool {
	switch detail {
	case PromptImageDetailAuto, PromptImageDetailLow, PromptImageDetailHigh:
		return true
	default:
		return false
	}
}

func promptPartSignature(parts []admittedPromptPart) string {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(string(part.kind))
		builder.WriteByte(':')
		switch part.kind {
		case promptPartText:
			builder.WriteString("ordered")
		case promptPartResourceLink, promptPartEmbeddedText:
			builder.WriteString(strconv.Itoa(len(part.text)))
			builder.WriteByte(':')
			builder.WriteString(part.text)
		case promptPartImage, promptPartEmbeddedBlob:
			if part.kind == promptPartEmbeddedBlob {
				builder.WriteString(strconv.Itoa(len(part.text)))
				builder.WriteByte(':')
				builder.WriteString(part.text)
				builder.WriteByte(':')
			}
			builder.WriteString(part.mediaRef.storeID)
			builder.WriteByte(':')
			builder.WriteString(part.mediaRef.mediaID)
			builder.WriteByte(':')
			builder.WriteString(part.mimeType)
			builder.WriteByte(':')
			builder.WriteString(string(part.detail))
			builder.WriteByte(':')
			builder.WriteString(strconv.Itoa(part.byteCount))
		}
		builder.WriteByte(';')
	}
	return builder.String()
}

func newPromptAdmissionError(
	partIndex int,
	partKind string,
	reason string,
	providerName string,
	modelName string,
) error {
	return &PromptInputAdmissionError{
		PartIndex:  partIndex,
		PartKind:   partKind,
		ReasonCode: reason,
		Provider:   boundedRouteIdentity(providerName),
		Model:      boundedRouteIdentity(modelName),
	}
}

func promptRecordAnnotations(
	value *PromptResourceAnnotations,
) (*promptrecord.Annotations, error) {
	if value == nil {
		return nil, nil
	}
	annotations := &promptrecord.Annotations{
		Audience:     append([]string(nil), value.Audience...),
		LastModified: cloneStringPointer(value.LastModified),
		Priority:     cloneFloatPointer(value.Priority),
	}
	_, err := promptrecord.RenderResourceLink(promptrecord.ResourceLinkPart{
		URI:         "urn:eino-agent:annotation-validation",
		Name:        "annotation",
		Annotations: annotations,
	})
	if err != nil {
		return nil, err
	}
	return annotations, nil
}

func promptRecordResourceLink(
	value PromptResourceLink,
) promptrecord.ResourceLinkPart {
	annotations, _ := promptRecordAnnotations(value.Annotations)
	return promptrecord.ResourceLinkPart{
		URI:         value.URI,
		Name:        value.Name,
		Title:       cloneStringPointer(value.Title),
		Description: cloneStringPointer(value.Description),
		MIMEType:    cloneStringPointer(value.MIMEType),
		Size:        cloneIntPointer(value.Size),
		Annotations: annotations,
	}
}

func promptRecordEmbeddedText(
	value PromptEmbeddedTextResource,
) promptrecord.EmbeddedTextPart {
	annotations, _ := promptRecordAnnotations(value.Annotations)
	return promptrecord.EmbeddedTextPart{
		URI:         value.URI,
		MIMEType:    cloneStringPointer(value.MIMEType),
		Text:        value.Text,
		Annotations: annotations,
	}
}

func promptRecordEmbeddedBlob(
	value PromptEmbeddedBlobResource,
	detail PromptImageDetail,
) promptrecord.EmbeddedBlobPart {
	annotations, _ := promptRecordAnnotations(value.Annotations)
	return promptrecord.EmbeddedBlobPart{
		URI:         value.URI,
		MIMEType:    value.MIMEType,
		Detail:      string(detail),
		Annotations: annotations,
	}
}

func promptRecordReason(err error, fallback string) string {
	var recordErr *promptrecord.Error
	if errors.As(err, &recordErr) &&
		boundedRouteIdentity(recordErr.Category) != "" {
		return recordErr.Category
	}
	return fallback
}

func clonePromptAnnotations(
	value *PromptResourceAnnotations,
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

func clonePromptResourceLink(value PromptResourceLink) PromptResourceLink {
	return PromptResourceLink{
		URI:         value.URI,
		Name:        value.Name,
		Title:       cloneStringPointer(value.Title),
		Description: cloneStringPointer(value.Description),
		MIMEType:    cloneStringPointer(value.MIMEType),
		Size:        cloneIntPointer(value.Size),
		Annotations: clonePromptAnnotations(value.Annotations),
	}
}

func clonePromptEmbeddedText(
	value PromptEmbeddedTextResource,
) PromptEmbeddedTextResource {
	return PromptEmbeddedTextResource{
		URI:         value.URI,
		MIMEType:    cloneStringPointer(value.MIMEType),
		Text:        value.Text,
		Annotations: clonePromptAnnotations(value.Annotations),
	}
}

func clonePromptEmbeddedBlob(
	value PromptEmbeddedBlobResource,
) PromptEmbeddedBlobResource {
	return PromptEmbeddedBlobResource{
		URI:         value.URI,
		MIMEType:    value.MIMEType,
		Base64Data:  value.Base64Data,
		Detail:      value.Detail,
		Annotations: clonePromptAnnotations(value.Annotations),
	}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func boundedRouteIdentity(value string) string {
	const maxBytes = 96
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes {
		return ""
	}
	for _, r := range value {
		if unicode.IsLetter(r) ||
			unicode.IsDigit(r) ||
			strings.ContainsRune("._:/@+-", r) {
			continue
		}
		return ""
	}
	return value
}

func newPromptMediaToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
