package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyInventoryRequiresEveryGoListModule(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, `- module: example.com/one
  version: v1.0.0
  source_url: https://example.com/one
  licenses: [{spdx: MIT, license_file: LICENSE, license_url: https://example.com/one/LICENSE}]
  compatibility: allow
  notice: none`, component("example.com/one", "v1.0.0", "MIT"))
	withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", "")+goList("example.com/two/pkg", false, "example.com/two", "v1.0.0", ""))
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil || !strings.Contains(err.Error(), "example.com/two") {
		t.Fatalf("missing policy accepted: %v", err)
	}
}

func TestDependencyInventoryIncludesLocalReplacementLicense(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, `- module: github.com/coder/acp-go-sdk
  version: v0.13.5
  source_url: https://github.com/coder/acp-go-sdk
  licenses: [{spdx: Apache-2.0, license_file: third_party/acp-go-sdk/LICENSE, license_url: https://github.com/coder/acp-go-sdk/blob/v0.13.5/LICENSE}]
  compatibility: allow
  notice: retain
  replacement: {kind: local-vendored, path: third_party/acp-go-sdk, modified: true}`, component("github.com/coder/acp-go-sdk", "", "Apache-2.0"))
	writePublicationFile(t, repo, "third_party/acp-go-sdk/LICENSE", "Apache License\n")
	withDependencyGoList(t, goList("github.com/coder/acp-go-sdk/pkg", false, "github.com/coder/acp-go-sdk", "v0.13.5", "./third_party/acp-go-sdk"))
	report, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
	if err != nil || len(report.Modules) != 1 || report.Modules[0].Version != "v0.13.5" || report.Modules[0].SPDX[0] != "Apache-2.0" || report.Modules[0].Replacement != "local-vendored" || !report.Modules[0].Modified {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestDependencyLocalReplacementRequiresModificationMetadata(t *testing.T) {
	for _, replacement := range []string{"", "\n  replacement: {kind: local-vendored, path: third_party/wrong, modified: true}", "\n  replacement: {kind: local-vendored, path: third_party/acp-go-sdk, modified: false}"} {
		entry := `- module: github.com/coder/acp-go-sdk
  version: v0.13.5
  source_url: https://github.com/coder/acp-go-sdk
  licenses: [{spdx: Apache-2.0, license_file: third_party/acp-go-sdk/LICENSE, license_url: https://github.com/coder/acp-go-sdk/blob/v0.13.5/LICENSE}]
  compatibility: allow
  notice: retain` + replacement
		repo, policy, sbom := dependencyFixture(t, entry, component("github.com/coder/acp-go-sdk", "", "Apache-2.0"))
		writePublicationFile(t, repo, "third_party/acp-go-sdk/LICENSE", "Apache License\n")
		withDependencyGoList(t, goList("github.com/coder/acp-go-sdk/pkg", false, "github.com/coder/acp-go-sdk", "v0.13.5", "./third_party/acp-go-sdk"))
		if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
			t.Fatal("local replacement without exact modification metadata was accepted")
		}
	}
}

