package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	engineauth "github.com/abietic/yhc/engine/auth"
	engineconfig "github.com/abietic/yhc/engine/config"
	enginemodel "github.com/abietic/yhc/engine/model"
	"github.com/cloudwego/eino/components/model"
)

// NamedCredentialLookup resolves one opaque user-owned credential reference.
// Implementations must not retain or include the returned value in errors.
type NamedCredentialLookup func(name string) (string, error)

// NamedCredentialOriginLookup resolves one named credential together with an
// optional opaque rotation identity. The secret is used only for immediate
// client construction.
type NamedCredentialOriginLookup func(name string) (secret, originID string, err error)

// ConfiguredRuntimeOptions joins the source-aware portfolio compiler and the
// existing provider runtime at one CLI/ACP composition boundary.
type ConfiguredRuntimeOptions struct {
	Sources                     *engineconfig.ConfigSources
	ExplicitModelProfile        string
	ExplicitLegacyFields        []string
	LegacyFallbackModel         string
	Resolution                  ResolveInput
	Preflight                   bool
	PreflightTimeout            time.Duration
	NamedCredentialLookup       NamedCredentialLookup
	NamedCredentialOriginLookup NamedCredentialOriginLookup

	factory   modelFactory
	preflight preflightCheck
}

// NewConfiguredRuntime compiles named or legacy inputs into one canonical,
// non-secret portfolio before constructing the selected route.
func NewConfiguredRuntime(ctx context.Context, options ConfiguredRuntimeOptions) (*Runtime, error) {
	snapshot, err := engineconfig.CompilePortfolio(engineconfig.PortfolioCompileInput{
		Sources:                  options.Sources,
		ExplicitModelProfile:     options.ExplicitModelProfile,
		ExplicitLegacyFields:     options.ExplicitLegacyFields,
		LegacyFallbackConfigured: strings.TrimSpace(options.LegacyFallbackModel) != "",
		Getenv:                   options.Resolution.Getenv,
		LegacyCompiler: func() (*engineconfig.PortfolioSnapshot, error) {
			return compileLegacyPortfolio(options.Resolution, options.LegacyFallbackModel)
		},
	})
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(string(snapshot.Default), "legacy.") {
		runtime, runtimeErr := NewRuntime(ctx, RuntimeOptions{
			Resolution:       options.Resolution,
			Preflight:        options.Preflight,
			PreflightTimeout: options.PreflightTimeout,
			factory:          options.factory,
			preflight:        options.preflight,
		})
		if runtimeErr != nil {
			return nil, runtimeErr
		}
		if err := attachPortfolioRouting(runtime.routes, snapshot, options); err != nil {
			return nil, err
		}
		runtime.portfolio = snapshot
		inventory, inventoryErr := newRuntimeInventory(snapshot, nil)
		if inventoryErr != nil {
			return nil, inventoryErr
		}
		runtime.inventory = inventory
		return runtime, nil
	}
	return newNamedPortfolioRuntime(ctx, snapshot, options)
}

func newNamedPortfolioRuntime(
	ctx context.Context,
	snapshot *engineconfig.PortfolioSnapshot,
	options ConfiguredRuntimeOptions,
) (*Runtime, error) {
	factory := options.factory
	if factory == nil {
		factory = newBaseChatModel
	}
	check := options.preflight
	if check == nil {
		check = defaultPreflightCheck
	}
	getenv, credentialLookup, credentialOriginLookup := portfolioRouteLookups(options)
	routes := &routeRegistry{
		resolution:            sanitizedResolutionInput(options.Resolution),
		preflight:             options.Preflight,
		timeout:               options.PreflightTimeout,
		factory:               factory,
		check:                 check,
		models:                make(map[RouteIdentity]model.BaseChatModel),
		published:             make(map[RouteIdentity]*publishedRoute),
		portfolio:             snapshot,
		getenv:                getenv,
		namedCredential:       credentialLookup,
		namedCredentialOrigin: credentialOriginLookup,
		accountRoutes:         make(map[engineconfig.AccountID]RouteIdentity),
		diagnostics:           make(map[originDiagnosticKey]uint32),
	}
	inventory, err := newRuntimeInventory(
		snapshot,
		routes.inventoryRouteIdentity,
	)
	if err != nil {
		return nil, err
	}
	main, _, err := routes.routeNamedProfile(ctx, string(snapshot.Default))
	if err != nil {
		return nil, err
	}
	routes.main = main
	router := &routingChatModel{routes: routes}
	return &Runtime{
		ChatModel: router,
		Main:      main,
		routes:    routes,
		portfolio: snapshot,
		inventory: inventory,
	}, nil
}

