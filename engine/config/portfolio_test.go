package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	enginemodel "github.com/abietic/yhc/engine/model"
)

const portfolioSecretSentinel = "portfolio-secret-sentinel-21c095"

func TestCompilePortfolioNamedProfile(t *testing.T) {
	sources := validPortfolioSources()
	snapshot, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Default != "primary" || snapshot.SelectionSource != "user" {
		t.Fatalf("selection = %q/%q", snapshot.Default, snapshot.SelectionSource)
	}
	account := snapshot.Accounts["openai-main"]
	if account.Provider != "openai" || account.Endpoint != "https://api.example/v1" {
		t.Fatalf("canonical account = %#v", account)
	}
	if account.AuthKind != "env" || account.AuthReference != "OPENAI_PRIMARY" {
		t.Fatalf("account auth = %#v", account)
	}
	profile := snapshot.Profiles["primary"]
	if profile.Metadata.ContextWindowTokens.Source != "profile-override" ||
		profile.Metadata.Tools.Source != "built-in" {
		t.Fatalf("metadata provenance = %#v", profile.Metadata)
	}
	for _, role := range []ModelRole{RoleMain, RoleExplore, RolePlan, RoleGeneral, RoleSummary} {
		if snapshot.Roles[role] != "primary" {
			t.Fatalf("role %q = %q", role, snapshot.Roles[role])
		}
	}
	if len(snapshot.ExplicitRoles) != 0 {
		t.Fatalf("absent optional roles became explicit: %#v", snapshot.ExplicitRoles)
	}
	if len(snapshot.Revision) != 64 {
		t.Fatalf("revision = %q", snapshot.Revision)
	}

	again, err := CompilePortfolio(PortfolioCompileInput{
		Sources: validPortfolioSources(),
		Getenv: func(name string) string {
			if name == "OPENAI_PRIMARY" {
				return portfolioSecretSentinel
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != snapshot.Revision {
		t.Fatalf("secret-independent revisions differ: %q != %q", again.Revision, snapshot.Revision)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	secretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(portfolioSecretSentinel)))
	for _, forbidden := range []string{portfolioSecretSentinel, secretHash, "api_key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestCompilePortfolioAdmitsExactDeepSeekV4ReasoningCapabilities(t *testing.T) {
	sources := validPortfolioSources()
	account := sources.User.ProviderAccounts["openai-main"]
	account.Provider = "deepseek"
	sources.User.ProviderAccounts["openai-main"] = account
	profile := sources.User.ModelProfiles["primary"]
	profile.APIModel = "deepseek-v4-flash"
	profile.Reasoning.DefaultEffort = "max"
	sources.User.ModelProfiles["primary"] = profile

	snapshot, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved := snapshot.Profiles["primary"]
	if got := strings.Join(
		resolved.Metadata.SupportedReasoningEfforts.Value,
		",",
	); got != "none,high,max" || resolved.Reasoning.DefaultEffort != "max" {
		t.Fatalf("compiled DeepSeek reasoning = %#v", resolved)
	}

	profile.Reasoning.DefaultEffort = "low"
	sources.User.ModelProfiles["primary"] = profile
	if _, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	}); err == nil || !strings.Contains(err.Error(), `default reasoning effort "low"`) {
		t.Fatalf("DeepSeek compatibility alias was admitted: %v", err)
	}

	disabled := false
	profile.Reasoning.DefaultEffort = ""
	profile.Metadata.Capabilities.Thinking = &disabled
	sources.User.ModelProfiles["primary"] = profile
	snapshot, err = CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if efforts := snapshot.Profiles["primary"].Metadata.SupportedReasoningEfforts; efforts.Source != "profile-override" || len(efforts.Value) != 0 {
		t.Fatalf("thinking=false retained efforts: %#v", efforts)
	}

	profile.Metadata.SupportedReasoningEfforts = []string{"high"}
	sources.User.ModelProfiles["primary"] = profile
	if _, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	}); err == nil || !strings.Contains(err.Error(), "thinking=false") {
		t.Fatalf("thinking=false accepted explicit efforts: %v", err)
	}
}

