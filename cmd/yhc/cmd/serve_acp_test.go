package cmd

import (
	"testing"

	"github.com/abietic/yhc/internal/identity"
)

func TestRuntimeEnvironmentCanonicalWinsLegacyForACP(t *testing.T) {
	tests := []struct {
		name      string
		canonical *string
		legacy    *string
		want      bool
	}{
		{name: "canonical only", canonical: environmentValue("true"), want: true},
		{name: "legacy only", legacy: environmentValue("true"), want: true},
		{name: "both prefer canonical", canonical: environmentValue("yes"), legacy: environmentValue("false"), want: true},
		{name: "present empty canonical blocks legacy", canonical: environmentValue(""), legacy: environmentValue("true")},
		{name: "invalid canonical blocks legacy", canonical: environmentValue("invalid"), legacy: environmentValue("true")},
		{name: "neither"},
	}
	fields := []struct {
		name identity.RuntimeEnvName
		read func(acpEnvironmentOptions) bool
	}{
		{name: identity.RuntimeEnvProviderPreflight, read: func(options acpEnvironmentOptions) bool { return options.ProviderPreflight }},
		{name: identity.RuntimeEnvSimple, read: func(options acpEnvironmentOptions) bool { return options.SimpleTools }},
		{name: identity.RuntimeEnvDisableACPAssistantMessageIDs, read: func(options acpEnvironmentOptions) bool { return options.DisableAssistantMessageIDs }},
		{name: identity.RuntimeEnvDisableACPCommandUpdates, read: func(options acpEnvironmentOptions) bool { return options.DisableCommandUpdates }},
	}
	for _, field := range fields {
		t.Run(string(field.name), func(t *testing.T) {
			pair := field.name.Pair()
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					setOptionalEnvironment(t, pair.Canonical, test.canonical)
					setOptionalEnvironment(t, pair.Legacy, test.legacy)
					if got := field.read(resolveACPEnvironmentOptions()); got != test.want {
						t.Fatalf("ACP environment %s = %t, want %t", field.name, got, test.want)
					}
				})
			}
		})
	}
}