func attachPortfolioRouting(
	routes *routeRegistry,
	snapshot *engineconfig.PortfolioSnapshot,
	options ConfiguredRuntimeOptions,
) error {
	if routes == nil || snapshot == nil {
		return fmt.Errorf("attach provider portfolio to uninitialized runtime")
	}
	getenv, credentialLookup, credentialOriginLookup := portfolioRouteLookups(options)
	routes.portfolio = snapshot
	routes.getenv = getenv
	routes.namedCredential = credentialLookup
	routes.namedCredentialOrigin = credentialOriginLookup
	routes.accountRoutes = make(
		map[engineconfig.AccountID]RouteIdentity,
		len(snapshot.Accounts),
	)
	for accountID, account := range snapshot.Accounts {
		identity, err := NewRouteIdentity(RouteIdentityInput{
			Provider:      Provider(account.Provider),
			Endpoint:      account.Endpoint,
			AuthKind:      account.AuthKind,
			AuthReference: account.AuthReference,
			AdapterDigest: account.AdapterDigest,
		})
		if err != nil {
			return fmt.Errorf(
				"attach model account %q route identity: %w",
				accountID,
				err,
			)
		}
		if routes.models[identity] != nil {
			routes.accountRoutes[accountID] = identity
		}
	}
	return nil
}

func portfolioRouteLookups(
	options ConfiguredRuntimeOptions,
) (func(string) string, NamedCredentialLookup, NamedCredentialOriginLookup) {
	getenv := options.Resolution.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	credentialLookup := options.NamedCredentialLookup
	if credentialLookup == nil {
		credentialLookup = engineauth.ResolveNamedCredential
	}
	credentialOriginLookup := options.NamedCredentialOriginLookup
	if credentialOriginLookup == nil && options.NamedCredentialLookup == nil {
		credentialOriginLookup = func(name string) (string, string, error) {
			resolved, err := engineauth.ResolveNamedCredentialOrigin(name)
			return resolved.Secret, resolved.OriginID, err
		}
	}
	return getenv, credentialLookup, credentialOriginLookup
}

func (r *routeRegistry) routeNamedProfile(
	ctx context.Context,
	modelSpec string,
) (ResolvedConfig, model.BaseChatModel, error) {
	for {
		config, published, err := r.routeNamedProfilePublished(ctx, modelSpec)
		if errors.Is(err, errRouteResolutionSuperseded) && ctx.Err() == nil {
			continue
		}
		if err != nil {
			return ResolvedConfig{}, nil, err
		}
		return config, published.client, nil
	}
}

