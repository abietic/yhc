package engine

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/abietic/yhc/engine/commands"
	enginecfg "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/plugins"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
)

// InspectionAdministrationConfig contains only the local, side-effect-free
// owners needed by diagnostics and extension CLI projections.
type InspectionAdministrationConfig struct {
	CWD                     string
	TranscriptDir           string
	Model                   string
	FallbackModel           string
	ModelResolver           ModelResolver
	PermissionMode          permission.Mode
	ToolRegistry            *tools.Registry
	MCPManager              *tools.MCPToolManager
	PluginDirs              []string
	DisableBundledWorkflows bool
	Clock                   func() time.Time
}

// NewInspectionAdministrationEngine creates a provider-free, connection-free
// QueryEngine host for the existing diagnostic, MCP inventory, and prompt
// command generation services. It loads one local prompt-command generation,
// but skips provider construction, MCP connection, skills, hooks, settings
// watchers, worktree recovery, Graph compilation, and long-lived services.
func NewInspectionAdministrationEngine(config InspectionAdministrationConfig) *QueryEngine {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	transcriptDir := config.TranscriptDir
	if transcriptDir == "" {
		transcriptDir = filepath.Join(config.CWD, identity.ProjectDirName, "transcripts")
	}
	toolRegistry := config.ToolRegistry
	if toolRegistry == nil {
		toolRegistry = tools.NewRegistry()
		tools.RegisterDefaults(toolRegistry)
	}
	mcpManager := config.MCPManager
	ownsMCPManager := mcpManager == nil
	if mcpManager == nil {
		mcpManager = tools.NewMCPToolManager()
	}

	eng := &QueryEngine{
		config: QueryEngineConfig{
			SessionID:               "inspection",
			ThreadID:                "inspection",
			RootSessionID:           "inspection",
			CWD:                     config.CWD,
			TranscriptDir:           transcriptDir,
			MemoryProjectRoot:       config.CWD,
			PermissionProjectRoot:   config.CWD,
			Model:                   config.Model,
			FallbackModel:           config.FallbackModel,
			ModelResolver:           config.ModelResolver,
			PermissionMode:          config.PermissionMode,
			ToolRegistry:            toolRegistry,
			MCPManager:              mcpManager,
			PluginDirs:              append([]string(nil), config.PluginDirs...),
			DisableBundledWorkflows: config.DisableBundledWorkflows,
			Clock:                   clock,
			CommandEntrypoint:       commands.EntrypointAdministration,
		},
		toolRegistry:       toolRegistry,
		mcpManager:         mcpManager,
		ownsMCPManager:     ownsMCPManager,
		pluginDirs:         resolvePromptCommandDirs(config.PluginDirs, config.CWD),
		sessionStartedAt:   clock().UTC(),
		sessionStatus:      "inspection",
		asyncHookEvents:    make(chan QueryEvent, 64),
		asyncHookDone:      make(chan struct{}),
		administrationOnly: true,
	}
	eng.ensureCommandRegistry()
	_, _ = eng.ReloadPromptCommands()
	return eng
}

func resolvePromptCommandDirs(configured []string, cwd string) []string {
	dirs := append([]string(nil), configured...)
	if len(dirs) != 0 {
		return dirs
	}
	dirs = plugins.DefaultPluginDirs(enginecfg.UserConfigDir(), cwd)
	projectPluginDir := filepath.Join(cwd, ".claude", "plugins")
	if !slices.Contains(dirs, projectPluginDir) {
		dirs = append(dirs, projectPluginDir)
	}
	return dirs
}
