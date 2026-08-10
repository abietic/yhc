package promptrecord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/cloudwego/eino/schema"
)

const (
	// Version1 is the first ref-backed durable user-prompt codec.
	Version1 = 1
	// Version2 adds typed resource-link and embedded-resource prompt parts while
	// retaining the version-1 text/image reader unchanged.
	Version2 = 2
	// Kind is the transcript record kind owned by this codec.
	Kind = "user-prompt"

	maxParts                   = 32
	maxTextBytes               = 1024 * 1024
	maxImages                  = 20
	maxMediaBytes              = 10 * 1024 * 1024
	maxResourceDescriptorBytes = 16 * 1024
	maxAnnotationAudience      = 8
	maxAnnotationStringBytes   = 256
)

const (
	PartText         = "text"
	PartImage        = "image"
	PartResourceLink = "resource_link"
	PartEmbeddedText = "embedded_text"
	PartEmbeddedBlob = "embedded_blob"
)

var turnIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Record is one closed, ordered durable user prompt. It never contains media
// bytes, filesystem paths, digests, caller names, or transport provenance.
type Record struct {
	Version int    `json:"version"`
	TurnID  string `json:"turn_id"`
	Parts   []Part `json:"parts"`
}

// Part is a closed union. Exactly one payload must match Kind.
type Part struct {
	Kind         string            `json:"kind"`
	Text         *TextPart         `json:"text,omitempty"`
	Image        *ImagePart        `json:"image,omitempty"`
	ResourceLink *ResourceLinkPart `json:"resource_link,omitempty"`
	EmbeddedText *EmbeddedTextPart `json:"embedded_text,omitempty"`
	EmbeddedBlob *EmbeddedBlobPart `json:"embedded_blob,omitempty"`
}

type TextPart struct {
	Text string `json:"text"`
}

