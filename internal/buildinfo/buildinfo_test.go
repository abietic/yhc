package buildinfo

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/identity"
)

func TestBuildInfoTextProjectionsShareYHCIdentity(t *testing.T) {
	info := Info{
		SchemaVersion: SchemaVersion,
		Version:       "1.2.3",
		Commit:        "0123456789abcdef",
		BuildTime:     "2026-07-22T00:00:00Z",
		Modified:      true,
		GoVersion:     "go1.25.0",
		OS:            "darwin",
		Arch:          "arm64",
		Dependencies: []Dependency{
			{Path: "github.com/cloudwego/eino", Version: "v0.9.12"},
		},
	}

	short := ShortText(info)
	if !strings.HasPrefix(short, identity.CommandName+" v1.2.3") ||
		strings.Contains(strings.ToLower(short), identity.LegacyCommandName) {
		t.Fatalf("short build info projects a noncanonical identity: %q", short)
	}
	for _, value := range []string{"v1.2.3", "0123456789ab+dirty", "darwin/arm64", "go1.25.0"} {
		if !strings.Contains(short, value) {
			t.Fatalf("short build info %q missing %q", short, value)
		}
	}

	detailed := DetailedText(info)
	if !strings.Contains(detailed, identity.ProductName+" version information") ||
		strings.Contains(strings.ToLower(detailed), identity.LegacyCommandName) {
		t.Fatalf("detailed build info projects a noncanonical identity: %q", detailed)
	}
	for _, value := range []string{"Version:    v1.2.3", "Commit:     0123456789ab (dirty)", "Built:      2026-07-22T00:00:00Z", "github.com/cloudwego/eino"} {
		if !strings.Contains(detailed, value) {
			t.Fatalf("detailed build info %q missing %q", detailed, value)
		}
	}
}
