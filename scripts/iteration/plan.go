package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

type GateStatus string

const (
	GatePass          GateStatus = "pass"
	GateFail          GateStatus = "fail"
	GateBlocked       GateStatus = "blocked"
	GateNotApplicable GateStatus = "not_applicable"
)

type PathKind string

const (
	PathProduction PathKind = "production"
	PathTest       PathKind = "test"
	PathClass      PathKind = "class"
)

type ChangedPath struct {
	Path   string   `json:"path"`
	Status string   `json:"status"`
	Owner  string   `json:"owner"`
	Kind   PathKind `json:"kind"`
}

type FocusedCheck struct {
	Owner    string   `json:"owner"`
	Packages []string `json:"packages"`
}

type SliceRef struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Contract string `json:"contract"`
	Outcome  string `json:"outcome"`
}

type Plan struct {
	SchemaVersion    int            `json:"schema_version"`
	Repository       string         `json:"repository"`
	PolicyVersion    int            `json:"policy_version"`
	BaseRef          string         `json:"base_ref"`
	Base             string         `json:"base"`
	Head             string         `json:"head"`
	DiffDigest       string         `json:"diff_digest"`
	Slice            *SliceRef      `json:"slice,omitempty"`
	Platform         string         `json:"platform"`
	Changed          []ChangedPath  `json:"changed"`
	Modules          []string       `json:"modules"`
	ChangeClasses    []string       `json:"change_classes"`
	Risks            []string       `json:"risks"`
	OwnerDocs        []string       `json:"owner_docs"`
	FocusedChecks    []FocusedCheck `json:"focused_checks"`
	RequiredTargets  []string       `json:"required_targets"`
	NotApplicable    []string       `json:"not_applicable"`
	OutsideUntracked int            `json:"outside_untracked_count"`
}

type ownerCandidate struct {
	owner    string
	kind     PathKind
	priority int
	risks    []string
	docs     []string
	packages []string
	targets  []string
}

func buildPlan(
	policy Policy,
	snapshot GitSnapshot,
	platform string,
	slice *SliceRef,
) (Plan, error) {
	if strings.TrimSpace(platform) == "" {
		return Plan{}, errors.New("platform is required")
	}

	modules := make(map[string]struct{})
	classes := make(map[string]struct{})
	risks := make(map[string]struct{})
	docs := make(map[string]struct{})
	targets := make(map[string]struct{})
	notApplicable := make(map[string]struct{})
	focused := make(map[string]map[string]struct{})
	changed := make([]ChangedPath, 0, len(snapshot.Changed))
	documentationOnly := len(snapshot.Changed) > 0

	for _, change := range snapshot.Changed {
		candidates, err := ownerCandidatesForPath(policy, change.Path)
		if err != nil {
			return Plan{}, err
		}
		winner, err := chooseOwner(change.Path, candidates)
		if err != nil {
			return Plan{}, err
		}
		changed = append(changed, ChangedPath{
			Path:   change.Path,
			Status: change.Status,
			Owner:  winner.owner,
			Kind:   winner.kind,
		})
		if winner.kind == PathClass {
			classes[winner.owner] = struct{}{}
		} else {
			modules[winner.owner] = struct{}{}
		}
		if winner.owner != "documentation" || winner.kind != PathClass {
			documentationOnly = false
		}
		addStrings(risks, winner.risks...)
		addStrings(docs, winner.docs...)
		addStrings(targets, winner.targets...)
		if len(winner.packages) > 0 {
			ownerPackages := focused[winner.owner]
			if ownerPackages == nil {
				ownerPackages = make(map[string]struct{})
				focused[winner.owner] = ownerPackages
			}
			addStrings(ownerPackages, winner.packages...)
		}
	}

	if len(changed) > 0 {
		if documentationOnly {
			addStrings(targets, "docs-check-ci", "docs-check", "git-diff-check")
		} else {
			addStrings(
				targets,
				"fmt",
				"lint",
				"test",
				"build",
				"docs-check-ci",
				"docs-check",
				"git-diff-check",
				"check-boundaries",
			)
		}
	}
	for risk := range risks {
		pack, ok := policy.RiskPacks[risk]
		if !ok {
			return Plan{}, fmt.Errorf("risk %q has no policy pack", risk)
		}
		addStrings(targets, pack.Target)
		if !riskPackApplies(pack, platform) {
			addStrings(notApplicable, pack.Target)
		}
	}

	slices.SortFunc(changed, func(left, right ChangedPath) int {
		if order := cmp.Compare(left.Path, right.Path); order != 0 {
			return order
		}
		return cmp.Compare(left.Status, right.Status)
	})
	var focusedChecks []FocusedCheck
	if len(focused) > 0 {
		focusedChecks = make([]FocusedCheck, 0, len(focused))
	}
	for owner, packages := range focused {
		focusedChecks = append(focusedChecks, FocusedCheck{
			Owner:    owner,
			Packages: sortedSet(packages),
		})
	}
	slices.SortFunc(focusedChecks, func(left, right FocusedCheck) int {
		return cmp.Compare(left.Owner, right.Owner)
	})

	return Plan{
		SchemaVersion:    1,
		Repository:       policy.Repository,
		PolicyVersion:    policy.Version,
		BaseRef:          snapshot.BaseRef,
		Base:             snapshot.Base,
		Head:             snapshot.Head,
		DiffDigest:       snapshot.DiffDigest,
		Slice:            cloneSliceRef(slice),
		Platform:         platform,
		Changed:          changed,
		Modules:          sortedSet(modules),
		ChangeClasses:    sortedSet(classes),
		Risks:            sortedSet(risks),
		OwnerDocs:        sortedSet(docs),
		FocusedChecks:    focusedChecks,
		RequiredTargets:  sortedSet(targets),
		NotApplicable:    sortedSet(notApplicable),
		OutsideUntracked: snapshot.OutsideUntracked,
	}, nil
}

