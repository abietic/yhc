package identity

import (
	"os"
	"strings"
)

// EnvSource identifies which supported environment name supplied a value.
type EnvSource uint8

const (
	// EnvUnset means neither environment name was present.
	EnvUnset EnvSource = iota
	// EnvCanonical means the canonical YHC name was present.
	EnvCanonical
	// EnvLegacy means the supported legacy name supplied the value.
	EnvLegacy
)

// EnvPair binds one canonical YHC environment name to its supported alias.
type EnvPair struct {
	Canonical string
	Legacy    string
}

// RuntimeEnvName identifies one supported runtime environment setting without
// duplicating the canonical and legacy prefixes at every consumer.
type RuntimeEnvName string

const (
	RuntimeEnvAccessibility     RuntimeEnvName = "ACCESSIBILITY"
	RuntimeEnvReducedMotion     RuntimeEnvName = "REDUCED_MOTION"
	RuntimeEnvProviderPreflight RuntimeEnvName = "PROVIDER_PREFLIGHT"
	RuntimeEnvSimple            RuntimeEnvName = "SIMPLE"
	// RuntimeEnvDisableACPAssistantMessageIDs is a feature flag name, not a credential.
	RuntimeEnvDisableACPAssistantMessageIDs RuntimeEnvName = "DISABLE_ACP_ASSISTANT_MESSAGE_IDS" //nolint:gosec // G101 mistakes the environment-variable suffix for a credential.
	RuntimeEnvDisableACPCommandUpdates      RuntimeEnvName = "DISABLE_ACP_COMMAND_UPDATES"
	RuntimeEnvDisableAutoMemory             RuntimeEnvName = "DISABLE_AUTO_MEMORY"
	RuntimeEnvRemoteMemoryDir               RuntimeEnvName = "REMOTE_MEMORY_DIR"
	RuntimeEnvConfigDir                     RuntimeEnvName = "CONFIG_DIR"
	RuntimeEnvMemoryPathOverride            RuntimeEnvName = "MEMORY_PATH_OVERRIDE"
	RuntimeEnvTeamMemoryDir                 RuntimeEnvName = "TEAM_MEMORY_DIR"
	RuntimeEnvSessionCatalog                RuntimeEnvName = "SESSION_CATALOG"
	RuntimeEnvPermissionReviewAuditDir      RuntimeEnvName = "PERMISSION_REVIEW_AUDIT_DIR"
	RuntimeEnvDisableMouse                  RuntimeEnvName = "DISABLE_MOUSE"
)

// Pair returns the canonical YHC name and its immutable legacy alias.
func (name RuntimeEnvName) Pair() EnvPair {
	suffix := string(name)
	return EnvPair{
		Canonical: "YHC_" + suffix,
		Legacy:    "EINO_AGENT_" + suffix,
	}
}

// LookupEnv selects a present canonical value before consulting its legacy alias.
func LookupEnv(pair EnvPair) (value string, source EnvSource, present bool) {
	if value, present := os.LookupEnv(pair.Canonical); present {
		return value, EnvCanonical, true
	}
	if value, present := os.LookupEnv(pair.Legacy); present {
		return value, EnvLegacy, true
	}
	return "", EnvUnset, false
}

// EnvTruthy reports whether the selected value uses the supported true spelling.
func EnvTruthy(pair EnvPair) bool {
	value, _, present := LookupEnv(pair)
	if !present {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