func (r *routeRegistry) routeNamedProfilePublished(
	ctx context.Context,
	modelSpec string,
) (ResolvedConfig, *publishedRoute, error) {
	config, profile, account, err := r.namedRouteDescriptor(modelSpec)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	if r.legacyBound() && strings.HasPrefix(string(profile.ID), "legacy.") {
		resolved, resolveErr := r.resolveLegacySelector(config.Model)
		if resolveErr != nil {
			return ResolvedConfig{}, nil, resolveErr
		}
		identity, identityErr := legacyRouteIdentity(resolved)
		if identityErr != nil {
			return ResolvedConfig{}, nil, identityErr
		}
		attempt := r.reserveRouteResolution()
		credential, credentialErr := r.resolveLegacyCredential(resolved)
		if credentialErr != nil {
			return ResolvedConfig{}, nil, credentialErr
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		published, publishErr := r.publishRouteLocked(
			ctx,
			resolved,
			identity,
			string(account.ID),
			credential,
			attempt,
		)
		if publishErr != nil {
			return ResolvedConfig{}, nil, publishErr
		}
		if r.accountRoutes == nil {
			r.accountRoutes = make(map[engineconfig.AccountID]RouteIdentity)
		}
		r.accountRoutes[account.ID] = identity
		return resolved, published, nil
	}

	attempt := r.reserveRouteResolution()
	identity, credential, err := r.resolveNamedRoute(account, config)
	if err != nil {
		return ResolvedConfig{}, nil, fmt.Errorf("initialize model profile %q: %w", profile.ID, err)
	}
	config.Sources.APIKey = credential.source
	r.mu.Lock()
	defer r.mu.Unlock()
	published, err := r.publishRouteLocked(
		ctx,
		config,
		identity,
		string(account.ID),
		credential,
		attempt,
	)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	if r.accountRoutes == nil {
		r.accountRoutes = make(map[engineconfig.AccountID]RouteIdentity)
	}
	r.accountRoutes[account.ID] = identity
	return config, published, nil
}

func (r *routeRegistry) routePortfolioSelectorPublished(
	ctx context.Context,
	modelSpec string,
) (ResolvedConfig, *publishedRoute, error) {
	if legacySelector, legacy := splitLegacySelector(modelSpec); legacy {
		return r.routeLegacySelectorPublished(ctx, legacySelector)
	}
	if _, err := r.selectedNamedProfile(modelSpec); err == nil {
		return r.routeNamedProfilePublished(ctx, modelSpec)
	}
	if r.legacyBound() {
		return r.routeLegacySelectorPublished(ctx, modelSpec)
	}
	return ResolvedConfig{}, nil, fmt.Errorf("model selector %q is not a configured profile; use legacy:<selector> for legacy resolution", modelSpec)
}

func (r *routeRegistry) resolvePortfolioSelector(modelSpec string) (ResolvedConfig, error) {
	if legacySelector, legacy := splitLegacySelector(modelSpec); legacy {
		return r.resolveLegacySelector(legacySelector)
	}
	if config, err := r.resolveNamedProfile(modelSpec); err == nil {
		return config, nil
	}
	if r.legacyBound() {
		return r.resolveLegacySelector(modelSpec)
	}
	return ResolvedConfig{}, fmt.Errorf("model selector %q is not a configured profile; use legacy:<selector> for legacy resolution", modelSpec)
}

func (r *routeRegistry) legacyBound() bool {
	return r.portfolio != nil && strings.HasPrefix(string(r.portfolio.Default), "legacy.")
}

func splitLegacySelector(modelSpec string) (string, bool) {
	trimmed := strings.TrimSpace(modelSpec)
	if !strings.HasPrefix(strings.ToLower(trimmed), "legacy:") {
		return "", false
	}
	return strings.TrimSpace(trimmed[len("legacy:"):]), true
}

func (r *routeRegistry) routeLegacySelectorPublished(
	ctx context.Context,
	modelSpec string,
) (ResolvedConfig, *publishedRoute, error) {
	if strings.TrimSpace(modelSpec) == "" {
		return ResolvedConfig{}, nil, fmt.Errorf("legacy model selector must not be empty")
	}
	config, err := r.resolveLegacySelector(modelSpec)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	identity, err := legacyRouteIdentity(config)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	attempt := r.reserveRouteResolution()
	credential, err := r.resolveLegacyCredential(config)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	published, err := r.publishRouteLocked(
		ctx,
		config,
		identity,
		r.legacyAccountID(config, identity),
		credential,
		attempt,
	)
	if err != nil {
		return ResolvedConfig{}, nil, err
	}
	return config, published, nil
}

func (r *routeRegistry) resolveLegacySelector(modelSpec string) (ResolvedConfig, error) {
	trimmed := strings.TrimSpace(modelSpec)
	if trimmed == "" {
		return ResolvedConfig{}, fmt.Errorf("legacy model selector must not be empty")
	}
	if r.portfolio == nil {
		return r.resolveModel(trimmed)
	}
	legacyMain, err := ResolveConfig(r.resolution)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf(
			"resolve legacy model inventory: %w",
			err,
		)
	}
	legacy := &routeRegistry{
		resolution: r.resolution,
		main:       legacyMain,
	}
	return legacy.resolveModel(trimmed)
}

