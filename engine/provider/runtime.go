package provider

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type (
	modelFactory   func(context.Context, Config) (model.BaseChatModel, error)
	preflightCheck func(context.Context, ResolvedConfig, time.Duration) error
)

// RuntimeOptions configures provider resolution, lazy provider routing, and an
// optional connectivity/authentication preflight.
type RuntimeOptions struct {
	Resolution       ResolveInput
	Preflight        bool
	PreflightTimeout time.Duration

	factory   modelFactory
	preflight preflightCheck
}

// Runtime owns the provider-aware chat model and its resolved main config.
// The router lazily initializes additional providers when a fallback or TUI
// model switch selects a model owned by another provider.
type Runtime struct {
	ChatModel model.BaseChatModel
	Main      ResolvedConfig
	routes    *routeRegistry
	portfolio *engineconfig.PortfolioSnapshot
	inventory RuntimeInventorySnapshot
}

// ReasoningOriginDiagnostic is a bounded redacted continuation decision
// counter. It intentionally exposes neither route nor message identity.
type ReasoningOriginDiagnostic struct {
	Path   string
	Reason string
	Count  uint32
}

// ReasoningOriginDiagnostics returns a detached, deterministically ordered
// snapshot of rejected private-continuation decisions.
func (r *Runtime) ReasoningOriginDiagnostics() []ReasoningOriginDiagnostic {
	if r == nil || r.routes == nil {
		return nil
	}
	r.routes.mu.Lock()
	defer r.routes.mu.Unlock()
	result := make([]ReasoningOriginDiagnostic, 0, len(r.routes.diagnostics))
	for key, count := range r.routes.diagnostics {
		result = append(result, ReasoningOriginDiagnostic{
			Path:   key.path,
			Reason: string(key.reason),
			Count:  count,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Reason < result[j].Reason
	})
	return result
}

// NewRuntime resolves and initializes the main provider route.
func NewRuntime(ctx context.Context, opts RuntimeOptions) (*Runtime, error) {
	mainConfig, err := ResolveConfig(opts.Resolution)
	if err != nil {
		return nil, err
	}
	factory := opts.factory
	if factory == nil {
		factory = newBaseChatModel
	}
	check := opts.preflight
	if check == nil {
		check = defaultPreflightCheck
	}
	routes := &routeRegistry{
		resolution:  opts.Resolution,
		main:        mainConfig,
		preflight:   opts.Preflight,
		timeout:     opts.PreflightTimeout,
		factory:     factory,
		check:       check,
		models:      make(map[RouteIdentity]model.BaseChatModel),
		published:   make(map[RouteIdentity]*publishedRoute),
		diagnostics: make(map[originDiagnosticKey]uint32),
	}
	if _, _, err := routes.route(ctx, mainConfig.Config.Model); err != nil {
		return nil, err
	}
	portfolio, err := compileLegacyPortfolio(opts.Resolution, "")
	if err != nil {
		return nil, err
	}
	inventory, err := newRuntimeInventory(portfolio, nil)
	if err != nil {
		return nil, err
	}
	router := &routingChatModel{routes: routes}
	return &Runtime{ChatModel: router, Main: mainConfig, routes: routes, inventory: inventory}, nil
}

// ResolveModel resolves a model spec using the runtime's provider policy
// without creating a network client.
func (r *Runtime) ResolveModel(modelSpec string) (ResolvedConfig, error) {
	if r == nil || r.routes == nil {
		return ResolvedConfig{}, fmt.Errorf("provider runtime is not initialized")
	}
	return r.routes.resolveModel(modelSpec)
}

// PrepareModel resolves and initializes a model route. It is used to fail fast
// for configured fallbacks while ordinary TUI model switches remain lazy.
func (r *Runtime) PrepareModel(ctx context.Context, modelSpec string) (ResolvedConfig, error) {
	if r == nil || r.routes == nil {
		return ResolvedConfig{}, fmt.Errorf("provider runtime is not initialized")
	}
	config, _, err := r.routes.route(ctx, modelSpec)
	return config, err
}

// PortfolioDiagnostics returns detached non-secret compiler/runtime
// diagnostics without constructing any unused route.
func (r *Runtime) PortfolioDiagnostics() []engineconfig.PortfolioDiagnostic {
	if r == nil || r.portfolio == nil {
		return nil
	}
	result := make([]engineconfig.PortfolioDiagnostic, len(r.portfolio.Diagnostics))
	for index, diagnostic := range r.portfolio.Diagnostics {
		diagnostic.Keys = append([]string(nil), diagnostic.Keys...)
		result[index] = diagnostic
	}
	return result
}

// UsesNamedPortfolio reports whether startup selected a user-owned named
// profile rather than the legacy compatibility compiler.
func (r *Runtime) UsesNamedPortfolio() bool {
	return r != nil &&
		r.portfolio != nil &&
		!strings.HasPrefix(string(r.portfolio.Default), "legacy.")
}

type routeRegistry struct {
	mu                      sync.Mutex
	resolution              ResolveInput
	main                    ResolvedConfig
	preflight               bool
	timeout                 time.Duration
	factory                 modelFactory
	check                   preflightCheck
	models                  map[RouteIdentity]model.BaseChatModel
	published               map[RouteIdentity]*publishedRoute
	nextPublication         uint64
	nextResolutionAttempt   uint64
	accountPublications     map[string]*publishedRoute
	latestAccountResolution map[string]uint64
	portfolio               *engineconfig.PortfolioSnapshot
	getenv                  func(string) string
	namedCredential         NamedCredentialLookup
	namedCredentialOrigin   NamedCredentialOriginLookup
	accountRoutes           map[engineconfig.AccountID]RouteIdentity
	diagnostics             map[originDiagnosticKey]uint32
	beforeProof             func()
	credentialTagKey        [32]byte
	credentialTagKeyReady   bool
}

type publishedRoute struct {
	identity           RouteIdentity
	client             model.BaseChatModel
	provider           Provider
	accountID          string
	apiFamily          string
	routeDigest        string
	credentialOriginID string
	credentialCacheTag [32]byte
	publication        uint64
}

type originDiagnosticKey struct {
	path   string
	reason providerorigin.Reason
}

func (r *routeRegistry) route(ctx context.Context, modelSpec string) (ResolvedConfig, model.BaseChatModel, error) {
	for {
		config, published, err := r.routePublished(ctx, modelSpec)
		if errors.Is(err, errRouteResolutionSuperseded) && ctx.Err() == nil {
			continue
		}
		if err != nil {
			return ResolvedConfig{}, nil, err
		}
		return config, published.client, nil
	}
}

var errRouteResolutionSuperseded = errors.New("provider route resolution was superseded")

func (r *routeRegistry) routePublished(
	ctx context.Context,
	modelSpec string,
) (ResolvedConfig, *publishedRoute, error) {
	if legacySelector, legacy := splitLegacySelector(modelSpec); legacy {
		return r.routeLegacySelectorPublished(ctx, legacySelector)
	}
	if r.portfolio != nil {
		return r.routePortfolioSelectorPublished(ctx, modelSpec)
	}
	config, err := r.resolveModel(modelSpec)
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

type resolvedRouteCredential struct {
	secret   string
	source   string
	originID string
}

func (r *routeRegistry) resolveLegacyCredential(
	config ResolvedConfig,
) (resolvedRouteCredential, error) {
	credential := resolvedRouteCredential{
		secret: config.APIKey,
		source: config.Sources.APIKey,
	}
	source := strings.TrimSpace(config.Sources.APIKey)
	switch {
	case strings.HasPrefix(source, "env:"):
		getenv := r.resolution.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		credential.secret = strings.TrimSpace(getenv(strings.TrimPrefix(source, "env:")))
		if credential.secret == "" {
			return resolvedRouteCredential{}, fmt.Errorf("provider credential source is no longer configured")
		}
	case source == "credential-store":
		originLookup := r.resolution.CredentialOriginLookup
		if originLookup == nil && r.resolution.CredentialLookup == nil {
			originLookup = storedCredentialOrigin
		}
		if originLookup != nil {
			secret, originID, ok, err := originLookup(providerCredentialID(config.Provider))
			if err != nil {
				return resolvedRouteCredential{}, fmt.Errorf("reload provider credential: %w", err)
			}
			if !ok || strings.TrimSpace(secret) == "" {
				return resolvedRouteCredential{}, fmt.Errorf("provider credential is no longer configured")
			}
			credential.secret = secret
			credential.originID = strings.TrimSpace(originID)
			break
		}
		lookup := r.resolution.CredentialLookup
		if lookup == nil {
			lookup = storedCredential
		}
		secret, ok, err := lookup(providerCredentialID(config.Provider))
		if err != nil {
			return resolvedRouteCredential{}, fmt.Errorf("reload provider credential: %w", err)
		}
		if !ok || strings.TrimSpace(secret) == "" {
			return resolvedRouteCredential{}, fmt.Errorf("provider credential is no longer configured")
		}
		credential.secret = secret
	}
	if strings.TrimSpace(credential.secret) == "" {
		return resolvedRouteCredential{}, fmt.Errorf("provider credential is empty")
	}
	return credential, nil
}

func (r *routeRegistry) publishRouteLocked(
	ctx context.Context,
	config ResolvedConfig,
	identity RouteIdentity,
	accountID string,
	credential resolvedRouteCredential,
	resolutionAttempt uint64,
) (*publishedRoute, error) {
	if r.published == nil {
		r.published = make(map[RouteIdentity]*publishedRoute)
	}
	if r.models == nil {
		r.models = make(map[RouteIdentity]model.BaseChatModel)
	}
	if r.accountPublications == nil {
		r.accountPublications = make(map[string]*publishedRoute)
	}
	if r.latestAccountResolution == nil {
		r.latestAccountResolution = make(map[string]uint64)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("provider route account identity is empty")
	}
	routeDigest, err := identity.Digest()
	if err != nil {
		return nil, err
	}
	apiFamily := providerAPIFamily(config.Provider)
	if latest := r.latestAccountResolution[accountID]; latest > resolutionAttempt {
		return nil, errRouteResolutionSuperseded
	}
	existing := r.published[identity]
	credentialCacheTag, err := r.cacheTagLocked(credential.secret)
	if err != nil {
		return nil, err
	}
	if existing != nil &&
		credentialReusable(existing, credential, credentialCacheTag) &&
		existing.provider == config.Provider &&
		existing.accountID == accountID &&
		existing.apiFamily == apiFamily &&
		existing.routeDigest == routeDigest {
		r.accountPublications[accountID] = existing
		r.latestAccountResolution[accountID] = resolutionAttempt
		return existing, nil
	}
	configWithSecret := config
	configWithSecret.APIKey = credential.secret
	if r.preflight {
		if err := r.check(ctx, configWithSecret, r.timeout); err != nil {
			return nil, redactRouteError(err, credential.secret)
		}
	}
	chatModel, err := r.factory(ctx, configWithSecret.Config)
	if err != nil {
		return nil, fmt.Errorf(
			"initialize %s model %q: %w",
			config.Provider,
			config.Model,
			redactRouteError(err, credential.secret),
		)
	}
	r.nextPublication++
	published := &publishedRoute{
		identity:           identity,
		client:             chatModel,
		provider:           config.Provider,
		accountID:          accountID,
		apiFamily:          apiFamily,
		routeDigest:        routeDigest,
		credentialOriginID: credential.originID,
		credentialCacheTag: credentialCacheTag,
		publication:        r.nextPublication,
	}
	r.published[identity] = published
	r.models[identity] = chatModel
	r.accountPublications[accountID] = published
	r.latestAccountResolution[accountID] = resolutionAttempt
	return published, nil
}

func (r *routeRegistry) reserveRouteResolution() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextResolutionAttempt++
	return r.nextResolutionAttempt
}

func (r *routeRegistry) cacheTagLocked(secret string) ([32]byte, error) {
	if !r.credentialTagKeyReady {
		if _, err := rand.Read(r.credentialTagKey[:]); err != nil {
			return [32]byte{}, fmt.Errorf(
				"initialize provider credential cache identity: %w",
				err,
			)
		}
		r.credentialTagKeyReady = true
	}
	hash := hmac.New(sha256.New, r.credentialTagKey[:])
	_, _ = hash.Write([]byte(secret))
	var tag [32]byte
	copy(tag[:], hash.Sum(nil))
	return tag, nil
}

func credentialReusable(
	existing *publishedRoute,
	credential resolvedRouteCredential,
	cacheTag [32]byte,
) bool {
	if existing == nil {
		return false
	}
	if credential.originID != "" || existing.credentialOriginID != "" {
		return credential.originID != "" &&
			existing.credentialOriginID == credential.originID
	}
	return hmac.Equal(existing.credentialCacheTag[:], cacheTag[:])
}

func providerAPIFamily(provider Provider) string {
	if provider == ProviderAgenticOpenAI {
		return providerorigin.OpenAIResponsesV1
	}
	return strings.ToLower(strings.TrimSpace(string(provider))) + "/v1"
}

func (r *routeRegistry) legacyAccountID(
	config ResolvedConfig,
	identity RouteIdentity,
) string {
	if r.portfolio != nil {
		for accountID, account := range r.portfolio.Accounts {
			candidate, err := r.inventoryRouteIdentity(
				engineconfig.ResolvedProfile{APIModel: config.Model},
				account,
			)
			if err == nil && candidate == identity {
				return string(accountID)
			}
		}
	}
	if r.main.Provider == config.Provider && r.main.BaseURL == config.BaseURL {
		return "legacy.main.account"
	}
	digest, err := identity.Digest()
	if err != nil || len(digest) < 16 {
		return "legacy.route"
	}
	return "legacy.route." + digest[:16]
}

func redactRouteError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
		message = strings.ReplaceAll(message, digest, "[REDACTED]")
	}
	return errors.New(message)
}

