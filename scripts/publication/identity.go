package main

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

const (
	identityPolicyCurrentCopy       = "current-copy"
	identityPolicyHistory           = "history"
	identityPolicySourceMapping     = "source-mapping"
	identityPolicyLegacyState       = "legacy-state"
	identityPolicyLegacyEnvironment = "legacy-environment"
	identityPolicyLegacyProtocol    = "legacy-protocol"

	currentIdentityPathsFilename = "current-identity-paths.txt"
)

var (
	legacyProductIdentityPattern  = regexp.MustCompile(`(?i)(^|[^[:alnum:]_])eino[-_ ]?agent($|[^[:alnum:]_])`)
	legacyEnvironmentAliasPattern = regexp.MustCompile(`\bEINO_AGENT_[A-Z0-9_]+\b`)
)

var legacyEnvironmentAliasesByPath = map[string]map[string]struct{}{
	"cmd/yhc/cmd/root.go": {
		"EINO_AGENT_DISABLE_MOUSE": {},
	},
}

var legacyEnvironmentPrefixesByPath = map[string]string{
	"docs/guides/configuration-and-providers.md": "`EINO_AGENT_`",
	"internal/identity/env.go":                   `"EINO_AGENT_"`,
}

func buildCurrentIdentityPathSet(ctx context.Context, inventory Inventory) ([]string, error) {
	paths := make([]string, 0)
	for _, file := range inventory.Files {
		if !isCurrentIdentityReviewPath(file.Path) {
			continue
		}
		contents, err := gitBytes(ctx, "show", ":"+file.Path)
		if err != nil {
			return nil, err
		}
		if containsCurrentCopyIdentity(file.Path, contents) {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func encodeNULPaths(paths []string) []byte {
	var encoded strings.Builder
	for _, name := range paths {
		encoded.WriteString(name)
		encoded.WriteByte(0)
	}
	return []byte(encoded.String())
}

func isCurrentIdentityReviewPath(name string) bool {
	switch name {
	case "README.md", "PROJECT_DIRECTION.md", "AGENTS.md", "docs/superpowers/plans/README.md":
		return true
	}
	for _, prefix := range []string{
		"docs/architecture/",
		"docs/guides/",
		"docs/contributing/",
		".agents/skills/",
		".codex/agents/",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func containsCurrentCopyIdentity(name string, contents []byte) bool {
	for _, line := range strings.Split(string(contents), "\n") {
		if hasLegacyIdentityLine(line) && classifyLegacyIdentityLine(name, line) == identityPolicyCurrentCopy {
			return true
		}
	}
	return false
}

func hasLegacyIdentityLine(line string) bool {
	return legacyProductIdentityPattern.MatchString(line) || legacyEnvironmentAliasPattern.MatchString(line)
}

func classifyLegacyIdentityLine(name, line string) string {
	switch {
	case strings.HasPrefix(name, "docs/migration/history/"):
		return identityPolicyHistory
	case strings.HasPrefix(name, "docs/migration/reference/"):
		return identityPolicySourceMapping
	case isLegacyEnvironmentIdentityLine(name, line):
		return identityPolicyLegacyEnvironment
	case isLegacyStateIdentityLine(line):
		return identityPolicyLegacyState
	case isLegacyProtocolIdentityLine(name, line):
		return identityPolicyLegacyProtocol
	default:
		return identityPolicyCurrentCopy
	}
}

func isLegacyEnvironmentIdentityLine(name, line string) bool {
	if prefix := legacyEnvironmentPrefixesByPath[name]; prefix != "" && strings.Contains(line, prefix) {
		return !hasLegacyIdentityLine(strings.ReplaceAll(line, prefix, ""))
	}
	matches := legacyEnvironmentAliasPattern.FindAllString(line, -1)
	allowed := legacyEnvironmentAliasesByPath[name]
	if len(matches) == 0 || len(allowed) == 0 {
		return false
	}
	for _, match := range matches {
		if _, ok := allowed[match]; !ok {
			return false
		}
	}
	return !hasLegacyIdentityLine(legacyEnvironmentAliasPattern.ReplaceAllString(line, ""))
}

func isLegacyStateIdentityLine(line string) bool {
	if !strings.Contains(line, ".eino-agent") {
		return false
	}
	return !hasLegacyIdentityLine(strings.ReplaceAll(line, ".eino-agent", ""))
}

func isLegacyProtocolIdentityLine(name, line string) bool {
	var fragments []string
	switch name {
	case "server/acp/agent.go":
		fragments = []string{`title := "Eino Agent"`, `Name:    "eino-agent"`}
	case "server/acp/goal_extension.go":
		fragments = []string{`"eino-agent.goal"`}
	case "server/mcp/server.go", "engine/mcp/sdk_client.go":
		fragments = []string{`Name:    "eino-agent"`}
	case "scripts/evaluation/report.go":
		fragments = []string{`"eino-agent/evaluation-report"`}
	case "docs/architecture/platform/acp-adapter.md":
		fragments = []string{"`eino-agent.goal`", "stable `eino-agent` name", "`Eino Agent` title"}
	default:
		return false
	}
	remainder := line
	matched := false
	for _, fragment := range fragments {
		if strings.Contains(remainder, fragment) {
			matched = true
			remainder = strings.ReplaceAll(remainder, fragment, "")
		}
	}
	return matched && !hasLegacyIdentityLine(remainder)
}
