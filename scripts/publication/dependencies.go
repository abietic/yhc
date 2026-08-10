package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type DependencyReport struct {
	Modules []DependencyModuleReport `json:"modules"`
}
type DependencyModuleReport struct {
	Module      string   `json:"module"`
	Version     string   `json:"version"`
	SPDX        []string `json:"spdx"`
	Notice      string   `json:"notice"`
	Packages    int      `json:"packages"`
	Replacement string   `json:"replacement,omitempty"`
	Modified    bool     `json:"modified,omitempty"`
}
type dependencyPolicy struct {
	Version      int                     `yaml:"version"`
	Dependencies []dependencyPolicyEntry `yaml:"dependencies"`
}
type dependencyPolicyEntry struct {
	Module           string                 `yaml:"module"`
	Version          string                 `yaml:"version"`
	SourceURL        string                 `yaml:"source_url"`
	Licenses         []dependencyLicense    `yaml:"licenses"`
	Compatibility    string                 `yaml:"compatibility"`
	Notice           string                 `yaml:"notice"`
	DetectorOverride string                 `yaml:"detector_override,omitempty"`
	Replacement      *dependencyReplacement `yaml:"replacement,omitempty"`
}
type dependencyReplacement struct {
	Kind     string `yaml:"kind"`
	Path     string `yaml:"path"`
	Modified bool   `yaml:"modified"`
}
type dependencyLicense struct {
	SPDX        string `yaml:"spdx"`
	LicenseFile string `yaml:"license_file"`
	LicenseURL  string `yaml:"license_url"`
}
type goListPackage struct {
	ImportPath string        `json:"ImportPath"`
	Standard   bool          `json:"Standard"`
	Module     *goListModule `json:"Module"`
}
type goListModule struct {
	Path     string        `json:"Path"`
	Version  string        `json:"Version"`
	Main     bool          `json:"Main"`
	Indirect bool          `json:"Indirect"`
	Replace  *goListModule `json:"Replace"`
}
type dependencyModule struct {
	path, version string
	local         bool
	localPath     string
	packages      map[string]struct{}
}
type cycloneDXBOM struct {
	BOMFormat   string               `json:"bomFormat"`
	SpecVersion string               `json:"specVersion"`
	Components  []cycloneDXComponent `json:"components"`
}
type cycloneDXComponent struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Evidence struct {
		Licenses []cycloneDXLicense `json:"licenses"`
	} `json:"evidence"`
	Licenses []cycloneDXLicense `json:"licenses"`
}
type cycloneDXLicense struct {
	License struct {
		ID string `json:"id"`
	} `json:"license"`
}

var dependencyCommandOutput = func(ctx context.Context, dir string, environment []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), environment...)
	return command.Output()
}

var dependencyTargets = []struct {
	goos   string
	goarch string
}{
	{goos: "linux", goarch: "amd64"},
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
}

func checkDependencyLicenses(ctx context.Context, repoRoot, policyPath, sbomPath string) (DependencyReport, error) {
	policy, err := loadDependencyPolicy(policyPath)
	if err != nil {
		return DependencyReport{}, err
	}
	listed := make([][]byte, 0, len(dependencyTargets))
	for _, target := range dependencyTargets {
		output, listErr := dependencyCommandOutput(ctx, repoRoot, []string{"GOWORK=off", "CGO_ENABLED=0", "GOOS=" + target.goos, "GOARCH=" + target.goarch}, "go", "list", "-deps", "-test", "-json", "./...")
		if listErr != nil {
			return DependencyReport{}, errors.New("enumerate supported Go dependencies failed")
		}
		listed = append(listed, output)
	}
	direct, err := dependencyCommandOutput(ctx, repoRoot, []string{"GOWORK=off"}, "go", "list", "-m", "-json", "all")
	if err != nil {
		return DependencyReport{}, errors.New("enumerate direct Go dependencies failed")
	}
	modules, err := mergeDependencyInventories(listed, direct)
	if err != nil {
		return DependencyReport{}, err
	}
	if err := reconcileDependencyPolicy(policy, modules); err != nil {
		return DependencyReport{}, err
	}
	if err := validateLocalDependencyLicenses(repoRoot, policy, modules); err != nil {
		return DependencyReport{}, err
	}
	components, err := loadSBOMComponents(sbomPath)
	if err != nil {
		return DependencyReport{}, err
	}
	return reconcileSBOM(policy, modules, components)
}

