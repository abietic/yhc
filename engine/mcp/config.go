package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MCPServerConfig holds the full configuration for a single MCP server,
// including lifecycle settings like timeout and enabled state.
type MCPServerConfig struct {
	// Name is the unique identifier for this server.
	Name string
	// Command is the executable to launch (stdio transport).
	Command string
	// Args are command-line arguments.
	Args []string
	// Env are additional environment variables for the server process.
	Env map[string]string
	// CWD is the working directory for the server process.
	CWD string
	// Enabled indicates whether this server should be started.
	Enabled bool
	// Timeout is the maximum time to wait for server responses.
	Timeout time.Duration
	// Type is the transport type: "stdio" (default), "sse", or "http".
	Type string
	// URL is the server URL (for "sse" and "http" transport types).
	URL string
	// Headers are custom HTTP headers (for remote transports).
	Headers map[string]string
}

// MCPConfig holds the complete MCP configuration loaded from settings files.
type MCPConfig struct {
	// Servers maps server names to their configurations.
	Servers map[string]*MCPServerConfig
	// GlobalTimeout is the default timeout applied when a server does not specify one.
	GlobalTimeout time.Duration
}

// defaultGlobalTimeout is used when no explicit timeout is configured.
const defaultGlobalTimeout = 60 * time.Second

// mcpServersFileName is the configuration file name within the .claude directory.
const mcpServersFileName = "mcp_servers.json"

// mcpProjectFileName is the project-level MCP config file at the project root.
// This mirrors the reference's .mcp.json convention. It is searched from
// projectDir upward through parent directories.
const mcpProjectFileName = ".mcp.json"

// mcpConfigFileJSON represents the on-disk JSON structure of mcp_servers.json.
type mcpConfigFileJSON struct {
	MCPServers    map[string]mcpServerJSON `json:"mcpServers"`
	GlobalTimeout *int                     `json:"globalTimeout,omitempty"`
}

