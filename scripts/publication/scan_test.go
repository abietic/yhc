package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScanExpressionDetectsPrivatePathEmailEndpointAndCredentialPatterns(t *testing.T) {
	entropyCanary := "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6-_"
	repo := newPublicationRepo(t, map[string]string{"README.md": strings.Join([]string{
		"path /Users/Alice/src", "mail alice@private.example", "endpoint HTTPS://private.example.:9443/api", "broken https://",
		"-----BEGIN OPENSSH PRIVATE KEY-----", "api_key = credential-value-123", "provider sk-ant-provider-token-123456", "Authorization: Bearer bearer-token-123456", entropyCanary,
	}, "\n")})
	report, err := scanExpression(context.Background(), Config{}, repo)
	if err != nil {
		t.Fatalf("scan expression: %v", err)
	}
	want := map[string]bool{"home-path": false, "private-email": false, "private-url": false, "private-key": false, "credential-assignment": false, "provider-token": false, "bearer-token": false, "high-entropy-token": false}
	for _, finding := range report.Findings {
		if _, ok := want[finding.RuleID]; ok {
			want[finding.RuleID] = true
		}
		if finding.Path != "README.md" || finding.Line < 1 || len(finding.MatchSHA256) != 64 {
			t.Fatalf("unsafe finding: %#v", finding)
		}
	}
	for rule, found := range want {
		if !found {
			t.Errorf("missing %s in %#v", rule, report.Findings)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"Alice", "alice@private.example", "credential-value-123", entropyCanary} {
		if strings.Contains(string(encoded), raw) {
			t.Fatalf("report leaked %q: %s", raw, encoded)
		}
	}
}

func TestScanEntropySkipsOnlyGoIdentifiers(t *testing.T) {
	canary := "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6"
	hasEntropy := func(contents, name string, config Config) bool {
		t.Helper()
		for _, finding := range scanBytes([]byte(contents), name, config) {
			if finding.RuleID == "high-entropy-token" {
				return true
			}
		}
		return false
	}
	if hasEntropy("package p\nvar "+canary+" = 1\n", "fixture.go", Config{}) {
		t.Fatal("reported a legal Go identifier as a high-entropy value")
	}
	for name, contents := range map[string]string{
		"string.go":   "package p\nvar value = \"" + canary + "\"\n",
		"comment.go":  "package p\n// " + canary + "\n",
		"README.md":   canary + "\n",
		"fixture.yml": "value: " + canary + "\n",
	} {
		if !hasEntropy(contents, name, Config{}) {
			t.Errorf("%s: high-entropy value was not reported", name)
		}
	}
	variant := canary[:len(canary)-1] + "7"
	if !hasEntropy("package p\nvar value = \""+variant+"\"\n", "variant.go", Config{Privacy: PrivacyPolicy{TestSentinels: []string{canary}}}) {
		t.Fatal("an exact sentinel suppressed a one-character variant")
	}
	findings := scanBytes([]byte("package p\nvar value = \"sk-ant-provider-token-123456\"\n"), "provider.go", Config{})
	providerFound := false
	for _, finding := range findings {
		providerFound = providerFound || finding.RuleID == "provider-token"
	}
	if !providerFound {
		t.Fatal("identifier filtering suppressed the dedicated provider detector")
	}
	for _, finding := range scanBytes([]byte("evidence: task-1-inventory\n"), "policy.yaml", Config{}) {
		if finding.RuleID == "provider-token" {
			t.Fatal("provider detector matched an embedded sk- substring")
		}
	}
	if hasEntropy("evidence: docs/superpowers/plans/2026-08-09-yhc-publication-readiness.md\n", "policy.yaml", Config{}) {
		t.Fatal("reported a repository path literal as a high-entropy value")
	}
}

func TestScanEmailDistinguishesModuleVersionsFromContacts(t *testing.T) {
	contents := []byte(strings.Join([]string{
		"contact maintainer@private.example",
		"numbered domain owner@1.example.com",
		"source https://pkg.go.dev/golang.org/x/net@v0.55.0",
		"purl pkg:golang/github.com/example/module@v1.2.3?type=module",
		"npm @agentclientprotocol/sdk@1.3.0",
	}, "\n"))
	findings := scanBytes(contents, "sbom.cdx.json", Config{Privacy: PrivacyPolicy{AllowedURLHosts: []string{"pkg.go.dev"}}})
	privateEmails := 0
	for _, finding := range findings {
		if finding.RuleID == "private-email" {
			privateEmails++
		}
	}
	if privateEmails != 2 {
		t.Fatalf("private email findings = %d, want both real contacts: %#v", privateEmails, findings)
	}
}

func TestScanURLRequiresACompletePrivateHost(t *testing.T) {
	contents := []byte("documentation https:// and http://\nendpoint https://private.example/path\n")
	privateURLs := 0
	for _, finding := range scanBytes(contents, "README.md", Config{}) {
		if finding.RuleID == "private-url" {
			privateURLs++
		}
	}
	if privateURLs != 1 {
		t.Fatalf("private URL findings = %d, want only the complete endpoint", privateURLs)
	}
}

func TestScanCredentialPrefixesRequirePlausiblePayloads(t *testing.T) {
	contents := []byte(strings.Join([]string{
		"docs Bearer token",
		"anthropic prefix sk-ant-",
		"openai prefix sk-",
		"authorization Bearer bearer-token-123456",
		"credential sk-ant-provider-token-123456",
	}, "\n"))
	counts := map[string]int{}
	for _, finding := range scanBytes(contents, "README.md", Config{}) {
		counts[finding.RuleID]++
	}
	if counts["bearer-token"] != 1 || counts["provider-token"] != 1 {
		t.Fatalf("credential prefix counts = %#v, want one plausible bearer and provider token", counts)
	}
}

func TestScanExpressionRequiresExactCurrentReviewedFindings(t *testing.T) {
	contents := []byte("Authorization: Bearer bearer-token-123456\n")
	root := t.TempDir()
	writePublicationFile(t, root, "README.md", string(contents))
	raw := scanBytes(contents, "README.md", Config{})
	if len(raw) != 1 || raw[0].RuleID != "bearer-token" {
		t.Fatalf("unexpected raw fixture findings: %#v", raw)
	}
	reviewed := ReviewedFinding{
		Path:        raw[0].Path,
		Line:        raw[0].Line,
		RuleID:      raw[0].RuleID,
		MatchSHA256: raw[0].MatchSHA256,
		Purpose:     "synthetic-security-fixture",
	}
	config := Config{Privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{reviewed}}}
	report, err := scanExpression(context.Background(), config, root)
	if err != nil {
		t.Fatalf("exact reviewed finding was rejected: %v", err)
	}
	if len(report.Findings) != 0 || report.ReviewedFindings != 1 {
		t.Fatalf("reviewed finding was not accounted for: %#v", report)
	}

	reviewed.Line++
	config.Privacy.ReviewedFindings[0] = reviewed
	if report, err := scanExpression(context.Background(), config, root); err == nil || len(report.Findings) != 0 {
		t.Fatalf("stale reviewed finding did not fail closed: report=%#v err=%v", report, err)
	}
}