type ImagePart struct {
	Ref         mediastore.Ref `json:"ref"`
	Detail      string         `json:"detail"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// Annotations is the bounded standard ACP/MCP annotation projection. Reserved
// protocol extension metadata is intentionally absent.
type Annotations struct {
	Audience     []string `json:"audience,omitempty"`
	LastModified *string  `json:"lastModified,omitempty"`
	Priority     *float64 `json:"priority,omitempty"`
}

// ResourceLinkPart retains client-supplied metadata but grants no URI
// authority. It is rendered only as a bounded deterministic descriptor.
type ResourceLinkPart struct {
	URI         string       `json:"uri"`
	Name        string       `json:"name"`
	Title       *string      `json:"title,omitempty"`
	Description *string      `json:"description,omitempty"`
	MIMEType    *string      `json:"mime_type,omitempty"`
	Size        *int         `json:"size,omitempty"`
	Annotations *Annotations `json:"annotations,omitempty"`
}

// EmbeddedTextPart is one logical embedded text resource.
type EmbeddedTextPart struct {
	URI         string       `json:"uri"`
	MIMEType    *string      `json:"mime_type,omitempty"`
	Text        string       `json:"text"`
	Annotations *Annotations `json:"annotations,omitempty"`
}

// EmbeddedBlobPart is one logical embedded safe-raster resource. The bytes
// remain exclusively in the Session-private MediaStore.
type EmbeddedBlobPart struct {
	URI         string         `json:"uri"`
	MIMEType    string         `json:"mime_type"`
	Ref         mediastore.Ref `json:"ref"`
	Detail      string         `json:"detail"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// Descriptor is the presentation-safe ordered projection of a durable prompt.
// It contains no media identity, digest, path, URI, or bytes.
type Descriptor struct {
	Parts []PartDescriptor
}

// PartDescriptor is a closed presentation union for every durable prompt kind.
type PartDescriptor struct {
	Kind     string
	Text     string
	MIMEType string
	Image    *ImageDescriptor
}

// ImageDescriptor contains only bounded public image metadata.
type ImageDescriptor struct {
	MIMEType  string
	SizeBytes int64
	Width     int
	Height    int
	Detail    string
}

// UnmarshalJSON rejects unknown fields, trailing values, unsupported versions,
// malformed unions, duplicate media identities, and oversized text.
func (r *Record) UnmarshalJSON(data []byte) error {
	type wireRecord Record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wireRecord
	if err := decoder.Decode(&decoded); err != nil {
		return bounded("decode", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return bounded("decode", err)
	}
	record := Record(decoded)
	if err := record.Validate(); err != nil {
		return err
	}
	*r = record
	return nil
}

// Validate enforces the complete versioned durable shape.
func (r Record) Validate() error {
	if r.Version != Version1 && r.Version != Version2 {
		return bounded("unsupported_version", nil)
	}
	if !turnIDPattern.MatchString(r.TurnID) {
		return bounded("invalid_turn_identity", nil)
	}
	if len(r.Parts) == 0 || len(r.Parts) > maxParts {
		return bounded("invalid_parts", nil)
	}
	seenMedia := make(map[string]struct{})
	textBytes := 0
	imageCount := 0
	var mediaBytes int64
	for _, part := range r.Parts {
		if payloadCount(part) != 1 {
			return bounded("invalid_part_union", nil)
		}
		switch part.Kind {
		case PartText:
			if part.Text == nil {
				return bounded("invalid_part_union", nil)
			}
			textBytes += len(part.Text.Text)
			if textBytes > maxTextBytes {
				return bounded("text_limit", nil)
			}
		case PartImage:
			if part.Image == nil {
				return bounded("invalid_part_union", nil)
			}
			if r.Version == Version1 && part.Image.Annotations != nil {
				return bounded("invalid_part_union", nil)
			}
			if err := validateAnnotations(part.Image.Annotations); err != nil {
				return err
			}
			if err := part.Image.Ref.Validate(); err != nil {
				return bounded("invalid_media_reference", err)
			}
			if err := validateImageDetail(part.Image.Detail); err != nil {
				return err
			}
			if _, exists := seenMedia[part.Image.Ref.MediaID]; exists {
				return bounded("duplicate_media_reference", nil)
			}
			seenMedia[part.Image.Ref.MediaID] = struct{}{}
			if err := addMediaLimits(
				&imageCount,
				&mediaBytes,
				part.Image.Ref.SizeBytes,
			); err != nil {
				return err
			}
		case PartResourceLink:
			if r.Version != Version2 || part.ResourceLink == nil {
				return bounded("invalid_part_union", nil)
			}
			rendered, err := RenderResourceLink(*part.ResourceLink)
			if err != nil {
				return err
			}
			textBytes += len(rendered)
		case PartEmbeddedText:
			if r.Version != Version2 || part.EmbeddedText == nil {
				return bounded("invalid_part_union", nil)
			}
			rendered, err := RenderEmbeddedText(*part.EmbeddedText)
			if err != nil {
				return err
			}
			textBytes += len(rendered)
		case PartEmbeddedBlob:
			if r.Version != Version2 || part.EmbeddedBlob == nil {
				return bounded("invalid_part_union", nil)
			}
			rendered, err := RenderEmbeddedBlob(*part.EmbeddedBlob)
			if err != nil {
				return err
			}
			textBytes += len(rendered)
			if err := part.EmbeddedBlob.Ref.Validate(); err != nil {
				return bounded("invalid_media_reference", err)
			}
			if part.EmbeddedBlob.Ref.MIMEType != part.EmbeddedBlob.MIMEType {
				return bounded("media_mime_mismatch", nil)
			}
			if err := validateImageDetail(part.EmbeddedBlob.Detail); err != nil {
				return err
			}
			if _, exists := seenMedia[part.EmbeddedBlob.Ref.MediaID]; exists {
				return bounded("duplicate_media_reference", nil)
			}
			seenMedia[part.EmbeddedBlob.Ref.MediaID] = struct{}{}
			if err := addMediaLimits(
				&imageCount,
				&mediaBytes,
				part.EmbeddedBlob.Ref.SizeBytes,
			); err != nil {
				return err
			}
		default:
			return bounded("unknown_part_kind", nil)
		}
		if textBytes > maxTextBytes {
			return bounded("text_limit", nil)
		}
	}
	if r.Version == Version1 && imageCount == 0 {
		return bounded("missing_media_reference", nil)
	}
	return nil
}

// HasMedia reports whether the record owns ref-backed media.
func (r Record) HasMedia() bool {
	for _, part := range r.Parts {
		if part.Kind == PartImage && part.Image != nil ||
			part.Kind == PartEmbeddedBlob && part.EmbeddedBlob != nil {
			return true
		}
	}
	return false
}

// Describe returns the ordered presentation-safe prompt projection.
func (r Record) Describe() (Descriptor, error) {
	if err := r.Validate(); err != nil {
		return Descriptor{}, err
	}
	descriptor := Descriptor{Parts: make([]PartDescriptor, 0, len(r.Parts))}
	for _, part := range r.Parts {
		switch part.Kind {
		case PartText:
			descriptor.Parts = append(descriptor.Parts, PartDescriptor{
				Kind: PartText,
				Text: part.Text.Text,
			})
		case PartImage:
			descriptor.Parts = append(descriptor.Parts, PartDescriptor{
				Kind: PartImage,
				Image: &ImageDescriptor{
					MIMEType:  part.Image.Ref.MIMEType,
					SizeBytes: part.Image.Ref.SizeBytes,
					Width:     part.Image.Ref.Width,
					Height:    part.Image.Ref.Height,
					Detail:    part.Image.Detail,
				},
			})
		case PartResourceLink:
			descriptor.Parts = append(descriptor.Parts, PartDescriptor{
				Kind:     PartResourceLink,
				MIMEType: optionalString(part.ResourceLink.MIMEType),
			})
		case PartEmbeddedText:
			descriptor.Parts = append(descriptor.Parts, PartDescriptor{
				Kind:     PartEmbeddedText,
				MIMEType: optionalString(part.EmbeddedText.MIMEType),
			})
		case PartEmbeddedBlob:
			descriptor.Parts = append(descriptor.Parts, PartDescriptor{
				Kind:     PartEmbeddedBlob,
				MIMEType: part.EmbeddedBlob.MIMEType,
				Image: &ImageDescriptor{
					MIMEType:  part.EmbeddedBlob.Ref.MIMEType,
					SizeBytes: part.EmbeddedBlob.Ref.SizeBytes,
					Width:     part.EmbeddedBlob.Ref.Width,
					Height:    part.EmbeddedBlob.Ref.Height,
					Detail:    part.EmbeddedBlob.Detail,
				},
			})
		}
	}
	return descriptor, nil
}

// MediaRefs returns detached private refs in source-part order.
func (r Record) MediaRefs() ([]mediastore.Ref, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	refs := make([]mediastore.Ref, 0)
	for _, part := range r.Parts {
		switch {
		case part.Kind == PartImage && part.Image != nil:
			refs = append(refs, part.Image.Ref)
		case part.Kind == PartEmbeddedBlob && part.EmbeddedBlob != nil:
			refs = append(refs, part.EmbeddedBlob.Ref)
		}
	}
	return refs, nil
}

// RewriteMediaRefs clones the record and replaces every private media identity.
// The replacement map is keyed only inside trusted lifecycle code.
func (r Record) RewriteMediaRefs(
	replacements map[string]mediastore.Ref,
) (Record, error) {
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	rewritten := r.Clone()
	for index := range rewritten.Parts {
		part := &rewritten.Parts[index]
		switch {
		case part.Kind == PartImage && part.Image != nil:
			replacement, ok := replacements[part.Image.Ref.MediaID]
			if !ok {
				return Record{}, bounded("missing_media_replacement", nil)
			}
			part.Image.Ref = replacement
		case part.Kind == PartEmbeddedBlob && part.EmbeddedBlob != nil:
			replacement, ok := replacements[part.EmbeddedBlob.Ref.MediaID]
			if !ok {
				return Record{}, bounded("missing_media_replacement", nil)
			}
			part.EmbeddedBlob.Ref = replacement
		}
	}
	if err := rewritten.Validate(); err != nil {
		return Record{}, err
	}
	return rewritten, nil
}

// Materialize resolves and revalidates every ref before returning any message.
// A failure clears already-resolved bytes and returns no partial prompt.
func (r Record) Materialize(
	ctx context.Context,
	store *mediastore.Store,
) (*schema.Message, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, bounded("store_unavailable", nil)
	}
	parts := make([]schema.MessageInputPart, 0, len(r.Parts))
	var content bytes.Buffer
	resolved := make([][]byte, 0)
	defer func() {
		for _, data := range resolved {
			clear(data)
		}
	}()
	for _, part := range r.Parts {
		switch part.Kind {
		case PartText:
			content.WriteString(part.Text.Text)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: part.Text.Text,
			})
		case PartImage:
			data, err := store.Resolve(ctx, part.Image.Ref)
			if err != nil {
				return nil, bounded("resolve_media", err)
			}
			resolved = append(resolved, data)
			encoded := base64.StdEncoding.EncodeToString(data)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   part.Image.Ref.MIMEType,
					},
					Detail: schema.ImageURLDetail(part.Image.Detail),
				},
			})
		case PartResourceLink:
			rendered, err := RenderResourceLink(*part.ResourceLink)
			if err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: rendered,
			})
		case PartEmbeddedText:
			rendered, err := RenderEmbeddedText(*part.EmbeddedText)
			if err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: rendered,
			})
		case PartEmbeddedBlob:
			rendered, err := RenderEmbeddedBlob(*part.EmbeddedBlob)
			if err != nil {
				return nil, err
			}
			content.WriteString(rendered)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: rendered,
			})
			data, err := store.Resolve(ctx, part.EmbeddedBlob.Ref)
			if err != nil {
				return nil, bounded("resolve_media", err)
			}
			resolved = append(resolved, data)
			encoded := base64.StdEncoding.EncodeToString(data)
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   part.EmbeddedBlob.Ref.MIMEType,
					},
					Detail: schema.ImageURLDetail(part.EmbeddedBlob.Detail),
				},
			})
		}
	}
	return &schema.Message{
		Role:                  schema.User,
		Content:               content.String(),
		UserInputMultiContent: parts,
	}, nil
}

