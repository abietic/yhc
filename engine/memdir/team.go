package memdir

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/identity"
)

// MemoryScope identifies whether a topic is private to the current user or
// shared through the configured team-memory directory.
type MemoryScope string

const (
	MemoryScopePrivate MemoryScope = "private"
	MemoryScopeTeam    MemoryScope = "team"
)

// ScopedMemoryHeader preserves directory scope when private and team topic
// manifests are combined.
type ScopedMemoryHeader struct {
	MemoryHeader
	Scope MemoryScope
}

// IsTeamMemoryEnabled reports whether the explicit shared-directory backend is
// usable. Team memory never operates independently of private auto memory.
func IsTeamMemoryEnabled() bool {
	return IsAutoMemoryEnabled() && GetTeamMemPath() != ""
}

// GetTeamMemPath returns the canonical configured shared directory, including
// one trailing separator. Relative and root-level paths fail closed.
func GetTeamMemPath() string {
	value, _, _ := identity.LookupEnv(identity.RuntimeEnvTeamMemoryDir.Pair())
	return validateMemoryPath(value)
}

// GetTeamMemEntrypoint returns the team MEMORY.md path when team memory is
// enabled, or an empty string otherwise.
func GetTeamMemEntrypoint() string {
	if !IsTeamMemoryEnabled() {
		return ""
	}
	return filepath.Join(GetTeamMemPath(), EntrypointName)
}

// IsTeamMemPath performs a logical containment check. Call
// ValidateTeamMemWritePath before writing so symlink aliases also fail closed.
func IsTeamMemPath(path string) bool {
	root := GetTeamMemPath()
	if root == "" || strings.ContainsRune(path, '\x00') {
		return false
	}
	logical := path
	if !filepath.IsAbs(logical) {
		cwd, err := os.Getwd()
		if err != nil {
			return false
		}
		logical = filepath.Join(cwd, logical)
	}
	return pathWithin(filepath.Clean(logical), filepath.Clean(root))
}

// IsSafeTeamMemPath requires every filesystem representation to stay inside
// the configured shared root.
func IsSafeTeamMemPath(path string) bool {
	if !IsTeamMemoryEnabled() || strings.TrimSpace(path) == "" {
		return false
	}
	return permission.PermissionPathsWithinRoots(
		permission.ResolvePermissionPath(path, ""),
		[]string{GetTeamMemPath()},
	)
}

// IsSafeAutoMemPathForProject verifies private-memory containment without
// relying on the process-wide compatibility project root.
func IsSafeAutoMemPathForProject(path, projectRoot string) bool {
	if !IsAutoMemoryEnabled() {
		return false
	}
	return permission.PermissionPathsWithinRoots(
		permission.ResolvePermissionPath(path, projectRoot),
		[]string{GetAutoMemPathForProject(projectRoot)},
	)
}

// IsSafeAgentMemoryPathForProject verifies all supported custom-agent memory
// roots using the same filesystem alias contract.
func IsSafeAgentMemoryPathForProject(path, projectRoot string) bool {
	if !IsAutoMemoryEnabled() || strings.TrimSpace(path) == "" {
		return false
	}
	resolution := permission.ResolvePermissionPath(path, projectRoot)
	for _, scope := range []AgentMemoryScope{ScopeUser, ScopeProject, ScopeLocal} {
		root := GetAgentMemoryRootForProject(scope, projectRoot)
		if permission.PermissionPathsWithinRoots(resolution, []string{root}) {
			return true
		}
	}
	return false
}

// ValidateTeamMemWritePath verifies logical and filesystem-resolved
// containment, including symlink chains and non-existent tails.
func ValidateTeamMemWritePath(path string) (string, error) {
	if !IsTeamMemoryEnabled() {
		return "", fmt.Errorf("team memory is not enabled")
	}
	if !IsTeamMemPath(path) {
		return "", fmt.Errorf("path escapes team memory directory: %q", path)
	}
	resolution := permission.ResolvePermissionPath(path, "")
	if !IsSafeTeamMemPath(path) {
		return "", fmt.Errorf("path escapes team memory directory through a filesystem alias: %q", path)
	}
	return resolution.Logical, nil
}

// ValidateTeamMemoryContent rejects unsafe paths and high-confidence secrets
// before Write or Edit persists shared content.
func ValidateTeamMemoryContent(path, content string) error {
	if !IsTeamMemoryEnabled() || !IsTeamMemPath(path) {
		return nil
	}
	if _, err := ValidateTeamMemWritePath(path); err != nil {
		return err
	}
	labels := ScanForSecrets(content)
	if len(labels) == 0 {
		return nil
	}
	return fmt.Errorf("content contains potential secrets (%s) and cannot be written to shared team memory", strings.Join(labels, ", "))
}

type secretRule struct {
	label string
	re    *regexp.Regexp
}

var teamMemorySecretRules = []secretRule{
	{label: "AWS access token", re: regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b`)},
	{label: "GCP API key", re: regexp.MustCompile(`\bAIza[\w-]{35}\b`)},
	{label: "GitHub token", re: regexp.MustCompile(`\b(?:ghp_[0-9A-Za-z]{36}|github_pat_\w{82}|gh[ousr]_[0-9A-Za-z]{36})\b`)},
	{label: "OpenAI API key", re: regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{40,}\b`)},
	{label: "Anthropic API key", re: regexp.MustCompile(`\bsk-ant-(?:api|admin)[A-Za-z0-9_-]{40,}\b`)},
	{label: "Slack token", re: regexp.MustCompile(`\bxox(?:b|p|e|a)-[0-9A-Za-z-]{20,}\b`)},
	{label: "private key", re: regexp.MustCompile(`(?is)-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----[\s\S]{64,}?-----END[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----`)},
}

// ScanForSecrets returns deduplicated labels for bounded, high-confidence
// credential patterns. It intentionally omits generic keyword heuristics.
func ScanForSecrets(content string) []string {
	labels := make([]string, 0, 2)
	for _, rule := range teamMemorySecretRules {
		if rule.re.MatchString(content) {
			labels = append(labels, rule.label)
		}
	}
	return labels
}

// ScanMemoryDirectories combines directory-local manifests without losing
// scope. Private entries are returned before team entries.
func ScanMemoryDirectories(projectRoot string) ([]ScopedMemoryHeader, error) {
	privateHeaders, err := ScanMemoryFiles(GetAutoMemPathForProject(projectRoot))
	if err != nil {
		return nil, err
	}
	teamRoot := GetTeamMemPath()
	result := make([]ScopedMemoryHeader, 0, len(privateHeaders))
	for _, header := range privateHeaders {
		if teamRoot != "" && pathWithin(header.FilePath, filepath.Clean(teamRoot)) {
			continue
		}
		result = append(result, ScopedMemoryHeader{MemoryHeader: header, Scope: MemoryScopePrivate})
	}
	if !IsTeamMemoryEnabled() {
		return result, nil
	}
	teamHeaders, err := ScanMemoryFiles(teamRoot)
	if err != nil {
		return nil, err
	}
	for _, header := range teamHeaders {
		result = append(result, ScopedMemoryHeader{MemoryHeader: header, Scope: MemoryScopeTeam})
	}
	return result, nil
}

func pathWithin(path, root string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
