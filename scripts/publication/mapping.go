package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	migrationMappingManifest = "docs/migration/manifest.yaml"
	maximumMappingBytes      = 4 << 20
)

type migrationManifest struct {
	Version       int                    `yaml:"version"`
	Updated       string                 `yaml:"updated"`
	ReferenceRepo string                 `yaml:"reference_repo"`
	PortRepo      string                 `yaml:"port_repo"`
	Authority     yaml.Node              `yaml:"authority"`
	Policy        yaml.Node              `yaml:"policy"`
	Summary       yaml.Node              `yaml:"summary"`
	Files         []migrationMappingFile `yaml:"files"`
}

type migrationMappingFile struct {
	Path        string   `yaml:"path"`
	Domain      string   `yaml:"domain"`
	Scope       string   `yaml:"scope"`
	Status      string   `yaml:"status"`
	Targets     []string `yaml:"targets"`
	Tests       []string `yaml:"tests"`
	Entrypoints []string `yaml:"entrypoints"`
	Symbols     []string `yaml:"symbols"`
	Notes       string   `yaml:"notes"`
}

type publicationMappings struct{ values map[string]bool }

func (m publicationMappings) mapped(candidate string) bool {
	return m.values[candidate]
}

func mappingsForIndex(ctx context.Context, config Config, entries []trackedEntry, candidates []string) (publicationMappings, error) {
	manifest, err := mappingManifestEntry(config, entries)
	if err != nil {
		return publicationMappings{}, err
	}
	contents, err := gitBytes(ctx, "cat-file", "blob", manifest.oid)
	if err != nil {
		return publicationMappings{}, fmt.Errorf("read tracked mapping manifest: %w", err)
	}
	return parsePublicationMappings(contents, candidates)
}

func mappingManifestEntry(config Config, entries []trackedEntry) (trackedEntry, error) {
	if config.Mappings.Manifest != migrationMappingManifest {
		return trackedEntry{}, errors.New("publication mapping manifest must be docs/migration/manifest.yaml")
	}
	for _, entry := range entries {
		if entry.path == migrationMappingManifest {
			return entry, nil
		}
	}
	return trackedEntry{}, errors.New("publication mapping manifest is not tracked")
}

func parsePublicationMappings(contents []byte, candidates []string) (publicationMappings, error) {
	if len(contents) > maximumMappingBytes {
		return publicationMappings{}, errors.New("migration mapping manifest is too large")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var manifest migrationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return publicationMappings{}, fmt.Errorf("decode migration mapping manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return publicationMappings{}, fmt.Errorf("decode trailing migration mapping manifest document: %w", err)
		}
		return publicationMappings{}, errors.New("migration mapping manifest has multiple YAML documents")
	}
	if manifest.Version != 4 {
		return publicationMappings{}, errors.New("migration mapping manifest version must be 4")
	}
	seenReferences := make(map[string]struct{}, len(manifest.Files))
	specifications := make([]string, 0)
	for _, file := range manifest.Files {
		if err := validateMappingReferencePath(file.Path); err != nil {
			return publicationMappings{}, fmt.Errorf("migration reference path: %w", err)
		}
		if _, exists := seenReferences[file.Path]; exists {
			return publicationMappings{}, fmt.Errorf("duplicate migration reference path %q", file.Path)
		}
		seenReferences[file.Path] = struct{}{}
		for _, value := range append(append([]string{}, file.Targets...), file.Tests...) {
			canonical, err := canonicalMappingPath(value)
			if err != nil {
				return publicationMappings{}, err
			}
			specifications = append(specifications, canonical)
		}
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	candidateDirectories := make(map[string]struct{})
	for _, candidate := range candidates {
		if err := validateMappingCandidatePath(candidate); err != nil {
			return publicationMappings{}, err
		}
		candidateSet[candidate] = struct{}{}
		for separator := strings.IndexByte(candidate, '/'); separator >= 0; {
			candidateDirectories[candidate[:separator]] = struct{}{}
			next := strings.IndexByte(candidate[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
	}
	exact := make(map[string]struct{})
	directories := make(map[string]struct{})
	for _, specification := range specifications {
		if _, exists := candidateSet[specification]; exists {
			exact[specification] = struct{}{}
		}
		if _, exists := candidateDirectories[specification]; exists {
			directories[specification] = struct{}{}
		}
	}
	mapped := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if _, exists := exact[candidate]; exists {
			mapped[candidate] = true
			continue
		}
		for separator := strings.IndexByte(candidate, '/'); separator >= 0; {
			if _, exists := directories[candidate[:separator]]; exists {
				mapped[candidate] = true
				break
			}
			next := strings.IndexByte(candidate[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
	}
	return publicationMappings{values: mapped}, nil
}

func canonicalMappingPath(value string) (string, error) {
	value = strings.TrimSuffix(value, "/")
	if err := validateMappingCandidatePath(value); err != nil {
		return "", fmt.Errorf("invalid migration mapping %q: %w", value, err)
	}
	return value, nil
}

func validateMappingReferencePath(value string) error {
	if value == "" || path.IsAbs(value) || strings.Contains(value, `\\`) || strings.Contains(value, "\x00") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("invalid reference path %q", value)
	}
	return nil
}

func validateMappingCandidatePath(value string) error {
	if err := validateRepositoryPath(value); err != nil {
		return err
	}
	for _, root := range []string{".reference", ".eino-agent", ".yhc", ".claude", ".git"} {
		if value == root || strings.HasPrefix(value, root+"/") {
			return fmt.Errorf("forbidden mapping path %q", value)
		}
	}
	return nil
}