// Clone returns a detached value suitable for recorder tracking.
func (r Record) Clone() Record {
	cloned := Record{
		Version: r.Version,
		TurnID:  r.TurnID,
		Parts:   make([]Part, 0, len(r.Parts)),
	}
	for _, part := range r.Parts {
		next := Part{Kind: part.Kind}
		if part.Text != nil {
			text := *part.Text
			next.Text = &text
		}
		if part.Image != nil {
			image := *part.Image
			image.Annotations = cloneAnnotations(part.Image.Annotations)
			next.Image = &image
		}
		if part.ResourceLink != nil {
			resource := *part.ResourceLink
			resource.Title = cloneString(part.ResourceLink.Title)
			resource.Description = cloneString(part.ResourceLink.Description)
			resource.MIMEType = cloneString(part.ResourceLink.MIMEType)
			resource.Size = cloneInt(part.ResourceLink.Size)
			resource.Annotations = cloneAnnotations(part.ResourceLink.Annotations)
			next.ResourceLink = &resource
		}
		if part.EmbeddedText != nil {
			resource := *part.EmbeddedText
			resource.MIMEType = cloneString(part.EmbeddedText.MIMEType)
			resource.Annotations = cloneAnnotations(part.EmbeddedText.Annotations)
			next.EmbeddedText = &resource
		}
		if part.EmbeddedBlob != nil {
			resource := *part.EmbeddedBlob
			resource.Annotations = cloneAnnotations(part.EmbeddedBlob.Annotations)
			next.EmbeddedBlob = &resource
		}
		cloned.Parts = append(cloned.Parts, next)
	}
	return cloned
}