func (r *routeRegistry) resolveModel(modelSpec string) (ResolvedConfig, error) {
	if legacySelector, legacy := splitLegacySelector(modelSpec); legacy {
		return r.resolveLegacySelector(legacySelector)
	}
	if r.portfolio != nil {
		return r.resolvePortfolioSelector(modelSpec)
	}
	trimmed := strings.TrimSpace(modelSpec)
	if trimmed == "" || trimmed == r.main.Config.Model {
		return r.main, nil
	}
	resolvedTarget := resolveAlias(trimmed, r.main.Config.ModelAliases)
	targetProvider, resolvedTarget, err := splitProviderModel(resolvedTarget)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if targetProvider == "" {
		targetProvider = providerFromModel(resolvedTarget)
	}
	if targetProvider == "" || targetProvider == r.main.Config.Provider {
		input := r.resolution
		input.Explicit = Config{
			Provider:     r.main.Config.Provider,
			Model:        trimmed,
			APIKey:       r.main.Config.APIKey,
			BaseURL:      r.main.Config.BaseURL,
			MaxTokens:    r.main.Config.MaxTokens,
			ModelAliases: r.main.Config.ModelAliases,
		}
		return ResolveConfig(input)
	}

	input := r.resolution
	input.Explicit = Config{Model: trimmed, ModelAliases: r.main.Config.ModelAliases}
	input.Configured = Config{ModelAliases: r.main.Config.ModelAliases}
	input.Getenv = withoutGenericProviderEnv(input.Getenv)
	return ResolveConfig(input)
}

