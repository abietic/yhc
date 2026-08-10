package providerorigin

import (
	"context"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const (
	OriginVersion     uint16 = 1
	OpenAIResponsesV1        = "openai-responses/v1"
)

type Reason string

const (
	ReasonExact              Reason = "origin_exact"
	ReasonAbsent             Reason = "origin_absent"
	ReasonLegacyUnverified   Reason = "origin_legacy_unverified"
	ReasonProviderMismatch   Reason = "origin_provider_mismatch"
	ReasonAccountMismatch    Reason = "origin_account_mismatch"
	ReasonAPIFamilyMismatch  Reason = "origin_api_family_mismatch"
	ReasonAPIModelMismatch   Reason = "origin_api_model_mismatch"
	ReasonCredentialMismatch Reason = "origin_credential_mismatch" //nolint:gosec // Stable diagnostic code, not a credential.
	ReasonRouteStale         Reason = "origin_route_stale"
	ReasonRecoveryMismatch   Reason = "origin_recovery_mismatch"
)

// Origin is the non-secret exact dispatch identity for one successfully
// completed assistant response. RoutePublication is attempt-local and is not
// part of the durable sidecar.
type Origin struct {
	Version             uint16 `json:"version"`
	Provider            string `json:"provider"`
	AccountID           string `json:"account_id"`
	APIFamily           string `json:"api_family"`
	APIModel            string `json:"api_model"`
	RouteIdentityDigest string `json:"route_identity_digest"`
	CredentialOriginID  string `json:"credential_origin_id"`
	RoutePublication    uint64 `json:"-"`
}

func (o Origin) DurableValid() bool {
	return o.Version == OriginVersion &&
		strings.TrimSpace(o.Provider) != "" &&
		strings.TrimSpace(o.AccountID) != "" &&
		strings.TrimSpace(o.APIFamily) != "" &&
		strings.TrimSpace(o.APIModel) != "" &&
		canonicalDigest(o.RouteIdentityDigest) &&
		strings.TrimSpace(o.CredentialOriginID) != ""
}

func canonicalDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

type BindingState uint8

const (
	BindingAbsent BindingState = iota
	BindingLegacyUnverified
	BindingRecoveryMismatch
	BindingVerified
)

type BindingResolution struct {
	State  BindingState
	Origin Origin
}

type BindingResolver interface {
	ResolveAssistantOrigin(*schema.Message) BindingResolution
}

type bindingResolverKey struct{}

func WithBindingResolver(ctx context.Context, resolver BindingResolver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, bindingResolverKey{}, resolver)
}

func ResolveBinding(ctx context.Context, message *schema.Message) BindingResolution {
	if ctx == nil || message == nil {
		return BindingResolution{State: BindingAbsent}
	}
	resolver, _ := ctx.Value(bindingResolverKey{}).(BindingResolver)
	if resolver == nil {
		return BindingResolution{State: BindingAbsent}
	}
	return resolver.ResolveAssistantOrigin(message)
}

type DispatchState struct {
	mu     sync.Mutex
	origin Origin
	set    bool
}

type dispatchStateKey struct{}

func WithDispatchState(ctx context.Context) (context.Context, *DispatchState) {
	if ctx == nil {
		ctx = context.Background()
	}
	state := &DispatchState{}
	return context.WithValue(ctx, dispatchStateKey{}, state), state
}

func PublishDispatch(ctx context.Context, origin Origin) {
	if ctx == nil || !origin.DurableValid() || origin.RoutePublication == 0 {
		return
	}
	state, _ := ctx.Value(dispatchStateKey{}).(*DispatchState)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.origin = origin
	state.set = true
	state.mu.Unlock()
}

func (s *DispatchState) Snapshot() (Origin, bool) {
	if s == nil {
		return Origin{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return Origin{}, false
	}
	return s.origin, true
}
