package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

type Inventory struct {
	SchemaVersion  int            `json:"schema_version"`
	Repository     string         `json:"repository"`
	BaselineCommit string         `json:"baseline_commit"`
	SourceCommit   string         `json:"source_commit"`
	Files          []FileDecision `json:"files"`
}

type FileDecision struct {
	Path       string `json:"path"`
	BlobSHA256 string `json:"blob_sha256"`
	RuleID     string `json:"rule_id"`
	Class      string `json:"class"`
	Decision   string `json:"decision"`
	License    string `json:"license,omitempty"`
	Mapped     bool   `json:"mapped"`
}

func buildInventory(ctx context.Context, config Config) (Inventory, error) {
	entries, err := trackedEntries(ctx)
	if err != nil {
		return Inventory{}, err
	}
	digests, err := trackedBlobDigests(ctx, entries)
	if err != nil {
		return Inventory{}, err
	}
	commit, err := gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{SchemaVersion: 1, Repository: config.Source.Repository, BaselineCommit: config.Source.BaselineCommit, SourceCommit: strings.TrimSpace(commit), Files: make([]FileDecision, 0, len(entries))}
	candidates := make([]string, 0, len(entries))
	rules := make([]PathRule, len(entries))
	for index, entry := range entries {
		rule, err := matchRule(config.Rules, entry.path)
		if err != nil {
			return Inventory{}, err
		}
		rules[index] = rule
		candidates = append(candidates, entry.path)
	}
	mappings, err := mappingsForIndex(ctx, config, entries, candidates)
	if err != nil {
		return Inventory{}, err
	}
	for index, entry := range entries {
		rule := rules[index]
		inventory.Files = append(inventory.Files, FileDecision{Path: entry.path, BlobSHA256: digests[index], RuleID: rule.ID, Class: rule.Class, Decision: rule.Decision, License: rule.License, Mapped: mappings.mapped(entry.path)})
	}
	return inventory, nil
}

func trackedBlobDigests(ctx context.Context, entries []trackedEntry) ([]string, error) {
	var input strings.Builder
	for _, entry := range entries {
		input.WriteString(entry.oid)
		input.WriteByte('\n')
	}
	batchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(batchContext, "git", "cat-file", "--batch")
	command.Stdin = strings.NewReader(input.String())
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open git cat-file output: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start git cat-file batch: %w", err)
	}
	waitOnError := func(parseErr error) error {
		cancel()
		_ = command.Wait()
		return parseErr
	}
	reader := bufio.NewReader(stdout)
	digests := make([]string, 0, len(entries))
	for _, entry := range entries {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, waitOnError(fmt.Errorf("read tracked blob header for %q: %w", entry.path, err))
		}
		fields := strings.Fields(strings.TrimSuffix(header, "\n"))
		if len(fields) != 3 || fields[0] != entry.oid || fields[1] != "blob" {
			return nil, waitOnError(fmt.Errorf("invalid tracked blob header for %q", entry.path))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return nil, waitOnError(fmt.Errorf("invalid tracked blob size for %q", entry.path))
		}
		hasher := sha256.New()
		if _, err := io.CopyN(hasher, reader, size); err != nil {
			return nil, waitOnError(fmt.Errorf("read tracked blob for %q: %w", entry.path, err))
		}
		terminator, err := reader.ReadByte()
		if err != nil || terminator != '\n' {
			return nil, waitOnError(fmt.Errorf("tracked blob for %q is missing a terminal newline", entry.path))
		}
		digests = append(digests, hex.EncodeToString(hasher.Sum(nil)))
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("git cat-file batch: %w", err)
	}
	return digests, nil
}

type trackedEntry struct {
	path string
	oid  string
	mode string
}

func trackedEntries(ctx context.Context) ([]trackedEntry, error) {
	output, err := gitOutput(ctx, "ls-files", "--stage", "--full-name", "-z", "--", ":/")
	if err != nil {
		return nil, err
	}
	if output != "" && !strings.HasSuffix(output, "\x00") {
		return nil, fmt.Errorf("tracked entry stream is missing terminal NUL")
	}
	items := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	entries := make([]trackedEntry, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid tracked entry")
		}
		metadata := strings.Fields(parts[0])
		if len(metadata) != 3 || metadata[2] != "0" {
			return nil, fmt.Errorf("tracked path %q must be stage 0", parts[1])
		}
		if metadata[0] != "100644" && metadata[0] != "100755" {
			return nil, fmt.Errorf("tracked path %q has unsupported mode %s", parts[1], metadata[0])
		}
		if err := validateRepositoryPath(parts[1]); err != nil {
			return nil, err
		}
		if err := validateObjectID(metadata[1]); err != nil {
			return nil, fmt.Errorf("tracked path %q: %w", parts[1], err)
		}
		if _, exists := seen[parts[1]]; exists {
			return nil, fmt.Errorf("duplicate tracked path %q", parts[1])
		}
		seen[parts[1]] = struct{}{}
		entries = append(entries, trackedEntry{path: parts[1], oid: metadata[1], mode: metadata[0]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
}

func validateObjectID(value string) error {
	if len(value) != 40 {
		return fmt.Errorf("invalid blob object ID")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("invalid blob object ID")
	}
	return nil
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	output, err := gitBytes(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func gitBytes(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func matchRule(rules []PathRule, name string) (PathRule, error) {
	matches := make([]PathRule, 0, 1)
	for _, rule := range rules {
		included := false
		for _, pattern := range rule.Include {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return PathRule{}, err
			}
			included = included || matched
		}
		if !included {
			continue
		}
		excluded := false
		for _, pattern := range rule.Exclude {
			matched, err := matchPathPattern(pattern, name)
			if err != nil {
				return PathRule{}, err
			}
			excluded = excluded || matched
		}
		if !excluded {
			matches = append(matches, rule)
		}
	}
	if len(matches) != 1 {
		return PathRule{}, fmt.Errorf("tracked path %q matches %d publication rules", name, len(matches))
	}
	return matches[0], nil
}

func matchPathPattern(pattern, name string) (bool, error) {
	if err := validateRepositoryPathPattern(pattern); err != nil {
		return false, err
	}
	if err := validateRepositoryPath(name); err != nil {
		return false, err
	}
	patterns, parts := strings.Split(pattern, "/"), strings.Split(name, "/")
	type key struct{ pattern, part int }
	memo, seen := map[key]bool{}, map[key]bool{}
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
