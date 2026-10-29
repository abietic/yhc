package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func noticeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"LICENSE", "NOTICE", "go.sum", "internal/webui/assets/vendor/marked.LICENSE.txt", "internal/webui/assets/vendor/marked.NOTICE.txt", chromaNoticeRoot + "chroma.COPYING", chromaNoticeRoot + "chroma.PROVENANCE.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		writePublicationFile(t, root, name, string(data))
	}
	writePublicationFile(t, root, "go.mod", "module example.com/noticefixture\n\ngo 1.27.0\n\nrequire "+chromaModule+" "+chromaVersion+"\n")
	return root
}

func compiledNoticeFixture(t *testing.T, target, version string, replacement bool) []byte {
	t.Helper()
	root := t.TempDir()
	mod := "module example.com/noticebinary\n\ngo 1.27.0\n\nrequire " + chromaModule + " " + version + "\n"
	if replacement {
		mod += "replace " + chromaModule + " => ./replacement\n"
		writePublicationFile(t, root, "replacement/go.mod", "module "+chromaModule+"\n\ngo 1.27.0\n")
		writePublicationFile(t, root, "replacement/chroma.go", "package chroma\n")
	}
	writePublicationFile(t, root, "go.mod", mod)
	writePublicationFile(t, root, "main.go", "package main\nimport _ \""+chromaModule+"\"\nfunc main() {}\n")
	parts := strings.Split(target, "-")
	command := exec.Command("go", "build", "-mod=mod", "-o", "fixture", ".")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOOS="+parts[0], "GOARCH="+parts[1])
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build actual Go fixture: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCLIArchiveRetainsExactLicenseContentOnEveryPlatform(t *testing.T) {
	root := noticeFixture(t)
	t.Chdir(root)
	for _, target := range []string{"linux-amd64", "darwin-amd64", "darwin-arm64", "windows-amd64"} {
		t.Run(target, func(t *testing.T) {
			filename := "yhc"
			if strings.HasPrefix(target, "windows") {
				filename += ".exe"
			}
			binary := compiledNoticeFixture(t, target, chromaVersion, false)
			writePublicationFile(t, root, "build/"+target+"/"+filename, string(binary))
			output := filepath.Join("build", "packages", target+".zip")
			if err := packageCLI(root, target, output); err != nil {
				t.Fatal(err)
			}
			archive, err := zip.OpenReader(output)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			if len(archive.File) != 7 {
				t.Fatalf("archive has %d files, want executable and six readable license/provenance files", len(archive.File))
			}
			files := map[string][]byte{}
			for _, file := range archive.File {
				r, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil {
					t.Fatal(err)
				}
				files[strings.TrimPrefix(file.Name, "yhc-"+target+"/")] = data
			}
			if !bytes.Equal(files[filename], binary) {
				t.Fatal("package changed executable")
			}
			copying := files["licenses/chroma.COPYING"]
			hash := sha256.Sum256(copying)
			if hex.EncodeToString(hash[:]) != "13654b052f885cb3fa47d7343e13975d9c8a64c3bcd84993c35d5eb01f22b23f" {
				t.Fatal("archive does not retain exact upstream COPYING")
			}
			for _, required := range []string{"SIL OPEN FONT LICENSE Version 1.1", "Copyright (c) 2012 Red Hat, Inc.", "Digitized data copyright (c) 2010 Google Corporation", "Reserved Font Name Liberation"} {
				if !bytes.Contains(copying, []byte(required)) {
					t.Fatalf("archive is missing %q", required)
				}
			}
			var proof chromaNoticeProof
			if err := json.Unmarshal(files["licenses/chroma.PROVENANCE.json"], &proof); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(proof.ReviewedLicenses, []string{"MIT", "OFL-1.1-RFN"}) || !slices.Equal(proof.Detector.ObservedLicenses, []string{"OFL-1.1-no-RFN"}) {
				t.Fatal("reviewed scopes were conflated with detector output")
			}
		})
	}
}

func TestChromaDistributionRejectsSourceVersionAndReplacementDrift(t *testing.T) {
	for _, suffix := range []string{
		"require " + chromaModule + " v2.28.0\n",
		"require " + chromaModule + " " + chromaVersion + "\nreplace " + chromaModule + " => ./replacement\n",
		"require " + chromaModule + " " + chromaVersion + "\nreplace " + chromaModule + " " + chromaVersion + " => " + chromaModule + " v2.20.0\n",
	} {
		t.Run(suffix, func(t *testing.T) {
			root := noticeFixture(t)
			writePublicationFile(t, root, "go.mod", "module example.com/drift\n\ngo 1.27.0\n"+suffix)
			writePublicationFile(t, root, "replacement/go.mod", "module "+chromaModule+"\n\ngo 1.27.0\n")
			t.Chdir(root)
			if err := bundleLicenses(root, "build/licenses"); err == nil {
				t.Fatal("bundle accepted unreviewed source version or replacement")
			}
			writePublicationFile(t, root, "build/linux-amd64/yhc", "uninspected executable")
			if err := packageCLI(root, "linux-amd64", "build/cli.zip"); err == nil {
				t.Fatal("CLI accepted source drift")
			}
		})
	}
}

func TestChromaDistributionRejectsActualBinaryDrift(t *testing.T) {
	for _, tc := range []struct {
		name, target, version string
		replacement           bool
	}{
		{"old_version", "linux-amd64", "v2.20.0", false},
		{"local_replacement", "linux-amd64", chromaVersion, true},
		{"wrong_platform", "darwin-arm64", chromaVersion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := compiledNoticeFixture(t, tc.target, tc.version, tc.replacement)
			root := noticeFixture(t)
			writePublicationFile(t, root, "build/linux-amd64/yhc", string(binary))
			t.Chdir(root)
			if err := packageCLI(root, "linux-amd64", "build/cli.zip"); err == nil {
				t.Fatal("CLI accepted actual binary dependency or platform drift")
			}
		})
	}
}

