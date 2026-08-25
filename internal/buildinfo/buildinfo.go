// Package buildinfo owns the process build identity projected by every
// supported entrypoint.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/abietic/yhc/internal/identity"
)

const SchemaVersion = 1

// Release builds override these values through -ldflags -X. Empty or unknown
// source values retain the Go runtime's VCS metadata as a development-build
// fallback.
var (
	Version   = "0.1.0"
	Commit    = "unknown"
	BuildTime = "unknown"
	Modified  = ""
)

// Dependency is one stable dependency identity in the build metadata.
type Dependency struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Info is the renderer-neutral, machine-readable build identity.
type Info struct {
	SchemaVersion int          `json:"schema_version"`
	Version       string       `json:"version"`
	Commit        string       `json:"commit"`
	BuildTime     string       `json:"build_time"`
	Modified      bool         `json:"modified"`
	GoVersion     string       `json:"go_version"`
	OS            string       `json:"os"`
	Arch          string       `json:"arch"`
	Dependencies  []Dependency `json:"dependencies,omitempty"`
}

var keyDependencies = []string{
	"github.com/cloudwego/eino",
	"charm.land/bubbletea/v2",
	"github.com/spf13/cobra",
}

// Current reads explicit release identity plus the Go runtime's build
// information. Missing VCS metadata remains explicit instead of being inferred
// from the working tree at process startup.
func Current() Info {
	build, ok := debug.ReadBuildInfo()
	return current(build, ok)
}

func current(build *debug.BuildInfo, ok bool) Info {
	info := Info{
		SchemaVersion: SchemaVersion,
		Version:       strings.TrimPrefix(strings.TrimSpace(Version), "v"),
		Commit:        normalizedSourceValue(Commit),
		BuildTime:     normalizedSourceValue(BuildTime),
		GoVersion:     runtime.Version(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	modified, modifiedSet := explicitModified(Modified)
	info.Modified = modified
	if info.Version == "" {
		info.Version = "unknown"
	}

	if !ok || build == nil {
		return info
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "unknown" {
				info.Commit = normalizedSourceValue(setting.Value)
			}
		case "vcs.time":
			if info.BuildTime == "unknown" {
				info.BuildTime = normalizedSourceValue(setting.Value)
			}
		case "vcs.modified":
			if !modifiedSet {
				info.Modified = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
			}
		}
	}
	for _, path := range keyDependencies {
		for _, dependency := range build.Deps {
			if dependency.Path == path {
				info.Dependencies = append(info.Dependencies, Dependency{
					Path:    dependency.Path,
					Version: dependency.Version,
				})
				break
			}
		}
	}
	return info
}

func normalizedSourceValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func explicitModified(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// ShortText is the stable single-line CLI version projection.
func ShortText(info Info) string {
	commit := info.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if info.Modified {
		commit += "+dirty"
	}
	return fmt.Sprintf("%s v%s (%s, %s/%s, %s)", identity.CommandName, info.Version, commit, info.OS, info.Arch, info.GoVersion)
}

// DetailedText preserves the detailed slash-command projection while sharing
// the same renderer-neutral identity as the CLI version command.
func DetailedText(info Info) string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s version information\n", identity.ProductName)
	output.WriteString("==============================\n\n")
	fmt.Fprintf(&output, "  Version:    v%s\n", info.Version)

	commit := info.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	fmt.Fprintf(&output, "  Commit:     %s", commit)
	if info.Modified {
		output.WriteString(" (dirty)")
	}
	output.WriteString("\n")
	fmt.Fprintf(&output, "  Built:      %s\n", info.BuildTime)
	fmt.Fprintf(&output, "  Go:         %s\n", info.GoVersion)
	fmt.Fprintf(&output, "  Platform:   %s/%s\n", info.OS, info.Arch)

	if len(info.Dependencies) > 0 {
		output.WriteString("\n  Key dependencies:\n")
		for _, dependency := range info.Dependencies {
			fmt.Fprintf(&output, "    %-40s %s\n", dependency.Path, dependency.Version)
		}
	}
	return output.String()
}
