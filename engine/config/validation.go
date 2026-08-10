package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/abietic/yhc/engine/model"
)

// ValidationSeverity indicates the severity of a validation issue.
type ValidationSeverity string

const (
	// SeverityError indicates the configuration is invalid and cannot be used.
	SeverityError ValidationSeverity = "error"
	// SeverityWarning indicates a potential issue that may cause problems.
	SeverityWarning ValidationSeverity = "warning"
	// SeverityInfo indicates informational feedback about the configuration.
	SeverityInfo ValidationSeverity = "info"
)

// ValidationResult represents a single validation issue found in configuration.
type ValidationResult struct {
	// Field is the configuration field that has the issue (e.g., "model", "api_key").
	Field string
	// Message is a human-readable description of the issue.
	Message string
	// Severity indicates how critical the issue is.
	Severity ValidationSeverity
}

// ValidationResults holds all validation issues found during config validation.
type ValidationResults struct {
	Results []ValidationResult
}

// HasErrors returns true if any validation result is an error.
func (vr *ValidationResults) HasErrors() bool {
	for _, r := range vr.Results {
		if r.Severity == SeverityError {
			return true
		}
	}
	return false
}

// HasWarnings returns true if any validation result is a warning.
func (vr *ValidationResults) HasWarnings() bool {
	for _, r := range vr.Results {
		if r.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

// Errors returns only error-severity results.
func (vr *ValidationResults) Errors() []ValidationResult {
	var result []ValidationResult
	for _, r := range vr.Results {
		if r.Severity == SeverityError {
			result = append(result, r)
		}
	}
	return result
}

// Warnings returns only warning-severity results.
func (vr *ValidationResults) Warnings() []ValidationResult {
	var result []ValidationResult
	for _, r := range vr.Results {
		if r.Severity == SeverityWarning {
			result = append(result, r)
		}
	}
	return result
}

// IsValid returns true if there are no errors (warnings are acceptable).
func (vr *ValidationResults) IsValid() bool {
	return !vr.HasErrors()
}

// String returns a human-readable summary of all validation results.
func (vr *ValidationResults) String() string {
	if len(vr.Results) == 0 {
		return "configuration is valid"
	}
	var sb strings.Builder
	for i, r := range vr.Results {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[%s] %s: %s", r.Severity, r.Field, r.Message)
	}
	return sb.String()
}

// ValidateConfig performs comprehensive validation on the configuration.
// It validates model name, provider configuration, API key availability,
// and settings consistency. Returns all issues found (not just the first).
func ValidateConfig(s *Settings) *ValidationResults {
	vr := &ValidationResults{}

	// Validate model
	validateModel(s, vr)

	// Validate provider/API key configuration
	validateProviderConfig(s, vr)

	// Validate numeric settings
	validateNumericSettings(s, vr)

	// Validate permission mode
	validatePermissionMode(s, vr)

	// Validate timeout
	validateTimeout(s, vr)

	return vr
}

// validateModel checks the model name for validity and deprecation.
func validateModel(s *Settings, vr *ValidationResults) {
	if s.Model == "" {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "model",
			Message:  "model name is empty; inference will not work",
			Severity: SeverityError,
		})
		return
	}

	// Check if model is recognized
	cap := model.GetCapabilities(s.Model)
	if cap.Name == "unknown" || cap.Name == s.Model && !isKnownModel(s.Model) {
		// Not a known model — not necessarily an error (could be a custom deployment)
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "model",
			Message:  fmt.Sprintf("model %q is not recognized; using default capabilities", s.Model),
			Severity: SeverityWarning,
		})
	}

	// Check deprecation
	if deprecation := model.CheckDeprecation(s.Model); deprecation != "" {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "model",
			Message:  deprecation,
			Severity: SeverityWarning,
		})
	}

	// Check tool support (important for agent functionality)
	if cap.Name != "unknown" && !cap.SupportsTools {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "model",
			Message:  fmt.Sprintf("model %q does not support tool calling; agent functionality will be limited", s.Model),
			Severity: SeverityWarning,
		})
	}
}

