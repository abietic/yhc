package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationConfigRejectsUnknownFieldsAndDuplicateRuleIDs(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
		writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: root
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
unknown: value
`))
		if err := publicationRun(repo, "check", "--config", "policy.yaml"); err == nil {
			t.Fatal("check accepted an unknown field")
		}
	})
	t.Run("duplicate rule ID", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
		writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: root
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
  - id: root
    include: [go.mod]
    class: project-owned-original
    decision: unresolved
    evidence: [review]
`))
		if err := publicationRun(repo, "check", "--config", "policy.yaml"); err == nil {
			t.Fatal("check accepted duplicate rule IDs")
		}
	})
	t.Run("unsafe rule ID", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
		writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: Unsafe_ID
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
		if err := publicationRun(repo, "check", "--config", "policy.yaml"); err == nil {
			t.Fatal("check accepted an unsafe rule ID")
		}
	})
}

func TestPublicationConfigCheckRejectsUnsafeIncludes(t *testing.T) {
	for _, test := range []struct {
		name    string
		class   string
		license string
	}{
		{name: "private operational", class: "private-operational"},
		{name: "proprietary", class: "proprietary-or-reconstructable"},
		{name: "reference without mapping", class: "reference-informed-independent"},
		{name: "third party without license", class: "license-compatible-third-party"},
		{name: "third party without license evidence", class: "license-compatible-third-party", license: "Apache-2.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
			license := ""
			if test.license != "" {
				license = "    license: " + test.license + "\n"
			}
			writePublicationFile(t, repo, "policy.yaml", publicationConfig("\nrules:\n  - id: docs\n    include: [README.md]\n    class: "+test.class+"\n    decision: include\n"+license+"    evidence: [review]\n"))
			if err := publicationRun(repo, "check", "--config", "policy.yaml"); err == nil {
				t.Fatal("check accepted unsafe included material")
			}
		})
	}
}

func TestPublicationConfigCheckAcceptsIncludedThirdPartyLicenseEvidence(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{"LICENSE": "Apache License\n"})
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: license
    include: [LICENSE]
    class: license-compatible-third-party
    decision: include
    license: Apache-2.0
    evidence: [LICENSE]
`))
	if err := publicationRun(repo, "check", "--config", "policy.yaml"); err != nil {
		t.Fatalf("check rejected included license evidence: %v", err)
	}
}

func TestPublicationConfigRejectsNoncanonicalPrivacyAllowlists(t *testing.T) {
	tests := []struct {
		name    string
		privacy PrivacyPolicy
	}{
		{name: "email whitespace", privacy: PrivacyPolicy{AllowedEmails: []string{" security@example.com"}}},
		{name: "email case duplicate", privacy: PrivacyPolicy{AllowedEmails: []string{"Security@example.com", "security@example.com"}}},
		{name: "URL host uppercase", privacy: PrivacyPolicy{AllowedURLHosts: []string{"Public.Example"}}},
		{name: "URL host port", privacy: PrivacyPolicy{AllowedURLHosts: []string{"public.example:443"}}},
		{name: "sentinel whitespace", privacy: PrivacyPolicy{TestSentinels: []string{" test-token"}}},
		{name: "sentinel multiline", privacy: PrivacyPolicy{TestSentinels: []string{"test\ntoken"}}},
		{name: "reviewed unsafe path", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{{Path: "../README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "synthetic-security-fixture"}}}},
		{name: "reviewed zero line", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{{Path: "README.md", RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "synthetic-security-fixture"}}}},
		{name: "reviewed unknown rule", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{{Path: "README.md", Line: 1, RuleID: "unknown", MatchSHA256: strings.Repeat("a", 64), Purpose: "synthetic-security-fixture"}}}},
		{name: "reviewed bad digest", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{{Path: "README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("A", 64), Purpose: "synthetic-security-fixture"}}}},
		{name: "reviewed unknown purpose", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{{Path: "README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "waive"}}}},
		{name: "reviewed duplicate", privacy: PrivacyPolicy{ReviewedFindings: []ReviewedFinding{
			{Path: "README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "synthetic-security-fixture"},
			{Path: "README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "documentation-example"},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePrivacy(test.privacy); err == nil {
				t.Fatal("accepted noncanonical privacy allowlist")
			}
		})
	}
	if err := validatePrivacy(PrivacyPolicy{AllowedEmails: []string{"Security@example.com"}, AllowedURLHosts: []string{"public.example"}, TestSentinels: []string{"TEST_TOKEN_123456"}, ReviewedFindings: []ReviewedFinding{{Path: "README.md", Line: 1, RuleID: "private-email", MatchSHA256: strings.Repeat("a", 64), Purpose: "synthetic-security-fixture"}}}); err != nil {
		t.Fatalf("rejected canonical privacy policy: %v", err)
	}
}

func newPublicationRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	if _, exists := files[migrationMappingManifest]; !exists {
		original := files
		files = make(map[string]string, len(files)+1)
		for name, contents := range original {
			files[name] = contents
		}
		files[migrationMappingManifest] = "version: 4\nfiles: []\n"
	}
	for name, contents := range files {
		writePublicationFile(t, repo, name, contents)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	return repo
}

func writePublicationFile(t *testing.T, root, name, contents string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
