package provider

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/cloudwego/eino/components/model"
)

func TestP292InventorySnapshotIsDetachedSortedAndNonSecret(t *testing.T) {
	var (
		mu        sync.Mutex
		factories int
	)
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: namedRuntimeSources(),
		Resolution: ResolveInput{Getenv: getenvMap(map[string]string{
			"OPENAI_A":       portfolioRuntimeSecret,
			"OPENAI_B":       "secondary-secret",
			"OPENAI_D":       "distinct-auth-secret",
			"OPENAI_API_KEY": "legacy-secret",
		})},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			mu.Lock()
			factories++
			mu.Unlock()
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if factories != 1 {
		mu.Unlock()
		t.Fatalf("startup client constructions = %d, want 1", factories)
	}
	mu.Unlock()

	snapshot := runtime.InventorySnapshot()
	if snapshot.Revision == "" || snapshot.Default != "primary" || len(snapshot.Entries) != 5 {
		t.Fatalf("inventory header = %#v", snapshot)
	}
	for index := 1; index < len(snapshot.Entries); index++ {
		if snapshot.Entries[index-1].ProfileID >= snapshot.Entries[index].ProfileID {
			t.Fatalf("inventory entries are not sorted: %#v", snapshot.Entries)
		}
	}
	primary := snapshot.Entries[2]
	if primary.Selector != "primary" || primary.ProfileID != "primary" ||
		primary.Provider != string(ProviderAgenticOpenAI) ||
		primary.APIModel != "gpt-4o" {
		t.Fatalf("primary inventory entry = %#v", primary)
	}
	if len(primary.RouteIdentityDigest) != 64 || len(primary.MetadataDigest) != 64 {
		t.Fatalf("inventory digests = %#v", primary)
	}
	encoded, marshalErr := json.Marshal(snapshot)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, forbidden := range []string{portfolioRuntimeSecret, "account-a", "OPENAI_A", "https://a.example/v1", "auth_kind", "auth_reference"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inventory exposed %q: %s", forbidden, encoded)
		}
	}

	snapshot.Entries[2].Metadata.SupportedReasoningEfforts.Value = append(snapshot.Entries[2].Metadata.SupportedReasoningEfforts.Value, "high")
	again := runtime.InventorySnapshot()
	if len(again.Entries[2].Metadata.SupportedReasoningEfforts.Value) != len(primary.Metadata.SupportedReasoningEfforts.Value) {
		t.Fatalf("inventory metadata was not detached: %#v", again.Entries[2].Metadata)
	}
	if _, err := runtime.ResolveModel("same-route"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if factories != 1 {
		t.Fatalf("profile lookup initialized a client: %d", factories)
	}
}

func TestP292SelectorGrammarKeepsProfileAndLegacyPathsDistinct(t *testing.T) {
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: namedRuntimeSources(),
		Resolution: ResolveInput{Getenv: getenvMap(map[string]string{
			"OPENAI_A":       portfolioRuntimeSecret,
			"OPENAI_B":       "secondary-secret",
			"OPENAI_D":       "distinct-auth-secret",
			"OPENAI_API_KEY": "legacy-secret",
		})},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := runtime.ResolveModel(" OTHER-ENDPOINT "); resolveErr != nil || resolved.Model != "gpt-4o" {
		t.Fatalf("normalized profile selector = %#v, %v", resolved, resolveErr)
	}
	if _, err := runtime.ResolveModel("openai:gpt-4o-mini"); err == nil {
		t.Fatal("named portfolio accepted an unlabelled legacy selector")
	}
	if resolved, resolveErr := runtime.ResolveModel(" legacy:openai:gpt-4o-mini "); resolveErr != nil || resolved.Model != "gpt-4o-mini" {
		t.Fatalf("labelled legacy selector = %#v, %v", resolved, resolveErr)
	}
	if _, resolveErr := runtime.ResolveInventorySelector("gpt-4o"); resolveErr == nil {
		t.Fatal("strict inventory selector accepted the active API model as a profile")
	}
	legacy, resolveErr := runtime.ResolveInventorySelector(
		" legacy:openai:gpt-4o-mini ",
	)
	if resolveErr != nil ||
		legacy.Selector != "legacy:openai:gpt-4o-mini" ||
		legacy.Provider != string(ProviderAgenticOpenAI) ||
		legacy.APIModel != "gpt-4o-mini" ||
		len(legacy.RouteIdentityDigest) != 64 ||
		len(legacy.MetadataDigest) != 64 {
		t.Fatalf("strict legacy inventory entry = %#v, %v", legacy, resolveErr)
	}
}

func TestP292LegacyInventoryLabelsItsDefaultSelector(t *testing.T) {
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: &engineconfig.ConfigSources{User: &engineconfig.Config{}, Project: &engineconfig.Config{}},
		Resolution: ResolveInput{Explicit: Config{
			Provider: ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   portfolioRuntimeSecret,
		}},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.InventorySnapshot()
	if snapshot.Default != "legacy:gpt-4o" || len(snapshot.Entries) != 1 || snapshot.Entries[0].Selector != "legacy:gpt-4o" {
		t.Fatalf("legacy inventory = %#v", snapshot)
	}
	if _, err := runtime.ResolveModel("gpt-4o"); err != nil {
		t.Fatalf("legacy-bound bare selector was rejected: %v", err)
	}
	if _, err := runtime.ResolveModel("legacy:gpt-4o"); err != nil {
		t.Fatalf("labelled legacy selector was rejected: %v", err)
	}
	entry, err := runtime.ResolveInventorySelector("gpt-4o")
	if err != nil || entry.Selector != "legacy:gpt-4o" {
		t.Fatalf("legacy-bound strict selector = %#v, %v", entry, err)
	}
}