// validateProviderConfig checks that the API key is available for the detected provider.
func validateProviderConfig(s *Settings, vr *ValidationResults) {
	if s.Model == "" {
		return // Already reported as error in validateModel
	}

	provider := model.DetectProvider(s.Model)
	if provider == model.ProviderUnknown {
		// Cannot determine provider — skip API key check, already warned about unknown model
		return
	}

	envCfg := model.GetProviderEnvConfig(provider)
	if envCfg == nil {
		return
	}

	// Check if API key is available from any known source.
	// We do NOT log the actual key values for security.
	hasAPIKey := false
	for _, envVar := range envCfg.APIKeyEnvVars {
		if os.Getenv(envVar) != "" {
			hasAPIKey = true
			break
		}
	}

	if !hasAPIKey {
		envVarList := strings.Join(envCfg.APIKeyEnvVars, ", ")
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "api_key",
			Message:  fmt.Sprintf("no API key found for provider %q; set one of: %s", provider, envVarList),
			Severity: SeverityError,
		})
	}

	// Check base URL override
	if envCfg.BaseURLEnvVar != "" {
		if baseURL := os.Getenv(envCfg.BaseURLEnvVar); baseURL != "" {
			// Informational: custom base URL is set
			vr.Results = append(vr.Results, ValidationResult{
				Field:    "base_url",
				Message:  fmt.Sprintf("using custom base URL from %s", envCfg.BaseURLEnvVar),
				Severity: SeverityInfo,
			})
		}
	}
}

// validateNumericSettings checks that numeric settings are within acceptable ranges.
func validateNumericSettings(s *Settings, vr *ValidationResults) {
	if s.MaxTurns < 0 {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "max_turns",
			Message:  fmt.Sprintf("max_turns is %d; must be zero (unlimited) or positive", s.MaxTurns),
			Severity: SeverityError,
		})
	} else if s.MaxTurns > 500 {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "max_turns",
			Message:  fmt.Sprintf("max_turns is %d; unusually high, may indicate misconfiguration", s.MaxTurns),
			Severity: SeverityWarning,
		})
	}

	if s.MaxTokens < 1 {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "max_tokens",
			Message:  fmt.Sprintf("max_tokens is %d; must be at least 1", s.MaxTokens),
			Severity: SeverityError,
		})
	}

	if s.Temperature < 0 || s.Temperature > 2 {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "temperature",
			Message:  fmt.Sprintf("temperature %.2f is outside the recommended range [0, 2]", s.Temperature),
			Severity: SeverityWarning,
		})
	}
}

// validatePermissionMode checks the permission mode value.
func validatePermissionMode(s *Settings, vr *ValidationResults) {
	validModes := map[string]bool{
		"default": true,
		"plan":    true,
		"bypass":  true,
		"auto":    true,
	}
	if s.PermissionMode != "" && !validModes[s.PermissionMode] {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "permission_mode",
			Message:  fmt.Sprintf("unknown permission_mode %q; expected one of: default, plan, bypass, auto", s.PermissionMode),
			Severity: SeverityError,
		})
	}
}

// validateTimeout checks the timeout duration.
func validateTimeout(s *Settings, vr *ValidationResults) {
	if s.Timeout < 0 {
		vr.Results = append(vr.Results, ValidationResult{
			Field:    "timeout",
			Message:  "timeout is negative",
			Severity: SeverityError,
		})
	}
}

// isKnownModel checks whether a model name resolves to something in the model table
// (as opposed to falling through to defaults).
func isKnownModel(modelName string) bool {
	cap := model.GetCapabilities(modelName)
	// If the name was resolved to something other than "unknown" or the input itself
	// stored verbatim, it was found in the registry.
	return cap.Name != "unknown" && cap.Name != modelName
}
