package permission

import (
	"errors"
	"os"
	"path/filepath"
)

// SetupOptions configures the initial permission context.
//
// Reference: src/utils/permissions/permissionSetup.ts (1,532 lines)
type SetupOptions struct {
	CWD             string
	HomeDir         string
	Mode            Mode
	ProjectSettings string
	UserSettings    string
	CLIFlags        []PermissionRule
}

// SetupResult contains the fully initialized permission context.
type SetupResult struct {
	Rules      []PermissionRule
	Mode       Mode
	Evaluator  *Evaluator
	Store      *SessionStore
	Approvals  *ApprovalTracker
	Killswitch *BypassKillswitch
}

// SetupPermissions constructs the full initial permission context from all
// settings sources (project, user, CLI flags, policy).
func SetupPermissions(opts SetupOptions) (*SetupResult, error) {
	if opts.HomeDir == "" {
		opts.HomeDir, _ = os.UserHomeDir()
	}

	// Load rules from all sources
	rules, err := LoadPermissionRules(opts.CWD)
	if err != nil {
		rules = nil
	}

	// Add CLI flag rules (highest priority)
	rules = append(rules, opts.CLIFlags...)

	// Load user-level rules
	userRulesPath := filepath.Join(opts.HomeDir, ".claude", "settings.json")
	if userRules, err := LoadPermissionRules(userRulesPath); err == nil {
		rules = append(userRules, rules...)
	}

	// Create the rules engine with precedence resolution

	// Initialize session store for persisted decisions
	store := NewSessionStore()

	// Initialize approval tracker
	approvals := NewApprovalTracker()
	approvalsPath, err := ApprovalStorePath(opts.CWD)
	if err != nil {
		return nil, errors.New("resolve persistent approval store failed")
	}
	if err := approvals.LoadFrom(approvalsPath); err != nil {
		return nil, errors.New("load persistent approvals failed")
	}

	// Initialize bypass killswitch
	killswitch := NewBypassKillswitch(func() bool {
		return false // Default: bypass is allowed
	})

	// Create evaluator
	evaluator := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Rules:        rules,
		Mode:         opts.Mode,
		WorkingDir:   opts.CWD,
	})

	return &SetupResult{
		Rules:      rules,
		Mode:       opts.Mode,
		Evaluator:  evaluator,
		Store:      store,
		Approvals:  approvals,
		Killswitch: killswitch,
	}, nil
}

// ValidateWorkspaceDirectory checks if a directory is safe for workspace use.
func ValidateWorkspaceDirectory(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.ErrNotExist
	}
	return nil
}