// mcpServerJSON represents a single server entry in the JSON config file.
type mcpServerJSON struct {
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Timeout  *int              `json:"timeout,omitempty"`
	Type     string            `json:"type,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// LoadMCPConfig loads MCP server configuration by merging multiple sources
// in priority order (later sources override earlier ones):
//
//  1. ~/.claude/mcp_servers.json (user-level)
//  2. <projectDir>/.claude/mcp_servers.json (project-level, legacy path)
//  3. .mcp.json found by walking from projectDir toward fs root (project-level, reference path)
//
// Environment variables in command, args, and env values are resolved.
func LoadMCPConfig(projectDir string) (*MCPConfig, error) {
	// Load user-level config.
	userConfig, err := loadMCPConfigFile(userMCPConfigPath())
	if err != nil {
		return nil, err
	}

	// Load project-level config (legacy path: .claude/mcp_servers.json).
	projectConfig, err := loadMCPConfigFile(projectMCPConfigPath(projectDir))
	if err != nil {
		return nil, err
	}

	// Load .mcp.json (reference-style path) by searching from projectDir upward.
	mcpJSONPath := findMCPJSONFile(projectDir)
	var mcpJSONConfig *mcpConfigFileJSON
	if mcpJSONPath != "" {
		mcpJSONConfig, err = loadMCPConfigFile(mcpJSONPath)
		if err != nil {
			return nil, err
		}
	}

	// Merge: user < legacy project < .mcp.json (highest priority).
	merged := mergeMCPFileConfigs(userConfig, projectConfig)
	merged = mergeMCPFileConfigs(merged, mcpJSONConfig)

	// Convert to MCPConfig with env var resolution.
	return buildMCPConfig(merged), nil
}

// ResolveEnvVars expands ${VAR} and $VAR references in a string using os.Getenv.
// Unset variables are replaced with an empty string.
func ResolveEnvVars(value string) string {
	// First handle ${VAR} syntax (with braces).
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	result := re.ReplaceAllStringFunc(value, func(match string) string {
		varName := match[2 : len(match)-1] // strip ${ and }
		return os.Getenv(varName)
	})

	// Then handle $VAR syntax (without braces).
	// Match $WORD where WORD is [A-Za-z_][A-Za-z0-9_]*
	re2 := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	result = re2.ReplaceAllStringFunc(result, func(match string) string {
		varName := match[1:] // strip leading $
		return os.Getenv(varName)
	})

	return result
}

// ValidateConfig checks that configured commands exist in PATH and returns
// a list of warning messages for missing or invalid configurations.
func ValidateConfig(config *MCPConfig) []string {
	if config == nil {
		return nil
	}

	var warnings []string

	for name, srv := range config.Servers {
		if srv.Command == "" {
			warnings = append(warnings, "server "+name+": command is empty")
			continue
		}

		// Check if command exists in PATH.
		_, err := exec.LookPath(srv.Command)
		if err != nil {
			warnings = append(warnings, "server "+name+": command "+srv.Command+" not found in PATH")
		}

		if !srv.Enabled {
			warnings = append(warnings, "server "+name+": server is disabled")
		}
	}

	return warnings
}

// ---------- Path helpers ----------

// userMCPConfigPath returns ~/.claude/mcp_servers.json.
func userMCPConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", mcpServersFileName)
}

// projectMCPConfigPath returns <projectDir>/.claude/mcp_servers.json.
func projectMCPConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", mcpServersFileName)
}

// findMCPJSONFile searches for .mcp.json starting from startDir and walking
// up to parent directories until the filesystem root. Returns the path to
// the first found file, or empty string if not found.
// This mirrors the reference behavior where .mcp.json is placed at a project
// root and discovered by parent traversal.
func findMCPJSONFile(startDir string) string {
	dir := startDir
	for {
		candidate := filepath.Join(dir, mcpProjectFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return ""
		}
		dir = parent
	}
}

// ---------- Internal loading ----------

// loadMCPConfigFile reads and parses a single mcp_servers.json file.
// Returns nil (not an error) if the file does not exist.
func loadMCPConfigFile(path string) (*mcpConfigFileJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg mcpConfigFileJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// mergeMCPFileConfigs merges two parsed config files. Project entries override
// user entries with the same server name. Global timeout from project takes
// precedence if set.
func mergeMCPFileConfigs(user, project *mcpConfigFileJSON) *mcpConfigFileJSON {
	merged := &mcpConfigFileJSON{
		MCPServers: make(map[string]mcpServerJSON),
	}

	// Apply user-level servers.
	if user != nil {
		for k, v := range user.MCPServers {
			merged.MCPServers[k] = v
		}
		merged.GlobalTimeout = user.GlobalTimeout
	}

	// Apply project-level servers (overrides user).
	if project != nil {
		for k, v := range project.MCPServers {
			merged.MCPServers[k] = v
		}
		if project.GlobalTimeout != nil {
			merged.GlobalTimeout = project.GlobalTimeout
		}
	}

	return merged
}

// buildMCPConfig converts the parsed JSON structure into the final MCPConfig,
// resolving environment variables in all string fields.
func buildMCPConfig(file *mcpConfigFileJSON) *MCPConfig {
	config := &MCPConfig{
		Servers:       make(map[string]*MCPServerConfig),
		GlobalTimeout: defaultGlobalTimeout,
	}

	if file == nil {
		return config
	}

	if file.GlobalTimeout != nil && *file.GlobalTimeout > 0 {
		config.GlobalTimeout = time.Duration(*file.GlobalTimeout) * time.Second
	}

	for name, srv := range file.MCPServers {
		serverCfg := &MCPServerConfig{
			Name:    name,
			Command: ResolveEnvVars(srv.Command),
			Enabled: !srv.Disabled,
			Timeout: config.GlobalTimeout,
			Type:    srv.Type,
			URL:     srv.URL,
		}

		// Resolve args.
		serverCfg.Args = resolveEnvSlice(srv.Args)

		// Resolve env values.
		serverCfg.Env = resolveEnvMap(srv.Env)

		// Resolve CWD.
		if srv.CWD != "" {
			serverCfg.CWD = ResolveEnvVars(srv.CWD)
		}

		// Server-specific timeout overrides global.
		if srv.Timeout != nil && *srv.Timeout > 0 {
			serverCfg.Timeout = time.Duration(*srv.Timeout) * time.Second
		}

		// Copy headers (resolve env vars in values).
		if len(srv.Headers) > 0 {
			serverCfg.Headers = make(map[string]string, len(srv.Headers))
			for k, v := range srv.Headers {
				serverCfg.Headers[k] = ResolveEnvVars(v)
			}
		}

		config.Servers[name] = serverCfg
	}

	return config
}

// resolveEnvSlice resolves environment variables in each element of a string slice.
func resolveEnvSlice(values []string) []string {
	if values == nil {
		return nil
	}
	resolved := make([]string, len(values))
	for i, v := range values {
		resolved[i] = ResolveEnvVars(v)
	}
	return resolved
}

// resolveEnvMap resolves environment variables in each value of a string map.
func resolveEnvMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	resolved := make(map[string]string, len(m))
	for k, v := range m {
		resolved[k] = ResolveEnvVars(v)
	}
	return resolved
}

// NormalizeNameForMCP sanitizes a server or tool name for use in MCP identifiers.
// It replaces characters outside [a-zA-Z0-9_-] with underscores, collapses
// consecutive underscores for names starting with "claude.ai ", and truncates
// to 64 characters. Mirrors reference normalization.ts.
func NormalizeNameForMCP(name string) string {
	// Replace invalid chars with underscore.
	normalized := nameInvalidChars.ReplaceAllString(name, "_")
	// For claude.ai names, collapse runs of underscores and trim leading/trailing.
	if strings.HasPrefix(name, "claude.ai ") {
		normalized = nameMultiUnderscore.ReplaceAllString(normalized, "_")
		normalized = strings.Trim(normalized, "_")
	}
	// Truncate to 64 characters.
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

var (
	nameInvalidChars    = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	nameMultiUnderscore = regexp.MustCompile(`_+`)
)

// ToServerConfig converts an MCPServerConfig to the simpler ServerConfig
// used by MCPClient. This bridges config loading with client construction.
func (c *MCPServerConfig) ToServerConfig() ServerConfig {
	return ServerConfig{
		Name:    c.Name,
		Command: c.Command,
		Args:    c.Args,
		Env:     c.Env,
		CWD:     c.CWD,
		Timeout: c.Timeout,
		Type:    c.Type,
		URL:     c.URL,
		Headers: c.Headers,
	}
}

// EnabledServers returns only the servers that are enabled.
func (c *MCPConfig) EnabledServers() map[string]*MCPServerConfig {
	enabled := make(map[string]*MCPServerConfig)
	for name, srv := range c.Servers {
		if srv.Enabled {
			enabled[name] = srv
		}
	}
	return enabled
}

// ServerNames returns the names of all configured servers (sorted is not guaranteed).
func (c *MCPConfig) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	return names
}

// AddServerToProjectConfig adds a new server entry to the project-level .mcp.json file.
// Creates the file if it does not exist. Returns an error if the server already exists.
func AddServerToProjectConfig(projectDir, name, command string, args []string, env map[string]string) error {
	if err := ValidateServerName(name); err != nil {
		return err
	}

	configPath := filepath.Join(projectDir, mcpProjectFileName)

	// Load existing or create empty.
	cfg, err := loadMCPConfigFile(configPath)
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &mcpConfigFileJSON{MCPServers: make(map[string]mcpServerJSON)}
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]mcpServerJSON)
	}

	if _, exists := cfg.MCPServers[name]; exists {
		return fmt.Errorf("mcp server %q already exists in %s", name, configPath)
	}

	cfg.MCPServers[name] = mcpServerJSON{
		Command: command,
		Args:    args,
		Env:     env,
	}

	return writeMCPConfigFile(configPath, cfg)
}

// RemoveServerFromProjectConfig removes a server from the project-level .mcp.json file.
// Returns an error if the server does not exist.
func RemoveServerFromProjectConfig(projectDir, name string) error {
	configPath := filepath.Join(projectDir, mcpProjectFileName)

	cfg, err := loadMCPConfigFile(configPath)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.MCPServers == nil {
		return fmt.Errorf("mcp server %q not found in config", name)
	}

	if _, exists := cfg.MCPServers[name]; !exists {
		return fmt.Errorf("mcp server %q not found in %s", name, configPath)
	}

	delete(cfg.MCPServers, name)
	return writeMCPConfigFile(configPath, cfg)
}

// ValidateServerName checks that a server name contains only valid characters.
func ValidateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name cannot be empty")
	}
	if nameInvalidChars.MatchString(name) {
		return fmt.Errorf("invalid server name %q: names can only contain letters, numbers, hyphens, and underscores", name)
	}
	return nil
}

// writeMCPConfigFile writes the config JSON to disk atomically.
func writeMCPConfigFile(path string, cfg *mcpConfigFileJSON) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Ensure parent dir exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// IsEmpty returns true if no servers are configured.
func (c *MCPConfig) IsEmpty() bool {
	return len(c.Servers) == 0
}

// String returns a human-readable summary (used for debugging).
func (c *MCPConfig) String() string {
	if c == nil || len(c.Servers) == 0 {
		return "MCPConfig{servers: none}"
	}

	var b strings.Builder
	b.WriteString("MCPConfig{servers: [")
	first := true
	for name, srv := range c.Servers {
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(name)
		if !srv.Enabled {
			b.WriteString("(disabled)")
		}
	}
	b.WriteString("]}")
	return b.String()
}
