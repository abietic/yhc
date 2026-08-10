// Package statepath resolves YHC canonical and supported legacy state roots.
package statepath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/internal/identity"
)

// Source identifies how one effective state path was selected.
type Source uint8

const (
	SourceDefault Source = iota
	SourceCanonicalEnv
	SourceLegacyEnv
)

// Roots binds one canonical YHC root to its immutable legacy peer.
type Roots struct {
	Canonical string
	Legacy    string
}

// Selection describes one effective state path without exposing environment
// values in diagnostics.
type Selection struct {
	Effective  string
	Roots      Roots
	Source     Source
	Migratable bool
}

// ProjectRoots resolves the canonical and legacy roots for one project.
func ProjectRoots(projectRoot string) (Roots, error) {
	return rootsFor(projectRoot)
}

// UserRoots resolves the canonical and legacy roots for one user home.
func UserRoots(userHome string) (Roots, error) {
	return rootsFor(userHome)
}

// ResolveOverride selects a canonical environment name before its legacy
// alias. A present empty value selects the canonical default; a non-empty
// explicit path remains exact and is never eligible for automatic migration.
func ResolveOverride(pair identity.EnvPair, defaults Roots) (Selection, error) {
	if err := validateDefaults(defaults); err != nil {
		return Selection{}, err
	}
	if strings.TrimSpace(pair.Canonical) == "" ||
		strings.TrimSpace(pair.Legacy) == "" ||
		pair.Canonical == pair.Legacy {
		return Selection{}, errors.New("state environment pair is invalid")
	}

	value, envSource, present := identity.LookupEnv(pair)
	if !present {
		return defaultSelection(defaults, SourceDefault), nil
	}

	source := SourceLegacyEnv
	if envSource == identity.EnvCanonical {
		source = SourceCanonicalEnv
	}
	if value == "" {
		return defaultSelection(defaults, source), nil
	}
	if !validExactOverride(value) {
		return Selection{}, errors.New("state path override is invalid")
	}
	return Selection{
		Effective:  value,
		Roots:      defaults,
		Source:     source,
		Migratable: false,
	}, nil
}

func rootsFor(base string) (Roots, error) {
	canonical, err := canonicalizeInput(base)
	if err != nil {
		return Roots{}, err
	}
	return Roots{
		Canonical: filepath.Join(canonical, identity.ProjectDirName),
		Legacy:    filepath.Join(canonical, identity.LegacyDirName),
	}, nil
}

func canonicalizeInput(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
		return "", errors.New("state root input is invalid")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", errors.New("state root input is invalid")
	}
	current := filepath.Clean(absolute)
	missing := make([]string, 0)
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", errors.New("state root input is invalid")
			}
			resolvedInfo, resolveErr := os.Stat(resolved)
			if resolveErr != nil || !resolvedInfo.IsDir() ||
				(info.Mode()&os.ModeSymlink == 0 && !info.IsDir()) {
				return "", errors.New("state root input is invalid")
			}
			resolved = filepath.Clean(resolved)
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.New("state root input is invalid")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("state root input is invalid")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func validateDefaults(defaults Roots) error {
	if !validExactOverride(defaults.Canonical) ||
		!validExactOverride(defaults.Legacy) ||
		filepath.Clean(defaults.Canonical) == filepath.Clean(defaults.Legacy) {
		return errors.New("default state roots are invalid")
	}
	return nil
}

func validExactOverride(value string) bool {
	return value != "" && !strings.ContainsRune(value, '\x00') && filepath.IsAbs(value)
}

func defaultSelection(defaults Roots, source Source) Selection {
	return Selection{
		Effective:  defaults.Canonical,
		Roots:      defaults,
		Source:     source,
		Migratable: true,
	}
}
