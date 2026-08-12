package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const nodeRegistryHost = "registry.npmjs.org"

type nodeLockfile struct {
	Name            string                     `json:"name"`
	LockfileVersion int                        `json:"lockfileVersion"`
	Packages        map[string]nodeLockPackage `json:"packages"`
}

type nodeLockPackage struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	License   string `json:"license"`
}

type nodeDependencyPolicy struct {
	Version      int                       `yaml:"version"`
	RegistryHost string                    `yaml:"registry_host"`
	Dependencies []nodeDependencyPolicyRow `yaml:"dependencies"`
	Vendored     []nodeVendoredPolicyRow   `yaml:"vendored"`
}

type nodeDependencyPolicyRow struct {
	Package  string `yaml:"package"`
	Version  string `yaml:"version"`
	License  string `yaml:"license"`
	Decision string `yaml:"decision"`
}

type nodeVendoredPolicyRow struct {
	Package     string `yaml:"package"`
	Version     string `yaml:"version"`
	License     string `yaml:"license"`
	Notice      string `yaml:"notice"`
	LicenseFile string `yaml:"license_file"`
}

type nodeSBOM struct {
	BOMFormat   string              `json:"bomFormat"`
	SpecVersion string              `json:"specVersion"`
	Components  []nodeSBOMComponent `json:"components"`
}

type nodeSBOMComponent struct {
	Type       string             `json:"type"`
	Name       string             `json:"name"`
	Version    string             `json:"version"`
	PURL       string             `json:"purl"`
	Hashes     []nodeSBOMHash     `json:"hashes"`
	Licenses   []nodeSBOMLicense  `json:"licenses"`
	Properties []nodeSBOMProperty `json:"properties,omitempty"`
}

type nodeSBOMHash struct {
	Algorithm string `json:"alg"`
	Content   string `json:"content"`
}

type nodeSBOMLicense struct {
	License struct {
		ID string `json:"id"`
	} `json:"license"`
}

type nodeSBOMProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func checkNodeDependencies(repoRoot, policyPath, lockPath string) (nodeSBOM, error) {
	policy, err := loadNodeDependencyPolicy(policyPath)
	if err != nil {
		return nodeSBOM{}, err
	}
	lock, err := loadNodeLockfile(lockPath)
	if err != nil {
		return nodeSBOM{}, err
	}
	components, err := nodeLockComponents(lock, policy)
	if err != nil {
		return nodeSBOM{}, err
	}
	if err := validateVendoredNodeComponents(repoRoot, policy.Vendored); err != nil {
		return nodeSBOM{}, err
	}
	components = append(components, nodeVendoredComponents(repoRoot, policy.Vendored)...)
	sort.Slice(components, func(i, j int) bool {
		if components[i].Name == components[j].Name {
			return components[i].Version < components[j].Version
		}
		return components[i].Name < components[j].Name
	})
	return nodeSBOM{BOMFormat: "CycloneDX", SpecVersion: "1.6", Components: components}, nil
}

