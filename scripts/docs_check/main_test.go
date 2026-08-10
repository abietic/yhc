package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRepositoryAcceptsReachableLinksAndAnchors(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[guide.md](guide.md#details)\n\n[Local](#local)\n\n## Local\n")
	writeFixture(t, root, "docs/guide.md", "# Guide\n\n**Status:** current\n\n> **Ownership:** guide\n\n## Details\n\n[Code](../code.go#L2)\n")
	writeFixture(t, root, "code.go", "package fixture\nfunc owner() {}\n")

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
	if result.markdownFiles != 2 || result.reachable != 2 || result.links != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPublicGovernanceFilesAndCanonicalLinks(t *testing.T) {
	root := filepath.Clean("../..")
	for _, path := range []string{
		"LICENSE",
		"NOTICE",
		"SECURITY.md",
		"CONTRIBUTING.md",
		"CODE_OF_CONDUCT.md",
		".github/dependabot.yml",
		".github/ISSUE_TEMPLATE/bug.yml",
		".github/ISSUE_TEMPLATE/feature.yml",
		".github/pull_request_template.md",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatalf("public governance file %s: %v", path, err)
		}
	}

	checks := map[string][]string{
		"NOTICE":                      {"YHC Authors", "github.com/coder/acp-go-sdk", "Contributor Covenant 2.1"},
		"SECURITY.md":                 {"https://github.com/abietic/yhc/security/advisories/new"},
		"CONTRIBUTING.md":             {"https://github.com/abietic/yhc", "provenance", "compatible"},
		"README.md":                   {"Apache-2.0", "CONTRIBUTING.md", "SECURITY.md", "docs/publication/README.md"},
		"docs/contributing/README.md": {"../../SECURITY.md", "../../CONTRIBUTING.md", "../publication/README.md"},
	}
	for path, want := range checks {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, text := range want {
			if !strings.Contains(string(data), text) {
				t.Errorf("%s does not contain %q", path, text)
			}
		}
		for _, forbidden := range []string{"github.com/abietic/eino-agent", "github.com/abietic/yhc-private-history", "Private — all rights reserved."} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s retains private-release text %q", path, forbidden)
			}
		}
	}
	license, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(license)
	if got := hex.EncodeToString(sum[:]); got != "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30" {
		t.Fatalf("LICENSE digest = %s, want the unmodified Apache-2.0 text", got)
	}

	// These hashes freeze the subset copied from the go.sum-authenticated
	// github.com/coder/acp-go-sdk v0.13.5 module. Any added or changed file must
	// carry a prominent local notice, including files introduced in the future.
	upstreamACP := map[string]string{
		"LICENSE":          "3cf3fec4549ad049b3defd633001ce9e89923cdaee3d45d5ff4686750706e3cd",
		"agent.go":         "d41b1a76f5a532d83681e3b37565757c0cb2c15309008e41e49a22df690417ed",
		"agent_gen.go":     "e230317c8029043b3c1686680cc0c068b6076bad24d50fd840d60017980e1a73",
		"client.go":        "6a6896f84c29e838ffdeaa8464d9c7c600d25ffafcf600cf4c4023058c93d4a9",
		"client_gen.go":    "daa7ff1f9abe726662cee0f5d95c9f7eadda77d955986a625177efbad54545a1",
		"connection.go":    "f47f0c609db5a9a5e65362ed53e24b4be4c5eda8a7ac2ab95f5a1a2209570796",
		"constants_gen.go": "f64e0e432b6db352d4c61533fc4c3739e6699a8599213039d503da63cde04d63",
		"doc.go":           "23e8149b3342669e9dbfbd79658d4bc1b2ec09c03ce8321a9d326623b87119aa",
		"errors.go":        "f4f45247e75348d7b4c82b24743f30a35f14e4923b074d4d6bc37220d0b9c7de",
		"extensions.go":    "f0a766a65f0fbbf5558ae00b8adda515677e44407f10f33459de4489b1bcb276",
		"go.mod":           "dfbacd788612fc9479a4257fa4ef7af8a5af88a64ebbc86d6f66145b22ddbbcf",
		"helpers.go":       "d5b7d1110aaeffcb4f9b48bc57e746c934b848c7522091bcba83f3b938fba199",
		"helpers_gen.go":   "f8da4115da946424e6ddaf1b587cabf7a14fbb316f5853bf0766f08d115c6996",
		"types_gen.go":     "13c09386250c7b6d3bd36efdca873dde74eafc6bb01a59f02d3e7f7fdacd29ff",
	}
	acpRoot := filepath.Join(root, "third_party/acp-go-sdk")
	if walkErr := filepath.WalkDir(acpRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return &fs.PathError{Op: "audit", Path: filePath, Err: fs.ErrInvalid}
		}
		relative, relativeErr := filepath.Rel(acpRoot, filePath)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(data)
		got := hex.EncodeToString(digest[:])
		upstream, existed := upstreamACP[relative]
		if existed && got == upstream {
			return nil
		}
		if relative == "LICENSE" {
			t.Errorf("vendored ACP LICENSE differs from the authenticated upstream copy")
			return nil
		}
		want := "Added by YHC"
		if existed {
			want = "Modified by YHC"
		}
		prefixLength := min(len(data), 512)
		if !strings.Contains(string(data[:prefixLength]), want) {
			t.Errorf("third_party/acp-go-sdk/%s does not begin with local notice %q", relative, want)
		}
		return nil
	}); walkErr != nil {
		t.Fatalf("audit vendored ACP files: %v", walkErr)
	}
	notice, err := os.ReadFile(filepath.Join(root, "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ContentBlock", "inbound-request"} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("NOTICE does not describe the vendored ACP delta %q", want)
		}
	}
}

