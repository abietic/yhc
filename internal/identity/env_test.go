package identity

import (
	"os"
	"testing"
)

func TestLookupEnvCanonicalWinsLegacyWithoutValueExposure(t *testing.T) {
	pair := EnvPair{
		Canonical: "YHC_TEST_CANONICAL_WINS",
		Legacy:    "EINO_AGENT_TEST_CANONICAL_WINS",
	}
	t.Setenv(pair.Canonical, "canonical-private-sentinel")
	t.Setenv(pair.Legacy, "legacy-private-sentinel")

	value, source, present := LookupEnv(pair)
	if !present || source != EnvCanonical || value != "canonical-private-sentinel" {
		t.Fatal("canonical environment value was not selected")
	}
}

func TestLookupEnvFallsBackToLegacy(t *testing.T) {
	pair := EnvPair{
		Canonical: "YHC_TEST_LEGACY_FALLBACK",
		Legacy:    "EINO_AGENT_TEST_LEGACY_FALLBACK",
	}
	unsetEnvForTest(t, pair.Canonical)
	t.Setenv(pair.Legacy, "legacy-private-sentinel")

	value, source, present := LookupEnv(pair)
	if !present || source != EnvLegacy || value != "legacy-private-sentinel" {
		t.Fatal("legacy environment value was not selected")
	}
}

func TestLookupEnvCanonicalPresentEmptyStillWins(t *testing.T) {
	pair := EnvPair{
		Canonical: "YHC_TEST_CANONICAL_EMPTY",
		Legacy:    "EINO_AGENT_TEST_CANONICAL_EMPTY",
	}
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, "legacy-private-sentinel")

	value, source, present := LookupEnv(pair)
	if !present || source != EnvCanonical || value != "" {
		t.Fatal("present empty canonical environment value did not win")
	}
}

func TestLookupEnvUnset(t *testing.T) {
	pair := EnvPair{
		Canonical: "YHC_TEST_UNSET",
		Legacy:    "EINO_AGENT_TEST_UNSET",
	}
	unsetEnvForTest(t, pair.Canonical, pair.Legacy)

	value, source, present := LookupEnv(pair)
	if present || source != EnvUnset || value != "" {
		t.Fatal("unset environment pair was reported present")
	}
}

func TestEnvTruthyPreservesExistingParsing(t *testing.T) {
	pair := EnvPair{
		Canonical: "YHC_TEST_TRUTHY",
		Legacy:    "EINO_AGENT_TEST_TRUTHY",
	}
	tests := []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: " true ", want: true},
		{value: "YES", want: true},
		{value: "\tOn\n", want: true},
		{value: "", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "enabled", want: false},
	}
	for index, test := range tests {
		t.Setenv(pair.Canonical, test.value)
		t.Setenv(pair.Legacy, "1")
		if got := EnvTruthy(pair); got != test.want {
			t.Fatalf("truthy case %d = %t, want %t", index, got, test.want)
		}
	}
}

func TestRuntimeEnvironmentPairsUseCanonicalAndLegacyPrefixes(t *testing.T) {
	tests := []struct {
		name   RuntimeEnvName
		suffix string
	}{
		{name: RuntimeEnvAccessibility, suffix: "ACCESSIBILITY"},
		{name: RuntimeEnvReducedMotion, suffix: "REDUCED_MOTION"},
		{name: RuntimeEnvProviderPreflight, suffix: "PROVIDER_PREFLIGHT"},
		{name: RuntimeEnvSimple, suffix: "SIMPLE"},
		{name: RuntimeEnvDisableACPAssistantMessageIDs, suffix: "DISABLE_ACP_ASSISTANT_MESSAGE_IDS"},
		{name: RuntimeEnvDisableACPCommandUpdates, suffix: "DISABLE_ACP_COMMAND_UPDATES"},
		{name: RuntimeEnvDisableAutoMemory, suffix: "DISABLE_AUTO_MEMORY"},
		{name: RuntimeEnvRemoteMemoryDir, suffix: "REMOTE_MEMORY_DIR"},
		{name: RuntimeEnvConfigDir, suffix: "CONFIG_DIR"},
		{name: RuntimeEnvMemoryPathOverride, suffix: "MEMORY_PATH_OVERRIDE"},
		{name: RuntimeEnvTeamMemoryDir, suffix: "TEAM_MEMORY_DIR"},
		{name: RuntimeEnvSessionCatalog, suffix: "SESSION_CATALOG"},
		{name: RuntimeEnvPermissionReviewAuditDir, suffix: "PERMISSION_REVIEW_AUDIT_DIR"},
		{name: RuntimeEnvDisableMouse, suffix: "DISABLE_MOUSE"},
	}
	for _, test := range tests {
		pair := test.name.Pair()
		if pair.Canonical != "YHC_"+test.suffix || pair.Legacy != "EINO_AGENT_"+test.suffix {
			t.Fatalf("%s pair = %#v", test.suffix, pair)
		}
	}
}

func unsetEnvForTest(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset test environment: %v", err)
		}
	}
}
