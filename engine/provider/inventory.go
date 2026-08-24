package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	engineconfig "github.com/abietic/yhc/engine/config"
	enginemodel "github.com/abietic/yhc/engine/model"
)

// RuntimeInventorySnapshot is a detached, non-secret view of the configured
// model routes available to a Runtime.
type RuntimeInventorySnapshot struct {
	Revision string                  `json:"revision"`
	Default  string                  `json:"default"`
	Entries  []RuntimeInventoryEntry `json:"entries"`
}

// RuntimeInventoryEntry is one selectable configured model profile. It omits
// account, endpoint, authentication, client, and route-health details.
type RuntimeInventoryEntry struct {
	Selector            string                             `json:"selector"`
	ProfileID           string                             `json:"profile_id"`
	DisplayName         string                             `json:"display_name,omitempty"`
	Provider            string                             `json:"provider"`
	APIModel            string                             `json:"api_model"`
	Metadata            enginemodel.EffectiveModelMetadata `json:"metadata"`
	ReasoningDefault    string                             `json:"reasoning_default,omitempty"`
	RouteIdentityDigest string                             `json:"route_identity_digest"`
	MetadataDigest      string                             `json:"metadata_digest"`
}

// InventorySnapshot returns a detached immutable-by-convention configured
// inventory. It never initializes a provider client.
func (r *Runtime) InventorySnapshot() RuntimeInventorySnapshot {
	if r == nil {
		return RuntimeInventorySnapshot{}
	}
	return cloneRuntimeInventorySnapshot(r.inventory)
}

// ResolveInventorySelector resolves one strict inventory selector without
// constructing a provider client. Exact profile IDs win; legacy resolution
// requires the label unless the runtime itself is legacy-bound.
func (r *Runtime) ResolveInventorySelector(
	modelSpec string,
) (RuntimeInventoryEntry, error) {
	if r == nil || r.routes == nil {
		return RuntimeInventoryEntry{}, fmt.Errorf(
			"provider runtime is not initialized",
		)
	}
	trimmed := strings.TrimSpace(modelSpec)
	normalized := strings.ToLower(trimmed)
	for _, entry := range r.inventory.Entries {
		if entry.ProfileID != "" &&
			strings.EqualFold(entry.ProfileID, normalized) {
			return cloneRuntimeInventoryEntry(entry), nil
		}
	}

	legacySelector, labelled := splitLegacySelector(trimmed)
	if !labelled {
		if !r.routes.legacyBound() {
			return RuntimeInventoryEntry{}, fmt.Errorf(
				"model selector %q is not a configured profile; use legacy:<selector> for legacy resolution",
				modelSpec,
			)
		}
		legacySelector = trimmed
	}
	resolved, err := r.routes.resolveLegacySelector(legacySelector)
	if err != nil {
		return RuntimeInventoryEntry{}, err
	}
	identity, err := legacyRouteIdentity(resolved)
	if err != nil {
		return RuntimeInventoryEntry{}, err
	}
	routeDigest, err := identity.Digest()
	if err != nil {
		return RuntimeInventoryEntry{}, err
	}
	metadata, err := enginemodel.ResolvePortfolioMetadataForProvider(
		string(resolved.Provider),
		resolved.Model,
		enginemodel.MetadataOverrides{},
	)
	if err != nil {
		return RuntimeInventoryEntry{}, err
	}
	metadataDigest, err := effectiveMetadataDigest(metadata)
	if err != nil {
		return RuntimeInventoryEntry{}, err
	}
	return RuntimeInventoryEntry{
		Selector:            "legacy:" + strings.TrimSpace(legacySelector),
		DisplayName:         resolved.Model,
		Provider:            string(resolved.Provider),
		APIModel:            resolved.Model,
		Metadata:            cloneEffectiveMetadata(metadata),
		RouteIdentityDigest: routeDigest,
		MetadataDigest:      metadataDigest,
	}, nil
}

type inventoryRouteIdentityResolver func(
	engineconfig.ResolvedProfile,
	engineconfig.ResolvedAccount,
) (RouteIdentity, error)