func (r *routeRegistry) resolveNamedRoute(
	account engineconfig.ResolvedAccount,
	config ResolvedConfig,
) (RouteIdentity, resolvedRouteCredential, error) {
	identityInput := RouteIdentityInput{
		Provider:      config.Provider,
		Endpoint:      account.Endpoint,
		AuthKind:      account.AuthKind,
		AuthReference: account.AuthReference,
		AdapterDigest: account.AdapterDigest,
	}
	switch account.AuthKind {
	case "env":
		identity, err := NewRouteIdentity(identityInput)
		if err != nil {
			return RouteIdentity{}, resolvedRouteCredential{}, err
		}
		secret := r.getenv(account.AuthReference)
		if strings.TrimSpace(secret) == "" {
			return RouteIdentity{}, resolvedRouteCredential{}, fmt.Errorf("environment credential %q is not configured", account.AuthReference)
		}
		return identity, resolvedRouteCredential{
			secret: secret,
			source: "portfolio:env:" + account.AuthReference,
		}, nil
	case "credential":
		identity, err := NewRouteIdentity(identityInput)
		if err != nil {
			return RouteIdentity{}, resolvedRouteCredential{}, err
		}
		var secret, originID string
		if r.namedCredentialOrigin != nil {
			secret, originID, err = r.namedCredentialOrigin(account.AuthReference)
		} else {
			secret, err = r.namedCredential(account.AuthReference)
		}
		if err != nil {
			return RouteIdentity{}, resolvedRouteCredential{}, err
		}
		if strings.TrimSpace(secret) == "" {
			return RouteIdentity{}, resolvedRouteCredential{}, fmt.Errorf("named credential %q is empty", account.AuthReference)
		}
		return identity, resolvedRouteCredential{
			secret:   secret,
			source:   "portfolio:credential:" + account.AuthReference,
			originID: strings.TrimSpace(originID),
		}, nil
	case "provider_default":
		identity, resolved, err := r.providerDefaultRouteIdentity(
			account,
			config.Provider,
			config.Model,
		)
		if err != nil {
			return RouteIdentity{}, resolvedRouteCredential{}, err
		}
		credential, err := r.resolveLegacyCredential(resolved)
		if err != nil {
			return RouteIdentity{}, resolvedRouteCredential{}, err
		}
		credential.source = "portfolio:provider_default:" + resolved.Sources.APIKey
		return identity, credential, nil
	default:
		return RouteIdentity{}, resolvedRouteCredential{}, fmt.Errorf("unsupported portfolio auth kind %q", account.AuthKind)
	}
}

func (r *routeRegistry) inventoryRouteIdentity(
	profile engineconfig.ResolvedProfile,
	account engineconfig.ResolvedAccount,
) (RouteIdentity, error) {
	provider, err := NormalizeProvider(Provider(account.Provider))
	if err != nil {
		return RouteIdentity{}, err
	}
	if account.AuthKind != "provider_default" {
		return NewRouteIdentity(RouteIdentityInput{
			Provider:      provider,
			Endpoint:      account.Endpoint,
			AuthKind:      account.AuthKind,
			AuthReference: account.AuthReference,
			AdapterDigest: account.AdapterDigest,
		})
	}
	identity, _, err := r.providerDefaultRouteIdentity(
		account,
		provider,
		profile.APIModel,
	)
	return identity, err
}