func TestLoadConfigSourcesRejectsProjectPortfolioAsWholeBeforeDecode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	userJSON := `{
		"model_profile":"primary",
		"provider_accounts":{"openai-main":{
			"provider":"openai",
			"auth":{"kind":"env","name":"OPENAI_PRIMARY"}
		}},
		"model_profiles":{"primary":{
			"account":"openai-main",
			"api_model":"gpt-4o",
			"project_selectable":true
		}}
	}`
	if err := os.WriteFile(UserConfigPath(), []byte(userJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := ProjectConfigPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
		t.Fatal(err)
	}
	projectJSON := `{
		"theme":"light",
		"model_profile":"attacker",
		"provider_accounts":{"attacker":{"api_key":"` + portfolioSecretSentinel + `"}}
	}`
	if err := os.WriteFile(projectPath, []byte(projectJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	sources, err := LoadConfigSources(projectDir)
	if err != nil {
		t.Fatalf("forbidden project definition value was decoded: %v", err)
	}
	if sources.Project.ModelProfile != "" || len(sources.Project.ProviderAccounts) != 0 {
		t.Fatalf("project portfolio subset was partially consumed: %#v", sources.Project)
	}
	if sources.Effective.Theme != "light" {
		t.Fatalf("unrelated project setting was not preserved: %#v", sources.Effective)
	}
	if len(sources.Diagnostics) != 1 ||
		!equalStrings(sources.Diagnostics[0].Keys, []string{"provider_accounts"}) {
		t.Fatalf("project authority diagnostic = %#v", sources.Diagnostics)
	}
	snapshot, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Default != "primary" {
		t.Fatalf("ignored project selection changed default: %q", snapshot.Default)
	}
	diagnosticJSON, err := json.Marshal(snapshot.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diagnosticJSON), portfolioSecretSentinel) {
		t.Fatalf("project diagnostic exposed forbidden value: %s", diagnosticJSON)
	}
}

func TestLoadUserConfigRejectsUnknownSecretFieldsWithoutEchoingValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := `{"provider_accounts":{"primary":{
		"provider":"openai",
		"api_key":"` + portfolioSecretSentinel + `",
		"auth":{"kind":"env","name":"OPENAI_API_KEY"}
	}}}`
	if err := os.WriteFile(UserConfigPath(), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadUserConfig()
	if err == nil {
		t.Fatal("plaintext api_key field should fail strict portfolio decoding")
	}
	if strings.Contains(err.Error(), portfolioSecretSentinel) {
		t.Fatalf("strict decode error exposed secret value: %v", err)
	}
}

func TestCompilePortfolioSelectionAuthorityAndMixedMode(t *testing.T) {
	t.Run("compiler defensively ignores forbidden project subset", func(t *testing.T) {
		sources := validPortfolioSources()
		sources.Project = &Config{
			ModelProfile: "attacker",
			ProviderAccounts: map[string]ProviderAccountConfig{
				"attacker": {},
			},
		}
		snapshot, err := CompilePortfolio(PortfolioCompileInput{
			Sources: sources,
			Getenv:  func(string) string { return "" },
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Default != "primary" ||
			len(snapshot.Diagnostics) != 1 ||
			snapshot.Diagnostics[0].Code != "forbidden_project_portfolio_keys" {
			t.Fatalf("defensive project authority result = %#v", snapshot)
		}
	})

	t.Run("project requires opt in", func(t *testing.T) {
		sources := validPortfolioSources()
		sources.User.ModelProfile = ""
		sources.Project = &Config{ModelProfile: "primary"}
		profile := sources.User.ModelProfiles["primary"]
		profile.ProjectSelectable = false
		sources.User.ModelProfiles["primary"] = profile
		if _, err := CompilePortfolio(PortfolioCompileInput{
			Sources: sources,
			Getenv:  func(string) string { return "" },
		}); err == nil || !strings.Contains(err.Error(), "project_selectable") {
			t.Fatalf("unauthorized project selection error = %v", err)
		}
	})

	t.Run("explicit profile conflicts with legacy flags", func(t *testing.T) {
		if _, err := CompilePortfolio(PortfolioCompileInput{
			Sources:              validPortfolioSources(),
			ExplicitModelProfile: "primary",
			ExplicitLegacyFields: []string{"--api-key"},
			Getenv:               func(string) string { return "" },
		}); err == nil || !strings.Contains(err.Error(), "--api-key") {
			t.Fatalf("mixed explicit selection error = %v", err)
		}
	})

	t.Run("environment profile conflicts with legacy environment", func(t *testing.T) {
		if _, err := CompilePortfolio(PortfolioCompileInput{
			Sources: validPortfolioSources(),
			Getenv: func(name string) string {
				switch name {
				case "PROV_MODEL_PROFILE":
					return "primary"
				case "PROV_API_KEY":
					return portfolioSecretSentinel
				default:
					return ""
				}
			},
		}); err == nil || !strings.Contains(err.Error(), "PROV_API_KEY") ||
			strings.Contains(err.Error(), portfolioSecretSentinel) {
			t.Fatalf("mixed environment selection error = %v", err)
		}
	})

	t.Run("single settings layer cannot mix", func(t *testing.T) {
		sources := validPortfolioSources()
		sources.User.Provider = "openai"
		if _, err := CompilePortfolio(PortfolioCompileInput{
			Sources: sources,
			Getenv:  func(string) string { return "" },
		}); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Fatalf("mixed user settings error = %v", err)
		}
	})
}

func TestCompilePortfolioLegacyCallbackAndWarning(t *testing.T) {
	sources := &ConfigSources{
		User:        &Config{},
		Project:     &Config{APIBaseURL: "https://project.example/v1"},
		Effective:   &Config{APIBaseURL: "https://project.example/v1"},
		ProjectPath: "/project/.claude/settings.json",
	}
	snapshot, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
		LegacyCompiler: func() (*PortfolioSnapshot, error) {
			return &PortfolioSnapshot{
				Default:  "legacy.main",
				Accounts: map[AccountID]ResolvedAccount{},
				Profiles: map[ProfileID]ResolvedProfile{},
				Roles:    map[ModelRole]ProfileID{RoleMain: "legacy.main"},
				Failover: map[ModelRole]ResolvedFailoverPolicy{},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Code != "legacy_project_route_authority" {
		t.Fatalf("legacy diagnostic = %#v", snapshot.Diagnostics)
	}
	if snapshot.Revision == "" {
		t.Fatal("legacy snapshot revision was not assigned")
	}
}

func TestCompilePortfolioValidatesRoleAndFailoverDefinitions(t *testing.T) {
	sources := validPortfolioSources()
	sources.User.ModelProfiles["secondary"] = ModelProfileConfig{
		Account:  "openai-main",
		APIModel: "gpt-4o-mini",
	}
	sources.User.ModelRoles = map[string]string{"explore": "secondary"}
	sources.User.FailoverPolicies = map[string]FailoverPolicyConfig{
		"main": {
			Alternates:       []string{"secondary"},
			On:               []string{"overloaded"},
			MaxSwitches:      1,
			MaxProviderCalls: 2,
			MaxElapsedMS:     1000,
		},
	}
	snapshot, err := CompilePortfolio(PortfolioCompileInput{
		Sources: sources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Roles[RoleExplore] != "secondary" ||
		snapshot.ExplicitRoles[RoleExplore] != "secondary" ||
		len(snapshot.Failover[RoleMain].Alternates) != 1 {
		t.Fatalf(
			"role/explicit/failover snapshot = %#v/%#v/%#v",
			snapshot.Roles,
			snapshot.ExplicitRoles,
			snapshot.Failover,
		)
	}
	if _, err := CompilePortfolio(PortfolioCompileInput{
		Sources:                  sources,
		LegacyFallbackConfigured: true,
		Getenv:                   func(string) string { return "" },
	}); err == nil || !strings.Contains(err.Error(), "legacy fallback_model") {
		t.Fatalf("mixed failover ownership error = %v", err)
	}
}

func TestCompilePortfolioExplicitRolePresenceIsDetachedAndRevisioned(t *testing.T) {
	absentSources := validPortfolioSources()
	absent, err := CompilePortfolio(PortfolioCompileInput{
		Sources: absentSources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}

	explicitSources := validPortfolioSources()
	explicitSources.User.ModelRoles = map[string]string{"summary": "primary"}
	explicit, err := CompilePortfolio(PortfolioCompileInput{
		Sources: explicitSources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if absent.Roles[RoleSummary] != explicit.Roles[RoleSummary] {
		t.Fatalf("effective role compatibility changed: %#v %#v", absent.Roles, explicit.Roles)
	}
	if _, exists := absent.ExplicitRoles[RoleSummary]; exists {
		t.Fatal("absent summary role was materialized as explicit")
	}
	if explicit.ExplicitRoles[RoleSummary] != "primary" {
		t.Fatalf("explicit role = %#v", explicit.ExplicitRoles)
	}
	if absent.Revision == explicit.Revision {
		t.Fatal("explicit role presence did not affect portfolio revision")
	}

	explicit.ExplicitRoles[RoleSummary] = "mutated"
	again, err := CompilePortfolio(PortfolioCompileInput{
		Sources: explicitSources,
		Getenv:  func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ExplicitRoles[RoleSummary] != "primary" {
		t.Fatalf("returned explicit role map retained caller mutation: %#v", again.ExplicitRoles)
	}
}

func TestCompilePortfolioRejectsInvalidDefinitions(t *testing.T) {
	tests := map[string]func(*ConfigSources){
		"unsafe endpoint": func(sources *ConfigSources) {
			account := sources.User.ProviderAccounts["openai-main"]
			account.BaseURL = "https://user:pass@api.example/v1"
			sources.User.ProviderAccounts["openai-main"] = account
		},
		"reserved ID": func(sources *ConfigSources) {
			sources.User.ProviderAccounts["legacy.account"] = sources.User.ProviderAccounts["openai-main"]
			delete(sources.User.ProviderAccounts, "openai-main")
		},
		"normalized account collision": func(sources *ConfigSources) {
			sources.User.ProviderAccounts["OPENAI-MAIN"] = sources.User.ProviderAccounts["openai-main"]
		},
		"profile alias collision": func(sources *ConfigSources) {
			sources.User.ModelAliases = map[string]string{"primary": "gpt-4o"}
		},
		"unknown auth kind": func(sources *ConfigSources) {
			account := sources.User.ProviderAccounts["openai-main"]
			account.Auth.Kind = "header"
			sources.User.ProviderAccounts["openai-main"] = account
		},
		"unknown role": func(sources *ConfigSources) {
			sources.User.ModelRoles = map[string]string{"reviewer": "primary"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			sources := validPortfolioSources()
			mutate(sources)
			if _, err := CompilePortfolio(PortfolioCompileInput{
				Sources: sources,
				Getenv:  func(string) string { return "" },
			}); err == nil {
				t.Fatal("invalid portfolio definition should fail")
			}
		})
	}
}

func validPortfolioSources() *ConfigSources {
	window := 200000
	return &ConfigSources{
		User: &Config{
			ModelProfile: "primary",
			ProviderAccounts: map[string]ProviderAccountConfig{
				"openai-main": {
					Provider: "openai",
					BaseURL:  "HTTPS://API.EXAMPLE:443/a/../v1/",
					Auth:     AccountAuthConfig{Kind: "env", Name: "OPENAI_PRIMARY"},
				},
			},
			ModelProfiles: map[string]ModelProfileConfig{
				"primary": {
					Account:           "openai-main",
					APIModel:          "gpt-4o",
					DisplayName:       "Primary",
					ProjectSelectable: true,
					Metadata: enginemodel.MetadataOverrides{
						ContextWindowTokens: &window,
					},
				},
			},
		},
		Project:   &Config{},
		Effective: &Config{},
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