func newRuntimeInventory(
	snapshot *engineconfig.PortfolioSnapshot,
	resolveIdentity inventoryRouteIdentityResolver,
) (RuntimeInventorySnapshot, error) {
	if snapshot == nil {
		return RuntimeInventorySnapshot{}, nil
	}
	entries := make([]RuntimeInventoryEntry, 0, len(snapshot.Profiles))
	seen := make(map[string]string, len(snapshot.Profiles))
	for mapID, profile := range snapshot.Profiles {
		profileID := strings.ToLower(strings.TrimSpace(string(profile.ID)))
		if profileID == "" {
			profileID = strings.ToLower(strings.TrimSpace(string(mapID)))
		}
		if profileID == "" {
			return RuntimeInventorySnapshot{}, fmt.Errorf("configured model profile ID must not be empty")
		}
		if previous, exists := seen[profileID]; exists {
			return RuntimeInventorySnapshot{}, fmt.Errorf("configured model profile collision after normalization: %q and %q", previous, mapID)
		}
		seen[profileID] = string(mapID)
		account, ok := snapshot.Accounts[profile.Account]
		if !ok {
			return RuntimeInventorySnapshot{}, fmt.Errorf("configured model profile %q references unavailable account %q", profileID, profile.Account)
		}
		provider, err := NormalizeProvider(Provider(account.Provider))
		if err != nil {
			return RuntimeInventorySnapshot{}, err
		}
		var identity RouteIdentity
		if resolveIdentity != nil {
			identity, err = resolveIdentity(profile, account)
		} else {
			identity, err = NewRouteIdentity(RouteIdentityInput{
				Provider:      provider,
				Endpoint:      account.Endpoint,
				AuthKind:      account.AuthKind,
				AuthReference: account.AuthReference,
				AdapterDigest: account.AdapterDigest,
			})
		}
		if err != nil {
			return RuntimeInventorySnapshot{}, fmt.Errorf("configured model profile %q route identity: %w", profileID, err)
		}
		routeDigest, err := identity.Digest()
		if err != nil {
			return RuntimeInventorySnapshot{}, err
		}
		metadataDigest, err := effectiveMetadataDigest(profile.Metadata)
		if err != nil {
			return RuntimeInventorySnapshot{}, err
		}
		selector := profileID
		if snapshot.SelectionSource == "legacy" && strings.HasPrefix(profileID, "legacy.") {
			selector = "legacy:" + profile.APIModel
		}
		entries = append(entries, RuntimeInventoryEntry{
			Selector:            selector,
			ProfileID:           profileID,
			DisplayName:         profile.DisplayName,
			Provider:            string(provider),
			APIModel:            profile.APIModel,
			Metadata:            cloneEffectiveMetadata(profile.Metadata),
			ReasoningDefault:    profile.Reasoning.DefaultEffort,
			RouteIdentityDigest: routeDigest,
			MetadataDigest:      metadataDigest,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ProfileID < entries[j].ProfileID })
	defaultID := strings.ToLower(strings.TrimSpace(string(snapshot.Default)))
	if _, ok := seen[defaultID]; !ok {
		return RuntimeInventorySnapshot{}, fmt.Errorf("configured default model profile %q is unavailable", snapshot.Default)
	}
	defaultSelector := defaultID
	for _, entry := range entries {
		if entry.ProfileID == defaultID {
			defaultSelector = entry.Selector
			break
		}
	}
	revision := snapshot.Revision
	if revision == "" {
		encoded, err := json.Marshal(struct {
			Default string                  `json:"default"`
			Entries []RuntimeInventoryEntry `json:"entries"`
		}{
			Default: defaultSelector,
			Entries: entries,
		})
		if err != nil {
			return RuntimeInventorySnapshot{}, fmt.Errorf(
				"encode runtime inventory revision: %w",
				err,
			)
		}
		revision = fmt.Sprintf("%x", sha256.Sum256(encoded))
	}
	return RuntimeInventorySnapshot{
		Revision: revision,
		Default:  defaultSelector,
		Entries:  entries,
	}, nil
}

func cloneRuntimeInventorySnapshot(snapshot RuntimeInventorySnapshot) RuntimeInventorySnapshot {
	clone := snapshot
	clone.Entries = make([]RuntimeInventoryEntry, len(snapshot.Entries))
	for index, entry := range snapshot.Entries {
		clone.Entries[index] = cloneRuntimeInventoryEntry(entry)
	}
	return clone
}

func cloneRuntimeInventoryEntry(
	entry RuntimeInventoryEntry,
) RuntimeInventoryEntry {
	entry.Metadata = cloneEffectiveMetadata(entry.Metadata)
	return entry
}

func cloneEffectiveMetadata(metadata enginemodel.EffectiveModelMetadata) enginemodel.EffectiveModelMetadata {
	metadata.SupportedReasoningEfforts.Value = append([]string(nil), metadata.SupportedReasoningEfforts.Value...)
	return metadata
}

func effectiveMetadataDigest(metadata enginemodel.EffectiveModelMetadata) (string, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode effective model metadata digest: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
