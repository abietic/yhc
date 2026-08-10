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

// Version is overridden for release builds through -ldflags -X.
var Version = "0.1.0"

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

// Current reads the build identity once from the Go runtime. Missing VCS
// metadata remains explicit instead of being inferred from the working tree.
func Current() Info {
	info := Info{
		SchemaVersion: SchemaVersion,
		Version:       strings.TrimPrefix(strings.TrimSpace(Version), "v"),
		Commit:        "unknown",
		BuildTime:     "unknown",
		GoVersion:     runtime.Version(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	if info.Version == "" {
		info.Version = "unknown"
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Commit = setting.Value
		case "vcs.time":
			info.BuildTime = setting.Value
		case "vcs.modified":
			info.Modified = setting.Value == "true"
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