func TestDependencyMixedLicenseScopeRequiresExplicitOverride(t *testing.T) {
	entry := `- module: github.com/modelcontextprotocol/go-sdk
  version: v1.6.1
  source_url: https://github.com/modelcontextprotocol/go-sdk
  licenses:
    - {spdx: Apache-2.0, license_file: LICENSE, license_url: https://github.com/modelcontextprotocol/go-sdk/blob/v1.6.1/LICENSE}
    - {spdx: MIT, license_file: LICENSE, license_url: https://github.com/modelcontextprotocol/go-sdk/blob/v1.6.1/LICENSE}
  compatibility: allow
  notice: retain`
	repo, policy, sbom := dependencyFixture(t, entry, component("github.com/modelcontextprotocol/go-sdk", "v1.6.1", "CC-BY-4.0"))
	withDependencyGoList(t, goList("github.com/modelcontextprotocol/go-sdk/mcp", false, "github.com/modelcontextprotocol/go-sdk", "v1.6.1", ""))
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
		t.Fatal("mixed detector result without an explicit scope review was accepted")
	}

	entry = strings.Replace(entry, "  notice: retain", "  notice: retain\n  detector_override: mixed-license-scope-reviewed", 1)
	repo, policy, sbom = dependencyFixture(t, entry, component("github.com/modelcontextprotocol/go-sdk", "v1.6.1", "CC-BY-4.0"))
	withDependencyGoList(t, goList("github.com/modelcontextprotocol/go-sdk/mcp", false, "github.com/modelcontextprotocol/go-sdk", "v1.6.1", ""))
	report, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
	if err != nil || len(report.Modules) != 1 || strings.Join(report.Modules[0].SPDX, ",") != "Apache-2.0,MIT" {
		t.Fatalf("reviewed mixed license report=%#v err=%v", report, err)
	}
}

func TestDependencyInventoryRejectsUnknownSPDXOrMissingNotice(t *testing.T) {
	for _, policyBody := range []string{baseEntry("GPL-3.0-only", "none"), strings.Replace(baseEntry("MIT", "none"), "  notice: none\n", "", 1)} {
		repo, policy, sbom := dependencyFixture(t, policyBody, component("example.com/one", "v1.0.0", "MIT"))
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestDependencyReportContainsNoModuleCacheOrHomePath(t *testing.T) {
	for _, unsafeValue := range []string{"/Users/private/go/pkg/mod/LICENSE", `C:\Users\private\go\pkg\mod\LICENSE`} {
		repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), `{"name":"example.com/one","version":"v1.0.0","unsafe_path":`+quotedJSON(unsafeValue)+`,"evidence":{"licenses":[{"license":{"id":"MIT"}}]}}`)
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		_, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
		if err == nil || strings.Contains(err.Error(), unsafeValue) || strings.Contains(strings.ToLower(err.Error()), "pkg/mod") {
			t.Fatalf("SBOM value leaked or was accepted: %v", err)
		}
	}
}

func TestDependencyMainModuleExcluded(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), component("example.com/one", "v1.0.0", "MIT"))
	withDependencyGoList(t, goList("example.com/app", true, "example.com/app", "", "")+goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyInventoryCombinesSupportedTargetsAndDirectModules(t *testing.T) {
	modules, err := mergeDependencyInventories([][]byte{
		[]byte(goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", "")),
		[]byte(goList("example.com/windows/pkg", false, "example.com/windows", "v2.0.0", "")),
	}, []byte(goModule("example.com/tagged", "v3.0.0", false)))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"example.com/one@v1.0.0", "example.com/windows@v2.0.0", "example.com/tagged@v3.0.0"} {
		if modules[key] == nil {
			t.Fatalf("supported-source dependency %q was omitted", key)
		}
	}
	if len(modules["example.com/tagged@v3.0.0"].packages) != 0 {
		t.Fatal("direct build-tag dependency was reported as a loaded package")
	}
}

func TestDependencyInventoryUsesSupportedSourceClosure(t *testing.T) {
	entries := `- module: example.com/linux
  version: v1.0.0
  source_url: https://example.com/linux
  licenses: [{spdx: MIT, license_file: LICENSE, license_url: https://example.com/linux/LICENSE}]
  compatibility: allow
  notice: none
- module: example.com/windows
  version: v2.0.0
  source_url: https://example.com/windows
  licenses: [{spdx: MIT, license_file: LICENSE, license_url: https://example.com/windows/LICENSE}]
  compatibility: allow
  notice: none
- module: example.com/tagged
  version: v3.0.0
  source_url: https://example.com/tagged
  licenses: [{spdx: MIT, license_file: LICENSE, license_url: https://example.com/tagged/LICENSE}]
  compatibility: allow
  notice: none`
	components := component("example.com/linux", "v1.0.0", "MIT") + "," + component("example.com/windows", "v2.0.0", "MIT") + "," + component("example.com/tagged", "v3.0.0", "MIT")
	repo, policy, sbom := dependencyFixture(t, entries, components)
	old := dependencyCommandOutput
	seen := map[string]bool{}
	dependencyCommandOutput = func(_ context.Context, _ string, environment []string, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "-m" {
			seen["direct"] = true
			return []byte(goModule("example.com/tagged", "v3.0.0", false)), nil
		}
		joined := strings.Join(environment, "\x00")
		if strings.Contains(joined, "GOOS=windows") {
			seen["windows"] = true
			return []byte(goList("example.com/windows/pkg", false, "example.com/windows", "v2.0.0", "")), nil
		}
		if strings.Contains(joined, "GOOS=linux") {
			seen["linux"] = true
			return []byte(goList("example.com/linux/pkg", false, "example.com/linux", "v1.0.0", "")), nil
		}
		seen["darwin"] = true
		return nil, nil
	}
	t.Cleanup(func() { dependencyCommandOutput = old })
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"linux", "darwin", "windows", "direct"} {
		if !seen[scope] {
			t.Fatalf("source-closure inventory omitted %s", scope)
		}
	}
}

