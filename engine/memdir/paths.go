// Package memdir manages the auto-memory directory system.
// Mirrors src/memdir/paths.ts from the reference.
package memdir

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statepath"
)

var (
	projectRoot   string
	projectRootMu sync.RWMutex
)

// SetProjectRoot sets the project root for memory path resolution.
func SetProjectRoot(root string) {
	projectRootMu.Lock()
	defer projectRootMu.Unlock()
	projectRoot = root
}

// GetProjectRoot returns the configured project root.
func GetProjectRoot() string {
	projectRootMu.RLock()
	defer projectRootMu.RUnlock()
	return projectRoot
}

// IsAutoMemoryEnabled returns whether auto-memory features are enabled.
// Mirrors the priority chain from paths.ts:
//  1. YHC_DISABLE_AUTO_MEMORY env var (supported legacy alias; 1/true -> OFF)
//  2. YHC_SIMPLE (supported legacy alias; --bare) -> OFF
//  3. Default: enabled
func IsAutoMemoryEnabled() bool {
	disableAutoMemory, _, _ := identity.LookupEnv(identity.RuntimeEnvDisableAutoMemory.Pair())
	if isEnvTruthy(disableAutoMemory) {
		return false
	}
	simple, _, _ := identity.LookupEnv(identity.RuntimeEnvSimple.Pair())
	return !isEnvTruthy(simple)
}

// GetMemoryBaseDir returns the base directory for persistent memory storage.
// Resolution order:
//  1. YHC_REMOTE_MEMORY_DIR env var (supported legacy alias)
//  2. YHC_CONFIG_DIR env var (supported legacy alias)
//  3. ~/.yhc
func GetMemoryBaseDir() string {
	selection, err := resolveMemoryBaseSelection()
	if err == nil {
		return selection.Effective
	}
	return defaultUserStateRoots().Canonical
}

// GetConfigHomeDir returns the user's canonical config home directory (~/.yhc)
// unless an exact supported override is selected.
func GetConfigHomeDir() string {
	selection, err := resolveConfigHomeSelection()
	if err == nil {
		return selection.Effective
	}
	return defaultUserStateRoots().Canonical
}

const (
	autoMemDirname        = "memory"
	autoMemEntrypointName = "MEMORY.md"
)

// GetAutoMemPath returns the auto-memory directory path for the current project.
// Resolution order:
//  1. YHC_MEMORY_PATH_OVERRIDE env var (supported legacy alias; full path)
//  2. <memoryBase>/projects/<sanitized-project-root>/memory/
func GetAutoMemPath() string {
	return GetAutoMemPathForProject(GetProjectRoot())
}

// GetAutoMemPathForProject returns the auto-memory directory for a specific
// project without mutating the process-wide compatibility root.
func GetAutoMemPathForProject(root string) string {
	selection, err := resolveAutoMemorySelection(root)
	if err == nil {
		return withTrailingSeparator(selection.Effective)
	}
	return withTrailingSeparator(defaultAutoMemoryPath(root))
}

// GetAutoMemEntrypoint returns the path to MEMORY.md inside the auto-memory dir.
func GetAutoMemEntrypoint() string {
	return filepath.Join(GetAutoMemPath(), autoMemEntrypointName)
}

// GetAutoMemEntrypointForProject returns the project-specific MEMORY.md path.
func GetAutoMemEntrypointForProject(root string) string {
	return filepath.Join(GetAutoMemPathForProject(root), autoMemEntrypointName)
}

// GetAutoMemDailyLogPath returns the daily log file path for the given date string (YYYY-MM-DD).
func GetAutoMemDailyLogPath(dateStr string) string {
	parts := strings.SplitN(dateStr, "-", 3)
	if len(parts) != 3 {
		return filepath.Join(GetAutoMemPath(), "logs", dateStr+".md")
	}
	yyyy, mm := parts[0], parts[1]
	return filepath.Join(GetAutoMemPath(), "logs", yyyy, mm, dateStr+".md")
}

// IsAutoMemPath checks if an absolute path is within the auto-memory directory.
func IsAutoMemPath(absolutePath string) bool {
	normalized := filepath.Clean(absolutePath)
	autoMemPath := filepath.Clean(GetAutoMemPath())
	return pathWithin(normalized, autoMemPath)
}

// HasAutoMemPathOverride returns true when the memory path override env var is set.
func HasAutoMemPathOverride() bool {
	selection, err := resolvePathOverride(identity.RuntimeEnvMemoryPathOverride.Pair())
	return err == nil && !selection.Migratable
}

// GetAgentMemoryDir returns the agent memory directory for a given agent type and scope.
func GetAgentMemoryDir(agentType string, scope AgentMemoryScope) string {
	return GetAgentMemoryDirForProject(agentType, scope, GetProjectRoot())
}

// GetAgentMemoryDirForProject resolves agent memory without relying on process
// CWD, allowing parent and worktree child engines to share one stable root.
func GetAgentMemoryDirForProject(agentType string, scope AgentMemoryScope, projectRoot string) string {
	dirName := sanitizeAgentType(agentType)
	root := GetAgentMemoryRootForProject(scope, projectRoot)
	if root == "" {
		root = GetAgentMemoryRootForProject(ScopeUser, projectRoot)
	}
	return filepath.Join(root, dirName) + string(filepath.Separator)
}