func withoutGenericProviderEnv(getenv func(string) string) func(string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	return func(name string) string {
		switch name {
		case "PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL":
			return ""
		default:
			return getenv(name)
		}
	}
}

type routingChatModel struct {
	routes *routeRegistry
	tools  []*schema.ToolInfo
}

func (r *routingChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	clone := *r
	clone.tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

func (r *routingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	prepared, err := r.prepareRoute(ctx, opts)
	if err != nil {
		return nil, err
	}
	chatModel := prepared.published.client
	if len(r.tools) > 0 {
		if toolModel, ok := chatModel.(model.ToolCallingChatModel); ok {
			chatModel, err = toolModel.WithTools(r.tools)
			if err != nil {
				return nil, fmt.Errorf("bind routed tools: %w", err)
			}
		}
	}
	transportInput, proof := r.routes.prepareTransport(
		ctx,
		prepared,
		chatModel,
		input,
		"generate",
	)
	var output *schema.Message
	if agentic, ok := chatModel.(*agenticChatModel); ok {
		output, err = agentic.generateTrusted(
			ctx,
			transportInput,
			proof,
			prepared.opts...,
		)
	} else {
		output, err = chatModel.Generate(ctx, transportInput, prepared.opts...)
	}
	if err == nil && output != nil &&
		prepared.published.provider == ProviderAgenticOpenAI {
		providerorigin.PublishDispatch(ctx, prepared.origin())
	}
	return output, err
}

func (r *routingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	prepared, err := r.prepareRoute(ctx, opts)
	if err != nil {
		return nil, err
	}
	chatModel := prepared.published.client
	if len(r.tools) > 0 {
		if toolModel, ok := chatModel.(model.ToolCallingChatModel); ok {
			chatModel, err = toolModel.WithTools(r.tools)
			if err != nil {
				return nil, fmt.Errorf("bind routed tools: %w", err)
			}
		}
	}
	transportInput, proof := r.routes.prepareTransport(
		ctx,
		prepared,
		chatModel,
		input,
		"stream",
	)
	var stream *schema.StreamReader[*schema.Message]
	if agentic, ok := chatModel.(*agenticChatModel); ok {
		stream, err = agentic.streamTrusted(
			ctx,
			transportInput,
			proof,
			prepared.opts...,
		)
	} else {
		stream, err = chatModel.Stream(ctx, transportInput, prepared.opts...)
	}
	if err == nil && prepared.published.provider == ProviderAgenticOpenAI {
		providerorigin.PublishDispatch(ctx, prepared.origin())
	}
	return stream, err
}

type preparedRoute struct {
	published *publishedRoute
	apiModel  string
	opts      []model.Option
}

func (p *preparedRoute) origin() providerorigin.Origin {
	if p == nil || p.published == nil {
		return providerorigin.Origin{}
	}
	return providerorigin.Origin{
		Version:             providerorigin.OriginVersion,
		Provider:            string(p.published.provider),
		AccountID:           p.published.accountID,
		APIFamily:           p.published.apiFamily,
		APIModel:            p.apiModel,
		RouteIdentityDigest: p.published.routeDigest,
		CredentialOriginID:  p.published.credentialOriginID,
		RoutePublication:    p.published.publication,
	}
}

func (r *routingChatModel) prepareRoute(ctx context.Context, opts []model.Option) (*preparedRoute, error) {
	common := model.GetCommonOptions(nil, opts...)
	modelSpec := r.routes.main.Config.Model
	if common.Model != nil && strings.TrimSpace(*common.Model) != "" {
		modelSpec = *common.Model
	}
	var config ResolvedConfig
	var published *publishedRoute
	for {
		var err error
		config, published, err = r.routes.routePublished(ctx, modelSpec)
		if errors.Is(err, errRouteResolutionSuperseded) && ctx.Err() == nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	return &preparedRoute{
		published: published,
		apiModel:  config.Model,
		opts:      replaceModelOption(opts, config.Model),
	}, nil
}

type routeProof struct {
	client      *agenticChatModel
	publication uint64
	allowed     map[int]struct{}
}

func (r *routeRegistry) prepareTransport(
	ctx context.Context,
	prepared *preparedRoute,
	chatModel model.BaseChatModel,
	input []*schema.Message,
	path string,
) ([]*schema.Message, *routeProof) {
	if r == nil || prepared == nil || prepared.published == nil {
		return stripProviderPrivateState(input), nil
	}
	if prepared.published.provider != ProviderAgenticOpenAI {
		return input, nil
	}
	if r.beforeProof != nil {
		r.beforeProof()
	}
	r.mu.Lock()
	current := r.published[prepared.published.identity]
	currentAccount := r.accountPublications[prepared.published.accountID]
	currentPublication := uint64(0)
	if current != nil {
		currentPublication = current.publication
	}
	publicationCurrent := current == prepared.published &&
		currentAccount == prepared.published &&
		currentPublication == prepared.published.publication
	allowed := make(map[int]struct{})
	transport := make([]*schema.Message, len(input))
	copy(transport, input)
	for index, message := range input {
		if !assistantHasPrivateState(message) {
			continue
		}
		resolution := providerorigin.ResolveBinding(ctx, message)
		allow, reason := evaluateOrigin(
			resolution,
			prepared.origin(),
			publicationCurrent,
		)
		if allow && prepared.published.provider == ProviderAgenticOpenAI {
			allowed[index] = struct{}{}
			continue
		}
		transport[index] = stripOneProviderPrivateMessage(message)
		r.recordOriginDiagnosticLocked(path, reason)
	}
	agentic, isAgentic := chatModel.(*agenticChatModel)
	if !publicationCurrent || !isAgentic {
		r.mu.Unlock()
		return transport, nil
	}
	proof := &routeProof{
		client:      agentic,
		publication: currentPublication,
		allowed:     allowed,
	}
	r.mu.Unlock()
	return transport, proof
}

func evaluateOrigin(
	resolution providerorigin.BindingResolution,
	current providerorigin.Origin,
	publicationCurrent bool,
) (bool, providerorigin.Reason) {
	switch resolution.State {
	case providerorigin.BindingAbsent:
		return false, providerorigin.ReasonAbsent
	case providerorigin.BindingLegacyUnverified:
		return false, providerorigin.ReasonLegacyUnverified
	case providerorigin.BindingRecoveryMismatch:
		return false, providerorigin.ReasonRecoveryMismatch
	case providerorigin.BindingVerified:
	default:
		return false, providerorigin.ReasonRecoveryMismatch
	}
	history := resolution.Origin
	if history.Version != providerorigin.OriginVersion {
		return false, providerorigin.ReasonLegacyUnverified
	}
	if history.Provider != current.Provider {
		return false, providerorigin.ReasonProviderMismatch
	}
	if history.AccountID != current.AccountID {
		return false, providerorigin.ReasonAccountMismatch
	}
	if history.APIFamily != current.APIFamily {
		return false, providerorigin.ReasonAPIFamilyMismatch
	}
	if history.APIModel != current.APIModel {
		return false, providerorigin.ReasonAPIModelMismatch
	}
	if history.CredentialOriginID == "" ||
		current.CredentialOriginID == "" ||
		history.CredentialOriginID != current.CredentialOriginID {
		return false, providerorigin.ReasonCredentialMismatch
	}
	if history.RouteIdentityDigest != current.RouteIdentityDigest ||
		current.RoutePublication == 0 ||
		!publicationCurrent {
		return false, providerorigin.ReasonRouteStale
	}
	return true, providerorigin.ReasonExact
}

func (r *routeRegistry) recordOriginDiagnosticLocked(
	path string,
	reason providerorigin.Reason,
) {
	if reason == providerorigin.ReasonExact {
		return
	}
	if r.diagnostics == nil {
		r.diagnostics = make(map[originDiagnosticKey]uint32)
	}
	key := originDiagnosticKey{path: path, reason: reason}
	if r.diagnostics[key] < ^uint32(0) {
		r.diagnostics[key]++
	}
}

func assistantHasPrivateState(message *schema.Message) bool {
	if message == nil || message.Role != schema.Assistant {
		return false
	}
	if message.ReasoningContent != "" {
		return true
	}
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeReasoning {
			return true
		}
	}
	return false
}

func stripProviderPrivateState(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, len(messages))
	for index, message := range messages {
		if assistantHasPrivateState(message) {
			result[index] = stripOneProviderPrivateMessage(message)
		} else {
			result[index] = message
		}
	}
	return result
}

func stripOneProviderPrivateMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	cloned := *message
	cloned.ReasoningContent = ""
	cloned.Extra = nil
	cloned.AssistantGenMultiContent = make(
		[]schema.MessageOutputPart,
		0,
		len(message.AssistantGenMultiContent),
	)
	for _, part := range message.AssistantGenMultiContent {
		if part.Type != schema.ChatMessagePartTypeReasoning {
			cloned.AssistantGenMultiContent = append(
				cloned.AssistantGenMultiContent,
				part,
			)
		}
	}
	return &cloned
}

func replaceModelOption(opts []model.Option, modelName string) []model.Option {
	result := make([]model.Option, 0, len(opts)+1)
	for _, opt := range opts {
		if model.GetCommonOptions(nil, opt).Model != nil {
			continue
		}
		result = append(result, opt)
	}
	return append(result, model.WithModel(modelName))
}

func newBaseChatModel(ctx context.Context, config Config) (model.BaseChatModel, error) {
	agenticModel, err := newAgenticModel(ctx, config)
	if err != nil {
		return nil, err
	}
	return wrapAgenticModel(agenticModel), nil
}

func defaultPreflightCheck(ctx context.Context, config ResolvedConfig, timeout time.Duration) error {
	result := engineconfig.CheckConnectivity(providerID(config.Config.Provider), config.Config.APIKey, &engineconfig.ConnectivityCheckOptions{
		Context: ctx,
		Timeout: timeout,
		BaseURL: config.Config.BaseURL,
	})
	if result.IsOK() {
		return nil
	}
	return fmt.Errorf("provider preflight failed for %s (%s): %s", config.Config.Provider, result.Status, result.Message)
}
