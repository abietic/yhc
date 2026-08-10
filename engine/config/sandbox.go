package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	sandboxProfileWorkspaceWrite   = "workspace-write"
	sandboxProfileDangerFullAccess = "danger-full-access"
)

var sandboxBroadSystemRoots = map[string]struct{}{
	"/System": {}, "/usr": {}, "/bin": {}, "/sbin": {}, "/Library": {},
	"/Applications": {}, "/Users": {}, "/Volumes": {}, "/private": {},
	"/var": {}, "/etc": {}, "/opt": {},
}

// SandboxConfig is user-owned sandbox authority. A nil Config.Sandbox means
// the user did not configure sandbox authority.
type SandboxConfig struct {
	GuestProfile   string   `json:"guest_profile,omitempty"`
	ExtraReadRoots []string `json:"extra_read_roots,omitempty"`
}

// SandboxDiagnostic describes a redacted project authority rejection.
type SandboxDiagnostic struct {
	Code    string   `json:"code"`
	Level   string   `json:"level"`
	Source  string   `json:"source"`
	Keys    []string `json:"keys,omitempty"`
	Message string   `json:"message"`
}

// SandboxSelection is a pure, validated composition input for a later
// enforcement slice. It does not itself enforce sandboxing.
type SandboxSelection struct {
	GuestProfile   string
	ExtraReadRoots []string
	Source         SandboxSelectionSource
}

// SandboxSelectionSource records the only authority that selected or narrowed
// the sandbox profile. Project configuration never reaches this type.
type SandboxSelectionSource string

const (
	SandboxSelectionDefault    SandboxSelectionSource = "default"
	SandboxSelectionUserConfig SandboxSelectionSource = "user-config"
	SandboxSelectionCLI        SandboxSelectionSource = "cli"
)

// SandboxSelectionInput supplies explicit CLI intent and user configuration.
type SandboxSelectionInput struct {
	Config        *SandboxConfig
	CLIProfile    string
	CLIProfileSet bool
}

// ResolveSandbox selects an explicit CLI profile over user config. The safe
// default is platform-neutral workspace-write; an unsupported host becomes an
// unavailable binding later and never silently widens to ambient authority.
func ResolveSandbox(input SandboxSelectionInput) (SandboxSelection, error) {
	profile := ""
	roots := []string(nil)
	source := SandboxSelectionDefault
	if input.Config != nil {
		if err := validateSandboxConfig(input.Config); err != nil {
			return SandboxSelection{}, err
		}
		profile = input.Config.GuestProfile
		roots = append(roots, input.Config.ExtraReadRoots...)
		if profile != "" || len(roots) > 0 {
			source = SandboxSelectionUserConfig
		}
	}
	if input.CLIProfileSet {
		profile = input.CLIProfile
		source = SandboxSelectionCLI
	}
	if profile == "" {
		profile = sandboxProfileWorkspaceWrite
	}
	if !supportedSandboxProfile(profile) {
		return SandboxSelection{}, fmt.Errorf("sandbox.guest_profile: unsupported")
	}
	return SandboxSelection{
		GuestProfile:   profile,
		ExtraReadRoots: append([]string(nil), roots...),
		Source:         source,
	}, nil
}

func validateSandboxConfig(config *SandboxConfig) error {
	if config == nil {
		return nil
	}
	if config.GuestProfile != "" && !supportedSandboxProfile(config.GuestProfile) {
		return fmt.Errorf("sandbox.guest_profile: unsupported")
	}
	roots, err := canonicalSandboxRoots(config.ExtraReadRoots)
	if err != nil {
		return err
	}
	config.ExtraReadRoots = roots
	return nil
}

func supportedSandboxProfile(profile string) bool {
	return profile == sandboxProfileWorkspaceWrite || profile == sandboxProfileDangerFullAccess
}

func canonicalSandboxRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sandbox.extra_read_roots: invalid")
	}
	home = filepath.Clean(home)
	canonical := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, fmt.Errorf("sandbox.extra_read_roots: invalid")
		}
		cleaned := filepath.Clean(root)
		if cleaned == string(filepath.Separator) || isSandboxBroadSystemRoot(cleaned) || isSameOrAncestor(cleaned, home) {
			return nil, fmt.Errorf("sandbox.extra_read_roots: invalid")
		}
		info, err := os.Lstat(cleaned)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("sandbox.extra_read_roots: invalid")
		}
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil || resolved != cleaned {
			return nil, fmt.Errorf("sandbox.extra_read_roots: invalid")
		}
		canonical = append(canonical, cleaned)
	}
	sort.Strings(canonical)
	return compactSandboxRoots(canonical), nil
}

func isSandboxBroadSystemRoot(path string) bool {
	_, ok := sandboxBroadSystemRoots[path]
	return ok
}

func isSameOrAncestor(ancestor, path string) bool {
	if ancestor == path {
		return true
	}
	return strings.HasPrefix(path, ancestor+string(filepath.Separator))
}

func compactSandboxRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	result := roots[:1]
	for _, root := range roots[1:] {
		if root != result[len(result)-1] {
			result = append(result, root)
		}
	}
	return result
}
