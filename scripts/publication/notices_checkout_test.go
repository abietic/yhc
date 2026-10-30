package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func windowsNoticeCheckout(t *testing.T, retainRule bool) string {
	t.Helper()
	source := noticeFixture(t)
	if retainRule {
		name := "third_party/notices/.gitattributes"
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err == nil {
			writePublicationFile(t, source, name, string(data))
		}
	}
	empty := filepath.Join(t.TempDir(), "empty-config")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		args = append([]string{"-c", "core.attributesfile=" + empty}, args...)
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = source
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+empty, "GIT_ATTR_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("real Git checkout: %v\n%s", err, output)
		}
	}
	git("init", "-q")
	git("-c", "core.autocrlf=false", "add", ".")
	checkout := t.TempDir()
	git("-c", "core.autocrlf=true", "checkout-index", "--all", "--prefix="+filepath.ToSlash(checkout)+"/")
	return checkout
}

func TestChromaWindowsCheckoutRetainsExactNoticeDistribution(t *testing.T) {
	binary := compiledNoticeFixture(t, "windows-amd64", chromaVersion, false)
	for _, tc := range []struct {
		name       string
		retainRule bool
		tamper     bool
		wantPass   bool
	}{
		{name: "reviewed_checkout", retainRule: true, wantPass: true},
		{name: "missing_rule_crlf", retainRule: false},
		{name: "changed_license_bytes", retainRule: true, tamper: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := windowsNoticeCheckout(t, tc.retainRule)
			copyingPath := filepath.Join(root, "third_party", "notices", "chroma.COPYING")
			copying, err := os.ReadFile(copyingPath)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.retainRule && !bytes.Contains(copying, []byte("\r\n")) {
				t.Fatal("unprotected Git checkout did not exercise CRLF conversion")
			}
			if tc.tamper {
				copying = append(copying, '\n')
				if err := os.WriteFile(copyingPath, copying, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writePublicationFile(t, root, "build/windows-amd64/yhc.exe", string(binary))
			t.Chdir(root)
			bundlePath := filepath.Join("build", "licenses")
			archivePath := filepath.Join("build", "packages", "windows-amd64.zip")
			bundleErr := bundleLicenses(root, bundlePath)
			archiveErr := packageCLI(root, "windows-amd64", archivePath)
			if !tc.wantPass {
				for name, err := range map[string]error{"bundle": bundleErr, "archive": archiveErr} {
					if err == nil || !strings.Contains(err.Error(), "chroma license source or digest") {
						t.Fatalf("%s accepted changed license bytes or failed for another reason: %v", name, err)
					}
				}
				for _, name := range []string{bundlePath, archivePath} {
					if _, err := os.Stat(name); !os.IsNotExist(err) {
						t.Fatalf("rejected distribution left output %s: %v", name, err)
					}
				}
				return
			}
			if bundleErr != nil || archiveErr != nil {
				t.Fatalf("Windows Git checkout cannot distribute reviewed notices: bundle=%v archive=%v", bundleErr, archiveErr)
			}
			hash := sha256.Sum256(copying)
			if hex.EncodeToString(hash[:]) != "13654b052f885cb3fa47d7343e13975d9c8a64c3bcd84993c35d5eb01f22b23f" {
				t.Fatal("Git checkout changed the exact upstream COPYING bytes")
			}
			bundled, err := os.ReadFile(filepath.Join(bundlePath, "chroma.COPYING"))
			if err != nil || !bytes.Equal(bundled, copying) {
				t.Fatalf("license bundle changed COPYING: %v", err)
			}
			archive, err := zip.OpenReader(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			if len(archive.File) != 7 {
				t.Fatalf("archive has %d entries, want binary and six notice files", len(archive.File))
			}
			found := false
			for _, file := range archive.File {
				if file.Name != "yhc-windows-amd64/licenses/chroma.COPYING" {
					continue
				}
				r, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil || !bytes.Equal(data, copying) {
					t.Fatalf("CLI archive changed COPYING: %v", err)
				}
				found = true
			}
			if !found {
				t.Fatal("CLI archive lost complete Chroma/font notices")
			}
		})
	}
}

func TestChromaCheckoutPolicyRetainsPublicationBoundary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "quality", "publication.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := matchRule(config.Rules, "third_party/notices/.gitattributes")
	if err != nil || rule.Class != "project-owned-original" || rule.Decision != "include" || rule.License != "" {
		t.Fatalf("checkout metadata was not classified independently: %+v %v", rule, err)
	}
	rule, err = matchRule(config.Rules, "third_party/notices/chroma.COPYING")
	if err != nil || rule.Class != "license-compatible-third-party" || rule.License != "MIT AND OFL-1.1-RFN" {
		t.Fatalf("retained license scopes changed: %+v %v", rule, err)
	}
	for _, name := range []string{"third_party/notices/unreviewed.COPYING", "third_party/notices/.gitattributes.extra", "third_party/notices/sub/.gitattributes"} {
		if _, err := matchRule(config.Rules, name); err == nil {
			t.Fatalf("checkout metadata rule admitted unreviewed path %s", name)
		}
	}
}
