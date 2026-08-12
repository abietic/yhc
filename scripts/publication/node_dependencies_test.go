package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeDesktopManifestPinsPublicIdentityAndElectronFloor(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "desktop", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name            string            `json:"name"`
		Author          string            `json:"author"`
		DevDependencies map[string]string `json:"devDependencies"`
		Build           struct {
			AppID       string `json:"appId"`
			ProductName string `json:"productName"`
		} `json:"build"`
	}
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "yhc-desktop" || manifest.Author != "YHC contributors" || manifest.Build.AppID != "com.abietic.yhc.desktop" || manifest.Build.ProductName != "YHC" || manifest.DevDependencies["electron"] != "41.10.4" {
		t.Fatalf("desktop manifest does not preserve public YHC identity and Electron floor: %#v", manifest)
	}
}

func TestNodeDependenciesRejectUnsafeLockSourcesAndMissingPolicy(t *testing.T) {
	for _, resolved := range []string{"file:../package.tgz", "git+https://github.com/example/package.git", "https://registry.example.test/package.tgz"} {
		repo, policy, lock := nodeDependencyFixture(t, resolved, `"integrity":"sha512-YQ=="`)
		if _, err := checkNodeDependencies(repo, policy, lock); err == nil {
			t.Fatalf("accepted unsafe resolved URL %q", resolved)
		}
	}
	repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
	contents, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(strings.Replace(string(contents), "package: example", "package: different", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkNodeDependencies(repo, policy, lock); err == nil || !strings.Contains(err.Error(), "missing reachable") {
		t.Fatalf("missing policy error = %v", err)
	}
}

func TestNodeDependenciesRejectStalePolicyAndMissingNotice(t *testing.T) {
	repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
	contents, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(contents), "vendored:\n", "  - package: stale\n    version: 1.0.0\n    license: MIT\n    decision: allow\nvendored:\n", 1)
	if err := os.WriteFile(policy, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkNodeDependencies(repo, policy, lock); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale policy error = %v", err)
	}
	if err := os.WriteFile(policy, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "internal/webui/assets/vendor/marked.NOTICE.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkNodeDependencies(repo, policy, lock); err == nil || !strings.Contains(err.Error(), "notice") {
		t.Fatalf("missing notice error = %v", err)
	}
}

func TestNodeDependenciesRejectLockLicenseDrift(t *testing.T) {
	repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
	contents, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(strings.Replace(string(contents), `"license":"MIT"`, `"license":"Apache-2.0"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkNodeDependencies(repo, policy, lock); err == nil || !strings.Contains(err.Error(), "license policy") {
		t.Fatalf("lock license drift error = %v", err)
	}
}

func TestNodeDependenciesLicenseAllowlistRejectsPolicyAndLockDrift(t *testing.T) {
	for _, license := range []string{
		"0BSD", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "BlueOak-1.0.0", "ISC", "MIT", "Python-2.0", "WTFPL",
		"WTFPL OR ISC", "(MIT OR CC0-1.0)", "(WTFPL OR MIT)",
	} {
		t.Run("allows_"+license, func(t *testing.T) {
			repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
			setNodeFixtureLicense(t, policy, lock, license)
			if _, err := checkNodeDependencies(repo, policy, lock); err != nil {
				t.Fatalf("license %q rejected: %v", license, err)
			}
		})
	}
	for _, license := range []string{"", "UNKNOWN", "GPL-3.0-only", "MIT OR Apache-2.0", "MIT OR GPL-3.0-only", "MIT AND Apache-2.0", "LicenseRef-local"} {
		t.Run("rejects_"+license, func(t *testing.T) {
			repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
			setNodeFixtureLicense(t, policy, lock, license)
			if _, err := checkNodeDependencies(repo, policy, lock); err == nil {
				t.Fatalf("license %q accepted after matching policy and lock mutation", license)
			}
		})
	}
}

func TestNodeDependenciesEmitDeterministicIndependentSBOM(t *testing.T) {
	repo, policy, lock := nodeDependencyFixture(t, "https://registry.npmjs.org/example/-/example-1.0.0.tgz", `"integrity":"sha512-YQ=="`)
	first, err := checkNodeDependencies(repo, policy, lock)
	if err != nil {
		t.Fatal(err)
	}
	second, err := checkNodeDependencies(repo, policy, lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Components) != 2 || first.Components[0].PURL != "pkg:npm/example@1.0.0" || first.Components[1].Name != "marked" {
		t.Fatalf("unexpected components: %#v", first.Components)
	}
	firstJSON, _ := encodeJSON(first)
	secondJSON, _ := encodeJSON(second)
	if string(firstJSON) != string(secondJSON) || strings.Contains(string(firstJSON), "sbom.cdx.json") {
		t.Fatal("Node SBOM is not deterministic or is coupled to the Go SBOM")
	}
}

func nodeDependencyFixture(t *testing.T, resolved, details string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	writePublicationFile(t, repo, "internal/webui/assets/vendor/marked.NOTICE.txt", "Marked 18.0.9 is MIT licensed.\n")
	writePublicationFile(t, repo, "internal/webui/assets/vendor/marked.LICENSE.txt", "MIT License\n")
	policy := filepath.Join(repo, "quality/node-dependency-licenses.yaml")
	writePublicationFile(t, repo, "quality/node-dependency-licenses.yaml", `version: 1
registry_host: registry.npmjs.org
dependencies:
  - package: example
    version: 1.0.0
    license: MIT
    decision: allow
vendored:
  - package: marked
    version: 18.0.9
    license: MIT
    notice: internal/webui/assets/vendor/marked.NOTICE.txt
    license_file: internal/webui/assets/vendor/marked.LICENSE.txt
`)
	lock := filepath.Join(repo, "desktop/package-lock.json")
	writePublicationFile(t, repo, "desktop/package-lock.json", `{"name":"yhc-desktop","lockfileVersion":3,"packages":{"":{"name":"yhc-desktop","version":"0.1.0"},"node_modules/example":{"version":"1.0.0","license":"MIT","resolved":"`+resolved+`",`+details+`}}}`)
	return repo, policy, lock
}

func setNodeFixtureLicense(t *testing.T, policy, lock, license string) {
	t.Helper()
	policyContents, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(strings.Replace(string(policyContents), "license: MIT", "license: \""+license+"\"", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	lockContents, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(strings.Replace(string(lockContents), `"license":"MIT"`, `"license":"`+license+`"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}