type resourceLinkDescriptor struct {
	Type        string       `json:"type"`
	URI         string       `json:"uri"`
	Name        string       `json:"name"`
	Title       *string      `json:"title,omitempty"`
	Description *string      `json:"description,omitempty"`
	MIMEType    *string      `json:"mimeType,omitempty"`
	Size        *int         `json:"size,omitempty"`
	Annotations *Annotations `json:"annotations,omitempty"`
}

type embeddedResourceEnvelope struct {
	Version     int          `json:"version"`
	Kind        string       `json:"kind"`
	URI         string       `json:"uri"`
	MIMEType    string       `json:"mimeType,omitempty"`
	Annotations *Annotations `json:"annotations,omitempty"`
	Text        string       `json:"text,omitempty"`
}

// RenderResourceLink returns the canonical no-fetch model descriptor.
func RenderResourceLink(resource ResourceLinkPart) (string, error) {
	if resource.URI == "" {
		return "", bounded("resource_uri_required", nil)
	}
	if resource.Name == "" {
		return "", bounded("resource_name_required", nil)
	}
	if resource.Size != nil && *resource.Size < 0 {
		return "", bounded("resource_size_negative", nil)
	}
	if err := validateAnnotations(resource.Annotations); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(resourceLinkDescriptor{
		Type:        PartResourceLink,
		URI:         resource.URI,
		Name:        resource.Name,
		Title:       resource.Title,
		Description: resource.Description,
		MIMEType:    resource.MIMEType,
		Size:        resource.Size,
		Annotations: resource.Annotations,
	})
	if err != nil {
		return "", bounded("resource_metadata_invalid", err)
	}
	return boundedEnvelope("<resource_link>", "</resource_link>", encoded)
}