func loadDependencyPolicy(name string) (dependencyPolicy, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return dependencyPolicy{}, errors.New("read dependency license policy failed")
	}
	if len(data) > 4<<20 {
		return dependencyPolicy{}, errors.New("dependency license policy is too large")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy dependencyPolicy
	if err := decoder.Decode(&policy); err != nil {
		return dependencyPolicy{}, fmt.Errorf("decode dependency license policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return dependencyPolicy{}, errors.New("decode trailing dependency license policy failed")
		}
		return dependencyPolicy{}, errors.New("dependency license policy has multiple YAML documents")
	}
	if err := validateDependencyPolicy(policy); err != nil {
		return dependencyPolicy{}, err
	}
	return policy, nil
}

func dependencyModules(data []byte) (map[string]*dependencyModule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	modules := map[string]*dependencyModule{}
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, errors.New("decode Go dependency inventory failed")
		}
		if pkg.Standard || pkg.Module == nil || pkg.Module.Main {
			continue
		}
		module, local, localPath, err := resolvedModule(pkg.Module)
		if err != nil {
			return nil, err
		}
		key := module.Path + "@" + module.Version
		if modules[key] == nil {
			modules[key] = &dependencyModule{path: module.Path, version: module.Version, local: local, localPath: localPath, packages: map[string]struct{}{}}
		}
		modules[key].packages[pkg.ImportPath] = struct{}{}
	}
	return modules, nil
}

func mergeDependencyInventories(packageInventories [][]byte, directInventory []byte) (map[string]*dependencyModule, error) {
	modules := make(map[string]*dependencyModule)
	for _, inventory := range packageInventories {
		parsed, err := dependencyModules(inventory)
		if err != nil {
			return nil, err
		}
		if err := mergeDependencyModules(modules, parsed); err != nil {
			return nil, err
		}
	}
	direct, err := directDependencyModules(directInventory)
	if err != nil {
		return nil, err
	}
	if err := mergeDependencyModules(modules, direct); err != nil {
		return nil, err
	}
	if len(modules) == 0 {
		return nil, errors.New("go dependency inventory contains no non-main dependencies")
	}
	return modules, nil
}

func directDependencyModules(data []byte) (map[string]*dependencyModule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	modules := make(map[string]*dependencyModule)
	for {
		var listed goListModule
		if err := decoder.Decode(&listed); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, errors.New("decode direct Go dependency inventory failed")
		}
		if listed.Main || listed.Indirect {
			continue
		}
		module, local, localPath, err := resolvedModule(&listed)
		if err != nil {
			return nil, err
		}
		key := module.Path + "@" + module.Version
		modules[key] = &dependencyModule{path: module.Path, version: module.Version, local: local, localPath: localPath, packages: map[string]struct{}{}}
	}
	return modules, nil
}

func mergeDependencyModules(destination, source map[string]*dependencyModule) error {
	for key, incoming := range source {
		current := destination[key]
		if current == nil {
			destination[key] = incoming
			continue
		}
		if current.path != incoming.path || current.version != incoming.version || current.local != incoming.local || current.localPath != incoming.localPath {
			return errors.New("go dependency inventory is inconsistent across supported targets")
		}
		for pkg := range incoming.packages {
			current.packages[pkg] = struct{}{}
		}
	}
	return nil
}

func resolvedModule(module *goListModule) (*goListModule, bool, string, error) {
	if module.Path == "" {
		return nil, false, "", errors.New("go dependency inventory contains an unnamed module")
	}
	if module.Replace == nil {
		if module.Version == "" {
			return nil, false, "", fmt.Errorf("go dependency module %q has no version", module.Path)
		}
		return module, false, "", nil
	}
	if module.Replace.Path == "" {
		return nil, false, "", errors.New("go dependency inventory contains an unnamed replacement")
	}
	if module.Replace.Version == "" {
		if module.Version == "" {
			return nil, false, "", fmt.Errorf("go dependency module %q has no version", module.Path)
		}
		if !strings.HasPrefix(module.Replace.Path, "./") {
			return nil, false, "", errors.New("go dependency inventory contains an external local replacement")
		}
		localPath := strings.TrimPrefix(module.Replace.Path, "./")
		if err := validateRepositoryPath(localPath); err != nil {
			return nil, false, "", errors.New("go dependency inventory contains an unsafe local replacement")
		}
		return &goListModule{Path: module.Path, Version: module.Version}, true, localPath, nil
	}
	return nil, false, "", errors.New("go dependency inventory contains an unsupported remote replacement")
}