func loadNodeDependencyPolicy(name string) (nodeDependencyPolicy, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nodeDependencyPolicy{}, errors.New("read Node dependency policy failed")
	}
	if len(data) > 4<<20 {
		return nodeDependencyPolicy{}, errors.New("node dependency policy is too large")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy nodeDependencyPolicy
	if err := decoder.Decode(&policy); err != nil {
		return nodeDependencyPolicy{}, errors.New("decode Node dependency policy failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nodeDependencyPolicy{}, errors.New("node dependency policy has multiple YAML documents")
	}
	if err := validateNodeDependencyPolicy(policy); err != nil {
		return nodeDependencyPolicy{}, err
	}
	return policy, nil
}

func validateNodeDependencyPolicy(policy nodeDependencyPolicy) error {
	if policy.Version != 1 || policy.RegistryHost != nodeRegistryHost || len(policy.Dependencies) == 0 || len(policy.Vendored) == 0 {
		return errors.New("node dependency policy must pin registry, dependencies, and vendored components")
	}
	seen := map[string]struct{}{}
	for _, row := range policy.Dependencies {
		if !validNodePackageName(row.Package) || !validNodeVersion(row.Version) || !validNodeLicense(row.License) || row.Decision != "allow" {
			return errors.New("node dependency policy has invalid package decision")
		}
		key := nodePolicyKey(row.Package, row.Version)
		if _, exists := seen[key]; exists {
			return errors.New("node dependency policy repeats a package version")
		}
		seen[key] = struct{}{}
	}
	for _, row := range policy.Vendored {
		if !validNodePackageName(row.Package) || !validNodeVersion(row.Version) || !validNodeLicense(row.License) || validateRepositoryPath(row.Notice) != nil || validateRepositoryPath(row.LicenseFile) != nil {
			return errors.New("node vendored dependency policy is invalid")
		}
	}
	return nil
}

func loadNodeLockfile(name string) (nodeLockfile, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nodeLockfile{}, errors.New("read Node package lock failed")
	}
	if len(data) > 16<<20 {
		return nodeLockfile{}, errors.New("node package lock is too large")
	}
	var lock nodeLockfile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&lock); err != nil {
		return nodeLockfile{}, errors.New("decode Node package lock failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nodeLockfile{}, errors.New("node package lock has trailing data")
	}
	if lock.LockfileVersion != 3 || lock.Name != "yhc-desktop" || len(lock.Packages) < 2 {
		return nodeLockfile{}, errors.New("node package lock is not an accepted lockfile v3")
	}
	root, ok := lock.Packages[""]
	if !ok || root.Name != "yhc-desktop" || root.Version == "" {
		return nodeLockfile{}, errors.New("node package lock has invalid root identity")
	}
	return lock, nil
}

func nodeLockComponents(lock nodeLockfile, policy nodeDependencyPolicy) ([]nodeSBOMComponent, error) {
	decisions := make(map[string]nodeDependencyPolicyRow, len(policy.Dependencies))
	for _, row := range policy.Dependencies {
		decisions[nodePolicyKey(row.Package, row.Version)] = row
	}
	used := make(map[string]struct{}, len(decisions))
	componentKeys := make(map[string]struct{}, len(decisions))
	components := make([]nodeSBOMComponent, 0, len(lock.Packages)-1)
	for location, pkg := range lock.Packages {
		if location == "" {
			continue
		}
		name, err := nodePackageName(location, pkg.Name)
		if err != nil || !validNodeVersion(pkg.Version) || !validNodeResolved(pkg.Resolved) || !validNodeIntegrity(pkg.Integrity) || !validNodeLicense(pkg.License) {
			return nil, errors.New("node package lock contains an unsafe reachable dependency")
		}
		row, ok := decisions[nodePolicyKey(name, pkg.Version)]
		if !ok {
			return nil, fmt.Errorf("node dependency policy is missing reachable package %q", nodePolicyKey(name, pkg.Version))
		}
		if pkg.License != row.License {
			return nil, fmt.Errorf("node dependency license policy does not match lock package %q", nodePolicyKey(name, pkg.Version))
		}
		used[nodePolicyKey(name, pkg.Version)] = struct{}{}
		key := nodePolicyKey(name, pkg.Version)
		if _, exists := componentKeys[key]; !exists {
			componentKeys[key] = struct{}{}
			components = append(components, nodeComponent(name, pkg.Version, row.License, pkg.Integrity))
		}
	}
	if len(used) != len(decisions) {
		return nil, errors.New("node dependency policy contains a stale package decision")
	}
	return components, nil
}