// RenderEmbeddedText returns the canonical embedded-text model envelope.
func RenderEmbeddedText(resource EmbeddedTextPart) (string, error) {
	if resource.URI == "" {
		return "", bounded("embedded_uri_required", nil)
	}
	if err := validateAnnotations(resource.Annotations); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(embeddedResourceEnvelope{
		Version:     1,
		Kind:        "text",
		URI:         resource.URI,
		MIMEType:    optionalString(resource.MIMEType),
		Annotations: resource.Annotations,
		Text:        resource.Text,
	})
	if err != nil {
		return "", bounded("embedded_metadata_invalid", err)
	}
	return boundedEnvelope("<embedded_resource>", "</embedded_resource>", encoded)
}

// RenderEmbeddedBlob returns the canonical ref-free embedded-blob metadata
// envelope. Media bytes and private identity never enter the output.
func RenderEmbeddedBlob(resource EmbeddedBlobPart) (string, error) {
	if resource.URI == "" {
		return "", bounded("embedded_uri_required", nil)
	}
	if strings.TrimSpace(resource.MIMEType) == "" {
		return "", bounded("embedded_mime_required", nil)
	}
	if err := validateAnnotations(resource.Annotations); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(embeddedResourceEnvelope{
		Version:     1,
		Kind:        "blob",
		URI:         resource.URI,
		MIMEType:    resource.MIMEType,
		Annotations: resource.Annotations,
	})
	if err != nil {
		return "", bounded("embedded_metadata_invalid", err)
	}
	return boundedEnvelope("<embedded_resource>", "</embedded_resource>", encoded)
}

func boundedEnvelope(prefix, suffix string, encoded []byte) (string, error) {
	if len(prefix)+len(encoded)+len(suffix) > maxResourceDescriptorBytes {
		return "", bounded("resource_descriptor_too_large", nil)
	}
	return prefix + string(encoded) + suffix, nil
}

func payloadCount(part Part) int {
	count := 0
	for _, present := range []bool{
		part.Text != nil,
		part.Image != nil,
		part.ResourceLink != nil,
		part.EmbeddedText != nil,
		part.EmbeddedBlob != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func validateAnnotations(annotations *Annotations) error {
	if annotations == nil {
		return nil
	}
	if len(annotations.Audience) > maxAnnotationAudience {
		return bounded("annotation_limit", nil)
	}
	for _, audience := range annotations.Audience {
		if audience != "user" && audience != "assistant" {
			return bounded("annotation_invalid", nil)
		}
	}
	if annotations.LastModified != nil &&
		(len(*annotations.LastModified) == 0 ||
			len(*annotations.LastModified) > maxAnnotationStringBytes) {
		return bounded("annotation_invalid", nil)
	}
	if annotations.Priority != nil &&
		(math.IsNaN(*annotations.Priority) ||
			math.IsInf(*annotations.Priority, 0) ||
			*annotations.Priority < 0 ||
			*annotations.Priority > 1) {
		return bounded("annotation_invalid", nil)
	}
	encoded, err := json.Marshal(annotations)
	if err != nil || len(encoded) > maxResourceDescriptorBytes {
		return bounded("annotation_limit", err)
	}
	return nil
}

func validateImageDetail(detail string) error {
	switch detail {
	case "auto", "low", "high":
		return nil
	default:
		return bounded("invalid_image_detail", nil)
	}
}

func addMediaLimits(count *int, total *int64, size int64) error {
	*count++
	if *count > maxImages || *total > maxMediaBytes-size {
		return bounded("media_limit", nil)
	}
	*total += size
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneAnnotations(value *Annotations) *Annotations {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Audience = append([]string(nil), value.Audience...)
	cloned.LastModified = cloneString(value.LastModified)
	cloned.Priority = cloneFloat(value.Priority)
	return &cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// Error is intentionally bounded: it never includes record data or media
// identifiers.
type Error struct {
	Category string
	Err      error
}

func (e *Error) Error() string {
	if e == nil {
		return "durable user prompt failed"
	}
	return fmt.Sprintf("durable user prompt failed: %s", e.Category)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func bounded(category string, err error) error {
	return &Error{Category: category, Err: err}
}
