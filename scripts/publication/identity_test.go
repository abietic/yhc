package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

var currentIdentityPaths = []string{
	"cmd/yhc/cmd/root.go",
	"cmd/yhc/cmd/serve.go",
	"cmd/yhc/cmd/serve_acp.go",
	"cmd/yhc/cmd/serve_mcp.go",
	"cmd/yhc/cmd/sessions.go",
	"engine/commands/registry.go",
	"engine/config/config.go",
	"engine/errors/errors.go",
	"engine/mcp/sdk_client.go",
	"engine/onboarding/onboarding.go",
	"engine/plugins/bundled/workflows.json",
	"engine/plugins/loader.go",
	"engine/session_service.go",
	"internal/buildinfo/buildinfo.go",
	"internal/identity/env.go",
	"internal/tui/app.go",
	"internal/tui/attachments/attachments.go",
	"internal/tui/composer_editor.go",
	"internal/tui/external_editor.go",
	"internal/tui/help.go",
	"internal/tui/parity/driver.go",
	"internal/tui/parity/normalize.go",
	"internal/tui/parity/parity.go",
	"internal/tui/parity/parity_test.go",
	"internal/tui/parity/scenario.go",
	"internal/tui/welcome.go",
	"scripts/evaluation/main.go",
	"scripts/evaluation/report.go",
	"scripts/evaluation/runner.go",
	"scripts/migration_scan/main.go",
	"server/acp/agent.go",
	"server/acp/goal_extension.go",
	"server/mcp/server.go",
	"tools/pdf.go",
	"tools/webfetch.go",
	"docs/README.md",
	"docs/migration/history/runtime/2026-07-23-runtime-hardening.md",
	"docs/migration/reference/runtime/command-surface-audit.md",
}

func TestCurrentIdentityAllowsLegacyOnlyForHistoryMappingAndCompatibility(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reviewPaths, err := currentIdentityReviewPaths(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	paths := append([]string(nil), currentIdentityPaths...)
	for _, name := range reviewPaths {
		if isCurrentIdentityReviewPath(name) {
			paths = append(paths, name)
		}
	}
	observed := map[string]bool{}
	seen := map[string]struct{}{}
	for _, name := range paths {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		for lineNumber := 1; scanner.Scan(); lineNumber++ {
			line := scanner.Text()
			if !hasLegacyIdentityLine(line) {
				continue
			}
			policyID := classifyLegacyIdentityLine(name, line)
			if policyID == identityPolicyCurrentCopy {
				t.Errorf("%s:%d has an unclassified legacy identity: %s", name, lineNumber, line)
				continue
			}
			observed[policyID] = true
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, policyID := range []string{
		identityPolicyHistory,
		identityPolicySourceMapping,
		identityPolicyLegacyState,
		identityPolicyLegacyEnvironment,
		identityPolicyLegacyProtocol,
	} {
		if !observed[policyID] {
			t.Errorf("identity scan observed no %s evidence", policyID)
		}
	}
}

func currentIdentityReviewPaths(ctx context.Context, root string) ([]string, error) {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		entries, listErr := trackedEntries(ctx)
		if listErr != nil {
			return nil, listErr
		}
		paths := make([]string, 0, len(entries))
		for _, entry := range entries {
			paths = append(paths, entry.path)
		}
		return paths, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect repository metadata: %w", err)
	}
	return currentIdentityDistributionPaths(root)
}

func currentIdentityDistributionPaths(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect distribution root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("distribution root %q is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, name)
		if relErr != nil {
			return fmt.Errorf("resolve distribution path %q: %w", name, relErr)
		}
		if relative == "." {
			return nil
		}
		repositoryPath := filepath.ToSlash(relative)
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("inspect distribution path %q: %w", repositoryPath, infoErr)
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			return fmt.Errorf("distribution path %q is not a regular file or directory", repositoryPath)
		}
		if repositoryPath == "build" {
			if !entryInfo.IsDir() {
				return fmt.Errorf("generated path %q is not a directory", repositoryPath)
			}
			return filepath.SkipDir
		}
		if repositoryPath == "PUBLICATION_MANIFEST.json" {
			if !entryInfo.Mode().IsRegular() {
				return fmt.Errorf("generated path %q is not a regular file", repositoryPath)
			}
			return nil
		}
		if entryInfo.IsDir() {
			return nil
		}
		if err := validateRepositoryPath(repositoryPath); err != nil {
			return err
		}
		paths = append(paths, repositoryPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk distribution tree: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func TestCurrentIdentityDistributionPathsSkipGeneratedEvidence(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"README.md", "docs/owner.md", "build/publication/report.json", "PUBLICATION_MANIFEST.json"} {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := currentIdentityDistributionPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "docs/owner.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("distribution paths = %v, want %v", paths, want)
	}
}

func TestCurrentIdentityDistributionPathsRejectSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := currentIdentityDistributionPaths(root); err == nil {
		t.Fatal("currentIdentityDistributionPaths accepted a symlink")
	}
}

func TestLegacyEnvironmentPolicyRejectsMixedOrUnknownCurrentCopy(t *testing.T) {
	tests := []struct {
		name string
		path string
		line string
	}{
		{
			name: "legacy product copy beside a supported alias",
			path: "cmd/yhc/cmd/root.go",
			line: `label := "Eino Agent"; enabled := envFlagEnabled("EINO_AGENT_SIMPLE")`,
		},
		{
			name: "unregistered environment alias",
			path: "cmd/yhc/cmd/root.go",
			line: `enabled := envFlagEnabled("EINO_AGENT_UNREGISTERED")`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyLegacyIdentityLine(test.path, test.line); got != identityPolicyCurrentCopy {
				t.Fatalf("classification = %q, want %q", got, identityPolicyCurrentCopy)
			}
		})
	}
}

func TestCompatibilityPoliciesRejectMixedCurrentCopy(t *testing.T) {
	tests := []struct {
		name string
		path string
		line string
	}{
		{
			name: "legacy product copy beside a state path",
			path: "docs/guides/sessions-and-transcripts.md",
			line: "Eino-Agent writes `.eino-agent/transcripts`.",
		},
		{
			name: "legacy environment beside a state path",
			path: "docs/guides/sessions-and-transcripts.md",
			line: "`EINO_AGENT_UNKNOWN` selects `.eino-agent/transcripts`.",
		},
		{
			name: "legacy product copy beside a protocol identifier",
			path: "docs/architecture/platform/acp-adapter.md",
			line: "Eino-Agent advertises `eino-agent.goal`.",
		},
		{
			name: "legacy product copy beside an artifact schema identifier",
			path: "scripts/evaluation/report.go",
			line: `const reportKind = "eino-agent/evaluation-report" // Eino-Agent report`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyLegacyIdentityLine(test.path, test.line); got != identityPolicyCurrentCopy {
				t.Fatalf("classification = %q, want %q", got, identityPolicyCurrentCopy)
			}
		})
	}
}