func ownerCandidatesForPath(policy Policy, name string) ([]ownerCandidate, error) {
	var candidates []ownerCandidate
	for owner, module := range policy.Modules {
		for _, rule := range module.ProductionPaths {
			matched, err := matchPathPattern(rule.Include, name)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			excluded := false
			for _, pattern := range rule.Exclude {
				excluded, err = matchPathPattern(pattern, name)
				if err != nil {
					return nil, err
				}
				if excluded {
					break
				}
			}
			if !excluded {
				candidates = append(candidates, moduleCandidate(owner, PathProduction, module))
			}
		}
		for _, pattern := range module.TestPaths {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return nil, err
			}
			if matched {
				candidates = append(candidates, moduleCandidate(owner, PathTest, module))
			}
		}
	}
	for owner, class := range policy.ChangeClasses {
		for _, pattern := range class.Paths {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return nil, err
			}
			if matched {
				candidates = append(candidates, ownerCandidate{
					owner:    owner,
					kind:     PathClass,
					priority: class.Priority,
					packages: append([]string(nil), class.FocusedPackages...),
					targets:  append([]string(nil), class.Targets...),
				})
			}
		}
	}
	return candidates, nil
}

func moduleCandidate(owner string, kind PathKind, module ModulePolicy) ownerCandidate {
	return ownerCandidate{
		owner:    owner,
		kind:     kind,
		priority: module.Priority,
		risks:    append([]string(nil), module.Risks...),
		docs:     append([]string(nil), module.OwnerDocs...),
		packages: append([]string(nil), module.FocusedPackages...),
	}
}

func chooseOwner(name string, candidates []ownerCandidate) (ownerCandidate, error) {
	if len(candidates) == 0 {
		return ownerCandidate{}, fmt.Errorf("unclassified path %q", name)
	}
	slices.SortFunc(candidates, func(left, right ownerCandidate) int {
		if order := cmp.Compare(right.priority, left.priority); order != 0 {
			return order
		}
		if order := cmp.Compare(left.owner, right.owner); order != 0 {
			return order
		}
		return cmp.Compare(left.kind, right.kind)
	})
	winner := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.priority != winner.priority {
			break
		}
		if candidate.owner != winner.owner || candidate.kind != winner.kind {
			return ownerCandidate{}, fmt.Errorf(
				"ambiguous path %q at priority %d: %s/%s and %s/%s",
				name,
				winner.priority,
				winner.owner,
				winner.kind,
				candidate.owner,
				candidate.kind,
			)
		}
	}
	return winner, nil
}

func riskPackApplies(pack RiskPack, platform string) bool {
	for _, supported := range pack.Platforms {
		if supported == "all" || supported == "unix" && platform != "windows" {
			return true
		}
	}
	return false
}

func addStrings(set map[string]struct{}, values ...string) {
	for _, value := range values {
		set[value] = struct{}{}
	}
}

func sortedSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

func cloneSliceRef(slice *SliceRef) *SliceRef {
	if slice == nil {
		return nil
	}
	copy := *slice
	return &copy
}

type SliceResolver interface {
	Resolve(ctx context.Context, id string) (*SliceRef, error)
}

type commandSliceResolver struct {
	root        string
	outputLimit int
}

func (resolver commandSliceResolver) Resolve(ctx context.Context, id string) (*SliceRef, error) {
	if strings.TrimSpace(resolver.root) == "" {
		return nil, errors.New("slice resolver root is required")
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("slice id is required")
	}
	limit := resolver.outputLimit
	if limit <= 0 {
		limit = defaultGitOutputLimit
	}
	stdout := newBoundedCommandBuffer(limit)
	stderr := newBoundedCommandBuffer(limit)
	command := exec.CommandContext(
		ctx,
		"go",
		"run",
		"./scripts/migration_queue",
		"--slice-id",
		id,
		"describe",
	)
	command.Dir = resolver.root
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if stdout.overflow || stderr.overflow {
			return nil, fmt.Errorf("migration queue output exceeds %d bytes", limit)
		}
		return nil, fmt.Errorf("describe migration slice %q: %w", id, err)
	}
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("migration queue output exceeds %d bytes", limit)
	}
	return decodeSliceRef(bytes.NewReader(stdout.buffer.Bytes()), id)
}

func decodeSliceRef(reader io.Reader, requestedID string) (*SliceRef, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var wire struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		State         string `json:"state"`
		Contract      string `json:"contract"`
		Outcome       string `json:"outcome"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode migration slice: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing migration slice: %w", err)
		}
		return nil, errors.New("decode migration slice: multiple JSON values are not allowed")
	}
	if wire.SchemaVersion != 1 || wire.ID != requestedID ||
		!oneOf(wire.State, "ready", "queued", "blocked") ||
		strings.TrimSpace(wire.Contract) == "" || strings.TrimSpace(wire.Outcome) == "" {
		return nil, errors.New("decode migration slice: invalid description")
	}
	return &SliceRef{
		ID:       wire.ID,
		State:    wire.State,
		Contract: wire.Contract,
		Outcome:  wire.Outcome,
	}, nil
}

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}