func TestDependencyPolicyAndSBOMAreStrict(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), component("example.com/one", "v1.0.0", "MIT"))
	withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
	writePublicationFile(t, repo, "policy.yaml", "version: 1\nunknown: reject\ndependencies: []\n")
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
		t.Fatal("unknown policy field accepted")
	}
	writePublicationFile(t, repo, "policy.yaml", "version: 1\ndependencies:\n"+baseEntry("MIT", "none"))
	writePublicationFile(t, repo, "sbom.json", `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[]}`)
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
		t.Fatal("wrong SBOM version accepted")
	}
	for _, invalid := range []string{
		strings.Replace(baseEntry("MIT", "none"), "version: v1.0.0", "version: latest", 1),
		strings.Replace(baseEntry("MIT", "none"), "module: example.com/one", "module: ../outside", 1),
	} {
		policy = writeDependencyPolicy(t, repo, "dependencies:\n"+invalid)
		if _, err := loadDependencyPolicy(policy); err == nil {
			t.Fatal("unsafe module identity was accepted")
		}
	}
}

func TestDependencySBOMRequiresExactComponents(t *testing.T) {
	for _, components := range []string{"", component("example.com/one", "v1.0.0", "MIT") + "," + component("example.com/one", "v1.0.0", "MIT"), component("example.com/one", "v1.0.0", "MIT") + "," + component("example.com/stale", "v1.0.0", "MIT")} {
		repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), components)
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
			t.Fatal("invalid component inventory accepted")
		}
	}
}

func TestDependencySBOMRequiresExactSPDX(t *testing.T) {
	for _, spdx := range []string{"Apache-2.0", "GPL-3.0-only"} {
		repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), component("example.com/one", "v1.0.0", spdx))
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
			t.Fatal("invalid SPDX accepted")
		}
	}
}

func TestDependencySBOMCombinesDeclaredAndDetectedSPDX(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), `{"name":"example.com/one","version":"v1.0.0","licenses":[{"license":{"id":"Apache-2.0"}}],"evidence":{"licenses":[{"license":{"id":"MIT"}}]}}`)
	withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
		t.Fatal("conflicting declared and detected SBOM licenses were accepted")
	}
}

func TestDependencyEvidenceOverride(t *testing.T) {
	repo, policy, sbom := dependencyFixture(t, baseEntry("MIT", "none"), component("example.com/one", "v1.0.0", ""))
	withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
	if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
		t.Fatal("missing evidence without override accepted")
	}
	base := strings.Replace(baseEntry("MIT", "none"), "  notice: none", "  notice: none\n  detector_override: upstream-module-archive-omits-license", 1)
	for _, evidence := range []string{"", "MIT"} {
		repo, policy, sbom := dependencyFixture(t, base, component("example.com/one", "v1.0.0", evidence))
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		_, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
		if evidence == "" && err != nil {
			t.Fatalf("explicit override rejected: %v", err)
		}
		if evidence != "" && err == nil {
			t.Fatal("stale override accepted")
		}
	}
}

