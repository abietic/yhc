package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	engineconfig "github.com/abietic/yhc/engine/config"
)

// RouteIdentity is the complete non-secret key for one provider client.
// Presentation-only profile IDs, API model names, and resolved secrets are
// deliberately excluded so compatible profiles reuse the same client.
type RouteIdentity struct {
	Provider      Provider `json:"provider"`
	Endpoint      string   `json:"endpoint"`
	AuthKind      string   `json:"auth_kind"`
	AuthReference string   `json:"auth_reference"`
	AdapterDigest string   `json:"adapter_digest"`
}

// RouteIdentityInput contains only client-construction inputs that are safe to
// retain in runtime state and diagnostics.
type RouteIdentityInput struct {
	Provider      Provider
	Endpoint      string
	AuthKind      string
	AuthReference string
	AdapterDigest string
}

// NewRouteIdentity validates and canonicalizes a complete route identity.
func NewRouteIdentity(input RouteIdentityInput) (RouteIdentity, error) {
	provider, err := NormalizeProvider(input.Provider)
	if err != nil {
		return RouteIdentity{}, err
	}
	endpoint, err := canonicalRouteEndpoint(input.Endpoint)
	if err != nil {
		return RouteIdentity{}, fmt.Errorf("invalid route endpoint: %w", err)
	}
	authKind := strings.TrimSpace(input.AuthKind)
	if authKind == "" {
		return RouteIdentity{}, fmt.Errorf("route auth kind must not be empty")
	}
	authReference := strings.TrimSpace(input.AuthReference)
	if authReference == "" {
		return RouteIdentity{}, fmt.Errorf("route auth reference must not be empty")
	}
	adapterDigest := strings.TrimSpace(input.AdapterDigest)
	if adapterDigest == "" {
		return RouteIdentity{}, fmt.Errorf("route adapter digest must not be empty")
	}
	return RouteIdentity{
		Provider:      provider,
		Endpoint:      endpoint,
		AuthKind:      authKind,
		AuthReference: authReference,
		AdapterDigest: adapterDigest,
	}, nil
}

// Digest returns the canonical non-secret route digest used for durable route
// comparison. Credentials and credential-derived values are not inputs.
func (identity RouteIdentity) Digest() (string, error) {
	encoded, err := json.Marshal(struct {
		Provider      Provider `json:"provider"`
		Endpoint      string   `json:"endpoint"`
		AuthKind      string   `json:"auth_kind"`
		AuthReference string   `json:"auth_reference"`
		AdapterDigest string   `json:"adapter_digest"`
	}{
		Provider:      identity.Provider,
		Endpoint:      identity.Endpoint,
		AuthKind:      identity.AuthKind,
		AuthReference: identity.AuthReference,
		AdapterDigest: identity.AdapterDigest,
	})
	if err != nil {
		return "", fmt.Errorf("encode route identity digest: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func legacyRouteIdentity(config ResolvedConfig) (RouteIdentity, error) {
	authKind, authReference := legacyAuthIdentity(config)
	return NewRouteIdentity(RouteIdentityInput{
		Provider:      config.Provider,
		Endpoint:      config.BaseURL,
		AuthKind:      authKind,
		AuthReference: authReference,
		AdapterDigest: string(config.Provider) + ":v1",
	})
}

func legacyAuthIdentity(config ResolvedConfig) (string, string) {
	source := strings.TrimSpace(config.Sources.APIKey)
	switch {
	case strings.HasPrefix(source, "env:"):
		return "env", strings.TrimPrefix(source, "env:")
	case source == "credential-store":
		return "credential", providerCredentialID(config.Provider)
	case source == "config":
		return "legacy-config", "api_key"
	default:
		return "explicit", "api-key"
	}
}

func canonicalRouteEndpoint(raw string) (string, error) {
	return engineconfig.CanonicalProviderEndpoint(raw)
}
