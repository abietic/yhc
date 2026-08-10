package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Policy struct {
	Version       int                     `yaml:"version"`
	Repository    string                  `yaml:"repository"`
	Modules       map[string]ModulePolicy `yaml:"modules"`
	RiskPacks     map[string]RiskPack     `yaml:"risk_packs"`
	ChangeClasses map[string]ChangeClass  `yaml:"change_classes"`
	Boundaries    BoundaryPolicy          `yaml:"boundaries"`
}

type ModulePolicy struct {
	Priority        int        `yaml:"priority"`
	ProductionPaths []PathRule `yaml:"production_paths"`
	TestPaths       []string   `yaml:"test_paths"`
	Packages        []string   `yaml:"packages"`
	OwnerDocs       []string   `yaml:"owner_docs"`
	Risks           []string   `yaml:"risks"`
	FocusedPackages []string   `yaml:"focused_packages"`
}

type PathRule struct {
	Include string   `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type RiskPack struct {
	Target      string   `yaml:"target"`
	DeepTargets []string `yaml:"deep_targets"`
	Platforms   []string `yaml:"platforms"`
}

type ChangeClass struct {
	Priority        int      `yaml:"priority"`
	Paths           []string `yaml:"paths"`
	Targets         []string `yaml:"targets"`
	FocusedPackages []string `yaml:"focused_packages"`
}

type BoundaryPolicy struct {
	ForbiddenProductionEdges []ForbiddenEdge `yaml:"forbidden_production_edges"`
	FlatPackageRoots         []string        `yaml:"flat_package_roots"`
}

type ForbiddenEdge struct {
	From []string `yaml:"from"`
	To   []string `yaml:"to"`
}

func loadPolicy(root *os.Root, name string) (Policy, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return Policy{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode iteration policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return Policy{}, fmt.Errorf("decode trailing iteration policy document: %w", err)
		}
		return Policy{}, errors.New("decode iteration policy: multiple YAML documents are not allowed")
	}
	if err := validatePolicy(policy, root); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func validatePolicy(policy Policy, root *os.Root) error {
	var problems []string
	if policy.Version != 1 {
		problems = append(problems, "version must be 1")
	}
	if strings.TrimSpace(policy.Repository) == "" {
		problems = append(problems, "repository must not be empty")
	}
	if len(policy.Modules) == 0 {
		problems = append(problems, "modules must not be empty")
	}
	if len(policy.ChangeClasses) == 0 {
		problems = append(problems, "change_classes must not be empty")
	}
	targets, targetErr := makeTargets(root)
	if targetErr != nil {
		problems = append(problems, targetErr.Error())
	}
	for name, module := range policy.Modules {
		prefix := "module " + name
		if strings.TrimSpace(name) == "" || module.Priority <= 0 || len(module.ProductionPaths) == 0 || len(module.Packages) == 0 || len(module.OwnerDocs) == 0 || len(module.FocusedPackages) == 0 {
			problems = append(problems, prefix+" must not be empty")
		}
		problems = append(problems, validateModule(prefix, module, policy.RiskPacks, root)...)
	}
	for name, class := range policy.ChangeClasses {
		prefix := "change class " + name
		if strings.TrimSpace(name) == "" || class.Priority <= 0 || len(class.Paths) == 0 || len(class.Targets) == 0 {
			problems = append(problems, prefix+" must not be empty")
		}
		problems = append(problems, validatePatterns(prefix, class.Paths)...)
		problems = append(problems, duplicateProblems(prefix+" paths", class.Paths)...)
		problems = append(problems, validateTargets(prefix, class.Targets, targets)...)
		problems = append(problems, duplicateProblems(prefix+" targets", class.Targets)...)
		problems = append(problems, duplicateProblems(prefix+" focused_packages", class.FocusedPackages)...)
	}
	for name, pack := range policy.RiskPacks {
		prefix := "risk pack " + name
		if strings.TrimSpace(name) == "" || strings.TrimSpace(pack.Target) == "" || len(pack.Platforms) == 0 {
			problems = append(problems, prefix+" must not be empty")
		}
		problems = append(problems, validateTargets(prefix, append([]string{pack.Target}, pack.DeepTargets...), targets)...)
		problems = append(problems, duplicateProblems(prefix+" deep_targets", pack.DeepTargets)...)
		for _, target := range pack.DeepTargets {
			if !slices.Contains(deepTargetOrder, target) || target == "check-boundaries" {
				problems = append(problems, fmt.Sprintf("%s has unsupported deep target %q", prefix, target))
			}
		}
		problems = append(problems, duplicateProblems(prefix+" platforms", pack.Platforms)...)
		for _, platform := range pack.Platforms {
			if platform != "all" && platform != "unix" {
				problems = append(problems, fmt.Sprintf("%s has invalid platform %q", prefix, platform))
			}
		}
	}
	problems = append(problems, duplicateProblems("flat_package_roots", policy.Boundaries.FlatPackageRoots)...)
	for _, root := range policy.Boundaries.FlatPackageRoots {
		if err := validateRepositoryPath(root); err != nil {
			problems = append(problems, "flat package root: "+err.Error())
		}
	}
	for index, edge := range policy.Boundaries.ForbiddenProductionEdges {
		prefix := fmt.Sprintf("forbidden production edge %d", index)
		if len(edge.From) == 0 || len(edge.To) == 0 {
			problems = append(problems, prefix+" must name from and to modules")
		}
		problems = append(problems, duplicateProblems(prefix+" from", edge.From)...)
		problems = append(problems, duplicateProblems(prefix+" to", edge.To)...)
		for _, owner := range append(append([]string(nil), edge.From...), edge.To...) {
			if _, ok := policy.Modules[owner]; !ok {
				problems = append(problems, fmt.Sprintf("%s references unknown module %q", prefix, owner))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateModule(prefix string, module ModulePolicy, riskPacks map[string]RiskPack, root *os.Root) []string {
	var problems []string
	for _, rule := range module.ProductionPaths {
		problems = append(problems, validatePatterns(prefix+" production_paths", []string{rule.Include})...)
		problems = append(problems, validatePatterns(prefix+" production_paths exclude", rule.Exclude)...)
		problems = append(problems, duplicateProblems(prefix+" production_paths exclude", rule.Exclude)...)
	}
	problems = append(problems, validatePatterns(prefix+" test_paths", module.TestPaths)...)
	problems = append(problems, duplicateProblems(prefix+" test_paths", module.TestPaths)...)
	problems = append(problems, duplicateProblems(prefix+" packages", module.Packages)...)
	problems = append(problems, duplicateProblems(prefix+" owner_docs", module.OwnerDocs)...)
	problems = append(problems, duplicateProblems(prefix+" risks", module.Risks)...)
	problems = append(problems, duplicateProblems(prefix+" focused_packages", module.FocusedPackages)...)
	for _, risk := range module.Risks {
		if _, ok := riskPacks[risk]; !ok {
			problems = append(problems, fmt.Sprintf("%s references unknown risk %q", prefix, risk))
		}
	}
	for _, ownerDoc := range module.OwnerDocs {
		if err := validateRepositoryPath(ownerDoc); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		info, err := root.Stat(ownerDoc)
		if err != nil || info.IsDir() {
			problems = append(problems, fmt.Sprintf("%s references missing owner document %q", prefix, ownerDoc))
		}
	}
	return problems
}

func validatePatterns(prefix string, patterns []string) []string {
	var problems []string
	for _, pattern := range patterns {
		if err := validateRepositoryPathPattern(pattern); err != nil {
			problems = append(problems, prefix+": "+err.Error())
		}
	}
	return problems
}

func validateTargets(prefix string, values []string, targets map[string]struct{}) []string {
	var problems []string
	for _, target := range values {
		if _, ok := targets[target]; !ok {
			problems = append(problems, fmt.Sprintf("%s references missing Make target %q", prefix, target))
		}
	}
	return problems
}

func duplicateProblems(label string, values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var problems []string
	for _, value := range values {
		if _, ok := seen[value]; ok {
			problems = append(problems, fmt.Sprintf("%s contains duplicate %q", label, value))
		}
		seen[value] = struct{}{}
	}
	return problems
}

func makeTargets(root *os.Root) (map[string]struct{}, error) {
	data, err := root.ReadFile("Makefile")
	if err != nil {
		return nil, fmt.Errorf("read Makefile: %w", err)
	}
	targets := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		name, _, ok := strings.Cut(line, ":")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		targets[name] = struct{}{}
	}
	return targets, nil
}

func unknownTrackedPaths(policy Policy) ([]string, error) {
	paths, err := repositoryReviewPaths(context.Background(), "../..")
	if err != nil {
		return nil, err
	}
	return unclassifiedTrackedPaths(policy, paths)
}

func repositoryReviewPaths(ctx context.Context, root string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	_, statErr := os.Lstat(filepath.Join(root, ".git"))
	if statErr == nil {
		return commandTrackedFiles(ctx, root)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return distributionPaths(root)
	}
	return nil, fmt.Errorf("inspect repository metadata: %w", statErr)
}

func distributionPaths(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect distribution root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("distribution root %q is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, name)
		if relErr != nil {
			return fmt.Errorf("resolve distribution path %q: %w", name, relErr)
		}
		if relative == "." {
			return nil
		}
		repositoryPath := filepath.ToSlash(relative)
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("inspect distribution path %q: %w", repositoryPath, infoErr)
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			return fmt.Errorf("distribution path %q is not a regular file or directory", repositoryPath)
		}
		if repositoryPath == "build" {
			if !entryInfo.IsDir() {
				return fmt.Errorf("generated path %q is not a directory", repositoryPath)
			}
			return filepath.SkipDir
		}
		if repositoryPath == "PUBLICATION_MANIFEST.json" {
			if !entryInfo.Mode().IsRegular() {
				return fmt.Errorf("generated path %q is not a regular file", repositoryPath)
			}
			return nil
		}
		if entryInfo.IsDir() {
			return nil
		}
		if err := validateRepositoryPath(repositoryPath); err != nil {
			return err
		}
		paths = append(paths, repositoryPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk distribution tree: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func policyCoversPath(policy Policy, name string) (bool, error) {
	for _, module := range policy.Modules {
		for _, rule := range module.ProductionPaths {
			matched, err := matchPathPattern(rule.Include, name)
			if err != nil || !matched {
				continue
			}
			excluded := false
			for _, exclude := range rule.Exclude {
				excluded, err = matchPathPattern(exclude, name)
				if err != nil || excluded {
					break
				}
			}
			if err != nil {
				return false, err
			}
			if !excluded {
				return true, nil
			}
		}
		for _, pattern := range module.TestPaths {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
	}
	for _, class := range policy.ChangeClasses {
		for _, pattern := range class.Paths {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
	}
	return false, nil
}