func validateDependencyPolicy(policy dependencyPolicy) error {
	if policy.Version != 1 || len(policy.Dependencies) == 0 {
		return errors.New("dependency license policy must be version 1 with dependencies")
	}
	seen := map[string]struct{}{}
	for _, entry := range policy.Dependencies {
		if err := validateDependencyEntry(entry); err != nil {
			return err
		}
		key := entry.Module + "@" + entry.Version
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate dependency policy entry for %q", entry.Module)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateDependencyEntry(entry dependencyPolicyEntry) error {
	if validateRepositoryPath(entry.Module) != nil || strings.Contains(entry.Module, "@") || !validDependencyVersion(entry.Version) {
		return fmt.Errorf("dependency policy module %q is invalid", entry.Module)
	}
	if entry.Compatibility != "allow" || (entry.Notice != "none" && entry.Notice != "retain") {
		return fmt.Errorf("dependency policy module %q has unsupported compatibility or notice", entry.Module)
	}
	if entry.DetectorOverride != "" && entry.DetectorOverride != "upstream-module-archive-omits-license" && entry.DetectorOverride != "mixed-license-scope-reviewed" && entry.DetectorOverride != "detector-spdx-classification-reviewed" {
		return fmt.Errorf("dependency policy module %q has invalid detector override", entry.Module)
	}
	if err := validateDependencyURL(entry.SourceURL); err != nil {
		return fmt.Errorf("dependency policy module %q has invalid source URL", entry.Module)
	}
	if len(entry.Licenses) == 0 {
		return fmt.Errorf("dependency policy module %q has no licenses", entry.Module)
	}
	seen := map[string]struct{}{}
	for _, license := range entry.Licenses {
		if !supportedSPDX(license.SPDX) {
			return fmt.Errorf("dependency policy module %q has unsupported SPDX", entry.Module)
		}
		if err := validateRepositoryPath(license.LicenseFile); err != nil {
			return fmt.Errorf("dependency policy module %q has invalid license file", entry.Module)
		}
		if err := validateDependencyURL(license.LicenseURL); err != nil {
			return fmt.Errorf("dependency policy module %q has invalid license URL", entry.Module)
		}
		if _, ok := seen[license.SPDX]; ok {
			return fmt.Errorf("dependency policy module %q repeats SPDX", entry.Module)
		}
		seen[license.SPDX] = struct{}{}
	}
	if entry.Replacement != nil {
		if entry.Replacement.Kind != "local-vendored" || !entry.Replacement.Modified || validateRepositoryPath(entry.Replacement.Path) != nil {
			return fmt.Errorf("dependency policy module %q has invalid replacement metadata", entry.Module)
		}
	}
	return nil
}

func validDependencyVersion(value string) bool {
	if len(value) < 2 || len(value) > 128 || value[0] != 'v' || value[1] < '0' || value[1] > '9' {
		return false
	}
	for _, character := range value[2:] {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '+' {
			continue
		}
		return false
	}
	return true
}

func validateDependencyURL(value string) error {
	parsed, err := url.Parse(value)
	host := parsed.Hostname()
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" || net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".local") {
		return errors.New("invalid URL")
	}
	return nil
}

func supportedSPDX(value string) bool {
	switch value {
	case "Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "BSD-Source-Code", "ISC", "MPL-2.0", "Unlicense", "CC-BY-4.0":
		return true
	}
	return false
}