func TestCheckRepositoryRejectsBrokenBlankAndUnreachableLinks(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[Missing](missing.md)\n[Blank](../code.go#L2)\n")
	writeFixture(t, root, "docs/orphan.md", "# Orphan\n\n**Status:** current\n\n> **Ownership:** orphan\n")
	writeFixture(t, root, "code.go", "package fixture\n\nfunc owner() {}\n")

	result := checkRepository(root)
	joined := errorsText(result.errs)
	for _, want := range []string{
		"local link target does not exist: missing.md",
		"line anchor #L2 lands on a blank line",
		"docs/orphan.md: not reachable from docs/README.md",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestCheckRepositoryRejectsInvalidMetadataAndStaleMarkdownLabel(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[old-name.md](bad_name.md)\n[old/guide.md](actual/guide.md)\n")
	writeFixture(t, root, "docs/bad_name.md", "# Bad Name\n\n**Status:** active-plan / ready\n")
	writeFixture(t, root, "docs/actual/guide.md", "# Guide\n\n**Status:** current\n\n> **Ownership:** guide\n")

	result := checkRepository(root)
	joined := errorsText(result.errs)
	for _, want := range []string{
		`markdown link label "old-name.md" does not match target path`,
		`markdown link label "old/guide.md" does not match target path`,
		`invalid Status "active-plan / ready"`,
		"missing Ownership metadata in first 30 lines",
		"filename is not lower-kebab",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestCheckRepositoryValidatesProjectDirectionAndLocalAnchors(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n")
	writeFixture(t, root, "PROJECT_DIRECTION.md", "# Direction\n\n[Missing](docs/missing.md)\n[Local](#missing-heading)\n")

	result := checkRepository(root)
	joined := errorsText(result.errs)
	for _, want := range []string{
		"PROJECT_DIRECTION.md:3: local link target does not exist: docs/missing.md",
		"PROJECT_DIRECTION.md:4: markdown anchor #missing-heading does not exist",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestCheckRepositoryAcceptsCurrentAgentRuntimeOwnership(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
	writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
	writeFixture(t, root, "AGENTS.md", validAgentInstructions())

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
}

func TestCheckRepositoryAllowsMissingAgentInstructions(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n")

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
}

func TestCheckRepositoryRejectsPlanIndexLifecycleMismatch(t *testing.T) {
	root := t.TempDir()
	writePlanLifecycleFixture(t, root, "Executed and retained as closeout evidence", "active-plan", "", "")

	joined := errorsText(checkRepository(root).errs)
	if !strings.Contains(joined, "executed plan index entry requires Status historical") {
		t.Fatalf("errors %q do not contain lifecycle mismatch", joined)
	}
}

func TestCheckRepositoryRejectsUnknownPlanIndexState(t *testing.T) {
	root := t.TempDir()
	writePlanLifecycleFixture(t, root, "Deferred pending discussion", "active-plan", "", "")

	joined := errorsText(checkRepository(root).errs)
	if !strings.Contains(joined, `unknown Implementation Plan Index state "Deferred pending discussion"`) {
		t.Fatalf("errors %q do not contain unknown state", joined)
	}
}

func TestCheckRepositoryRejectsMissingOrDuplicatePlanIndexEntry(t *testing.T) {
	for _, tt := range []struct {
		name       string
		indexState string
		index      string
		want       string
	}{
		{
			name:  "missing",
			index: "| Plan | Owning accepted contract | State |\n|---|---|---|\n",
			want:  "has no Implementation Plan Index entry",
		},
		{
			name:       "duplicate",
			indexState: "Active; accepted work",
			index:      "| [`plan.md`](plan.md) | contract | Active; accepted work |\n",
			want:       "has multiple Implementation Plan Index entries",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePlanLifecycleFixture(t, root, tt.indexState, "active-plan", "", tt.index)

			joined := errorsText(checkRepository(root).errs)
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("errors %q do not contain %q", joined, tt.want)
			}
		})
	}
}

func TestCheckRepositoryRejectsUnknownRequiredProjectSkill(t *testing.T) {
	for _, tt := range []struct {
		name        string
		status      string
		instruction string
		want        string
	}{
		{
			name:        "missing local skill",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n\n",
			want:        "required project skill $missing-skill does not exist",
		},
		{
			name:        "legacy superpowers skill",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$superpowers:legacy`.\n\n",
			want:        "legacy required project skill is not allowed",
		},
		{
			name:        "bare legacy superpowers skill",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:legacy`.\n\n",
			want:        "legacy required project skill is not allowed",
		},
		{
			name:        "historical live instruction",
			status:      "historical",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill`.\n\n",
			want:        "historical plan must not contain a live REQUIRED SUB-SKILL instruction",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			state := "Active; accepted work"
			if tt.status == "historical" {
				state = "Executed and retained as closeout evidence"
			}
			writePlanLifecycleFixture(t, root, state, tt.status, tt.instruction, "")

			joined := errorsText(checkRepository(root).errs)
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("errors %q do not contain %q", joined, tt.want)
			}
		})
	}
}

func TestCheckRepositoryAcceptsPlanLifecycleAndSkillContract(t *testing.T) {
	for _, tt := range []struct {
		name        string
		indexState  string
		status      string
		instruction string
		skill       bool
	}{
		{
			name:       "historical without live instruction",
			indexState: "Executed and retained as closeout evidence",
			status:     "historical",
		},
		{
			name:        "active local skill",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill`.\n\n",
			skill:       true,
		},
		{
			name:        "fenced example",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "```markdown\n> **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n```\n\n",
		},
		{
			name:        "mixed marker fenced example",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "~~~~markdown\n```\n> **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n~~~~\n\n",
		},
		{
			name:        "ordinary prose",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "Ordinary prose may mention REQUIRED SUB-SKILL: `$missing-skill` without creating a live instruction.\n\n",
		},
		{
			name:        "nested blockquote",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "> > **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n\n",
		},
		{
			name:        "only first top-level block",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill`.\n\n> **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n\n",
			skill:       true,
		},
		{
			name:        "accepted-design lifecycle",
			indexState:  "Accepted-design; queue admission pending",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill`.\n\n",
			skill:       true,
		},
		{
			name:       "draft lifecycle",
			indexState: "Draft; accepted design is not yet scheduled",
			status:     "active-plan",
		},
		{
			name:        "canonical token with ordinary code and prose",
			indexState:  "Active; accepted work",
			status:      "active-plan",
			instruction: "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill` to inspect `docs-check`, then use the documented flow.\n\n",
			skill:       true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePlanLifecycleFixture(t, root, tt.indexState, tt.status, tt.instruction, "")
			if tt.skill {
				writeFixture(t, root, ".agents/skills/local-skill/SKILL.md", "# Local Skill\n")
			}

			result := checkRepository(root)
			if len(result.errs) != 0 {
				t.Fatalf("checkRepository() errors = %v", result.errs)
			}
		})
	}
}

func TestCheckRepositoryRequiresAcceptedPlanRequiredSkillBlock(t *testing.T) {
	for _, state := range []string{"Accepted; ready for execution", "Accepted-design; queue admission pending"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			writePlanLifecycleFixture(t, root, state, "active-plan", "", "")

			joined := errorsText(checkRepository(root).errs)
			if !strings.Contains(joined, "accepted plan requires REQUIRED SUB-SKILL in its first top-level blockquote") {
				t.Fatalf("errors %q do not contain accepted-plan skill requirement", joined)
			}
		})
	}
}

func TestCheckRepositoryRejectsRequiredSkillLookalikes(t *testing.T) {
	for _, tt := range []struct {
		name        string
		instruction string
	}{
		{"bare", "> **For agentic workers:** REQUIRED SUB-SKILL: Use local-skill.\n\n"},
		{"bare code span", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `local-skill`.\n\n"},
		{"double dollar", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$$local-skill`.\n\n"},
		{"embedded token", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `x$local-skill`.\n\n"},
		{"escaped token", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `\\$local-skill`.\n\n"},
		{"uppercase", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$Local-skill`.\n\n"},
		{"underscore", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local_skill`.\n\n"},
		{"slash", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$local-skill/extra`.\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePlanLifecycleFixture(t, root, "Active; accepted work", "active-plan", tt.instruction, "")
			writeFixture(t, root, ".agents/skills/local-skill/SKILL.md", "# Local Skill\n")

			joined := errorsText(checkRepository(root).errs)
			if !strings.Contains(joined, "invalid required project skill token") {
				t.Fatalf("errors %q do not contain token grammar rejection", joined)
			}
		})
	}
}

func TestCheckRepositoryRejectsBareKnownSkillAlongsideCanonicalToken(t *testing.T) {
	root := t.TempDir()
	writePlanLifecycleFixture(t, root, "Active; accepted work", "active-plan", "> **For agentic workers:** REQUIRED SUB-SKILL: Use `$write-docs`; then iteration-workflow closes evidence.\n\n", "")
	writeFixture(t, root, ".agents/skills/write-docs/SKILL.md", "# Write Docs\n")
	writeFixture(t, root, ".agents/skills/iteration-workflow/SKILL.md", "# Iteration Workflow\n")

	joined := errorsText(checkRepository(root).errs)
	if !strings.Contains(joined, "invalid required project skill token") {
		t.Fatalf("errors %q do not contain bare known skill rejection", joined)
	}
}

func TestCheckRepositoryIgnoresRequiredSkillBlockAfterFirstTopLevelBlockquote(t *testing.T) {
	root := t.TempDir()
	writePlanLifecycleFixture(t, root, "Active; accepted work", "active-plan", "> **Note:** this first top-level blockquote is not an instruction.\n\n> **For agentic workers:** REQUIRED SUB-SKILL: Use `$missing-skill`.\n\n", "")

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
}

func TestCheckRepositoryAcceptsCanonicalClosedGapMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
	}{
		{"single", "**Closed gaps:** G22\n"},
		{"multiple", "**Closed gaps:** G6, G7\n"},
		{"unbounded numeric IDs", "**Closed gaps:** G9223372036854775808, G18446744073709551616\n"},
		{"optional", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeGapTraceabilityFixture(t, root)
			writeGapHistoryDocument(t, root, "runtime/closeout.md", tt.metadata)

			result := checkRepository(root)
			if len(result.errs) != 0 {
				t.Fatalf("checkRepository() errors = %v", result.errs)
			}
		})
	}
}

