package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
)

const p290SecretSentinel = "p290-" + "secret-" + "sentinel-7f1834d9"

func TestP290LegacyCredentialPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		explicit   string
		env        map[string]string
		configured string
		stored     string
		want       string
		wantSource string
	}{
		{
			name:       "explicit flag",
			explicit:   "explicit-key",
			env:        map[string]string{"PROV_API_KEY": "generic-key", "OPENAI_API_KEY": "provider-key"},
			configured: "configured-key",
			stored:     "stored-key",
			want:       "explicit-key",
			wantSource: "explicit",
		},
		{
			name:       "generic provider environment",
			env:        map[string]string{"PROV": "openai", "PROV_API_KEY": "generic-key", "OPENAI_API_KEY": "provider-key"},
			configured: "configured-key",
			stored:     "stored-key",
			want:       "generic-key",
			wantSource: "env:PROV_API_KEY",
		},
		{
			name:       "provider environment",
			env:        map[string]string{"OPENAI_API_KEY": "provider-key"},
			configured: "configured-key",
			stored:     "stored-key",
			want:       "provider-key",
			wantSource: "env:OPENAI_API_KEY",
		},
		{
			name:       "configured value",
			configured: "configured-key",
			stored:     "stored-key",
			want:       "configured-key",
			wantSource: "config",
		},
		{
			name:       "credential store",
			stored:     "stored-key",
			want:       "stored-key",
			wantSource: "credential-store",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := ResolveConfig(ResolveInput{
				Explicit: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   tt.explicit,
				},
				Configured: Config{APIKey: tt.configured},
				Getenv:     getenvMap(tt.env),
				CredentialLookup: credStore(map[string]string{
					"openai": tt.stored,
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if resolved.APIKey != tt.want || resolved.Sources.APIKey != tt.wantSource {
				t.Fatalf("resolved key/source = %q/%q, want %q/%q",
					resolved.APIKey, resolved.Sources.APIKey, tt.want, tt.wantSource)
			}
		})
	}
}

func TestP290LegacyProviderModelAndEndpointPrecedence(t *testing.T) {
	resolved, err := ResolveConfig(ResolveInput{
		Explicit: Config{
			Provider: ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   "explicit-key",
		},
		Configured: Config{
			Provider: ProviderAgenticClaude,
			Model:    "claude-sonnet-4-6",
			BaseURL:  "https://configured.example/v1",
		},
		Getenv: getenvMap(map[string]string{
			"PROV":            "google",
			"PROV_MODEL":      "gemini-2.5-flash",
			"PROV_BASE_URL":   "https://generic.example/v1",
			"OPENAI_BASE_URL": "https://provider.example/v1",
		}),
		CredentialLookup: credStore(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != ProviderAgenticOpenAI ||
		resolved.Model != "gpt-4o" ||
		resolved.Sources.Provider != "explicit" ||
		resolved.Sources.Model != "explicit" {
		t.Fatalf("explicit provider/model precedence = %#v", resolved)
	}
	// PROV_BASE_URL is ignored when PROV selects another provider. The current
	// endpoint policy then chooses configured data before provider-specific env.
	if resolved.BaseURL != "https://configured.example/v1" ||
		resolved.Sources.BaseURL != "config" {
		t.Fatalf("legacy endpoint precedence = %q/%q",
			resolved.BaseURL, resolved.Sources.BaseURL)
	}
}

func TestP291LegacyResolutionKeepsSingleRoute(t *testing.T) {
	ctx := context.Background()
	var (
		factoryMu    sync.Mutex
		factoryCalls []Config
	)
	factory := func(_ context.Context, cfg Config) (model.BaseChatModel, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		factoryCalls = append(factoryCalls, cfg)
		return &routeModel{provider: cfg.Provider, recorder: &routeRecorder{}}, nil
	}

	runtime, err := NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   p290SecretSentinel,
				BaseURL:  "https://account-a.example/v1",
			},
			Configured: Config{
				Provider: ProviderAgenticOpenAI,
				APIKey:   "account-b-key",
				BaseURL:  "https://account-b.example/v1",
			},
			Getenv:           getenvMap(nil),
			CredentialLookup: credStore(nil),
		},
		factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}

	resolved, secondClient, err := runtime.routes.route(ctx, "openai:gpt-4.1")
	if err != nil {
		t.Fatal(err)
	}
	mainIdentity, err := legacyRouteIdentity(runtime.Main)
	if err != nil {
		t.Fatal(err)
	}
	firstClient := runtime.routes.models[mainIdentity]
	if secondClient != firstClient {
		t.Fatal("same legacy route did not reuse the route-keyed client")
	}
	if resolved.APIKey != p290SecretSentinel ||
		resolved.BaseURL != "https://account-a.example/v1" {
		t.Fatalf("same-provider route unexpectedly isolated endpoint or credential: %#v", resolved)
	}
	if len(factoryCalls) != 1 {
		t.Fatalf("factory calls = %d, want one route construction", len(factoryCalls))
	}
}

type p290TargetRouteInput struct {
	Provider           Provider
	Endpoint           string
	AuthKind           string
	AuthRef            string
	AdapterDigest      string
	ProfileID          string
	APIModel           string
	ResolvedKey        string
	ResolvedSecretHash string
}

type p290TargetRouteIdentity struct {
	Provider      Provider `json:"provider"`
	Endpoint      string   `json:"endpoint"`
	AuthKind      string   `json:"auth_kind"`
	AuthRef       string   `json:"auth_ref"`
	AdapterDigest string   `json:"adapter_digest"`
}

func (in p290TargetRouteInput) identity() (p290TargetRouteIdentity, error) {
	endpoint, err := p290TargetCanonicalEndpoint(in.Endpoint)
	if err != nil {
		return p290TargetRouteIdentity{}, err
	}
	return p290TargetRouteIdentity{
		Provider:      in.Provider,
		Endpoint:      endpoint,
		AuthKind:      strings.TrimSpace(in.AuthKind),
		AuthRef:       strings.TrimSpace(in.AuthRef),
		AdapterDigest: strings.TrimSpace(in.AdapterDigest),
	}, nil
}

func p290TargetCanonicalEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("absolute endpoint required")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

type p290TargetRouteCache struct {
	mu             sync.Mutex
	models         map[p290TargetRouteIdentity]string
	constructions  map[p290TargetRouteIdentity]int
	nextIdentifier int
}

func newP290TargetRouteCache() *p290TargetRouteCache {
	return &p290TargetRouteCache{
		models:        make(map[p290TargetRouteIdentity]string),
		constructions: make(map[p290TargetRouteIdentity]int),
	}
}

func (c *p290TargetRouteCache) route(input p290TargetRouteInput) (string, error) {
	identity, err := input.identity()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.models[identity]; existing != "" {
		return existing, nil
	}
	c.nextIdentifier++
	identifier := fmt.Sprintf("target-client-%d", c.nextIdentifier)
	c.models[identity] = identifier
	c.constructions[identity]++
	return identifier, nil
}

func TestP290TargetRouteIdentityEqualityAndIsolation(t *testing.T) {
	base := p290TargetRouteInput{
		Provider:           ProviderAgenticOpenAI,
		Endpoint:           "HTTPS://API.EXAMPLE/v1/",
		AuthKind:           "env",
		AuthRef:            "OPENAI_PRIMARY",
		AdapterDigest:      "adapter-v1",
		ProfileID:          "profile-a",
		APIModel:           "gpt-4o",
		ResolvedKey:        p290SecretSentinel,
		ResolvedSecretHash: "hash-a",
	}
	baseIdentity, err := base.identity()
	if err != nil {
		t.Fatal(err)
	}
	changedPresentation := base
	changedPresentation.ProfileID = "profile-b"
	changedPresentation.APIModel = "gpt-4.1"
	changedPresentation.ResolvedKey = "different-resolved-key"
	changedPresentation.ResolvedSecretHash = "hash-b"
	equalIdentity, err := changedPresentation.identity()
	if err != nil {
		t.Fatal(err)
	}
	if equalIdentity != baseIdentity {
		t.Fatalf("profile/model/secret changed target identity: %#v != %#v", equalIdentity, baseIdentity)
	}
	if baseIdentity.Endpoint != "https://api.example/v1" {
		t.Fatalf("canonical endpoint = %q", baseIdentity.Endpoint)
	}
	for name, mutate := range map[string]func(*p290TargetRouteInput){
		"provider": func(input *p290TargetRouteInput) {
			input.Provider = ProviderAgenticClaude
		},
		"endpoint": func(input *p290TargetRouteInput) {
			input.Endpoint = "https://other.example/v1"
		},
		"auth kind": func(input *p290TargetRouteInput) {
			input.AuthKind = "credential-store"
		},
		"auth ref": func(input *p290TargetRouteInput) {
			input.AuthRef = "OPENAI_SECONDARY"
		},
		"adapter digest": func(input *p290TargetRouteInput) {
			input.AdapterDigest = "adapter-v2"
		},
	} {
		changed := base
		mutate(&changed)
		changedIdentity, identityErr := changed.identity()
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if changedIdentity == baseIdentity {
			t.Fatalf("%s did not participate in target route identity", name)
		}
	}

	cache := newP290TargetRouteCache()
	baseClient, err := cache.route(base)
	if err != nil {
		t.Fatal(err)
	}
	sameRouteClient, err := cache.route(changedPresentation)
	if err != nil {
		t.Fatal(err)
	}
	if sameRouteClient != baseClient {
		t.Fatal("same route with a different API model constructed another target client")
	}

	distinctEndpoint := base
	distinctEndpoint.Endpoint = "https://other.example/v1"
	endpointClient, err := cache.route(distinctEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	distinctAuth := base
	distinctAuth.AuthRef = "OPENAI_SECONDARY"
	authClient, err := cache.route(distinctAuth)
	if err != nil {
		t.Fatal(err)
	}
	if endpointClient == baseClient || authClient == baseClient || endpointClient == authClient {
		t.Fatalf("target routes were not isolated: base=%q endpoint=%q auth=%q",
			baseClient, endpointClient, authClient)
	}

	unused := base
	unused.AdapterDigest = "adapter-unused"
	unusedIdentity, err := unused.identity()
	if err != nil {
		t.Fatal(err)
	}
	if cache.constructions[unusedIdentity] != 0 {
		t.Fatal("unused target route was initialized")
	}

	identities := make([]p290TargetRouteIdentity, 0, len(cache.models))
	for identity := range cache.models {
		identities = append(identities, identity)
	}
	for name, value := range map[string]any{
		"identity":   baseIdentity,
		"identities": identities,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, forbidden := range []string{
			p290SecretSentinel,
			base.ResolvedSecretHash,
			changedPresentation.ResolvedSecretHash,
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("%s serialization exposed secret-derived data", name)
			}
		}
	}
}

func TestP290TargetRouteCacheConstructsOnceConcurrently(t *testing.T) {
	cache := newP290TargetRouteCache()
	input := p290TargetRouteInput{
		Provider:           ProviderAgenticClaude,
		Endpoint:           "https://api.example/v1",
		AuthKind:           "credential-store",
		AuthRef:            "anthropic-primary",
		AdapterDigest:      "adapter-v1",
		APIModel:           "claude-sonnet-4-6",
		ResolvedKey:        p290SecretSentinel,
		ResolvedSecretHash: "hash-a",
	}
	identity, err := input.identity()
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	results := make(chan string, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			client, routeErr := cache.route(input)
			if routeErr != nil {
				results <- "error:" + routeErr.Error()
				return
			}
			results <- client
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var first string
	for result := range results {
		if strings.HasPrefix(result, "error:") {
			t.Fatal(result)
		}
		if first == "" {
			first = result
		}
		if result != first {
			t.Fatalf("concurrent route returned %q, want %q", result, first)
		}
	}
	if cache.constructions[identity] != 1 {
		t.Fatalf("target constructions = %d, want 1", cache.constructions[identity])
	}
}

func TestP290CurrentErrorsAndSixProviderConstructorsDoNotExposeSecret(t *testing.T) {
	ctx := context.Background()
	providers := []Provider{
		ProviderAgenticDeepSeek,
		ProviderAgenticClaude,
		ProviderAgenticGemini,
		ProviderAgenticOpenAI,
		ProviderAgenticArk,
		ProviderAgenticQwen,
	}
	for _, provider := range providers {
		t.Run(string(provider), func(t *testing.T) {
			constructed, err := newAgenticModel(ctx, Config{
				Provider: provider,
				Model:    "p290-construction-only-model",
				APIKey:   p290SecretSentinel,
				BaseURL:  "https://provider.example.invalid/v1",
			})
			if err != nil {
				if strings.Contains(err.Error(), p290SecretSentinel) {
					t.Fatal("constructor error exposed the credential sentinel")
				}
				t.Fatalf("constructor fixture: %v", err)
			}
			if constructed == nil {
				t.Fatal("constructor returned a nil model")
			}
		})
	}

	_, err := newAgenticModel(ctx, Config{
		Provider: "unsupported-provider",
		APIKey:   p290SecretSentinel,
	})
	if err == nil {
		t.Fatal("unsupported provider did not fail")
	}
	if strings.Contains(err.Error(), p290SecretSentinel) {
		t.Fatal("dispatch error exposed the credential sentinel")
	}

	_, err = ResolveConfig(ResolveInput{
		Explicit: Config{
			Provider: ProviderAgenticClaude,
			Model:    "openai:gpt-4o",
			APIKey:   p290SecretSentinel,
		},
		Getenv:           getenvMap(nil),
		CredentialLookup: credStore(nil),
	})
	if err == nil {
		t.Fatal("conflicting provider/model resolution did not fail")
	}
	if strings.Contains(err.Error(), p290SecretSentinel) {
		t.Fatal("resolution error exposed the credential sentinel")
	}

	_, err = NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   p290SecretSentinel,
			},
			Getenv:           getenvMap(nil),
			CredentialLookup: credStore(nil),
		},
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			return nil, errors.New("deliberate initialization failure")
		},
	})
	if err == nil {
		t.Fatal("initialization fixture did not fail")
	}
	if strings.Contains(err.Error(), p290SecretSentinel) {
		t.Fatal("initialization error exposed the credential sentinel")
	}

	secretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(p290SecretSentinel)))
	_, err = NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   p290SecretSentinel,
			},
			Getenv:           getenvMap(nil),
			CredentialLookup: credStore(nil),
		},
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			return nil, fmt.Errorf("unsafe upstream error key=%s hash=%s", p290SecretSentinel, secretHash)
		},
	})
	if err == nil {
		t.Fatal("unsafe initialization fixture did not fail")
	}
	if strings.Contains(err.Error(), p290SecretSentinel) || strings.Contains(err.Error(), secretHash) {
		t.Fatalf("initialization error exposed secret-derived data: %v", err)
	}
}
