package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	enginemodel "github.com/abietic/yhc/engine/model"
)

type (
	AccountID string
	ProfileID string
	ModelRole string
)

const (
	RoleMain    ModelRole = "main"
	RoleExplore ModelRole = "explore"
	RolePlan    ModelRole = "plan"
	RoleGeneral ModelRole = "general"
	RoleSummary ModelRole = "summary"
)

var (
	portfolioIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	envNamePattern         = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	ErrLegacyRouteRequired = errors.New("legacy provider route compilation required")
)

// ProviderAccountConfig is a user-owned non-secret provider route definition.
type ProviderAccountConfig struct {
	Enabled  *bool             `json:"enabled,omitempty"`
	Provider string            `json:"provider"`
	BaseURL  string            `json:"base_url,omitempty"`
	Auth     AccountAuthConfig `json:"auth"`
}

// AccountAuthConfig points to an environment variable, named credential, or
// the explicit provider-default resolver. It never contains a credential.
type AccountAuthConfig struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// ModelProfileConfig binds one presentation profile to an account and exact
// provider-local model.
type ModelProfileConfig struct {
	Enabled           *bool                         `json:"enabled,omitempty"`
	Account           string                        `json:"account"`
	APIModel          string                        `json:"api_model"`
	DisplayName       string                        `json:"display_name,omitempty"`
	ProjectSelectable bool                          `json:"project_selectable,omitempty"`
	Metadata          enginemodel.MetadataOverrides `json:"metadata,omitempty"`
	Reasoning         ReasoningDefaultsConfig       `json:"reasoning,omitempty"`
}

type ReasoningDefaultsConfig struct {
	DefaultEffort string `json:"default_effort,omitempty"`
}

type FailoverPolicyConfig struct {
	Alternates       []string `json:"alternates"`
	On               []string `json:"on"`
	MaxSwitches      int      `json:"max_switches"`
	MaxProviderCalls int      `json:"max_provider_calls"`
	MaxElapsedMS     int64    `json:"max_elapsed_ms"`
}