// GetAgentMemoryRootForProject returns the unsuffixed root for one persistence
// scope, used by snapshot containment checks.
func GetAgentMemoryRootForProject(scope AgentMemoryScope, projectRoot string) string {
	switch scope {
	case ScopeProject:
		return filepath.Join(resolveProjectRoot(projectRoot), identity.ProjectDirName, "agent-memory")
	case ScopeLocal:
		return getLocalAgentMemoryRootForProject(projectRoot)
	case ScopeUser:
		return filepath.Join(GetMemoryBaseDir(), "agent-memory")
	default:
		return ""
	}
}

// AgentMemoryScope defines the persistence scope for agent memory.
type AgentMemoryScope string

const (
	ScopeUser    AgentMemoryScope = "user"
	ScopeProject AgentMemoryScope = "project"
	ScopeLocal   AgentMemoryScope = "local"
)

// IsAgentMemoryPath checks if a path is within any agent memory directory.
func IsAgentMemoryPath(absolutePath string) bool {
	normalized := filepath.Clean(absolutePath)
	memoryBase := GetMemoryBaseDir()
	if strings.HasPrefix(normalized, filepath.Join(memoryBase, "agent-memory")+string(filepath.Separator)) {
		return true
	}
	cwd, _ := os.Getwd()
	return strings.HasPrefix(normalized, filepath.Join(cwd, identity.ProjectDirName, "agent-memory")+string(filepath.Separator))
}

func getLocalAgentMemoryRootForProject(projectRoot string) string {
	selection, err := resolveRemoteMemorySelection()
	if err == nil && !selection.Migratable {
		root := resolveProjectRoot(projectRoot)
		return filepath.Join(selection.Effective, "projects", sanitizePath(root), "agent-memory-local")
	}
	return filepath.Join(resolveProjectRoot(projectRoot), identity.ProjectDirName, "agent-memory-local")
}

func resolveMemoryBaseSelection() (statepath.Selection, error) {
	remote, err := resolveRemoteMemorySelection()
	if err != nil {
		return statepath.Selection{}, err
	}
	if !remote.Migratable {
		return remote, nil
	}
	return resolveConfigHomeSelection()
}

func resolveRemoteMemorySelection() (statepath.Selection, error) {
	return statepath.ResolveOverride(
		identity.RuntimeEnvRemoteMemoryDir.Pair(),
		defaultUserStateRoots(),
	)
}

func resolveConfigHomeSelection() (statepath.Selection, error) {
	return statepath.ResolveOverride(
		identity.RuntimeEnvConfigDir.Pair(),
		defaultUserStateRoots(),
	)
}

func resolveAutoMemorySelection(projectRoot string) (statepath.Selection, error) {
	override, err := resolvePathOverride(identity.RuntimeEnvMemoryPathOverride.Pair())
	if err != nil {
		return statepath.Selection{}, err
	}
	if !override.Migratable {
		return override, nil
	}
	base, err := resolveMemoryBaseSelection()
	if err != nil {
		return statepath.Selection{}, err
	}
	return statepath.Selection{
		Effective: filepath.Join(
			base.Effective,
			"projects",
			sanitizePath(resolveProjectRoot(projectRoot)),
			autoMemDirname,
		),
		Roots:      base.Roots,
		Source:     base.Source,
		Migratable: base.Migratable,
	}, nil
}

func resolvePathOverride(pair identity.EnvPair) (statepath.Selection, error) {
	return statepath.ResolveOverride(pair, defaultUserStateRoots())
}

func defaultUserStateRoots() statepath.Roots {
	home, err := os.UserHomeDir()
	if err == nil {
		if roots, rootsErr := statepath.UserRoots(home); rootsErr == nil {
			return roots
		}
	}
	roots, err := statepath.UserRoots(os.TempDir())
	if err == nil {
		return roots
	}
	return statepath.Roots{
		Canonical: filepath.Join(os.TempDir(), identity.ProjectDirName),
		Legacy:    filepath.Join(os.TempDir(), identity.LegacyDirName),
	}
}

func defaultAutoMemoryPath(projectRoot string) string {
	return filepath.Join(
		GetMemoryBaseDir(),
		"projects",
		sanitizePath(resolveProjectRoot(projectRoot)),
		autoMemDirname,
	)
}

func withTrailingSeparator(value string) string {
	if strings.HasSuffix(value, string(filepath.Separator)) {
		return value
	}
	return value + string(filepath.Separator)
}

func sanitizeAgentType(agentType string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(agentType) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	value := strings.Trim(b.String(), "-_")
	if value == "" {
		return "agent"
	}
	return value
}

// ParseAgentMemoryScope validates a custom-agent memory scope.
func ParseAgentMemoryScope(raw string) AgentMemoryScope {
	switch AgentMemoryScope(strings.ToLower(strings.TrimSpace(raw))) {
	case ScopeUser:
		return ScopeUser
	case ScopeProject:
		return ScopeProject
	case ScopeLocal:
		return ScopeLocal
	default:
		return ""
	}
}

func resolveProjectRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	if roots, err := statepath.ProjectRoots(root); err == nil {
		return filepath.Dir(roots.Canonical)
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return os.TempDir()
}

func sanitizePath(p string) string {
	p = filepath.Clean(p)
	p = strings.ReplaceAll(p, string(filepath.Separator), "_")
	p = strings.ReplaceAll(p, ":", "_")
	p = strings.ReplaceAll(p, " ", "_")
	return p
}

func validateMemoryPath(raw string) string {
	if raw == "" {
		return ""
	}
	normalized := filepath.Clean(raw)
	if !filepath.IsAbs(normalized) {
		return ""
	}
	if len(normalized) < 3 {
		return ""
	}
	if strings.Contains(normalized, "\x00") {
		return ""
	}
	return normalized + string(filepath.Separator)
}

func isEnvTruthy(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes":
		return true
	}
	return false
}
