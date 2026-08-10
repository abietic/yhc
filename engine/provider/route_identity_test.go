package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
)

func TestRouteIdentityEqualityAndIsolation(t *testing.T) {
	baseInput := RouteIdentityInput{
		Provider:      ProviderAgenticOpenAI,
		Endpoint:      "HTTPS://API.EXAMPLE:443/a/../v1/",
		AuthKind:      "env",
		AuthReference: "OPENAI_PRIMARY",
		AdapterDigest: "agenticopenai:v1",
	}
	base, err := NewRouteIdentity(baseInput)
	if err != nil {
		t.Fatal(err)
	}
	if base.Endpoint != "https://api.example/v1" {
		t.Fatalf("canonical endpoint = %q", base.Endpoint)
	}
	equal, err := NewRouteIdentity(RouteIdentityInput{
		Provider:      ProviderAgenticOpenAI,
		Endpoint:      "https://api.example/v1",
		AuthKind:      "env",
		AuthReference: "OPENAI_PRIMARY",
		AdapterDigest: "agenticopenai:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if equal != base {
		t.Fatalf("equivalent route identities differ: %#v != %#v", equal, base)
	}

	for name, mutate := range map[string]func(*RouteIdentityInput){
		"provider": func(input *RouteIdentityInput) {
			input.Provider = ProviderAgenticClaude
		},
		"endpoint": func(input *RouteIdentityInput) {
			input.Endpoint = "https://other.example/v1"
		},
		"auth kind": func(input *RouteIdentityInput) {
			input.AuthKind = "credential"
		},
		"auth reference": func(input *RouteIdentityInput) {
			input.AuthReference = "openai-secondary"
		},
		"adapter digest": func(input *RouteIdentityInput) {
			input.AdapterDigest = "agenticopenai:v2"
		},
	} {
		changedInput := baseInput
		mutate(&changedInput)
		changed, identityErr := NewRouteIdentity(changedInput)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if changed == base {
			t.Fatalf("%s did not participate in route identity", name)
		}
	}
}

func TestRouteIdentityDigestUsesCanonicalNonSecretFields(t *testing.T) {
	identity, err := NewRouteIdentity(RouteIdentityInput{
		Provider:      ProviderAgenticOpenAI,
		Endpoint:      "HTTPS://API.EXAMPLE:443/a/../v1/",
		AuthKind:      "env",
		AuthReference: "OPENAI_PRIMARY",
		AdapterDigest: "agenticopenai:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := identity.Digest()
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(`{"provider":"agenticopenai","endpoint":"https://api.example/v1","auth_kind":"env","auth_reference":"OPENAI_PRIMARY","adapter_digest":"agenticopenai:v1"}`)
	want := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if digest != want {
		t.Fatalf("route digest = %q, want %q", digest, want)
	}
}

func TestRouteIdentityRejectsUnsafeEndpointAndContainsNoSecret(t *testing.T) {
	for _, endpoint := range []string{
		"api.example/v1",
		"ftp://api.example/v1",
		"https://user:pass@api.example/v1",
		"https://api.example/v1?key=value",
		"https://api.example/v1#fragment",
	} {
		if _, err := NewRouteIdentity(RouteIdentityInput{
			Provider:      ProviderAgenticOpenAI,
			Endpoint:      endpoint,
			AuthKind:      "credential",
			AuthReference: "openai-primary",
			AdapterDigest: "agenticopenai:v1",
		}); err == nil {
			t.Fatalf("unsafe endpoint %q should fail", endpoint)
		}
	}

	identity, err := NewRouteIdentity(RouteIdentityInput{
		Provider:      ProviderAgenticOpenAI,
		Endpoint:      "https://api.example/v1",
		AuthKind:      "credential",
		AuthReference: "openai-primary",
		AdapterDigest: "agenticopenai:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{p290SecretSentinel, "resolved_secret", "secret_hash", "profile"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("route identity contains forbidden field or value %q: %s", forbidden, encoded)
		}
	}
}

func TestRouteKeyedRuntimeConstructsLazyRouteOnceConcurrently(t *testing.T) {
	ctx := context.Background()
	var (
		mu            sync.Mutex
		constructions = make(map[Provider]int)
	)
	runtime, err := NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticClaude,
				Model:    "claude-sonnet-4-6",
				APIKey:   "anthropic-key",
			},
			Getenv: getenvMap(map[string]string{"OPENAI_API_KEY": "openai-key"}),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			mu.Lock()
			constructions[config.Provider]++
			mu.Unlock()
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	results := make(chan model.BaseChatModel, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			_, chatModel, routeErr := runtime.routes.route(ctx, "openai:gpt-4o")
			if routeErr != nil {
				errs <- routeErr
				return
			}
			results <- chatModel
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for routeErr := range errs {
		t.Fatal(routeErr)
	}

	var first model.BaseChatModel
	for chatModel := range results {
		if first == nil {
			first = chatModel
		}
		if chatModel != first {
			t.Fatal("concurrent route lookup returned distinct clients")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if constructions[ProviderAgenticClaude] != 1 || constructions[ProviderAgenticOpenAI] != 1 {
		t.Fatalf("route constructions = %#v, want one per route", constructions)
	}
}
