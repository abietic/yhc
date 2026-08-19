package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/identity"
)

func TestCurrentPrefersExplicitReleaseSourceIdentity(t *testing.T) {
	setBuildVariables(t, "v1.2.3", "release-commit", "2026-08-20T00:00:00Z", "false")
	info := current(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "runtime-commit"},
		{Key: "vcs.time", Value: "2026-08-19T00:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}, true)

	if info.Version != "1.2.3" || info.Commit != "release-commit" ||
		info.BuildTime != "2026-08-20T00:00:00Z" || info.Modified {
		t.Fatalf("explicit build identity = %#v", info)
	}
}

func TestCurrentFallsBackToRuntimeVCSForDevelopmentBuild(t *testing.T) {
	setBuildVariables(t, "0.1.0", "unknown", "", "")
	info := current(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "runtime-commit"},
		{Key: "vcs.time", Value: "2026-08-19T00:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}, true)

	if info.Commit != "runtime-commit" || info.BuildTime != "2026-08-19T00:00:00Z" || !info.Modified {
		t.Fatalf("runtime build identity = %#v", info)
	}
}

func setBuildVariables(t *testing.T, version, commit, buildTime, modified string) {
	t.Helper()
	oldVersion, oldCommit, oldBuildTime, oldModified := Version, Commit, BuildTime, Modified
	Version, Commit, BuildTime, Modified = version, commit, buildTime, modified
	t.Cleanup(func() {
		Version, Commit, BuildTime, Modified = oldVersion, oldCommit, oldBuildTime, oldModified
	})
}

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