func TestChromaFullLicenseCheckRejectsChangedObservedSet(t *testing.T) {
	entry := `- module: github.com/alecthomas/chroma/v2
  version: v2.27.0
  source_url: https://github.com/alecthomas/chroma/tree/v2.27.0
  licenses:
    - {spdx: MIT, license_file: COPYING, license_url: https://github.com/alecthomas/chroma/blob/v2.27.0/COPYING}
    - {spdx: OFL-1.1-RFN, license_file: COPYING, license_url: https://github.com/alecthomas/chroma/blob/v2.27.0/COPYING}
  compatibility: allow
  notice: retain
  detector_override: mixed-license-scope-reviewed`
	for _, observed := range []string{"Apache-2.0", "CC-BY-4.0", "unknown-license", chromaObservedSPDX} {
		t.Run(observed, func(t *testing.T) {
			repo, policy, sbom := dependencyFixture(t, entry, component(chromaModule, chromaVersion, observed))
			for _, name := range []string{"chroma.COPYING", "chroma.PROVENANCE.json"} {
				data, err := os.ReadFile(filepath.Join("..", "..", chromaNoticeRoot, name))
				if err != nil {
					t.Fatal(err)
				}
				writePublicationFile(t, repo, chromaNoticeRoot+name, string(data))
			}
			withDependencyGoList(t, goList(chromaModule, false, chromaModule, chromaVersion, ""))
			_, err := checkDependencyLicenses(context.Background(), repo, policy, sbom)
			if (err == nil) != (observed == chromaObservedSPDX) {
				t.Fatalf("unexpected acceptance for observed %s: %v", observed, err)
			}
		})
	}
	policy, err := loadDependencyPolicy(filepath.Join("..", "..", "quality", "dependency-licenses.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	modules := map[string]*dependencyModule{chromaModule + "@" + chromaVersion: {path: chromaModule, version: chromaVersion}}
	for _, other := range []string{"MIT", "Apache-2.0", "CC-BY-4.0"} {
		var component cycloneDXComponent
		if err := json.Unmarshal([]byte(`{"name":"`+chromaModule+`","version":"`+chromaVersion+`","licenses":[{"license":{"id":"`+other+`"}},{"license":{"id":"`+chromaObservedSPDX+`"}}]}`), &component); err != nil {
			t.Fatal(err)
		}
		if _, err := reconcileSBOM(policy, modules, map[string]cycloneDXComponent{chromaModule + "@" + chromaVersion: component}); err == nil {
			t.Fatal("accepted an expanded detector observation set")
		}
	}
}

func TestChromaCopyingPublicationExpressionIsExplicit(t *testing.T) {
	if !validLicenseID("MIT AND OFL-1.1-RFN") {
		t.Fatal("reviewed combined COPYING expression rejected")
	}
	if validLicenseID("MIT AND unknown-license") {
		t.Fatal("unreviewed expression accepted")
	}
}

func TestChromaNoticeRejectsDriftAndUnreviewedClassification(t *testing.T) {
	for _, kind := range []string{"copying", "source_hash", "conclusion", "detector", "reserved_name", "unknown_field"} {
		t.Run(kind, func(t *testing.T) {
			root := noticeFixture(t)
			if kind == "copying" {
				writePublicationFile(t, root, chromaNoticeRoot+"chroma.COPYING", "MIT only")
			} else {
				p := filepath.Join(root, chromaNoticeRoot+"chroma.PROVENANCE.json")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				var proof map[string]any
				if err := json.Unmarshal(data, &proof); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "source_hash":
					proof["source_sha256"] = strings.Repeat("0", 64)
				case "conclusion":
					proof["reviewed_licenses"] = []string{"OFL-1.1-no-RFN"}
				case "detector":
					proof["detector"].(map[string]any)["observed_licenses"] = []string{"MIT"}
				case "reserved_name":
					proof["reserved_font_names"] = []string{}
				case "unknown_field":
					proof["auto_approve"] = true
				}
				data, err = json.Marshal(proof)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadChromaNoticeProof(root); err == nil {
				t.Fatal("accepted changed source or unreviewed license metadata")
			}
		})
	}
}

func TestChromaDetectorExceptionIsVersionBoundAndNotAPolicyLicense(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		allow         bool
	}{{chromaModule, chromaVersion, true}, {chromaModule, "v2.28.0", false}, {"example.com/other", chromaVersion, false}} {
		c := cycloneDXComponent{Name: tc.name, Version: tc.version}
		var license cycloneDXLicense
		license.License.ID = "OFL-1.1-no-RFN"
		c.Evidence.Licenses = []cycloneDXLicense{license}
		_, err := componentSPDX(c)
		if (err == nil) != tc.allow {
			t.Fatalf("detector exception for %s@%s err=%v", tc.name, tc.version, err)
		}
	}
	if supportedSPDX("OFL-1.1-no-RFN") || supportedSPDX("unknown-license") {
		t.Fatal("raw classification or unknown license became an allowed policy conclusion")
	}
	policy, err := loadDependencyPolicy(filepath.Join("..", "..", "quality", "dependency-licenses.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	entry := policyEntries(policy)[chromaModule+"@"+chromaVersion]
	entry.Notice = "none"
	if err := validateDependencyEntry(entry); err == nil {
		t.Fatal("OFL attribution omission accepted")
	}
}

func TestCLIArchiveRejectsUnsafeBinaryAndLicenseSource(t *testing.T) {
	root := noticeFixture(t)
	writePublicationFile(t, root, "build/linux-amd64/target", "not the executable")
	if err := os.Symlink("target", filepath.Join(root, "build/linux-amd64/yhc")); err != nil {
		t.Fatal(err)
	}
	if err := packageCLI(root, "linux-amd64", filepath.Join(t.TempDir(), "cli.zip")); err == nil {
		t.Fatal("accepted symlink executable")
	}
	if err := packageCLI(root, "../outside", filepath.Join(t.TempDir(), "cli.zip")); err == nil {
		t.Fatal("accepted unknown target")
	}
	if err := os.Remove(filepath.Join(root, chromaNoticeRoot+"chroma.COPYING")); err != nil {
		t.Fatal(err)
	}
	if err := bundleLicenses(root, t.TempDir()); err == nil {
		t.Fatal("accepted missing source license")
	}
}
