package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func TestInitialEvidenceUsesBlockedAndNotApplicableStatuses(t *testing.T) {
	plan := Plan{
		SchemaVersion:   1,
		Changed:         []ChangedPath{{Path: "engine/query.go", Status: "M", Owner: "engine-runtime", Kind: PathProduction}},
		RequiredTargets: []string{"docs-check-ci", "test-contract", "test-pty"},
		NotApplicable:   []string{"test-pty"},
	}

	got := initialEvidence(plan)
	want := Evidence{
		SchemaVersion: 1,
		Plan:          plan,
		State:         "changed",
		Gates: []GateEvidence{
			{Target: "docs-check-ci", Level: "merge", Status: GateBlocked},
			{Target: "test-contract", Level: "merge", Status: GateBlocked},
			{Target: "test-pty", Level: "merge", Status: GateNotApplicable},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initialEvidence() = %#v, want %#v", got, want)
	}

	empty := initialEvidence(Plan{SchemaVersion: 1})
	if empty.State != "planned" || len(empty.Gates) != 0 {
		t.Fatalf("empty evidence = %#v", empty)
	}
}

func TestDecodeEvidenceRejectsUnknownStatusAndLevel(t *testing.T) {
	valid := `{"schema_version":1,"plan":{"schema_version":1},"state":"changed","gates":[{"target":"test","level":"merge","status":"blocked","duration_ms":0}]}`
	if _, err := decodeEvidence(strings.NewReader(valid)); err != nil {
		t.Fatalf("decode valid evidence: %v", err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{"status", strings.Replace(valid, `"blocked"`, `"unknown"`, 1)},
		{"level", strings.Replace(valid, `"merge"`, `"unknown"`, 1)},
		{"state", strings.Replace(valid, `"changed"`, `"unknown"`, 1)},
		{"second value", valid + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeEvidence(strings.NewReader(test.input)); err == nil {
				t.Fatal("decodeEvidence accepted invalid input")
			}
		})
	}
}

func TestRenderPlanMarkdownEscapesCellsAndShowsStableEvidence(t *testing.T) {
	plan := Plan{
		SchemaVersion:    1,
		Base:             strings.Repeat("a", 40),
		Head:             strings.Repeat("b", 40),
		DiffDigest:       strings.Repeat("c", 64),
		Slice:            &SliceRef{ID: "P1.0", State: "ready", Contract: "plans/p1.md", Outcome: "do not render this prose"},
		Changed:          []ChangedPath{{Path: "docs/a|b.md", Status: "M\nnext", Owner: "documentation", Kind: PathClass}},
		RequiredTargets:  []string{"docs-check-ci"},
		OutsideUntracked: 4,
	}
	var output bytes.Buffer
	if err := renderPlanMarkdown(plan, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"# Change Plan",
		"- Base: `" + plan.Base + "`",
		"- Head: `" + plan.Head + "`",
		"- Diff digest: `" + plan.DiffDigest + "`",
		"- State: `changed`",
		"- Outside-scope untracked count: `4`",
		"P1.0",
		"docs/a\\|b.md",
		"M<br>next",
		"docs-check-ci",
		"blocked",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered Markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, plan.Slice.Outcome) {
		t.Fatal("rendered Markdown leaked queue outcome prose")
	}
}

func TestRenderPrivacyMarkersNeverEscapeDiffSource(t *testing.T) {
	const (
		promptMarker        = "PROMPT_SECRET_MARKER"
		sourceMarker        = "SOURCE_SECRET_MARKER"
		transcriptMarker    = "TRANSCRIPT_SECRET_MARKER"
		argvMarker          = "ARGV_SECRET_MARKER"
		commandOutputMarker = "COMMAND_OUTPUT_SECRET_MARKER"
	)
	private := strings.Join([]string{
		promptMarker,
		sourceMarker,
		transcriptMarker,
		argvMarker,
		commandOutputMarker,
	}, "\n")
	source := &fakeGitSource{
		resolved:       map[string]string{"HEAD": strings.Repeat("b", 40)},
		mergeBase:      strings.Repeat("a", 40),
		nameStatus:     []byte("M\x00docs/contributing/verification.md\x00"),
		binaryDiff:     []byte(private),
		untrackedCount: 2,
	}
	snapshot, err := resolveSnapshot(context.Background(), "/repo", "origin/master", "", source)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildPlan(testPlanningPolicy(), snapshot, "linux", &SliceRef{
		ID: "P1.0", State: "ready", Contract: "plans/p1.md", Outcome: "accepted outcome",
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := initialEvidence(plan)

	var rendered bytes.Buffer
	for _, value := range []any{plan, evidence} {
		if err := renderJSON(value, &rendered); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderPlanMarkdown(plan, &rendered); err != nil {
		t.Fatal(err)
	}
	if err := renderEvidenceMarkdown(evidence, &rendered); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	for _, marker := range []string{promptMarker, sourceMarker, transcriptMarker, argvMarker, commandOutputMarker} {
		if strings.Contains(text, marker) {
			t.Fatalf("rendered output leaked %s", marker)
		}
	}
	digest := sha256.Sum256([]byte(private))
	for _, want := range []string{
		"docs/contributing/verification.md",
		"docs-check-ci",
		"P1.0",
		"2",
		hex.EncodeToString(digest[:]),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered output missing stable field %q", want)
		}
	}
}