func TestDependencySPDXClassificationOverride(t *testing.T) {
	entry := strings.Replace(baseEntry("BSD-3-Clause", "retain"), "  notice: retain", "  notice: retain\n  detector_override: detector-spdx-classification-reviewed", 1)
	for _, observed := range []string{"BSD-Source-Code", "BSD-3-Clause", ""} {
		repo, policy, sbom := dependencyFixture(t, entry, component("example.com/one", "v1.0.0", observed))
		withDependencyGoList(t, goList("example.com/one/pkg", false, "example.com/one", "v1.0.0", ""))
		_, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
		if observed == "BSD-Source-Code" && err != nil {
			t.Fatalf("reviewed detector classification was rejected: %v", err)
		}
		if observed != "BSD-Source-Code" && err == nil {
			t.Fatalf("stale classification override accepted for %q", observed)
		}
	}
}

func TestDependencyLocalLicenseMustBeSafe(t *testing.T) {
	outside := t.TempDir()
	writePublicationFile(t, outside, "LICENSE", "external license\n")
	for _, setup := range []func(string){func(string) {}, func(repo string) { writePublicationFile(t, repo, "third_party/acp-go-sdk/LICENSE", "") }, func(repo string) {
		writePublicationFile(t, repo, "target", "license\n")
		if err := os.MkdirAll(filepath.Join(repo, "third_party", "acp-go-sdk"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(repo, "target"), filepath.Join(repo, "third_party", "acp-go-sdk", "LICENSE")); err != nil {
			t.Fatal(err)
		}
	}, func(repo string) {
		if err := os.MkdirAll(filepath.Join(repo, "third_party"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, "third_party", "acp-go-sdk")); err != nil {
			t.Fatal(err)
		}
	}} {
		repo, policy, sbom := dependencyFixture(t, `- module: github.com/coder/acp-go-sdk
  version: v0.13.5
  source_url: https://github.com/coder/acp-go-sdk
  licenses: [{spdx: Apache-2.0, license_file: third_party/acp-go-sdk/LICENSE, license_url: https://github.com/coder/acp-go-sdk/blob/v0.13.5/LICENSE}]
  compatibility: allow
  notice: retain
  replacement: {kind: local-vendored, path: third_party/acp-go-sdk, modified: true}`, component("github.com/coder/acp-go-sdk", "", "Apache-2.0"))
		setup(repo)
		withDependencyGoList(t, goList("github.com/coder/acp-go-sdk/pkg", false, "github.com/coder/acp-go-sdk", "v0.13.5", "./third_party/acp-go-sdk"))
		if _, err := checkDependencyLicenses(context.Background(), repo, policy, sbom); err == nil {
			t.Fatal("unsafe local evidence accepted")
		}
	}
}

func dependencyFixture(t *testing.T, entries, components string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	policy := filepath.Join(repo, "policy.yaml")
	sbom := filepath.Join(repo, "sbom.json")
	writePublicationFile(t, repo, "policy.yaml", "version: 1\ndependencies:\n"+entries+"\n")
	writePublicationFile(t, repo, "sbom.json", `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[`+components+`]}`)
	return repo, policy, sbom
}

func writeDependencyPolicy(t *testing.T, repo, body string) string {
	t.Helper()
	writePublicationFile(t, repo, "policy.yaml", "version: 1\n"+body)
	return filepath.Join(repo, "policy.yaml")
}

func baseEntry(spdx, notice string) string {
	return "- module: example.com/one\n  version: v1.0.0\n  source_url: https://example.com/one\n  licenses: [{spdx: " + spdx + ", license_file: LICENSE, license_url: https://example.com/one/LICENSE}]\n  compatibility: allow\n  notice: " + notice + "\n"
}

func component(name, version, spdx string) string {
	evidence := ""
	if spdx != "" {
		evidence = `,"evidence":{"licenses":[{"license":{"id":"` + spdx + `"}}]}`
	}
	return `{"name":"` + name + `","version":"` + version + `"` + evidence + `}`
}

func withDependencyGoList(t *testing.T, list string) {
	t.Helper()
	old := dependencyCommandOutput
	dependencyCommandOutput = func(_ context.Context, _ string, _ []string, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "list" && args[1] == "-m" {
			return nil, nil
		}
		return []byte(list), nil
	}
	t.Cleanup(func() { dependencyCommandOutput = old })
}

func goList(importPath string, main bool, module, version, replace string) string {
	replacement := ""
	if replace != "" {
		replacement = `,"Replace":{"Path":"` + replace + `"}`
	}
	return `{"ImportPath":"` + importPath + `","Module":{"Path":"` + module + `","Version":"` + version + `","Main":` + map[bool]string{true: "true", false: "false"}[main] + replacement + `}}` + "\n"
}

func goModule(module, version string, indirect bool) string {
	return `{"Path":"` + module + `","Version":"` + version + `","Indirect":` + map[bool]string{true: "true", false: "false"}[indirect] + `}` + "\n"
}

func quotedJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestNormalizeGeneratedSBOMRemovesOnlyHostToolHashes(t *testing.T) {
	hashes := func(value string) []cycloneDXHash {
		return []cycloneDXHash{
			{Algorithm: "MD5", Content: strings.Repeat(value, 32)},
			{Algorithm: "SHA-1", Content: strings.Repeat(value, 40)},
			{Algorithm: "SHA-256", Content: strings.Repeat(value, 64)},
			{Algorithm: "SHA-384", Content: strings.Repeat(value, 96)},
			{Algorithm: "SHA-512", Content: strings.Repeat(value, 128)},
		}
	}
	document := cycloneDXGeneratorDocument{
		Schema:      "http://cyclonedx.org/schema/bom-1.6.schema.json",
		BOMFormat:   "CycloneDX",
		SpecVersion: "1.6",
		Version:     1,
		Metadata: cycloneDXGeneratorMetadata{
			Tools: []cycloneDXGeneratorTool{{
				Vendor:             "CycloneDX",
				Name:               "cyclonedx-gomod",
				Version:            "v1.10.0",
				Hashes:             hashes("a"),
				ExternalReferences: []json.RawMessage{json.RawMessage(`{"url":"https://cyclonedx.org","type":"website"}`)},
			}},
			Component: json.RawMessage(`{"type":"application","name":"example"}`),
		},
		Components:   []json.RawMessage{json.RawMessage(`{"type":"library","name":"dependency"}`)},
		Dependencies: []json.RawMessage{json.RawMessage(`{"ref":"dependency"}`)},
	}
	first, err := encodeJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	document.Metadata.Tools[0].Hashes = hashes("b")
	second, err := encodeJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	firstNormalized, err := normalizeGeneratedSBOM(first)
	if err != nil {
		t.Fatal(err)
	}
	secondNormalized, err := normalizeGeneratedSBOM(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstNormalized) != string(secondNormalized) || strings.Contains(string(firstNormalized), `"hashes"`) {
		t.Fatal("host-specific generator hashes were not normalized deterministically")
	}
	unknown := strings.Replace(string(first), `"bomFormat": "CycloneDX",`, `"bomFormat": "CycloneDX",\n  "unknown": true,`, 1)
	if _, err := normalizeGeneratedSBOM([]byte(unknown)); err == nil {
		t.Fatal("normalizer accepted an unknown SBOM field")
	}
	componentsBlock := `"components": [
    {
      "type": "library",
      "name": "dependency"
    }
  ]`
	dependenciesBlock := `"dependencies": [
    {
      "ref": "dependency"
    }
  ]`
	for name, invalid := range map[string]string{
		"null components":         strings.Replace(string(first), componentsBlock, `"components": null`, 1),
		"object components":       strings.Replace(string(first), componentsBlock, `"components": {"unexpected": true}`, 1),
		"scalar component entry":  strings.Replace(string(first), componentsBlock, `"components": [true]`, 1),
		"null dependencies":       strings.Replace(string(first), dependenciesBlock, `"dependencies": null`, 1),
		"object dependencies":     strings.Replace(string(first), dependenciesBlock, `"dependencies": {"unexpected": true}`, 1),
		"scalar dependency entry": strings.Replace(string(first), dependenciesBlock, `"dependencies": [true]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if invalid == string(first) {
				t.Fatal("test fixture mutation did not apply")
			}
			if _, err := normalizeGeneratedSBOM([]byte(invalid)); err == nil {
				t.Fatal("normalizer accepted invalid SBOM structure")
			}
		})
	}
	document.Metadata.Tools[0].Hashes = nil
	missingHashes, err := encodeJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	document.Metadata.Tools[0].Hashes = hashes("a")
	document.Metadata.Tools[0].Hashes[1] = document.Metadata.Tools[0].Hashes[0]
	duplicateHashes, err := encodeJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	document.Metadata.Tools[0].Hashes = hashes("a")
	document.Metadata.Tools[0].Hashes[0].Content = strings.Repeat("G", 32)
	malformedHashes, err := encodeJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string][]byte{
		"missing hashes":   missingHashes,
		"duplicate hashes": duplicateHashes,
		"malformed hashes": malformedHashes,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeGeneratedSBOM(invalid); err == nil {
				t.Fatal("normalizer accepted invalid generator hashes")
			}
		})
	}
	fileRoot := newPublicationRepo(t, map[string]string{
		"build/publication/sbom.raw.cdx.json": string(first),
	})
	t.Chdir(fileRoot)
	if err := normalizeGeneratedSBOMFile(context.Background(), "build/publication/sbom.raw.cdx.json", "build/publication/sbom.cdx.json"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile("build/publication/sbom.cdx.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, firstNormalized) {
		t.Fatal("file normalizer changed normalized SBOM bytes")
	}
	for name, input := range map[string]string{
		"absolute":  filepath.Join(fileRoot, "build", "publication", "sbom.raw.cdx.json"),
		"traversal": "../sbom.raw.cdx.json",
		"root file": "sbom.raw.cdx.json",
	} {
		t.Run(name+" input path", func(t *testing.T) {
			if err := normalizeGeneratedSBOMFile(context.Background(), input, "build/publication/rejected.cdx.json"); err == nil {
				t.Fatal("file normalizer accepted an unsafe input path")
			}
		})
	}
	if err := normalizeGeneratedSBOMFile(context.Background(), "build/publication/sbom.raw.cdx.json", "build/publication/sbom.raw.cdx.json"); err == nil {
		t.Fatal("file normalizer accepted identical input and output paths")
	}
	if err := os.Symlink("sbom.raw.cdx.json", "build/publication/sbom.link.cdx.json"); err == nil {
		if err := normalizeGeneratedSBOMFile(context.Background(), "build/publication/sbom.link.cdx.json", "build/publication/rejected.cdx.json"); err == nil {
			t.Fatal("file normalizer accepted a symlink input")
		}
	}
	t.Run("nested current working directory", func(t *testing.T) {
		t.Chdir(filepath.Join(fileRoot, "build"))
		if err := normalizeGeneratedSBOMFile(context.Background(), "publication/sbom.raw.cdx.json", "publication/rejected.cdx.json"); err == nil {
			t.Fatal("file normalizer accepted a nested working directory")
		}
	})
	t.Run("non-repository current working directory", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := normalizeGeneratedSBOMFile(context.Background(), "build/publication/sbom.raw.cdx.json", "build/publication/rejected.cdx.json"); err == nil {
			t.Fatal("file normalizer accepted a non-repository working directory")
		}
	})
}