func TestScanGoCredentialAssignmentsRequireLiteralEvidence(t *testing.T) {
	credentialFindings := func(contents string) []ScanFinding {
		t.Helper()
		findings := make([]ScanFinding, 0)
		for _, finding := range scanBytes([]byte(contents), "fixture.go", Config{}) {
			if finding.RuleID == "credential-assignment" {
				findings = append(findings, finding)
			}
		}
		return findings
	}

	symbolic := `package fixture

var model = struct{ CostPerInputToken float64 }{
	CostPerInputToken: 0.0000002,
}

var config = struct{ APIKey string }{
	APIKey: credentials.APIKey,
}
`
	if findings := credentialFindings(symbolic); len(findings) != 0 {
		t.Fatalf("reported numeric or symbolic Go expressions as credentials: %#v", findings)
	}

	literals := `package fixture

func literal() {
	apiKey := "synthetic-go-secret"
	_ = apiKey
}
// token = synthetic-comment-secret
var schema = "token = synthetic-string-secret"
`
	if findings := credentialFindings(literals); len(findings) != 3 {
		t.Fatalf("literal credential findings = %d, want 3: %#v", len(findings), findings)
	}
}

func TestScanExpressionMasksOnlyValidatedNodeLockFields(t *testing.T) {
	resolved := "https://" + "registry.npmjs.org/example/-/example-1.0.0.tgz"
	repo, _, lock := nodeDependencyFixture(t, resolved, `"integrity":"sha512-YQ=="`)
	if report, err := scanExpression(context.Background(), Config{}, repo); err != nil || len(report.Findings) != 0 {
		t.Fatalf("valid structured Node lock scan = report=%#v err=%v", report, err)
	}
	contents, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Join([]string{"Bearer ", "malicious-", "token-123456"}, "")
	needle := `"license":"MIT"`
	replacement := needle + `,"metadata":"` + secret + `"`
	if err := os.WriteFile(lock, []byte(strings.Replace(string(contents), needle, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := scanExpression(context.Background(), Config{}, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.RuleID == "bearer-token" {
			return
		}
	}
	t.Fatalf("unknown lockfile field escaped generic scan: %#v", report.Findings)
}

func TestScanExpressionAllowsDocumentedPublicContactsAndTestSentinels(t *testing.T) {
	t.Run("allowlists and exact sentinels", func(t *testing.T) {
		provider := "sk-provider-sentinel-123456"
		repo := newPublicationRepo(t, map[string]string{"README.md": strings.Join([]string{
			"PUBLIC@EXAMPLE.COM", "wss://Public.Example.:444/path", "/home/${USER}/src", "token = sentinel-token-123", provider,
			"Bearer bearer-sentinel-123", "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6-_",
		}, "\n")})
		config := Config{Privacy: PrivacyPolicy{AllowedEmails: []string{"public@example.com"}, AllowedURLHosts: []string{"public.example"}, TestSentinels: []string{"sentinel-token-123", provider, "bearer-sentinel-123", "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6-_"}}}
		report, err := scanExpression(context.Background(), config, repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Findings) != 0 || report.Findings == nil {
			t.Fatalf("allowlisted or sentinel values reported: %#v", report)
		}
		first, _ := json.Marshal(report)
		second, _ := json.Marshal(report)
		if string(first) != string(second) || strings.Contains(string(first), "sentinel-token") {
			t.Fatalf("nondeterministic or unredacted report: %s", first)
		}
	})
	t.Run("unsafe and colliding paths are rejected", func(t *testing.T) {
		for _, stream := range [][]byte{
			[]byte("100644 deadbeef 0\tbad \x00"),
			[]byte("100644 deadbeef 0\te\xcc\x81.txt\x00100644 deadbeef 0\t\xc3\xa9.txt\x00"),
		} {
			if _, err := parseScanGitPaths(stream); err == nil {
				t.Fatal("accepted unsafe or colliding path")
			}
		}
	})
	t.Run("cancellation, ignored roots, and symlink replacement fail closed", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n", ".gitignore": "ignored/\n"})
		for _, name := range []string{"ignored/secret.txt", "untracked.txt", ".reference/secret.txt", ".eino-agent/transcript.jsonl", ".claude/credentials.json"} {
			writePublicationFile(t, repo, name, "never read")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := scanExpression(ctx, Config{}, repo); err == nil {
			t.Fatal("accepted cancelled scan")
		}
		oldHook := scanReadFile
		t.Cleanup(func() { scanReadFile = oldHook })
		scanReadFile = func(_ *os.Root, name string) ([]byte, error) {
			if name != "README.md" && name != ".gitignore" && name != migrationMappingManifest {
				t.Fatalf("opened forbidden or untracked %q", name)
			}
			return []byte("public\n"), nil
		}
		if _, err := scanExpression(context.Background(), Config{}, repo); err != nil {
			t.Fatalf("tracked-only scan: %v", err)
		}
		scanReadFile = oldHook
		if runtime.GOOS != "windows" {
			outside := filepath.Join(repo, "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			if _, err := scanExpression(context.Background(), Config{}, repo); err == nil {
				t.Fatal("followed a worktree symlink")
			}
		}
	})
	t.Run("root identity and Git root are pinned", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
		if err := os.Mkdir(filepath.Join(repo, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := scanExpression(context.Background(), Config{}, filepath.Join(repo, "nested")); err == nil {
			t.Fatal("accepted a Git worktree subdirectory")
		}
		if runtime.GOOS != "windows" {
			link := filepath.Join(t.TempDir(), "repository-link")
			if err := os.Symlink(repo, link); err != nil {
				t.Fatal(err)
			}
			if _, err := scanExpression(context.Background(), Config{}, link); err == nil {
				t.Fatal("accepted a symlink scan root")
			}
		}
		root := t.TempDir()
		writePublicationFile(t, root, "README.md", "public\n")
		detached := root + "-detached"
		oldWalk := scanTreeAfterWalk
		t.Cleanup(func() { scanTreeAfterWalk = oldWalk })
		scanTreeAfterWalk = func(_ *os.Root, name string) {
			if name != "" {
				return
			}
			if err := os.Rename(root, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanExpression(context.Background(), Config{}, root); err == nil {
			t.Fatal("accepted a replaced scan root")
		}
	})
	t.Run("tree and file identities are rechecked after hooks", func(t *testing.T) {
		root := t.TempDir()
		writePublicationFile(t, root, "directory/README.md", "public\n")
		oldRead, oldWalk := scanReadFile, scanTreeAfterWalk
		t.Cleanup(func() { scanReadFile, scanTreeAfterWalk = oldRead, oldWalk })
		scanTreeAfterWalk = func(_ *os.Root, name string) {
			if name != "directory" {
				return
			}
			if err := os.Rename(filepath.Join(root, "directory"), filepath.Join(root, "replaced")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanExpression(context.Background(), Config{}, root); err == nil {
			t.Fatal("accepted replaced tree directory")
		}
		_ = os.RemoveAll(filepath.Join(root, "directory"))
		if err := os.Rename(filepath.Join(root, "replaced"), filepath.Join(root, "directory")); err != nil {
			t.Fatal(err)
		}
		scanTreeAfterWalk = nil
		scanReadFile = func(_ *os.Root, name string) ([]byte, error) {
			if err := os.Rename(filepath.Join(root, "directory", name), filepath.Join(root, "replaced-file")); err != nil {
				return nil, err
			}
			return []byte("public\n"), nil
		}
		if _, err := scanExpression(context.Background(), Config{}, root); err == nil {
			t.Fatal("hook bypassed post-read identity check")
		}
	})
	t.Run("in-place content changes are rehashed", func(t *testing.T) {
		root := t.TempDir()
		writePublicationFile(t, root, "README.md", "public\n")
		oldHook := scanReadFile
		t.Cleanup(func() { scanReadFile = oldHook })
		modified := false
		scanReadFile = func(_ *os.Root, name string) ([]byte, error) {
			if name == "README.md" && !modified {
				modified = true
				credentialLine := strings.Join([]string{"api", "_key = in-place-", "secret-123\n"}, "")
				if err := os.WriteFile(filepath.Join(root, name), []byte(credentialLine), 0o644); err != nil {
					return nil, err
				}
			}
			return nil, nil
		}
		if _, err := scanExpression(context.Background(), Config{}, root); err == nil {
			t.Fatal("accepted an in-place content change after inspection")
		}
	})
	t.Run("URL spans suppress entropy and allowlists are exact", func(t *testing.T) {
		urlCanary := "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6-_"
		findings := scanBytes([]byte("https://public.example/"+urlCanary+" https://sub.public.example/ok"), "README.md", Config{Privacy: PrivacyPolicy{AllowedURLHosts: []string{"public.example"}}})
		for _, finding := range findings {
			if finding.RuleID == "high-entropy-token" {
				t.Fatalf("reported a token wholly inside a URL: %#v", finding)
			}
		}
		urls := 0
		for _, finding := range findings {
			if finding.RuleID == "private-url" {
				urls++
			}
		}
		if urls != 1 {
			t.Fatalf("allowlist accepted a subdomain or rejected exact host: %#v", findings)
		}
	})
}
