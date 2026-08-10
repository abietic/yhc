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

func TestInventoryCommandWritesNULDelimitedCurrentIdentityPaths(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{
		".agents/skills/demo/SKILL.md":       "current eino-agent workflow\n",
		"AGENTS.md":                          "Eino AgenticModel remains an Eino framework API.\n",
		"README.md":                          "# eino-agent\n",
		"docs/architecture/current.md":       "run eino-agent exec\n",
		"docs/architecture/state.md":         "legacy path `.eino-agent/transcripts`\n",
		"docs/migration/history/old-name.md": "historical Eino-Agent release\n",
		"scripts/publication/fixture.txt":    "tracked subdirectory fixture\n",
	})
	configPath := filepath.Join(repo, "policy.yaml")
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: candidate
    include: [README.md, AGENTS.md, docs/**, .agents/**, scripts/**]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	writePublicationFile(t, repo, "README.md", "# YHC\n")
	output := "build/publication/inventory.json"
	commandRoot := filepath.Join(repo, "scripts", "publication")
	if err := runIn(context.Background(), commandRoot, []string{"inventory", "--config", configPath, "--output", output}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(commandRoot, "build", "publication", currentIdentityPathsFilename))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		".agents/skills/demo/SKILL.md",
		"README.md",
		"docs/architecture/current.md",
		"",
	}, "\x00")
	if string(contents) != want {
		t.Fatalf("current identity paths = %q, want %q", contents, want)
	}
}

func TestPublicationScanExpressionCommandEmitsOnlyRedactedDeterministicFindings(t *testing.T) {
	root := t.TempDir()
	writePublicationFile(t, root, "README.md", "contact private@example.com\n")
	configPath := filepath.Join(t.TempDir(), "policy.yaml")
	writePublicationFile(t, filepath.Dir(configPath), filepath.Base(configPath), publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	args := []string{"scan-expression", "--config", configPath, "--root", root}
	var first, second bytes.Buffer
	err := run(context.Background(), args, &first, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "private@example.com") {
		t.Fatalf("scan error was absent or exposed a finding value: %v", err)
	}
	if err := run(context.Background(), args, &second, &bytes.Buffer{}); err == nil {
		t.Fatal("repeat scan unexpectedly passed")
	}
	if first.String() != second.String() || strings.Contains(first.String(), "private@example.com") {
		t.Fatal("scan report is nondeterministic or exposes the matched value")
	}
	var report ScanReport
	if err := json.Unmarshal(first.Bytes(), &report); err != nil {
		t.Fatalf("decode scan report: %v", err)
	}
	if len(report.Findings) != 1 || report.Findings[0].RuleID != "private-email" || len(report.Findings[0].MatchSHA256) != 64 {
		t.Fatalf("unexpected scan report: %#v", report)
	}
}

func TestPublicationCommandsRejectMissingFlagsAndPositionalArguments(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "policy.yaml")
	writePublicationFile(t, filepath.Dir(configPath), filepath.Base(configPath), publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	tests := [][]string{
		{"scan-expression", "--config", configPath},
		{"materialize", "--config", configPath, "--source-commit", strings.Repeat("a", 40)},
		{"check-tree", "--config", configPath},
		{"manifest", "--config", configPath, "--root", t.TempDir()},
		{"normalize-sbom", "--config", configPath, "--output", filepath.Join(t.TempDir(), "normalized.json")},
		{"normalize-sbom", "--config", configPath, "--input", filepath.Join(t.TempDir(), "raw.json")},
		{"inventory", "--config", configPath, "extra"},
		{"normalize-sbom", "--config", configPath, "--input", "raw.json", "--output", "normalized.json", "extra"},
	}
	for _, args := range tests {
		if err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid command arguments: %v", args)
		}
	}
}