// PortfolioDiagnostic is stable, source-labelled, and contains no setting
// values. Keys and paths are authority evidence, not secret material.
type PortfolioDiagnostic struct {
	Code    string   `json:"code"`
	Level   string   `json:"level"`
	Source  string   `json:"source"`
	Path    string   `json:"path,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Message string   `json:"message"`
}

// ConfigSources preserves user/project authority until portfolio compilation.
type ConfigSources struct {
	User                 *Config
	Project              *Config
	Effective            *Config
	UserPath             string
	ProjectPath          string
	ProjectForbiddenKeys []string
	Diagnostics          []PortfolioDiagnostic
	SandboxDiagnostics   []SandboxDiagnostic
}

type ResolvedAccount struct {
	ID            AccountID `json:"id"`
	Provider      string    `json:"provider"`
	Endpoint      string    `json:"endpoint"`
	AuthKind      string    `json:"auth_kind"`
	AuthReference string    `json:"auth_reference"`
	AdapterDigest string    `json:"adapter_digest"`
}

type ReasoningDefaults struct {
	DefaultEffort string `json:"default_effort,omitempty"`
}

type ResolvedProfile struct {
	ID                ProfileID                          `json:"id"`
	Account           AccountID                          `json:"account"`
	APIModel          string                             `json:"api_model"`
	DisplayName       string                             `json:"display_name,omitempty"`
	ProjectSelectable bool                               `json:"project_selectable"`
	Metadata          enginemodel.EffectiveModelMetadata `json:"metadata"`
	Reasoning         ReasoningDefaults                  `json:"reasoning"`
}

type ResolvedFailoverPolicy struct {
	Alternates       []ProfileID `json:"alternates"`
	On               []string    `json:"on"`
	MaxSwitches      int         `json:"max_switches"`
	MaxProviderCalls int         `json:"max_provider_calls"`
	MaxElapsedMS     int64       `json:"max_elapsed_ms"`
}

// PortfolioSnapshot is immutable by convention and contains only canonical,
// non-secret definitions. Callers receive detached copies.
type PortfolioSnapshot struct {
	Revision string                        `json:"revision"`
	Default  ProfileID                     `json:"default"`
	Accounts map[AccountID]ResolvedAccount `json:"accounts"`
	Profiles map[ProfileID]ResolvedProfile `json:"profiles"`
	Roles    map[ModelRole]ProfileID       `json:"roles"`
	// ExplicitRoles retains only user-configured optional role bindings.
	// Roles remains the effective compatibility projection.
	ExplicitRoles   map[ModelRole]ProfileID              `json:"explicit_roles"`
	Failover        map[ModelRole]ResolvedFailoverPolicy `json:"failover"`
	SelectionSource string                               `json:"selection_source"`
	Diagnostics     []PortfolioDiagnostic                `json:"-"`
}

// LegacyPortfolioCompiler lowers the existing resolver through the same safe
// snapshot boundary when no named profile is selected.
type LegacyPortfolioCompiler func() (*PortfolioSnapshot, error)

type PortfolioCompileInput struct {
	Sources                  *ConfigSources
	ExplicitModelProfile     string
	ExplicitLegacyFields     []string
	LegacyFallbackConfigured bool
	Getenv                   func(string) string
	LegacyCompiler           LegacyPortfolioCompiler
}

// CompilePortfolio enforces source authority, validates the complete user
// inventory, selects one startup profile, and emits a deterministic snapshot.
func CompilePortfolio(input PortfolioCompileInput) (*PortfolioSnapshot, error) {
	if input.Sources == nil {
		return nil, fmt.Errorf("portfolio config sources are required")
	}
	sources := input.Sources
	user := sources.User
	project := sources.Project
	if user == nil {
		user = &Config{}
	}
	if project == nil {
		project = &Config{}
	}
	diagnostics := appendDetachedDiagnostics(nil, sources.Diagnostics)
	if forbidden := projectForbiddenKeysFromConfig(project); len(forbidden) > 0 {
		projectCopy := *project
		projectCopy.ModelProfile = ""
		projectCopy.ProviderAccounts = nil
		projectCopy.ModelProfiles = nil
		projectCopy.ModelRoles = nil
		projectCopy.FailoverPolicies = nil
		project = &projectCopy
		diagnostics = append(diagnostics, PortfolioDiagnostic{
			Code:    "forbidden_project_portfolio_keys",
			Level:   "warning",
			Source:  "project",
			Path:    sources.ProjectPath,
			Keys:    forbidden,
			Message: "project portfolio fields were ignored as one authority subset",
		})
	}
	getenv := input.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	explicitProfile := strings.TrimSpace(input.ExplicitModelProfile)
	explicitLegacy := normalizedFieldNames(input.ExplicitLegacyFields)
	if explicitProfile != "" && len(explicitLegacy) > 0 {
		return nil, fmt.Errorf("--model-profile cannot be combined with %s", strings.Join(explicitLegacy, ", "))
	}
	envProfile := strings.TrimSpace(getenv("PROV_MODEL_PROFILE"))
	envLegacy := presentEnvironmentFields(getenv)
	if envProfile != "" && len(envLegacy) > 0 {
		return nil, fmt.Errorf("PROV_MODEL_PROFILE cannot be combined with %s", strings.Join(envLegacy, ", "))
	}
	if user.ModelProfile != "" {
		if fields := legacyFieldsInConfig(user); len(fields) > 0 {
			return nil, fmt.Errorf("user settings model_profile cannot be combined with %s", strings.Join(fields, ", "))
		}
	}
	if project.ModelProfile != "" {
		if fields := legacyFieldsInConfig(project); len(fields) > 0 {
			return nil, fmt.Errorf("project settings model_profile cannot be combined with %s", strings.Join(fields, ", "))
		}
	}

	selection := ""
	selectionSource := ""
	switch {
	case explicitProfile != "":
		selection, selectionSource = explicitProfile, "explicit"
	case envProfile != "":
		selection, selectionSource = envProfile, "env:PROV_MODEL_PROFILE"
	case project.ModelProfile != "":
		selection, selectionSource = project.ModelProfile, "project"
	case user.ModelProfile != "":
		selection, selectionSource = user.ModelProfile, "user"
	}

	accounts, err := compileAccounts(user.ProviderAccounts)
	if err != nil {
		return nil, err
	}
	profiles, err := compileProfiles(user.ModelProfiles, accounts, user.ModelAliases)
	if err != nil {
		return nil, err
	}
	if selection == "" {
		if len(user.ModelRoles) > 0 || len(user.FailoverPolicies) > 0 {
			return nil, fmt.Errorf("model_roles and failover_policies require an effective named model_profile")
		}
		if input.LegacyCompiler == nil {
			return nil, ErrLegacyRouteRequired
		}
		snapshot, legacyErr := input.LegacyCompiler()
		if legacyErr != nil {
			return nil, legacyErr
		}
		if snapshot.Accounts == nil {
			snapshot.Accounts = make(map[AccountID]ResolvedAccount)
		}
		for id, account := range accounts {
			snapshot.Accounts[id] = account
		}
		if snapshot.Profiles == nil {
			snapshot.Profiles = make(map[ProfileID]ResolvedProfile)
		}
		for id, profile := range profiles {
			snapshot.Profiles[id] = profile
		}
		if snapshot.ExplicitRoles == nil {
			snapshot.ExplicitRoles = make(map[ModelRole]ProfileID)
		}
		snapshot.Diagnostics = appendDetachedDiagnostics(snapshot.Diagnostics, diagnostics)
		if project.APIBaseURL != "" {
			snapshot.Diagnostics = append(snapshot.Diagnostics, legacyProjectRouteDiagnostic(sources.ProjectPath))
		}
		if revisionErr := assignPortfolioRevision(snapshot); revisionErr != nil {
			return nil, revisionErr
		}
		return clonePortfolioSnapshot(snapshot), nil
	}

	selectedID, err := normalizePortfolioID("model profile", selection)
	if err != nil {
		return nil, err
	}
	selected, ok := profiles[ProfileID(selectedID)]
	if !ok {
		return nil, fmt.Errorf("selected model profile %q is not defined or is disabled", selectedID)
	}
	if selectionSource == "project" && !selected.ProjectSelectable {
		return nil, fmt.Errorf("project settings cannot select model profile %q because project_selectable is false", selectedID)
	}
	if input.LegacyFallbackConfigured && len(user.FailoverPolicies) > 0 {
		return nil, fmt.Errorf("failover_policies cannot be combined with legacy fallback_model")
	}
	roles, explicitRoles, err := compileRoles(
		user.ModelRoles,
		profiles,
		ProfileID(selectedID),
	)
	if err != nil {
		return nil, err
	}
	failover, err := compileFailover(user.FailoverPolicies, profiles, roles)
	if err != nil {
		return nil, err
	}

	snapshot := &PortfolioSnapshot{
		Default:         ProfileID(selectedID),
		Accounts:        accounts,
		Profiles:        profiles,
		Roles:           roles,
		ExplicitRoles:   explicitRoles,
		Failover:        failover,
		SelectionSource: selectionSource,
		Diagnostics:     diagnostics,
	}
	if err := assignPortfolioRevision(snapshot); err != nil {
		return nil, err
	}
	return clonePortfolioSnapshot(snapshot), nil
}

func compileAccounts(raw map[string]ProviderAccountConfig) (map[AccountID]ResolvedAccount, error) {
	result := make(map[AccountID]ResolvedAccount)
	seen := make(map[AccountID]struct{}, len(raw))
	for _, rawID := range sortedStringKeys(raw) {
		account := raw[rawID]
		id, err := normalizePortfolioID("provider account", rawID)
		if err != nil {
			return nil, err
		}
		accountID := AccountID(id)
		if _, exists := seen[accountID]; exists {
			return nil, fmt.Errorf("provider account ID collision after normalization: %q", id)
		}
		seen[accountID] = struct{}{}
		if account.Enabled != nil && !*account.Enabled {
			continue
		}
		providerID, err := normalizePortfolioProvider(account.Provider)
		if err != nil {
			return nil, fmt.Errorf("provider account %q: %w", id, err)
		}
		providerEnv := enginemodel.GetProviderEnvConfig(providerID)
		endpoint := strings.TrimSpace(account.BaseURL)
		if endpoint == "" {
			endpoint = providerEnv.DefaultBaseURL
		}
		endpoint, err = CanonicalProviderEndpoint(endpoint)
		if err != nil {
			return nil, fmt.Errorf("provider account %q base_url: %w", id, err)
		}
		authKind, authReference, err := validateAccountAuth(account.Auth)
		if err != nil {
			return nil, fmt.Errorf("provider account %q auth: %w", id, err)
		}
		result[accountID] = ResolvedAccount{
			ID:            accountID,
			Provider:      string(providerID),
			Endpoint:      endpoint,
			AuthKind:      authKind,
			AuthReference: authReference,
			AdapterDigest: string(providerID) + ":v1",
		}
	}
	return result, nil
}

func compileProfiles(
	raw map[string]ModelProfileConfig,
	accounts map[AccountID]ResolvedAccount,
	aliases map[string]string,
) (map[ProfileID]ResolvedProfile, error) {
	aliasIDs := make(map[string]struct{}, len(aliases))
	for alias := range aliases {
		aliasIDs[strings.ToLower(strings.TrimSpace(alias))] = struct{}{}
	}
	result := make(map[ProfileID]ResolvedProfile)
	seen := make(map[ProfileID]struct{}, len(raw))
	for _, rawID := range sortedStringKeys(raw) {
		profile := raw[rawID]
		id, err := normalizePortfolioID("model profile", rawID)
		if err != nil {
			return nil, err
		}
		profileID := ProfileID(id)
		if _, exists := seen[profileID]; exists {
			return nil, fmt.Errorf("model profile ID collision after normalization: %q", id)
		}
		seen[profileID] = struct{}{}
		if _, collides := aliasIDs[id]; collides {
			return nil, fmt.Errorf("model profile %q collides with a legacy model alias", id)
		}
		if profile.Enabled != nil && !*profile.Enabled {
			continue
		}
		accountIDValue, err := normalizePortfolioID("profile account", profile.Account)
		if err != nil {
			return nil, fmt.Errorf("model profile %q: %w", id, err)
		}
		accountID := AccountID(accountIDValue)
		account, ok := accounts[accountID]
		if !ok {
			return nil, fmt.Errorf("model profile %q references missing or disabled account %q", id, accountID)
		}
		apiModel := strings.TrimSpace(profile.APIModel)
		if apiModel == "" {
			return nil, fmt.Errorf("model profile %q api_model must not be empty", id)
		}
		metadata, err := enginemodel.ResolvePortfolioMetadataForProvider(
			account.Provider,
			apiModel,
			profile.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("model profile %q metadata: %w", id, err)
		}
		for _, supported := range metadata.SupportedReasoningEfforts.Value {
			if !enginemodel.SupportsAdapterReasoningEffort(account.Provider, supported) {
				return nil, fmt.Errorf(
					"model profile %q metadata reasoning effort %q cannot be lowered by provider %q",
					id,
					supported,
					account.Provider,
				)
			}
		}
		effort, err := enginemodel.ValidateReasoningEffort(profile.Reasoning.DefaultEffort)
		if err != nil {
			return nil, fmt.Errorf("model profile %q reasoning: %w", id, err)
		}
		if effort != "" && metadata.SupportedReasoningEfforts.Source != "unknown" {
			if !containsString(metadata.SupportedReasoningEfforts.Value, effort) {
				return nil, fmt.Errorf("model profile %q default reasoning effort %q is not supported by its metadata", id, effort)
			}
		}
		if effort != "" && !enginemodel.SupportsAdapterReasoningEffort(account.Provider, effort) {
			return nil, fmt.Errorf(
				"model profile %q default reasoning effort %q cannot be lowered by provider %q",
				id,
				effort,
				account.Provider,
			)
		}
		result[profileID] = ResolvedProfile{
			ID:                profileID,
			Account:           accountID,
			APIModel:          apiModel,
			DisplayName:       strings.TrimSpace(profile.DisplayName),
			ProjectSelectable: profile.ProjectSelectable,
			Metadata:          metadata,
			Reasoning:         ReasoningDefaults{DefaultEffort: effort},
		}
	}
	return result, nil
}

func compileRoles(
	raw map[string]string,
	profiles map[ProfileID]ResolvedProfile,
	selected ProfileID,
) (map[ModelRole]ProfileID, map[ModelRole]ProfileID, error) {
	result := map[ModelRole]ProfileID{
		RoleMain: selected, RoleExplore: selected, RolePlan: selected, RoleGeneral: selected, RoleSummary: selected,
	}
	explicit := make(map[ModelRole]ProfileID, len(raw))
	seen := make(map[ModelRole]struct{}, len(raw))
	for _, rawRole := range sortedStringKeys(raw) {
		role := ModelRole(strings.ToLower(strings.TrimSpace(rawRole)))
		if _, duplicate := seen[role]; duplicate {
			return nil, nil, fmt.Errorf("model role collision after normalization: %q", role)
		}
		seen[role] = struct{}{}
		if role == RoleMain {
			return nil, nil, fmt.Errorf("model_roles.main is forbidden; model_profile owns the main binding")
		}
		if !isOptionalModelRole(role) {
			return nil, nil, fmt.Errorf("unknown model role %q", rawRole)
		}
		profileIDValue, err := normalizePortfolioID("role profile", raw[rawRole])
		if err != nil {
			return nil, nil, fmt.Errorf("model role %q: %w", role, err)
		}
		profileID := ProfileID(profileIDValue)
		if _, ok := profiles[profileID]; !ok {
			return nil, nil, fmt.Errorf("model role %q references missing or disabled profile %q", role, profileID)
		}
		result[role] = profileID
		explicit[role] = profileID
	}
	return result, explicit, nil
}

func compileFailover(
	raw map[string]FailoverPolicyConfig,
	profiles map[ProfileID]ResolvedProfile,
	roles map[ModelRole]ProfileID,
) (map[ModelRole]ResolvedFailoverPolicy, error) {
	result := make(map[ModelRole]ResolvedFailoverPolicy)
	seenRoles := make(map[ModelRole]struct{}, len(raw))
	for _, rawRole := range sortedStringKeys(raw) {
		role := ModelRole(strings.ToLower(strings.TrimSpace(rawRole)))
		if _, duplicate := seenRoles[role]; duplicate {
			return nil, fmt.Errorf("failover role collision after normalization: %q", role)
		}
		seenRoles[role] = struct{}{}
		if role != RoleMain && !isOptionalModelRole(role) {
			return nil, fmt.Errorf("unknown failover role %q", rawRole)
		}
		policy := raw[rawRole]
		if policy.MaxSwitches < 0 {
			return nil, fmt.Errorf("failover policy %q max_switches must be non-negative", role)
		}
		if policy.MaxProviderCalls <= 0 || policy.MaxProviderCalls < policy.MaxSwitches+1 {
			return nil, fmt.Errorf("failover policy %q max_provider_calls must be positive and at least max_switches + 1", role)
		}
		if policy.MaxElapsedMS <= 0 {
			return nil, fmt.Errorf("failover policy %q max_elapsed_ms must be positive", role)
		}
		if len(policy.On) == 0 {
			return nil, fmt.Errorf("failover policy %q on must contain overloaded", role)
		}
		on := make([]string, 0, len(policy.On))
		onSeen := make(map[string]struct{}, len(policy.On))
		for _, rawClass := range policy.On {
			class := strings.ToLower(strings.TrimSpace(rawClass))
			if class != "overloaded" {
				return nil, fmt.Errorf("failover policy %q has unsupported error class %q", role, rawClass)
			}
			if _, duplicate := onSeen[class]; duplicate {
				return nil, fmt.Errorf("failover policy %q repeats error class %q", role, class)
			}
			onSeen[class] = struct{}{}
			on = append(on, class)
		}
		primary := roles[role]
		alternates := make([]ProfileID, 0, len(policy.Alternates))
		seen := make(map[ProfileID]struct{}, len(policy.Alternates))
		for _, rawProfile := range policy.Alternates {
			profileIDValue, err := normalizePortfolioID("failover profile", rawProfile)
			if err != nil {
				return nil, fmt.Errorf("failover policy %q: %w", role, err)
			}
			profileID := ProfileID(profileIDValue)
			if _, ok := profiles[profileID]; !ok {
				return nil, fmt.Errorf("failover policy %q references missing or disabled profile %q", role, profileID)
			}
			if profileID == primary {
				return nil, fmt.Errorf("failover policy %q repeats its primary profile %q", role, profileID)
			}
			if _, duplicate := seen[profileID]; duplicate {
				return nil, fmt.Errorf("failover policy %q repeats alternate profile %q", role, profileID)
			}
			seen[profileID] = struct{}{}
			alternates = append(alternates, profileID)
		}
		if policy.MaxSwitches > len(alternates) {
			return nil, fmt.Errorf("failover policy %q max_switches exceeds alternate count", role)
		}
		result[role] = ResolvedFailoverPolicy{
			Alternates:       alternates,
			On:               on,
			MaxSwitches:      policy.MaxSwitches,
			MaxProviderCalls: policy.MaxProviderCalls,
			MaxElapsedMS:     policy.MaxElapsedMS,
		}
	}
	return result, nil
}

func validateAccountAuth(auth AccountAuthConfig) (string, string, error) {
	kind := strings.ToLower(strings.TrimSpace(auth.Kind))
	name := strings.TrimSpace(auth.Name)
	switch kind {
	case "env":
		if !envNamePattern.MatchString(name) {
			return "", "", fmt.Errorf("env auth name must be a valid environment variable")
		}
	case "credential":
		normalized, err := normalizePortfolioID("credential reference", name)
		if err != nil {
			return "", "", err
		}
		name = normalized
	case "provider_default":
		if name != "" {
			return "", "", fmt.Errorf("provider_default auth must not set name")
		}
		name = "provider-default"
	default:
		return "", "", fmt.Errorf("unsupported auth kind %q", auth.Kind)
	}
	return kind, name, nil
}

func normalizePortfolioProvider(raw string) (enginemodel.ProviderID, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "anthropic", "claude", "agenticclaude":
		return enginemodel.ProviderAnthropic, nil
	case "openai", "agenticopenai":
		return enginemodel.ProviderOpenAI, nil
	case "google", "gemini", "agenticgemini":
		return enginemodel.ProviderGoogle, nil
	case "deepseek", "agenticdeepseek":
		return enginemodel.ProviderDeepSeek, nil
	case "qwen", "dashscope", "agenticqwen":
		return enginemodel.ProviderQwen, nil
	case "ark", "volcengine", "agenticark":
		return enginemodel.ProviderArk, nil
	default:
		return enginemodel.ProviderUnknown, fmt.Errorf("unsupported provider %q", raw)
	}
}

func normalizePortfolioID(kind, raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if !portfolioIDPattern.MatchString(id) {
		return "", fmt.Errorf("%s ID must match [a-z][a-z0-9._-]{0,63}", kind)
	}
	if strings.HasPrefix(id, "legacy.") {
		return "", fmt.Errorf("%s ID %q uses reserved prefix legacy", kind, id)
	}
	return id, nil
}

// CanonicalProviderEndpoint validates the shared provider route URL form.
func CanonicalProviderEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("absolute HTTP(S) URL required")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("HTTP(S) URL required")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("userinfo, query, and fragment are forbidden")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", fmt.Errorf("host must not be empty")
	}
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	switch {
	case port != "":
		parsed.Host = net.JoinHostPort(hostname, port)
	case strings.Contains(hostname, ":"):
		parsed.Host = "[" + hostname + "]"
	default:
		parsed.Host = hostname
	}
	cleanPath := path.Clean(parsed.Path)
	if cleanPath == "." || cleanPath == "/" {
		cleanPath = ""
	}
	parsed.Path = strings.TrimRight(cleanPath, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func legacyFieldsInConfig(config *Config) []string {
	var fields []string
	if strings.TrimSpace(config.Provider) != "" {
		fields = append(fields, "provider")
	}
	if strings.TrimSpace(config.Model) != "" {
		fields = append(fields, "model")
	}
	if strings.TrimSpace(config.APIBaseURL) != "" {
		fields = append(fields, "api_base_url")
	}
	if strings.TrimSpace(config.FallbackModel) != "" {
		fields = append(fields, "fallback_model")
	}
	return fields
}

func projectForbiddenKeysFromConfig(config *Config) []string {
	var keys []string
	if config.ProviderAccounts != nil {
		keys = append(keys, "provider_accounts")
	}
	if config.ModelProfiles != nil {
		keys = append(keys, "model_profiles")
	}
	if config.ModelRoles != nil {
		keys = append(keys, "model_roles")
	}
	if config.FailoverPolicies != nil {
		keys = append(keys, "failover_policies")
	}
	sort.Strings(keys)
	return keys
}

func presentEnvironmentFields(getenv func(string) string) []string {
	names := []string{"PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL", "PROV_FALLBACK_MODEL"}
	var present []string
	for _, name := range names {
		if strings.TrimSpace(getenv(name)) != "" {
			present = append(present, name)
		}
	}
	return present
}

func normalizedFieldNames(fields []string) []string {
	seen := make(map[string]struct{}, len(fields))
	for _, raw := range fields {
		field := strings.TrimSpace(raw)
		if field != "" {
			seen[field] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for field := range seen {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func isOptionalModelRole(role ModelRole) bool {
	switch role {
	case RoleExplore, RolePlan, RoleGeneral, RoleSummary:
		return true
	default:
		return false
	}
}

func assignPortfolioRevision(snapshot *PortfolioSnapshot) error {
	type revisionView struct {
		Default       ProfileID                            `json:"default"`
		Accounts      map[AccountID]ResolvedAccount        `json:"accounts"`
		Profiles      map[ProfileID]ResolvedProfile        `json:"profiles"`
		Roles         map[ModelRole]ProfileID              `json:"roles"`
		ExplicitRoles map[ModelRole]ProfileID              `json:"explicit_roles"`
		Failover      map[ModelRole]ResolvedFailoverPolicy `json:"failover"`
	}
	encoded, err := json.Marshal(revisionView{
		Default: snapshot.Default, Accounts: snapshot.Accounts, Profiles: snapshot.Profiles,
		Roles: snapshot.Roles, ExplicitRoles: snapshot.ExplicitRoles, Failover: snapshot.Failover,
	})
	if err != nil {
		return fmt.Errorf("encode portfolio revision: %w", err)
	}
	snapshot.Revision = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return nil
}

func clonePortfolioSnapshot(snapshot *PortfolioSnapshot) *PortfolioSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.Accounts = make(map[AccountID]ResolvedAccount, len(snapshot.Accounts))
	for id, account := range snapshot.Accounts {
		clone.Accounts[id] = account
	}
	clone.Profiles = make(map[ProfileID]ResolvedProfile, len(snapshot.Profiles))
	for id, profile := range snapshot.Profiles {
		profile.Metadata.SupportedReasoningEfforts.Value = append([]string(nil), profile.Metadata.SupportedReasoningEfforts.Value...)
		clone.Profiles[id] = profile
	}
	clone.Roles = make(map[ModelRole]ProfileID, len(snapshot.Roles))
	for role, profile := range snapshot.Roles {
		clone.Roles[role] = profile
	}
	clone.ExplicitRoles = make(map[ModelRole]ProfileID, len(snapshot.ExplicitRoles))
	for role, profile := range snapshot.ExplicitRoles {
		clone.ExplicitRoles[role] = profile
	}
	clone.Failover = make(map[ModelRole]ResolvedFailoverPolicy, len(snapshot.Failover))
	for role, policy := range snapshot.Failover {
		policy.Alternates = append([]ProfileID(nil), policy.Alternates...)
		policy.On = append([]string(nil), policy.On...)
		clone.Failover[role] = policy
	}
	clone.Diagnostics = appendDetachedDiagnostics(nil, snapshot.Diagnostics)
	return &clone
}

func appendDetachedDiagnostics(dst, src []PortfolioDiagnostic) []PortfolioDiagnostic {
	for _, diagnostic := range src {
		diagnostic.Keys = append([]string(nil), diagnostic.Keys...)
		dst = append(dst, diagnostic)
	}
	return dst
}

func legacyProjectRouteDiagnostic(projectPath string) PortfolioDiagnostic {
	return PortfolioDiagnostic{
		Code:    "legacy_project_route_authority",
		Level:   "warning",
		Source:  "project",
		Path:    projectPath,
		Message: "project api_base_url is retained only by the legacy compatibility compiler",
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