func nodePackageName(location, declared string) (string, error) {
	if declared != "" {
		if !validNodePackageName(declared) {
			return "", errors.New("invalid Node package name")
		}
		return declared, nil
	}
	parts := strings.Split(location, "/")
	index := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "node_modules" {
			index = i
			break
		}
	}
	if index < 0 || index == len(parts)-1 {
		return "", errors.New("invalid Node package location")
	}
	name := strings.Join(parts[index+1:], "/")
	if !validNodePackageName(name) {
		return "", errors.New("invalid Node package name")
	}
	return name, nil
}

func validNodePackageName(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || strings.Contains(value, "..") || strings.HasPrefix(value, "/") {
		return false
	}
	if strings.HasPrefix(value, "@") {
		return strings.Count(value, "/") == 1 && len(strings.TrimPrefix(value, "@")) > 1
	}
	return !strings.Contains(value, "/")
}

func validNodeVersion(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 128 && !strings.ContainsAny(value, "\x00/\\")
}

func validNodeLicense(value string) bool {
	// Node lockfiles are untrusted input. Keep this an exact SPDX allowlist so a
	// policy and lockfile changed together cannot admit a new license silently.
	switch value {
	case "0BSD", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "BlueOak-1.0.0", "ISC", "MIT", "Python-2.0", "WTFPL",
		"WTFPL OR ISC", "(MIT OR CC0-1.0)", "(WTFPL OR MIT)":
		return true
	}
	return false
}

func validNodeResolved(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host == nodeRegistryHost && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && strings.HasPrefix(parsed.Path, "/")
}

func validNodeIntegrity(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 2 || (parts[0] != "sha512" && parts[0] != "sha256") {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(parts[1])
	return err == nil && len(parts[1]) > 0
}

func nodePolicyKey(name, version string) string { return name + "@" + version }

func nodeComponent(name, version, license, integrity string) nodeSBOMComponent {
	parts := strings.Split(integrity, "-")
	decoded, _ := base64.StdEncoding.DecodeString(parts[1])
	algorithm := "SHA-256"
	if parts[0] == "sha512" {
		algorithm = "SHA-512"
	}
	component := nodeSBOMComponent{Type: "library", Name: name, Version: version, PURL: "pkg:npm/" + strings.ReplaceAll(name, "@", "%40") + "@" + version, Hashes: []nodeSBOMHash{{Algorithm: algorithm, Content: hex.EncodeToString(decoded)}}}
	component.Licenses = []nodeSBOMLicense{{}}
	component.Licenses[0].License.ID = license
	return component
}

func validateVendoredNodeComponents(repoRoot string, rows []nodeVendoredPolicyRow) error {
	for _, row := range rows {
		for _, name := range []string{row.Notice, row.LicenseFile} {
			contents, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(name)))
			if err != nil || len(contents) == 0 {
				return fmt.Errorf("vendored Node component %q lacks its required notice", row.Package)
			}
		}
	}
	return nil
}

func nodeVendoredComponents(repoRoot string, rows []nodeVendoredPolicyRow) []nodeSBOMComponent {
	components := make([]nodeSBOMComponent, 0, len(rows))
	for _, row := range rows {
		contents, _ := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(row.LicenseFile)))
		digest := sha256.Sum256(contents)
		component := nodeSBOMComponent{Type: "library", Name: row.Package, Version: row.Version, PURL: "pkg:npm/" + row.Package + "@" + row.Version, Hashes: []nodeSBOMHash{{Algorithm: "SHA-256", Content: hex.EncodeToString(digest[:])}}, Properties: []nodeSBOMProperty{{Name: "yhc:vendored", Value: "true"}}}
		component.Licenses = []nodeSBOMLicense{{}}
		component.Licenses[0].License.ID = row.License
		components = append(components, component)
	}
	return components
}

func writeNodeSBOM(repoRoot, policyPath, lockPath, outputPath string) error {
	bom, err := checkNodeDependencies(repoRoot, policyPath, lockPath)
	if err != nil {
		return err
	}
	encoded, err := encodeJSON(bom)
	if err != nil {
		return err
	}
	return writeInventory(outputPath, encoded)
}
