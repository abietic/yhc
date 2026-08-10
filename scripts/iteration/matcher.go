package main

import (
	"fmt"
	"path"
	"strings"
)

func matchPathPattern(pattern, name string) (bool, error) {
	if err := validateRepositoryPathPattern(pattern); err != nil {
		return false, err
	}
	if err := validateRepositoryPath(name); err != nil {
		return false, err
	}

	patterns := strings.Split(pattern, "/")
	parts := strings.Split(name, "/")
	type key struct{ pattern, part int }
	memo := map[key]bool{}
	seen := map[key]bool{}
	var visit func(int, int) (bool, error)
	visit = func(i, j int) (bool, error) {
		state := key{i, j}
		if seen[state] {
			return memo[state], nil
		}
		seen[state] = true
		if i == len(patterns) {
			memo[state] = j == len(parts)
			return memo[state], nil
		}
		if patterns[i] == "**" {
			zero, err := visit(i+1, j)
			if err != nil || zero {
				memo[state] = zero
				return zero, err
			}
			if j < len(parts) {
				memo[state], err = visit(i, j+1)
				return memo[state], err
			}
			return false, nil
		}
		if j == len(parts) {
			return false, nil
		}
		matched, err := path.Match(patterns[i], parts[j])
		if err != nil || !matched {
			return false, err
		}
		memo[state], err = visit(i+1, j+1)
		return memo[state], err
	}
	return visit(0, 0)
}

func validateRepositoryPathPattern(pattern string) error {
	if pattern == "" || path.IsAbs(pattern) || strings.Contains(pattern, `\`) ||
		strings.Contains(pattern, "\x00") {
		return fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	if clean := path.Clean(pattern); clean != pattern || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("invalid repository path pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func validateRepositoryPath(name string) error {
	if strings.Contains(name, "\x00") || strings.Contains(name, `\`) {
		return fmt.Errorf("invalid repository path %q", name)
	}
	if clean := path.Clean(name); clean != name || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("invalid repository path %q", name)
	}
	return nil
}
