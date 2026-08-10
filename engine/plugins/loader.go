package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/engine/skills"
)

// Plugin represents a loaded plugin with its metadata and capabilities.
//
// Reference: src/utils/plugins/ (~50 files)
type Plugin struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Directory   string            `json:"directory"`
	Enabled     bool              `json:"enabled"`
	Skills      []PluginSkill     `json:"skills,omitempty"`
	Commands    []PluginCommand   `json:"commands,omitempty"`
	Hooks       []PluginHook      `json:"hooks,omitempty"`
	MCPServers  []PluginMCPServer `json:"mcpServers,omitempty"`

	directoryIdentity    pluginDirectoryIdentity
	materializedCommands []materializedPluginCommand
}

// PluginSkill is a skill contributed by a plugin.
type PluginSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
}

// PluginCommand is a command contributed by a plugin.
type PluginCommand struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description"`
	FilePath    string   `json:"filePath,omitempty"`
}

// PluginHook is a hook contributed by a plugin.
type PluginHook struct {
	Event         string `json:"event"`
	Type          string `json:"type"` // "command", "http", "prompt"
	Command       string `json:"command,omitempty"`
	Matcher       string `json:"matcher,omitempty"`
	Timeout       int    `json:"timeout,omitempty"` // seconds
	Async         bool   `json:"async,omitempty"`
	AsyncRewake   bool   `json:"asyncRewake,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

// PluginMCPServer is an MCP server contributed by a plugin.
type PluginMCPServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// PluginManifest is the plugin.json file format.
type PluginManifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Skills      []PluginSkill     `json:"skills,omitempty"`
	Commands    []PluginCommand   `json:"commands,omitempty"`
	Hooks       []PluginHook      `json:"hooks,omitempty"`
	MCPServers  []PluginMCPServer `json:"mcpServers,omitempty"`
	Options     []PluginOption    `json:"options,omitempty"`
}

// Loader discovers and loads plugins from configured directories.
type Loader struct {
	dirs                    []string
	disableBundledWorkflows bool
	bundledWorkflowData     []byte
	mu                      sync.RWMutex
	plugins                 map[string]*Plugin
}

// GenerationError reports every invalid source in a rejected candidate.
// Callers must keep the prior live generation when this error is returned.
type GenerationError struct {
	Diagnostics []commands.PluginDiagnostic
}

func (e *GenerationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "plugin generation rejected"
	}
	parts := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		if diagnostic.Severity != "error" {
			continue
		}
		source := diagnostic.Source
		if source == "" {
			source = diagnostic.Plugin
		}
		if source == "" {
			source = "plugin source"
		}
		parts = append(parts, source+": "+diagnostic.Message)
	}
	if len(parts) == 0 {
		return "plugin generation rejected"
	}
	return fmt.Sprintf(
		"plugin generation rejected with %d error(s): %s",
		len(parts),
		strings.Join(parts, "; "),
	)
}

// NewLoader creates a plugin loader for the given directories.
func NewLoader(dirs ...string) *Loader {
	return NewLoaderWithOptions(LoaderOptions{Dirs: dirs})
}

// NewLoaderWithOptions creates a loader over one deterministic prompt-command
// source set. The embedded bundled pack is enabled unless explicitly disabled.
func NewLoaderWithOptions(opts LoaderOptions) *Loader {
	bundledData := opts.BundledWorkflowData
	if bundledData == nil {
		bundledData = defaultBundledWorkflowData
	}
	return &Loader{
		dirs:                    append([]string(nil), opts.Dirs...),
		disableBundledWorkflows: opts.DisableBundledWorkflows,
		bundledWorkflowData:     append([]byte(nil), bundledData...),
		plugins:                 make(map[string]*Plugin),
	}
}

// DefaultPluginDirs returns the standard plugin search directories.
func DefaultPluginDirs(configDir, cwd string) []string {
	var dirs []string
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "plugins"))
	}
	projectPlugins := filepath.Join(cwd, ".claude", "plugins")
	if info, err := os.Stat(projectPlugins); err == nil && info.IsDir() {
		dirs = append(dirs, projectPlugins)
	}
	return dirs
}

// Load discovers and loads all plugins from configured directories.
func (l *Loader) Load() error {
	next, diagnostics := l.discover()
	if hasDiagnosticErrors(diagnostics) {
		return &GenerationError{Diagnostics: diagnostics}
	}
	l.mu.Lock()
	l.plugins = next
	l.mu.Unlock()
	return nil
}

func (l *Loader) discover() (
	map[string]*Plugin,
	[]commands.PluginDiagnostic,
) {
	next := make(map[string]*Plugin)
	var diagnostics []commands.PluginDiagnostic
	for _, dir := range l.dirs {
		source, err := openPluginSourceAuthority(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			diagnostics = append(diagnostics, commands.PluginDiagnostic{
				Source:   dir,
				Severity: "error",
				Code:     "read_source",
				Message:  err.Error(),
			})
			continue
		}
		entries, err := source.readDir()
		if err != nil {
			diagnostics = append(diagnostics, commands.PluginDiagnostic{
				Source:   dir,
				Severity: "error",
				Code:     "read_source",
				Message:  err.Error(),
			})
			_ = source.Close()
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pluginDir := filepath.Join(dir, entry.Name())
			entryInfo, err := entry.Info()
			if err != nil || !entryInfo.IsDir() {
				if err == nil {
					err = fmt.Errorf(
						"plugins: plugin directory changed during discovery",
					)
				}
				diagnostics = append(diagnostics, commands.PluginDiagnostic{
					Source:   pluginDir,
					Plugin:   entry.Name(),
					Severity: "error",
					Code:     "load_manifest",
					Message:  err.Error(),
				})
				continue
			}
			authority, err := source.openPluginWithExpectedIdentity(
				entry.Name(),
				entryInfo,
			)
			if err != nil {
				diagnostics = append(diagnostics, commands.PluginDiagnostic{
					Source:   pluginDir,
					Plugin:   entry.Name(),
					Severity: "error",
					Code:     "load_manifest",
					Message:  err.Error(),
				})
				continue
			}
			plugin, err := l.loadPluginFromAuthority(authority)
			_ = authority.Close()
			if err != nil {
				diagnostics = append(diagnostics, commands.PluginDiagnostic{
					Source:   pluginDir,
					Plugin:   entry.Name(),
					Severity: "error",
					Code:     "load_manifest",
					Message:  err.Error(),
				})
				continue
			}
			for _, materialized := range plugin.materializedCommands {
				if materialized.err == nil {
					continue
				}
				diagnostics = append(diagnostics, commands.PluginDiagnostic{
					Source:   plugin.Directory,
					Plugin:   plugin.Name,
					Severity: "error",
					Code:     "invalid_command",
					Message:  materialized.err.Error(),
				})
			}
			key := pluginKey(plugin.Name)
			if previous := next[key]; previous != nil {
				diagnostics = append(diagnostics, commands.PluginDiagnostic{
					Source:   pluginDir,
					Plugin:   plugin.Name,
					Severity: "info",
					Code:     "source_override",
					Message: fmt.Sprintf(
						"source overrides %s by configured precedence",
						previous.Directory,
					),
				})
			}
			next[key] = plugin
		}
		_ = source.Close()
	}
	return next, diagnostics
}

func (l *Loader) loadPlugin(dir string) (*Plugin, error) {
	authority, err := openStandalonePluginAuthority(dir)
	if err != nil {
		return nil, err
	}
	defer authority.Close()
	return l.loadPluginFromAuthority(authority)
}

func (l *Loader) loadPluginFromAuthority(
	authority *pluginFileAuthority,
) (*Plugin, error) {
	data, err := authority.readRegularFile("plugin.json")
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	if manifest.Name == "" {
		manifest.Name = authority.identity.entryName
	}

	plugin := &Plugin{
		Name:              manifest.Name,
		Version:           manifest.Version,
		Description:       manifest.Description,
		Directory:         authority.displayDir,
		Enabled:           true,
		Skills:            manifest.Skills,
		Commands:          manifest.Commands,
		Hooks:             manifest.Hooks,
		MCPServers:        manifest.MCPServers,
		directoryIdentity: authority.identity,
	}
	plugin.materializedCommands = make(
		[]materializedPluginCommand,
		0,
		len(plugin.Commands),
	)
	for _, spec := range plugin.Commands {
		command, material, err := buildPluginCommand(
			authority,
			plugin,
			spec,
		)
		plugin.materializedCommands = append(
			plugin.materializedCommands,
			materializedPluginCommand{
				spec:     spec,
				command:  command,
				material: material,
				err:      err,
			},
		)
	}
	return plugin, nil
}

// Get returns a plugin by name.
func (l *Loader) Get(name string) (*Plugin, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	p, ok := l.plugins[pluginKey(name)]
	return p, ok
}

// List returns all loaded plugins.
func (l *Loader) List() []*Plugin {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]*Plugin, 0, len(l.plugins))
	for _, p := range l.plugins {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return pluginKey(result[i].Name) < pluginKey(result[j].Name)
	})
	return result
}

// Enable enables a plugin by name.
func (l *Loader) Enable(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.plugins[pluginKey(name)]; ok {
		p.Enabled = true
		return true
	}
	return false
}

// Disable disables a plugin by name.
func (l *Loader) Disable(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.plugins[pluginKey(name)]; ok {
		p.Enabled = false
		return true
	}
	return false
}

func pluginKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Validate checks a plugin manifest for correctness.
func Validate(dir string) error {
	authority, err := openStandalonePluginAuthority(dir)
	if err != nil {
		return fmt.Errorf("missing plugin.json")
	}
	defer authority.Close()
	data, err := authority.readRegularFile("plugin.json")
	if err != nil {
		return fmt.Errorf("missing plugin.json")
	}
	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid plugin.json: %w", err)
	}
	if manifest.Name == "" {
		return fmt.Errorf("plugin name is required")
	}
	return nil
}

// RegisterSkills loads enabled plugin-contributed skills into the provided
// skill registry. It supports both explicit manifest skill file paths and the
// conventional plugin-local skills/ directory.
func (l *Loader) RegisterSkills(registry *skills.SkillRegistry) error {
	if l == nil || registry == nil {
		return nil
	}
	var candidate skills.Snapshot
	for _, plugin := range l.List() {
		if plugin == nil || !plugin.Enabled {
			continue
		}
		snapshot, err := collectPluginSkills(plugin)
		if err != nil {
			return err
		}
		appendSkillSnapshot(&candidate, snapshot)
	}
	registry.MergeSnapshot(candidate)
	return nil
}

// RegisterShellHooks appends enabled plugin command hooks into a shell hook
// configuration. Only runtime-facing shell hook phases supported by the Go
// shell hook runner are wired here.
func (l *Loader) RegisterShellHooks(config *hooks.ShellHookConfig) error {
	if l == nil || config == nil {
		return nil
	}
	for _, plugin := range l.List() {
		if plugin == nil || !plugin.Enabled {
			continue
		}
		for _, pluginHook := range plugin.Hooks {
			shellHook, err := pluginHookToShellHook(plugin, pluginHook)
			if err != nil {
				return err
			}
			if shellHook == nil {
				continue
			}
			switch hooks.HookEvent(pluginHook.Event) {
			case hooks.HookEventPreToolUse:
				config.PreToolHooks = append(config.PreToolHooks, *shellHook)
			case hooks.HookEventPostToolUse:
				config.PostToolHooks = append(config.PostToolHooks, *shellHook)
			case hooks.HookEventUserPromptSubmit:
				config.UserPromptHooks = append(config.UserPromptHooks, *shellHook)
			}
		}
	}
	return nil
}

func pluginHookToShellHook(plugin *Plugin, pluginHook PluginHook) (*hooks.ShellHook, error) {
	if pluginHook.Type != "" && pluginHook.Type != string(hooks.HookCommandTypeCommand) {
		return nil, nil
	}
	event := hooks.HookEvent(pluginHook.Event)
	switch event {
	case hooks.HookEventPreToolUse, hooks.HookEventPostToolUse, hooks.HookEventUserPromptSubmit:
	default:
		return nil, nil
	}
	if pluginHook.Command == "" {
		return nil, fmt.Errorf("plugins: hook for plugin %s event %s is missing command", plugin.Name, pluginHook.Event)
	}
	timeout := hooks.DefaultShellHookTimeout
	if pluginHook.Timeout > 0 {
		timeout = time.Duration(pluginHook.Timeout) * time.Second
	}
	var phase string
	switch event {
	case hooks.HookEventPostToolUse:
		phase = "post"
	case hooks.HookEventUserPromptSubmit:
		phase = "user_prompt"
	default:
		phase = "pre"
	}
	return &hooks.ShellHook{
		Command:       pluginHook.Command,
		Timeout:       timeout,
		Phase:         phase,
		ToolPattern:   pluginHook.Matcher,
		Async:         pluginHook.Async || pluginHook.AsyncRewake,
		AsyncRewake:   pluginHook.AsyncRewake,
		StatusMessage: pluginHook.StatusMessage,
	}, nil
}

// RegisterMCPServers merges enabled plugin-contributed MCP server declarations
// into cfg. This covers inline plugin.json MCP server declarations only; MCPB
// files, marketplace config, and user option substitution remain higher-level
// plugin workflows.
func (l *Loader) RegisterMCPServers(cfg *mcp.MCPConfig) error {
	if l == nil || cfg == nil {
		return nil
	}
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]*mcp.MCPServerConfig)
	}
	for _, plugin := range l.List() {
		if plugin == nil || !plugin.Enabled {
			continue
		}
		for _, server := range plugin.MCPServers {
			if strings.TrimSpace(server.Name) == "" {
				return fmt.Errorf("plugins: MCP server in plugin %s is missing name", plugin.Name)
			}
			cfg.Servers[server.Name] = pluginMCPServerToConfig(plugin, server)
		}
	}
	return nil
}

func pluginMCPServerToConfig(plugin *Plugin, server PluginMCPServer) *mcp.MCPServerConfig {
	enabled := true
	if server.Enabled != nil {
		enabled = *server.Enabled
	}
	cwd := server.CWD
	if cwd == "" {
		cwd = plugin.Directory
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(plugin.Directory, cwd)
	}
	return &mcp.MCPServerConfig{
		Name:    server.Name,
		Command: server.Command,
		Args:    append([]string(nil), server.Args...),
		Env:     cloneStringMap(server.Env),
		CWD:     cwd,
		Enabled: enabled,
		Type:    server.Type,
		URL:     server.URL,
		Headers: cloneStringMap(server.Headers),
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// RegisterCommands registers enabled plugin-contributed prompt commands into
// the command registry. The command action is ActionPrompt and the output
// contains either the declared command markdown file body or the command
// description as a bounded prompt payload.
func (l *Loader) RegisterCommands(registry *commands.Registry) error {
	if l == nil || registry == nil {
		return nil
	}
	candidate, err := l.BuildCommandGeneration()
	if err != nil {
		return err
	}
	_, err = registry.ReplacePromptCommandGeneration(candidate)
	return err
}

// BuildCommandGeneration validates every configured plugin source and prompt
// command before publishing a complete candidate. It aggregates diagnostics
// instead of stopping after the first malformed source.
func (l *Loader) BuildCommandGeneration() (
	commands.PromptCommandGenerationCandidate,
	error,
) {
	if l == nil {
		return commands.PromptCommandGenerationCandidate{}, nil
	}

	next, diagnostics := l.discover()
	l.mu.RLock()
	for key, current := range l.plugins {
		if current != nil && !current.Enabled {
			if candidate := next[key]; candidate != nil {
				candidate.Enabled = false
			}
		}
	}
	l.mu.RUnlock()
	plugins := sortedPlugins(next)
	sources := make(
		[]commands.PromptCommandSourceSnapshot,
		0,
		len(plugins)+1,
	)
	var promptCommands []*commands.Command
	hasher := sha256.New()
	if !l.disableBundledWorkflows {
		bundledCommands, source, material, err := buildBundledWorkflowPack(
			l.bundledWorkflowData,
		)
		if err != nil {
			diagnostics = append(diagnostics, commands.PromptCommandDiagnostic{
				Source:   bundledWorkflowLocation,
				Plugin:   "yhc-workflows",
				Severity: "error",
				Code:     "invalid_bundled_workflow_pack",
				Message:  err.Error(),
			})
		} else {
			promptCommands = append(promptCommands, bundledCommands...)
			sources = append(sources, source)
			_, _ = hasher.Write([]byte(source.Kind))
			_, _ = hasher.Write([]byte{0})
			_, _ = hasher.Write([]byte(source.Name))
			_, _ = hasher.Write([]byte{0})
			_, _ = hasher.Write([]byte(source.Version))
			_, _ = hasher.Write([]byte{0})
			_, _ = hasher.Write(material)
			_, _ = hasher.Write([]byte{0})
		}
	}
	for _, plugin := range plugins {
		if plugin == nil || !plugin.Enabled {
			continue
		}
		source := commands.PromptCommandSourceSnapshot{
			Kind:       commands.CommandSourcePlugin,
			Trust:      commands.CommandTrustConfigured,
			Name:       plugin.Name,
			Version:    plugin.Version,
			Directory:  plugin.Directory,
			Commands:   len(plugin.Commands),
			Skills:     len(plugin.Skills),
			Hooks:      len(plugin.Hooks),
			MCPServers: len(plugin.MCPServers),
			Health:     "healthy",
		}
		pluginCommands := append(
			[]materializedPluginCommand(nil),
			plugin.materializedCommands...,
		)
		sort.SliceStable(pluginCommands, func(i, j int) bool {
			return strings.ToLower(pluginCommands[i].spec.Name) <
				strings.ToLower(pluginCommands[j].spec.Name)
		})
		for _, materialized := range pluginCommands {
			if materialized.err != nil {
				source.Health = "invalid"
				continue
			}
			if materialized.command != nil {
				promptCommands = append(
					promptCommands,
					materialized.command,
				)
				_, _ = hasher.Write([]byte(pluginKey(plugin.Name)))
				_, _ = hasher.Write([]byte{0})
				_, _ = hasher.Write([]byte(plugin.Version))
				_, _ = hasher.Write([]byte{0})
				_, _ = hasher.Write([]byte(materialized.material))
				_, _ = hasher.Write([]byte{0})
			}
		}
		sources = append(sources, source)
	}

	candidate := commands.PromptCommandGenerationCandidate{
		Digest:      fmt.Sprintf("%x", hasher.Sum(nil)),
		Commands:    promptCommands,
		Sources:     sources,
		Diagnostics: diagnostics,
	}
	if hasDiagnosticErrors(diagnostics) {
		return candidate, &GenerationError{
			Diagnostics: append([]commands.PluginDiagnostic(nil), diagnostics...),
		}
	}

	l.mu.Lock()
	l.plugins = next
	l.mu.Unlock()
	return candidate, nil
}

// Commands builds a deterministic snapshot of enabled plugin commands.
func (l *Loader) Commands() ([]*commands.Command, error) {
	if l == nil {
		return nil, nil
	}
	var result []*commands.Command
	for _, plugin := range l.List() {
		if plugin == nil || !plugin.Enabled {
			continue
		}
		pluginCommands := append(
			[]materializedPluginCommand(nil),
			plugin.materializedCommands...,
		)
		sort.SliceStable(pluginCommands, func(i, j int) bool {
			return strings.ToLower(pluginCommands[i].spec.Name) <
				strings.ToLower(pluginCommands[j].spec.Name)
		})
		for _, materialized := range pluginCommands {
			if materialized.err != nil {
				return nil, materialized.err
			}
			if materialized.command != nil {
				result = append(result, materialized.command)
			}
		}
	}
	return result, nil
}

func sortedPlugins(plugins map[string]*Plugin) []*Plugin {
	result := make([]*Plugin, 0, len(plugins))
	for _, plugin := range plugins {
		result = append(result, plugin)
	}
	sort.Slice(result, func(i, j int) bool {
		return pluginKey(result[i].Name) < pluginKey(result[j].Name)
	})
	return result
}

func hasDiagnosticErrors(diagnostics []commands.PluginDiagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

func buildPluginCommand(
	authority *pluginFileAuthority,
	plugin *Plugin,
	pluginCommand PluginCommand,
) (*commands.Command, string, error) {
	name := strings.TrimSpace(pluginCommand.Name)
	if name == "" {
		return nil, "", fmt.Errorf(
			"plugins: command in plugin %s is missing name",
			plugin.Name,
		)
	}
	content := strings.TrimSpace(pluginCommand.Description)
	if pluginCommand.FilePath != "" {
		data, err := authority.readRegularFile(pluginCommand.FilePath)
		if err != nil {
			return nil, "", fmt.Errorf(
				"plugins: read command file %q: %w",
				pluginCommand.FilePath,
				err,
			)
		}
		content = strings.TrimSpace(string(data))
	}
	if content == "" {
		content = fmt.Sprintf("Run plugin command %s from plugin %s.", name, plugin.Name)
	}
	description := pluginCommand.Description
	if description == "" {
		description = "Plugin command from " + plugin.Name
	}
	prefix := pluginKey(plugin.Name) + ":"
	qualifiedName := prefix + strings.ToLower(name)
	aliases := make([]string, 0, len(pluginCommand.Aliases))
	for _, alias := range pluginCommand.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return nil, "", fmt.Errorf(
				"plugins: command %s in plugin %s has an empty alias",
				name,
				plugin.Name,
			)
		}
		aliases = append(aliases, prefix+strings.ToLower(alias))
	}
	materialBytes, _ := json.Marshal(struct {
		Name        string
		Aliases     []string
		Description string
		Content     string
	}{
		Name:        qualifiedName,
		Aliases:     aliases,
		Description: description,
		Content:     content,
	})
	return &commands.Command{
		Name:           qualifiedName,
		Aliases:        aliases,
		Description:    description,
		Usage:          "/" + qualifiedName + " [arguments]",
		Source:         "plugin:" + plugin.Name,
		SourceVersion:  plugin.Version,
		Trust:          commands.CommandTrustConfigured,
		Kind:           commands.CommandKindPromptWorkflow,
		Entrypoints:    commands.EntrypointsTUI | commands.EntrypointsPlain,
		Availability:   commands.AvailabilitySupported,
		SideEffect:     commands.SideEffectNone,
		ResultKind:     commands.ResultKindPrompt,
		ExecutionOwner: commands.ExecutionOwnerEntrypoint,
		Execute: func(_ context.Context, ctx *commands.CommandContext) (*commands.CommandResult, error) {
			output := content
			args := strings.Join(ctx.Args, " ")
			if strings.TrimSpace(args) != "" {
				output += "\n\nArguments: " + strings.TrimSpace(args)
			}
			return &commands.CommandResult{
				Output: output,
				Action: commands.ActionPrompt,
				Data: map[string]any{
					"plugin":  plugin.Name,
					"command": name,
				},
			}, nil
		},
	}, string(materialBytes), nil
}