func TestCheckRepositoryRejectsMalformedClosedGapMetadata(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"separator", "**Closed gaps:** G6,G7\n", "must use comma-space separators"},
		{"zero", "**Closed gaps:** G0\n", `invalid closed Gap ID "G0"`},
		{"leading zero", "**Closed gaps:** G06\n", `invalid closed Gap ID "G06"`},
		{"descending", "**Closed gaps:** G7, G6\n", "must be in strictly increasing numeric order"},
		{"local duplicate", "**Closed gaps:** G6, G6\n", "duplicates closed Gap G6"},
		{"sub-program", "**Closed gaps:** G11.F2\n", `invalid closed Gap ID "G11.F2"`},
		{"empty field", "**Closed gaps:**\n", "invalid Closed gaps metadata field"},
		{"after lifecycle prefix", strings.Repeat("line\n", 30) + "**Closed gaps:** G22\n", "Closed gaps metadata must appear in first 30 lines"},
		{"multiple fields", "**Closed gaps:** G6\n**Closed gaps:** G7\n", "multiple Closed gaps metadata fields"},
		{"second field after lifecycle prefix", "**Closed gaps:** G6\n" + strings.Repeat("line\n", 30) + "**Closed gaps:** G7\n", "multiple Closed gaps metadata fields"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeGapTraceabilityFixture(t, root)
			writeGapHistoryDocument(t, root, "runtime/closeout.md", tt.body)

			if got := errorsText(checkRepository(root).errs); !strings.Contains(got, tt.want) {
				t.Fatalf("errors %q do not contain %q", got, tt.want)
			}
		})
	}
}

