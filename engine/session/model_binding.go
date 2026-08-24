package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	enginemodel "github.com/abietic/yhc/engine/model"
)

const PersistedModelBindingVersion uint16 = 1

const (
	ModelBindingKindProfile = "profile"
	ModelBindingKindLegacy  = "legacy"
)

const (
	ModelBindingStateAbsent             = "absent"
	ModelBindingStateValid              = "valid"
	ModelBindingStateInvalid            = "invalid"
	ModelBindingStateUnsupportedVersion = "unsupported_version"
)

var (
	modelBindingDigestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	modelBindingProfilePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

// PersistedModelBinding is the additive durable identity for one selected
// provider route. It intentionally contains no account, endpoint, credential,
// header, client, or route-health data.
type PersistedModelBinding struct {
	Version             uint16 `json:"version"`
	Kind                string `json:"kind"`
	Value               string `json:"value"`
	Provider            string `json:"provider"`
	APIModel            string `json:"api_model"`
	PortfolioRevision   string `json:"portfolio_revision"`
	RouteIdentityDigest string `json:"route_identity_digest"`
	MetadataDigest      string `json:"metadata_digest"`
	ContextWindowTokens *int   `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     *int   `json:"max_output_tokens,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`

	invalidEncoding    bool
	unsupportedVersion bool
	rawEncoding        json.RawMessage
}

// UnmarshalJSON contains malformed-but-valid or unsupported nested binding
// JSON so one optional record cannot make the enclosing Session unreadable.
func (b *PersistedModelBinding) UnmarshalJSON(data []byte) error {
	if b == nil {
		return nil
	}
	var header struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		*b = PersistedModelBinding{
			invalidEncoding: true,
			rawEncoding:     append(json.RawMessage(nil), data...),
		}
		return nil
	}
	var version uint16
	if len(header.Version) == 0 || json.Unmarshal(header.Version, &version) != nil {
		*b = PersistedModelBinding{
			invalidEncoding: true,
			rawEncoding:     append(json.RawMessage(nil), data...),
		}
		return nil
	}
	if version != PersistedModelBindingVersion {
		*b = PersistedModelBinding{
			Version:            version,
			unsupportedVersion: true,
			rawEncoding:        append(json.RawMessage(nil), data...),
		}
		return nil
	}
	type persistedModelBindingAlias PersistedModelBinding
	var decoded persistedModelBindingAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		*b = PersistedModelBinding{
			Version:         version,
			invalidEncoding: true,
			rawEncoding:     append(json.RawMessage(nil), data...),
		}
		return nil
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		*b = PersistedModelBinding{
			Version:         version,
			invalidEncoding: true,
			rawEncoding:     append(json.RawMessage(nil), data...),
		}
		return nil
	}
	*b = PersistedModelBinding(decoded)
	return nil
}

// MarshalJSON preserves opaque nested JSON across automatic checkpoints and
// forks. Only an explicit successful rebind may replace it with validated v1.
func (b PersistedModelBinding) MarshalJSON() ([]byte, error) {
	if (b.invalidEncoding || b.unsupportedVersion) && json.Valid(b.rawEncoding) {
		return append([]byte(nil), b.rawEncoding...), nil
	}
	type persistedModelBindingAlias PersistedModelBinding
	return json.Marshal(persistedModelBindingAlias(b))
}

// Clone returns a detached binding, including opaque JSON and known limits.
func (b *PersistedModelBinding) Clone() *PersistedModelBinding {
	if b == nil {
		return nil
	}
	clone := *b
	clone.rawEncoding = append(json.RawMessage(nil), b.rawEncoding...)
	if b.ContextWindowTokens != nil {
		value := *b.ContextWindowTokens
		clone.ContextWindowTokens = &value
	}
	if b.MaxOutputTokens != nil {
		value := *b.MaxOutputTokens
		clone.MaxOutputTokens = &value
	}
	return &clone
}

// ValidateV1 validates the complete stable wire contract without projecting
// any untrusted selector from invalid or unsupported records.
func (b *PersistedModelBinding) ValidateV1() error {
	if b == nil {
		return fmt.Errorf("model binding is absent")
	}
	if b.unsupportedVersion || b.Version != PersistedModelBindingVersion {
		return fmt.Errorf("unsupported model binding version")
	}
	if b.invalidEncoding {
		return fmt.Errorf("invalid model binding encoding")
	}
	if b.Kind != ModelBindingKindProfile && b.Kind != ModelBindingKindLegacy {
		return fmt.Errorf("invalid model binding kind")
	}
	if b.Value == "" || b.Value != strings.TrimSpace(b.Value) {
		return fmt.Errorf("invalid model binding value")
	}
	if b.Kind == ModelBindingKindProfile {
		if !modelBindingProfilePattern.MatchString(b.Value) ||
			b.Value != strings.ToLower(b.Value) {
			return fmt.Errorf("invalid model profile value")
		}
	} else {
		if strings.HasPrefix(strings.ToLower(b.Value), "legacy:") {
			return fmt.Errorf("legacy binding stores an unlabelled selector")
		}
		if len(b.Value) > 512 ||
			strings.IndexFunc(b.Value, unicode.IsControl) >= 0 {
			return fmt.Errorf("invalid legacy model selector")
		}
	}
	if b.Provider == "" || b.Provider != strings.TrimSpace(b.Provider) ||
		b.APIModel == "" || b.APIModel != strings.TrimSpace(b.APIModel) {
		return fmt.Errorf("invalid resolved model identity")
	}
	for _, digest := range []string{
		b.PortfolioRevision,
		b.RouteIdentityDigest,
		b.MetadataDigest,
	} {
		if !modelBindingDigestPattern.MatchString(digest) {
			return fmt.Errorf("invalid model binding digest")
		}
	}
	if b.ContextWindowTokens != nil && *b.ContextWindowTokens <= 0 {
		return fmt.Errorf("invalid model binding context limit")
	}
	if b.MaxOutputTokens != nil && *b.MaxOutputTokens <= 0 {
		return fmt.Errorf("invalid model binding output limit")
	}
	validatedEffort, err := enginemodel.ValidateReasoningEffort(b.ReasoningEffort)
	if err != nil || validatedEffort != b.ReasoningEffort {
		return fmt.Errorf("unsupported model binding reasoning effort")
	}
	return nil
}

// ModelBindingProjection is the only listing/export view of a durable
// binding. Digests and opaque raw JSON are never included.
type ModelBindingProjection struct {
	State string `json:"state"`
	Kind  string `json:"kind,omitempty"`
	Value string `json:"value,omitempty"`
}

// SafeModelBindingProjection returns a closed, non-secret projection.
func SafeModelBindingProjection(binding *PersistedModelBinding) ModelBindingProjection {
	if binding == nil {
		return ModelBindingProjection{State: ModelBindingStateAbsent}
	}
	if binding.unsupportedVersion {
		return ModelBindingProjection{State: ModelBindingStateUnsupportedVersion}
	}
	if err := binding.ValidateV1(); err != nil {
		return ModelBindingProjection{State: ModelBindingStateInvalid}
	}
	return ModelBindingProjection{
		State: ModelBindingStateValid,
		Kind:  binding.Kind,
		Value: binding.Value,
	}
}