func reconcileDependencyPolicy(policy dependencyPolicy, modules map[string]*dependencyModule) error {
	entries := policyEntries(policy)
	for key, module := range modules {
		entry, ok := entries[key]
		if !ok {
			return fmt.Errorf("missing dependency policy entry for %q", module.path)
		}
		if err := validateDependencyEntry(entry); err != nil {
			return err
		}
		if module.local {
			if entry.Replacement == nil || entry.Replacement.Path != module.localPath || entry.Notice != "retain" {
				return fmt.Errorf("dependency policy module %q lacks exact local replacement metadata", module.path)
			}
		} else if entry.Replacement != nil {
			return fmt.Errorf("dependency policy module %q has stale replacement metadata", module.path)
		}
	}
	for key, entry := range entries {
		if _, ok := modules[key]; !ok {
			return fmt.Errorf("stale dependency policy entry for %q", entry.Module)
		}
	}
	return nil
}

func policyEntries(policy dependencyPolicy) map[string]dependencyPolicyEntry {
	entries := map[string]dependencyPolicyEntry{}
	for _, entry := range policy.Dependencies {
		entries[entry.Module+"@"+entry.Version] = entry
	}
	return entries
}

func validateLocalDependencyLicenses(repoRoot string, policy dependencyPolicy, modules map[string]*dependencyModule) error {
	before, err := os.Lstat(repoRoot)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("dependency repository root is unsafe")
	}
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return errors.New("dependency repository root is unsafe")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return errors.New("dependency repository root is unsafe")
	}
	entries := policyEntries(policy)
	for key, module := range modules {
		if !module.local {
			continue
		}
		for _, license := range entries[key].Licenses {
			if license.LicenseFile != module.localPath && !strings.HasPrefix(license.LicenseFile, module.localPath+"/") {
				return fmt.Errorf("local dependency %q license evidence is missing or unsafe", module.path)
			}
			name := filepath.FromSlash(license.LicenseFile)
			beforeLicense, err := root.Lstat(name)
			if err != nil || !beforeLicense.Mode().IsRegular() || beforeLicense.Mode()&os.ModeSymlink != 0 || beforeLicense.Size() == 0 || beforeLicense.Size() > 1<<20 {
				return fmt.Errorf("local dependency %q license evidence is missing or unsafe", module.path)
			}
			file, err := root.Open(name)
			if err != nil {
				return fmt.Errorf("local dependency %q license evidence is missing or unsafe", module.path)
			}
			openedLicense, statErr := file.Stat()
			closeErr := file.Close()
			afterLicense, afterErr := root.Lstat(name)
			if statErr != nil || closeErr != nil || afterErr != nil || !openedLicense.Mode().IsRegular() || !os.SameFile(beforeLicense, openedLicense) || !os.SameFile(beforeLicense, afterLicense) {
				return fmt.Errorf("local dependency %q license evidence is missing or unsafe", module.path)
			}
		}
	}
	return nil
}

func loadSBOMComponents(name string) (map[string]cycloneDXComponent, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, errors.New("read dependency SBOM failed")
	}
	if len(data) > 16<<20 {
		return nil, errors.New("dependency SBOM is too large")
	}
	if containsUnsafeDependencyPath(data) {
		return nil, errors.New("dependency SBOM contains unsafe path data")
	}
	var bom cycloneDXBOM
	if err := json.Unmarshal(data, &bom); err != nil || bom.BOMFormat != "CycloneDX" || bom.SpecVersion != "1.6" {
		return nil, errors.New("dependency SBOM must be CycloneDX 1.6")
	}
	components := map[string]cycloneDXComponent{}
	for _, component := range bom.Components {
		if component.Name == "" {
			return nil, errors.New("dependency SBOM contains invalid component")
		}
		key := component.Name + "@" + component.Version
		if _, ok := components[key]; ok {
			return nil, errors.New("dependency SBOM has duplicate component")
		}
		components[key] = component
	}
	return components, nil
}

