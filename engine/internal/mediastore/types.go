package mediastore

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// RefVersion is the only durable media-reference version currently accepted.
	RefVersion = 1
	// ManifestVersion is the only session media manifest version currently accepted.
	ManifestVersion = 1
	// MaxBlobBytes matches the generic P30 image-admission ceiling.
	MaxBlobBytes = 5 * 1024 * 1024
	// MaxManifestBytes bounds strict manifest decoding before P30.2c adds paging.
	MaxManifestBytes = 64 * 1024 * 1024
	// MaxManifestEntries prevents an unbounded in-memory manifest.
	MaxManifestEntries = 131_072
)

const (
	CategoryCanceled            = "canceled"
	CategoryInvalidReference    = "invalid_reference"
	CategoryInvalidMetadata     = "invalid_metadata"
	CategoryUnsupportedVersion  = "unsupported_version"
	CategoryManifestCorrupt     = "manifest_corrupt"
	CategoryManifestLimit       = "manifest_limit"
	CategoryMediaMissing        = "media_missing"
	CategoryMediaCorrupt        = "media_corrupt"
	CategoryUnsafePath          = "unsafe_path"
	CategoryStoreUnavailable    = "store_unavailable"
	CategoryDurabilityUncertain = "durability_uncertain"
)

var (
	mediaIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Error is a bounded media-store failure. It deliberately excludes paths,
// media IDs, digests, byte content, and caller-provided metadata.
type Error struct {
	Category  string
	Operation string
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "media store error"
	}
	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "operation"
	}
	category := strings.TrimSpace(e.Category)
	if category == "" {
		category = CategoryStoreUnavailable
	}
	return fmt.Sprintf("media store %s failed: %s", operation, category)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsCategory reports whether err contains the requested bounded category.
func IsCategory(err error, category string) bool {
	var mediaErr *Error
	return errors.As(err, &mediaErr) && mediaErr.Category == category
}

// Ref is the opaque durable identity stored in transcript/runtime-input
// records. It contains no digest or filesystem location.
type Ref struct {
	Version   int    `json:"version"`
	MediaID   string `json:"media_id"`
	MIMEType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// Metadata is trusted generic-admission output supplied to Store.Put.
type Metadata struct {
	MIMEType string
	Width    int
	Height   int
	Kind     string
}

// Entry is private manifest authority for one opaque MediaID.
type Entry struct {
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
	MIMEType  string `json:"mime_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Kind      string `json:"kind"`
}

// Manifest is the versioned session-private mapping from opaque identities to
// content-addressed bytes.
type Manifest struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

func (r Ref) Validate() error {
	if r.Version != RefVersion {
		return bounded(CategoryUnsupportedVersion, "validate reference", nil)
	}
	if !validMediaID(r.MediaID) {
		return bounded(CategoryInvalidReference, "validate reference", nil)
	}
	return validatePublicMetadata(r.MIMEType, r.SizeBytes, r.Width, r.Height)
}

func (m Metadata) Validate(size int64) error {
	if strings.TrimSpace(m.Kind) != m.Kind || m.Kind != "prompt_image" {
		return bounded(CategoryInvalidMetadata, "validate metadata", nil)
	}
	return validatePublicMetadata(m.MIMEType, size, m.Width, m.Height)
}

func (e Entry) Validate() error {
	if !digestPattern.MatchString(e.Digest) {
		return bounded(CategoryManifestCorrupt, "validate manifest", nil)
	}
	if strings.TrimSpace(e.Kind) != e.Kind || e.Kind != "prompt_image" {
		return bounded(CategoryManifestCorrupt, "validate manifest", nil)
	}
	if err := validatePublicMetadata(
		e.MIMEType,
		e.SizeBytes,
		e.Width,
		e.Height,
	); err != nil {
		return bounded(CategoryManifestCorrupt, "validate manifest", err)
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return bounded(CategoryUnsupportedVersion, "validate manifest", nil)
	}
	if m.Entries == nil {
		return bounded(CategoryManifestCorrupt, "validate manifest", nil)
	}
	if len(m.Entries) > MaxManifestEntries {
		return bounded(CategoryManifestLimit, "validate manifest", nil)
	}
	for mediaID, entry := range m.Entries {
		if !validMediaID(mediaID) {
			return bounded(CategoryManifestCorrupt, "validate manifest", nil)
		}
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e Entry) Ref(mediaID string) Ref {
	return Ref{
		Version:   RefVersion,
		MediaID:   mediaID,
		MIMEType:  e.MIMEType,
		SizeBytes: e.SizeBytes,
		Width:     e.Width,
		Height:    e.Height,
	}
}

func (e Entry) MatchesRef(ref Ref) bool {
	return ref.Version == RefVersion &&
		ref.SizeBytes == e.SizeBytes &&
		ref.MIMEType == e.MIMEType &&
		ref.Width == e.Width &&
		ref.Height == e.Height
}

func emptyManifest() Manifest {
	return Manifest{
		Version: ManifestVersion,
		Entries: make(map[string]Entry),
	}
}

func encodeManifest(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, bounded(CategoryManifestCorrupt, "encode manifest", err)
	}
	data = append(data, '\n')
	if len(data) > MaxManifestBytes {
		return nil, bounded(CategoryManifestLimit, "encode manifest", nil)
	}
	return data, nil
}

func validMediaID(mediaID string) bool {
	return mediaIDPattern.MatchString(mediaID)
}

func validatePublicMetadata(
	mimeType string,
	sizeBytes int64,
	width int,
	height int,
) error {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return bounded(CategoryInvalidMetadata, "validate metadata", nil)
	}
	if sizeBytes <= 0 || sizeBytes > MaxBlobBytes ||
		width <= 0 || height <= 0 {
		return bounded(CategoryInvalidMetadata, "validate metadata", nil)
	}
	return nil
}

func bounded(category, operation string, err error) error {
	return &Error{
		Category:  category,
		Operation: operation,
		Err:       err,
	}
}