func (r *routeRegistry) providerDefaultRouteIdentity(
	account engineconfig.ResolvedAccount,
	provider Provider,
	apiModel string,
) (RouteIdentity, ResolvedConfig, error) {
	resolved, err := ResolveConfig(ResolveInput{
		Explicit: Config{
			Provider: provider,
			Model:    apiModel,
			BaseURL:  account.Endpoint,
		},
		Getenv:                 withoutGenericProviderEnv(r.getenv),
		CredentialLookup:       r.resolution.CredentialLookup,
		CredentialOriginLookup: r.resolution.CredentialOriginLookup,
	})
	if err != nil {
		return RouteIdentity{}, ResolvedConfig{}, err
	}
	authKind, authReference := legacyAuthIdentity(resolved)
	identity, err := NewRouteIdentity(RouteIdentityInput{
		Provider:      provider,
		Endpoint:      account.Endpoint,
		AuthKind:      authKind,
		AuthReference: authReference,
		AdapterDigest: account.AdapterDigest,
	})
	if err != nil {
		return RouteIdentity{}, ResolvedConfig{}, err
	}
	return identity, resolved, nil
}

func (r *routeRegistry) namedRouteDescriptor(
	modelSpec string,
) (ResolvedConfig, engineconfig.ResolvedProfile, engineconfig.ResolvedAccount, error) {
	profile, err := r.selectedNamedProfile(modelSpec)
	if err != nil {
		return ResolvedConfig{}, engineconfig.ResolvedProfile{}, engineconfig.ResolvedAccount{}, err
	}
	account, ok := r.portfolio.Accounts[profile.Account]
	if !ok {
		return ResolvedConfig{}, engineconfig.ResolvedProfile{}, engineconfig.ResolvedAccount{},
			fmt.Errorf("model profile %q references unavailable account %q", profile.ID, profile.Account)
	}
	provider, err := NormalizeProvider(Provider(account.Provider))
	if err != nil {
		return ResolvedConfig{}, engineconfig.ResolvedProfile{}, engineconfig.ResolvedAccount{}, err
	}
	return ResolvedConfig{
		Config: Config{
			Provider: provider,
			Model:    profile.APIModel,
			BaseURL:  account.Endpoint,
		},
		Sources: ResolutionSources{
			Provider: "portfolio:account:" + string(account.ID),
			Model:    "portfolio:profile:" + string(profile.ID),
			APIKey:   "portfolio:" + account.AuthKind,
			BaseURL:  "portfolio:account:" + string(account.ID),
		},
		CredentialConfigured: true,
	}, profile, account, nil
}

func (r *routeRegistry) resolveNamedProfile(modelSpec string) (ResolvedConfig, error) {
	config, _, _, err := r.namedRouteDescriptor(modelSpec)
	return config, err
}

func (r *routeRegistry) selectedNamedProfile(
	modelSpec string,
) (engineconfig.ResolvedProfile, error) {
	selected, ok := r.portfolio.Profiles[r.portfolio.Default]
	if !ok {
		return engineconfig.ResolvedProfile{}, fmt.Errorf("selected model profile %q is unavailable", r.portfolio.Default)
	}
	trimmed := strings.TrimSpace(modelSpec)
	if trimmed == "" {
		return selected, nil
	}
	profileID := engineconfig.ProfileID(strings.ToLower(trimmed))
	if profile, exists := r.portfolio.Profiles[profileID]; exists {
		return profile, nil
	}
	// Preserve ResolveModel compatibility for the active provider-local model.
	// A canonical profile ID has already won above, so this cannot select a
	// profile by map iteration when a legacy token collides with that ID.
	if strings.EqualFold(trimmed, selected.APIModel) {
		return selected, nil
	}
	return engineconfig.ResolvedProfile{}, fmt.Errorf(
		"model %q is not a configured model profile",
		trimmed,
	)
}