func TestCheckRepositoryReportsAllInvalidClosedGapDocuments(t *testing.T) {
	root := t.TempDir()
	writeGapTraceabilityFixture(t, root)
	writeGapHistoryDocument(t, root, "runtime/first.md", "**Closed gaps:**\n")
	writeGapHistoryDocument(t, root, "runtime/second.md", "**Closed gaps:** G0\n")

	joined := errorsText(checkRepository(root).errs)
	for _, want := range []string{
		"docs/migration/history/runtime/first.md",
		"docs/migration/history/runtime/second.md",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestCheckRepositoryRejectsDuplicateClosedGapOwners(t *testing.T) {
	root := t.TempDir()
	writeGapTraceabilityFixture(t, root)
	writeGapHistoryDocument(t, root, "runtime/first.md", "**Closed gaps:** G22\n")
	writeGapHistoryDocument(t, root, "runtime/second.md", "**Closed gaps:** G22\n")

	joined := errorsText(checkRepository(root).errs)
	for _, want := range []string{
		"closed Gap G22 has multiple historical owners",
		"docs/migration/history/runtime/first.md",
		"docs/migration/history/runtime/second.md",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestCheckRepositoryRejectsOpenAndClosedGapOverlap(t *testing.T) {
	root := t.TempDir()
	writeGapTraceabilityFixture(t, root)
	writeFixture(t, root, "docs/migration/REMAINING.md", "# Remaining\n\n**Status:** gap-inventory\n\n> **Ownership:** unresolved gaps\n\n| Gap | State |\n|---|---|\n| G22 | unresolved |\n")
	writeGapHistoryDocument(t, root, "runtime/closeout.md", "**Closed gaps:** G22\n")

	want := "closed Gap G22 is still present in docs/migration/REMAINING.md"
	if got := errorsText(checkRepository(root).errs); !strings.Contains(got, want) {
		t.Fatalf("errors %q do not contain %q", got, want)
	}
}

func TestCheckRepositoryDoesNotParseHistoryPolicyExamplesAsMetadata(t *testing.T) {
	root := t.TempDir()
	writeGapTraceabilityFixture(t, root)
	writeFixture(t, root, "docs/migration/history/README.md", "# History\n\n**Status:** historical\n\n> **Ownership:** history index\n\nA closeout uses `**Closed gaps:** G22`; multiple IDs use `**Closed gaps:** G6, G7`.\n")

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
}

func TestCheckRepositoryIgnoresFencedGapExamples(t *testing.T) {
	root := t.TempDir()
	writeGapTraceabilityFixture(t, root)
	writeFixture(t, root, "docs/migration/REMAINING.md", "# Remaining\n\n**Status:** gap-inventory\n\n> **Ownership:** unresolved gaps\n\n```markdown\n| G22 | example only |\n```\n")
	writeGapHistoryDocument(t, root, "runtime/example.md", "```markdown\n**Closed gaps:** G22\n```\n")
	writeGapHistoryDocument(t, root, "runtime/closeout.md", "**Closed gaps:** G22\n")

	result := checkRepository(root)
	if len(result.errs) != 0 {
		t.Fatalf("checkRepository() errors = %v", result.errs)
	}
}

func writeGapTraceabilityFixture(t *testing.T, root string) {
	t.Helper()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[Migration](migration/README.md)\n")
	writeFixture(t, root, "docs/migration/README.md", "# Migration\n\n**Status:** current\n\n> **Ownership:** migration index\n\n[Remaining](REMAINING.md)\n[History](history/README.md)\n")
	writeFixture(t, root, "docs/migration/REMAINING.md", "# Remaining\n\n**Status:** gap-inventory\n\n> **Ownership:** unresolved gaps\n\n| Gap | State |\n|---|---|\n")
	writeFixture(t, root, "docs/migration/history/README.md", "# History\n\n**Status:** historical\n\n> **Ownership:** history index\n")
}

func writeGapHistoryDocument(t *testing.T, root, relative, metadata string) {
	t.Helper()
	path := "docs/migration/history/" + relative
	writeFixture(t, root, path, "# Closeout\n\n**Status:** historical\n\n> **Ownership:** closeout\n"+metadata)
	historyIndex := filepath.Join(root, "docs", "migration", "history", "README.md")
	data, err := os.ReadFile(historyIndex)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "docs/migration/history/README.md", string(data)+"\n[Closeout]("+relative+")\n")
}

func TestCheckRepositoryRejectsMissingAgentRuntimeOwnership(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   string
	}{
		{"query engine", "QueryEngine", `AGENTS.md: missing required runtime owner "QueryEngine"`},
		{"project graph kernel", "projectGraphQueryKernel", `AGENTS.md: missing required runtime owner "projectGraphQueryKernel"`},
		{"architecture link", "docs/architecture/runtime/query-engine.md", `AGENTS.md: missing required runtime architecture link "docs/architecture/runtime/query-engine.md"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
			writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
			writeFixture(t, root, "AGENTS.md", strings.Replace(validAgentInstructions(), tt.remove, "", 1))

			result := checkRepository(root)
			if got := errorsText(result.errs); !strings.Contains(got, tt.want) {
				t.Fatalf("errors %q do not contain %q", got, tt.want)
			}
		})
	}
}

func TestCheckRepositoryRejectsPlainTextAgentRuntimeArchitecturePath(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
	writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
	agentInstructions := strings.Replace(
		validAgentInstructions(),
		"[Query Engine architecture](docs/architecture/runtime/query-engine.md)",
		"docs/architecture/runtime/query-engine.md",
		1,
	)
	writeFixture(t, root, "AGENTS.md", agentInstructions)

	result := checkRepository(root)
	want := `AGENTS.md: missing required runtime architecture link "docs/architecture/runtime/query-engine.md"`
	if got := errorsText(result.errs); !strings.Contains(got, want) {
		t.Fatalf("errors %q do not contain %q", got, want)
	}
}

func TestCheckRepositoryRejectsRetiredAgentRuntimeOwnershipClaims(t *testing.T) {
	tests := []struct {
		name  string
		claim string
		want  string
	}{
		{"imperative loop", "imperative agent loop, not graph-based", "imperative agent loop, not graph-based"},
		{"query go authority", "`engine/query.go` remains the production loop\nauthority", "`engine/query.go` remains the production loop authority"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
			writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
			writeFixture(t, root, "AGENTS.md", validAgentInstructions()+tt.claim)

			result := checkRepository(root)
			want := `AGENTS.md: contains retired runtime ownership claim "` + tt.want + `"`
			if got := errorsText(result.errs); !strings.Contains(got, want) {
				t.Fatalf("errors %q do not contain %q", got, want)
			}
		})
	}
}

func TestCheckRepositoryRequiresPublicIterationCommands(t *testing.T) {
	for _, command := range []string{
		"make change-plan",
		"make verify-focused",
		"make verify-merge",
		"make change-evidence",
		"make change-evidence-ready",
	} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
			writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
			writeFixture(t, root, "AGENTS.md", strings.Replace(validAgentInstructions(), command, "", 1))

			result := checkRepository(root)
			want := `AGENTS.md: missing public iteration command "` + command + `"`
			if got := errorsText(result.errs); !strings.Contains(got, want) {
				t.Fatalf("errors %q do not contain %q", got, want)
			}
		})
	}
}

func TestCheckRepositoryRejectsIterationImplementationDetailsInAgentInstructions(t *testing.T) {
	for _, detail := range []string{"retention_keep", "test-contract", "test-race", "test-pty", "test-fuzz-smoke", "test-e2e"} {
		t.Run(detail, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[query-engine.md](architecture/runtime/query-engine.md)\n")
			writeFixture(t, root, "docs/architecture/runtime/query-engine.md", "# Query Engine\n\n**Status:** current\n\n> **Ownership:** runtime\n")
			writeFixture(t, root, "AGENTS.md", validAgentInstructions()+detail+"\n")

			result := checkRepository(root)
			want := `AGENTS.md: contains iteration implementation detail "` + detail + `"`
			if got := errorsText(result.errs); !strings.Contains(got, want) {
				t.Fatalf("errors %q do not contain %q", got, want)
			}
		})
	}
}

func TestMarkdownHeadingsMatchGitHubStyleAndDuplicates(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "doc.md", "# API: Query / Tool\n\n## 重复 标题\n## 重复 标题\n")
	path := filepath.Join(root, "doc.md")

	headings, err := markdownHeadings(root, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"api-query-tool", "重复-标题", "重复-标题-1"} {
		if _, ok := headings[want]; !ok {
			t.Fatalf("heading %q missing from %#v", want, headings)
		}
	}
}

func TestReadLinesConfinedToRoot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docs/file.md", "# Title\n\nbody\n")
	file := filepath.Join(root, "docs", "file.md")

	lines, err := readLines(root, file)
	if err != nil {
		t.Fatalf("readLines(%q) under root: %v", file, err)
	}
	if len(lines) != 3 || lines[0] != "# Title" || lines[2] != "body" {
		t.Fatalf("unexpected lines: %v", lines)
	}

	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside.md")
	if err := os.WriteFile(outside, []byte("leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if _, err := readLines(root, outside); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("expected escape error for absolute outside path, got %v", err)
	}

	traversal := filepath.Join(root, "..", filepath.Base(outside))
	if _, err := readLines(root, traversal); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("expected escape error for relative traversal, got %v", err)
	}

	linkPath := filepath.Join(root, "link.md")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if _, err := readLines(root, linkPath); err == nil {
		t.Fatalf("expected escape error for symlink outside root, got %v", err)
	}
}

func TestCheckRepositoryRejectsExternalSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := writeOutsideMarkdown(t, root)
	linkPath := filepath.Join(root, "outside-link.md")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[Outside](../outside-link.md)\n")

	result := checkRepository(root)
	joined := errorsText(result.errs)
	if !strings.Contains(joined, "local link target is not confined to repository: ../outside-link.md") {
		t.Fatalf("expected external target rejection, got %q", joined)
	}
}

func TestCheckRepositoryRejectsExternalMarkdownSourceSymlink(t *testing.T) {
	root := t.TempDir()
	outside := writeOutsideMarkdown(t, root)
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n")
	linkPath := filepath.Join(root, "docs", "external.md")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	result := checkRepository(root)
	joined := errorsText(result.errs)
	if !strings.Contains(joined, "docs/external.md: read path confined to repository root") {
		t.Fatalf("expected external source rejection, got %q", joined)
	}
}

func writeOutsideMarkdown(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside.md")
	if err := os.WriteFile(path, []byte("# Outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func validAgentInstructions() string {
	return strings.Join([]string{
		"# Agent Instructions",
		"",
		"QueryEngine owns conversation and session composition.",
		"projectGraphQueryKernel owns the production traversal shared by direct Query calls.",
		"See [Query Engine architecture](docs/architecture/runtime/query-engine.md).",
		"Run make change-plan, make verify-focused, and make verify-merge.",
		"Use make change-evidence for status and make change-evidence-ready for completion.",
		"",
	}, "\n")
}

func writePlanLifecycleFixture(t *testing.T, root, indexState, status, instruction, extraIndex string) {
	t.Helper()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[Plans](superpowers/plans/README.md)\n")
	indexRow := ""
	if indexState != "" {
		indexRow = "| [`plan.md`](plan.md) | contract | " + indexState + " |\n"
	}
	writeFixture(t, root, "docs/superpowers/plans/README.md", "# Implementation Plan Index\n\n**Status:** active-plan\n\n> **Ownership:** plan index\n\n| Plan | Owning accepted contract | State |\n|---|---|---|\n"+indexRow+extraIndex)
	writeFixture(t, root, "docs/superpowers/plans/plan.md", "# Plan\n\n"+instruction+"**Status:** "+status+"\n\n> **Ownership:** fixture plan\n")
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func errorsText(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "\n")
}