func containsUnsafeDependencyPath(data []byte) bool {
	lower := strings.ToLower(string(data))
	for _, marker := range []string{"/users/", "/home/", "/pkg/mod/", `c:\users\`, `c:\\users\\`, `\pkg\mod\`, `\\pkg\\mod\\`} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func reconcileSBOM(policy dependencyPolicy, modules map[string]*dependencyModule, components map[string]cycloneDXComponent) (DependencyReport, error) {
	entries := policyEntries(policy)
	report := DependencyReport{Modules: make([]DependencyModuleReport, 0, len(modules))}
	consumed := make(map[string]struct{}, len(modules))
	for key, module := range modules {
		componentKey := key
		if module.local {
			componentKey = module.path + "@"
		}
		component, ok := components[componentKey]
		if !ok {
			return DependencyReport{}, fmt.Errorf("dependency SBOM is missing component for %q", module.path)
		}
		consumed[componentKey] = struct{}{}
		observed, err := componentSPDX(component)
		if err != nil {
			return DependencyReport{}, fmt.Errorf("dependency SBOM has invalid license evidence for %q", module.path)
		}
		entry := entries[key]
		switch entry.DetectorOverride {
		case "":
			if len(observed) == 0 {
				return DependencyReport{}, fmt.Errorf("dependency module %q has no license evidence", module.path)
			}
		case "upstream-module-archive-omits-license":
			if len(observed) != 0 {
				return DependencyReport{}, fmt.Errorf("dependency module %q has stale detector override", module.path)
			}
		case "mixed-license-scope-reviewed", "detector-spdx-classification-reviewed":
			if len(observed) == 0 {
				return DependencyReport{}, fmt.Errorf("dependency module %q has stale detector override", module.path)
			}
		}
		expected := spdxSet(entry.Licenses)
		if entry.DetectorOverride == "" && !sameSPDX(expected, observed) {
			return DependencyReport{}, fmt.Errorf("dependency module %q SPDX does not match policy", module.path)
		}
		if entry.DetectorOverride == "mixed-license-scope-reviewed" && (len(expected) < 2 || sameSPDX(expected, observed)) {
			return DependencyReport{}, fmt.Errorf("dependency module %q has stale detector override", module.path)
		}
		if entry.DetectorOverride == "detector-spdx-classification-reviewed" && (len(expected) != 1 || len(observed) != 1 || sameSPDX(expected, observed)) {
			return DependencyReport{}, fmt.Errorf("dependency module %q has stale detector override", module.path)
		}
		item := DependencyModuleReport{Module: module.path, Version: module.version, SPDX: sortedSPDX(expected), Notice: entry.Notice, Packages: len(module.packages)}
		if module.local {
			item.Replacement = entry.Replacement.Kind
			item.Modified = entry.Replacement.Modified
		}
		report.Modules = append(report.Modules, item)
	}
	for key := range components {
		if _, ok := consumed[key]; !ok {
			return DependencyReport{}, errors.New("dependency SBOM has stale component")
		}
	}
	sort.Slice(report.Modules, func(i, j int) bool { return report.Modules[i].Module < report.Modules[j].Module })
	return report, nil
}

func componentSPDX(component cycloneDXComponent) (map[string]struct{}, error) {
	licenses := append(append([]cycloneDXLicense{}, component.Evidence.Licenses...), component.Licenses...)
	result := map[string]struct{}{}
	for _, item := range licenses {
		if !supportedSPDX(item.License.ID) {
			return nil, errors.New("unsupported SPDX")
		}
		result[item.License.ID] = struct{}{}
	}
	return result, nil
}

func spdxSet(licenses []dependencyLicense) map[string]struct{} {
	result := map[string]struct{}{}
	for _, license := range licenses {
		result[license.SPDX] = struct{}{}
	}
	return result
}

func sameSPDX(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if _, ok := b[value]; !ok {
			return false
		}
	}
	return true
}

func sortedSPDX(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

type cycloneDXGeneratorDocument struct {
	Schema       string                     `json:"$schema"`
	BOMFormat    string                     `json:"bomFormat"`
	SpecVersion  string                     `json:"specVersion"`
	Version      int                        `json:"version"`
	Metadata     cycloneDXGeneratorMetadata `json:"metadata"`
	Components   []json.RawMessage          `json:"components"`
	Dependencies []json.RawMessage          `json:"dependencies"`
}

type cycloneDXGeneratorMetadata struct {
	Tools     []cycloneDXGeneratorTool `json:"tools"`
	Component json.RawMessage          `json:"component"`
}

type cycloneDXGeneratorTool struct {
	Vendor             string            `json:"vendor"`
	Name               string            `json:"name"`
	Version            string            `json:"version"`
	Hashes             []cycloneDXHash   `json:"hashes,omitempty"`
	ExternalReferences []json.RawMessage `json:"externalReferences"`
}

type cycloneDXHash struct {
	Algorithm string `json:"alg"`
	Content   string `json:"content"`
}

// normalizeGeneratedSBOMFile removes only the generator executable hashes.
// Those hashes identify the host-specific tool binary, not the dependency graph.
func normalizeGeneratedSBOMFile(ctx context.Context, inputPath, outputPath string) error {
	if err := validateGitSourceRoot(ctx); err != nil {
		return err
	}
	if !filepath.IsLocal(inputPath) || filepath.Clean(inputPath) != inputPath || filepath.Dir(inputPath) == "." {
		return errors.New("generated dependency SBOM input path must be a clean repository-relative path")
	}
	if filepath.Clean(inputPath) == filepath.Clean(outputPath) {
		return errors.New("raw and normalized SBOM paths must differ")
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		return errors.New("open generated dependency SBOM root failed")
	}
	defer root.Close()
	before, err := root.Lstat(inputPath)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() == 0 || before.Size() > 32<<20 {
		return errors.New("generated dependency SBOM is missing or unsafe")
	}
	input, err := root.Open(inputPath)
	if err != nil {
		return errors.New("open generated dependency SBOM failed")
	}
	opened, statErr := input.Stat()
	data, readErr := io.ReadAll(input)
	closeErr := input.Close()
	after, afterErr := root.Lstat(inputPath)
	if statErr != nil || readErr != nil || closeErr != nil || afterErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return errors.New("generated dependency SBOM changed while reading")
	}
	normalized, err := normalizeGeneratedSBOM(data)
	if err != nil {
		return err
	}
	return writeInventory(outputPath, normalized)
}

func normalizeGeneratedSBOM(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document cycloneDXGeneratorDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("decode generated dependency SBOM failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("generated dependency SBOM has trailing data")
	}
	if document.Schema != "http://cyclonedx.org/schema/bom-1.6.schema.json" || document.BOMFormat != "CycloneDX" || document.SpecVersion != "1.6" || document.Version != 1 {
		return nil, errors.New("generated dependency SBOM has unexpected format")
	}
	if len(document.Metadata.Tools) != 1 || !isJSONObject(document.Metadata.Component) || !areJSONObjects(document.Components) || !areJSONObjects(document.Dependencies) {
		return nil, errors.New("generated dependency SBOM has unexpected structure")
	}
	tool := &document.Metadata.Tools[0]
	if tool.Vendor != "CycloneDX" || tool.Name != "cyclonedx-gomod" || tool.Version != "v1.10.0" || !areJSONObjects(tool.ExternalReferences) {
		return nil, errors.New("generated dependency SBOM has unexpected tool identity")
	}
	wantHashes := map[string]int{"MD5": 16, "SHA-1": 20, "SHA-256": 32, "SHA-384": 48, "SHA-512": 64}
	if len(tool.Hashes) != len(wantHashes) {
		return nil, errors.New("generated dependency SBOM has unexpected tool hashes")
	}
	seen := make(map[string]struct{}, len(tool.Hashes))
	for _, hash := range tool.Hashes {
		want, ok := wantHashes[hash.Algorithm]
		decoded, decodeErr := hex.DecodeString(hash.Content)
		if !ok || decodeErr != nil || len(decoded) != want || hash.Content != strings.ToLower(hash.Content) {
			return nil, errors.New("generated dependency SBOM has invalid tool hash")
		}
		if _, duplicate := seen[hash.Algorithm]; duplicate {
			return nil, errors.New("generated dependency SBOM has duplicate tool hash")
		}
		seen[hash.Algorithm] = struct{}{}
	}
	canonical, err := encodeJSON(document)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, errors.New("generated dependency SBOM is not canonical")
	}
	tool.Hashes = nil
	return encodeJSON(document)
}

func areJSONObjects(values []json.RawMessage) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !isJSONObject(value) {
			return false
		}
	}
	return true
}

func isJSONObject(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}