func compileLegacyPortfolio(
	resolution ResolveInput,
	fallbackModel string,
) (*engineconfig.PortfolioSnapshot, error) {
	main, err := ResolveConfig(resolution)
	if err != nil {
		return nil, err
	}
	mainIdentity, err := legacyRouteIdentity(main)
	if err != nil {
		return nil, err
	}
	mainAccountID := engineconfig.AccountID("legacy.main.account")
	mainProfileID := engineconfig.ProfileID("legacy.main")
	mainMetadata, err := enginemodel.ResolvePortfolioMetadata(main.Model, enginemodel.MetadataOverrides{})
	if err != nil {
		return nil, err
	}
	snapshot := &engineconfig.PortfolioSnapshot{
		Default: mainProfileID,
		Accounts: map[engineconfig.AccountID]engineconfig.ResolvedAccount{
			mainAccountID: legacyResolvedAccount(mainAccountID, mainIdentity),
		},
		Profiles: map[engineconfig.ProfileID]engineconfig.ResolvedProfile{
			mainProfileID: {
				ID:       mainProfileID,
				Account:  mainAccountID,
				APIModel: main.Model,
				Metadata: mainMetadata,
			},
		},
		Roles: map[engineconfig.ModelRole]engineconfig.ProfileID{
			engineconfig.RoleMain: mainProfileID, engineconfig.RoleExplore: mainProfileID,
			engineconfig.RolePlan: mainProfileID, engineconfig.RoleGeneral: mainProfileID,
			engineconfig.RoleSummary: mainProfileID,
		},
		Failover:        make(map[engineconfig.ModelRole]engineconfig.ResolvedFailoverPolicy),
		SelectionSource: "legacy",
	}

	fallbackModel = strings.TrimSpace(fallbackModel)
	if fallbackModel == "" {
		return snapshot, nil
	}
	temporary := &routeRegistry{resolution: resolution, main: main}
	fallback, err := temporary.resolveModel(fallbackModel)
	if err != nil {
		return nil, fmt.Errorf("fallback model %q: %w", fallbackModel, err)
	}
	if fallback.Provider == main.Provider && fallback.Model == main.Model {
		return nil, fmt.Errorf("fallback model cannot resolve to the same provider and model as the main model")
	}
	fallbackIdentity, err := legacyRouteIdentity(fallback)
	if err != nil {
		return nil, err
	}
	fallbackAccountID := mainAccountID
	if fallbackIdentity != mainIdentity {
		fallbackAccountID = engineconfig.AccountID("legacy.fallback.account")
		snapshot.Accounts[fallbackAccountID] = legacyResolvedAccount(fallbackAccountID, fallbackIdentity)
	}
	fallbackProfileID := engineconfig.ProfileID("legacy.fallback")
	fallbackMetadata, err := enginemodel.ResolvePortfolioMetadata(fallback.Model, enginemodel.MetadataOverrides{})
	if err != nil {
		return nil, err
	}
	snapshot.Profiles[fallbackProfileID] = engineconfig.ResolvedProfile{
		ID: fallbackProfileID, Account: fallbackAccountID, APIModel: fallback.Model, Metadata: fallbackMetadata,
	}
	snapshot.Failover[engineconfig.RoleMain] = engineconfig.ResolvedFailoverPolicy{
		Alternates:       []engineconfig.ProfileID{fallbackProfileID},
		On:               []string{"overloaded"},
		MaxSwitches:      1,
		MaxProviderCalls: 6,
		MaxElapsedMS:     45000,
	}
	return snapshot, nil
}

func legacyResolvedAccount(
	accountID engineconfig.AccountID,
	identity RouteIdentity,
) engineconfig.ResolvedAccount {
	return engineconfig.ResolvedAccount{
		ID:            accountID,
		Provider:      string(providerID(identity.Provider)),
		Endpoint:      identity.Endpoint,
		AuthKind:      identity.AuthKind,
		AuthReference: identity.AuthReference,
		AdapterDigest: identity.AdapterDigest,
	}
}

func sanitizedResolutionInput(input ResolveInput) ResolveInput {
	input.Explicit.APIKey = ""
	input.Configured.APIKey = ""
	return input
}
