package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateQueueAcceptsTopologicalOrder(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	if err := validateQueue(queue, root); err != nil {
		t.Fatalf("validateQueue: %v", err)
	}
}

func TestValidateQueueAcceptsNoActiveSlice(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.Slices = nil
	if err := validateQueue(queue, root); err != nil {
		t.Fatalf("validateQueue: %v", err)
	}
	fragment := renderQueue(queue)
	for _, want := range []string{"0 `Ready`, 0 `Queued`, 0 `Blocked`", "No accepted active slices", "There is no accepted incomplete slice"} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("renderQueue() missing %q:\n%s", want, fragment)
		}
	}
}

func TestValidateQueueRejectsCycle(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.Slices[0].DependsOn = []string{"P2.0"}
	queue.Slices[1].DependsOn = []string{"P1.0"}
	if err := validateQueue(queue, root); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("validateQueue error = %v, want cycle", err)
	}
}

func TestValidateQueueRejectsUnsatisfiedReadySlice(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.Slices[0].Promotion.State = "pending"
	if err := validateQueue(queue, root); err == nil || !strings.Contains(err.Error(), "unsatisfied promotion gate") {
		t.Fatalf("validateQueue error = %v, want promotion failure", err)
	}
}

func TestValidateQueueRejectsConfigurableReadyLimit(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.ReadyLimit = 2
	if err := validateQueue(queue, root); err == nil ||
		!strings.Contains(err.Error(), "ready_limit must be 1") {
		t.Fatalf("validateQueue error = %v, want fixed ready limit", err)
	}
}

func TestValidateQueueRejectsTwoReadySlices(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.Slices[1].State = "ready"
	queue.Slices[1].DependsOn = nil
	queue.Slices[1].BlockedBy = nil
	queue.Slices[1].Promotion.State = "satisfied"
	if err := validateQueue(queue, root); err == nil ||
		!strings.Contains(err.Error(), "ready slices 2 exceed limit 1") {
		t.Fatalf("validateQueue error = %v, want two-ready rejection", err)
	}
}

func TestValidateQueueRejectsGapMissingFromInventory(t *testing.T) {
	root := queueFixtureRoot(t)
	queue := validQueue()
	queue.Deferred[0].Gaps = []string{"G99"}
	if err := validateQueue(queue, root); err == nil ||
		!strings.Contains(err.Error(), "P3 references gap G99 missing from REMAINING.md") {
		t.Fatalf("validateQueue error = %v, want missing gap rejection", err)
	}
}

func TestGeneratedBlockRoundTrip(t *testing.T) {
	fragment := renderQueue(validQueue())
	plan := "before\n" + generatedBegin + "\nstale\n" + generatedEnd + "\nafter\n"
	updated, err := replaceGeneratedBlock(plan, fragment)
	if err != nil {
		t.Fatalf("replaceGeneratedBlock: %v", err)
	}
	got, err := extractGeneratedBlock(updated)
	if err != nil {
		t.Fatalf("extractGeneratedBlock: %v", err)
	}
	if got != fragment {
		t.Fatalf("generated block drift\ngot:\n%s\nwant:\n%s", got, fragment)
	}
}

func TestLoadQueueRejectsUnknownField(t *testing.T) {
	root := openTestRoot(t)
	if err := root.WriteFile("queue.yaml", []byte("version: 1\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQueue(root, "queue.yaml"); err == nil {
		t.Fatal("loadQueue accepted an unknown field")
	}
}

func TestLoadQueueRejectsSecondDocument(t *testing.T) {
	root := openTestRoot(t)
	data := "version: 1\nupdated: 2026-08-02\nready_limit: 1\nslices: []\ndeferred: []\n---\nversion: 1\n"
	if err := root.WriteFile("queue.yaml", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQueue(root, "queue.yaml"); err == nil ||
		!strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("loadQueue error = %v, want multiple-document rejection", err)
	}
}

func TestRunRendersPlanWithinRepositoryRoot(t *testing.T) {
	work := migrationQueueRunFixture(t)
	t.Chdir(work)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"render"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(render) = %d, stderr = %q", code, stderr.String())
	}
	plan, err := os.ReadFile(filepath.Join(work, "docs/migration/PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "No accepted active slices") {
		t.Fatalf("rendered plan missing queue fragment:\n%s", plan)
	}
}

func TestRunDescribeActiveSlice(t *testing.T) {
	work := migrationQueueRunFixture(t)
	writeRunFixtureQueue(t, work, describeFixtureQueue)
	t.Chdir(work)

	for _, test := range []struct {
		id    string
		state string
	}{
		{"P1.0", "ready"},
		{"P2.0", "queued"},
		{"P3.0", "blocked"},
	} {
		t.Run(test.id, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"--slice-id", test.id, "describe"}, &stdout, &stderr); code != 0 {
				t.Fatalf("run(describe) = %d, stderr = %q", code, stderr.String())
			}
			var got struct {
				SchemaVersion int    `json:"schema_version"`
				ID            string `json:"id"`
				State         string `json:"state"`
				Contract      string `json:"contract"`
				Outcome       string `json:"outcome"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.SchemaVersion != 1 || got.ID != test.id || got.State != test.state ||
				got.Contract == "" || got.Outcome == "" {
				t.Fatalf("describe = %#v", got)
			}
		})
	}
}

func TestRunDescribeRejectsDeferredAndMissingSlice(t *testing.T) {
	work := migrationQueueRunFixture(t)
	writeRunFixtureQueue(t, work, describeFixtureQueue)
	t.Chdir(work)

	for _, id := range []string{"P4.0", "P99.0"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--slice-id", id, "describe"}, &stdout, &stderr); code != 1 {
			t.Fatalf("run(describe %q) = %d, stderr = %q", id, code, stderr.String())
		}
	}
}

func TestRunDescribeRequiresSliceID(t *testing.T) {
	work := migrationQueueRunFixture(t)
	writeRunFixtureQueue(t, work, "invalid: queue\n")
	t.Chdir(work)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"describe"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(describe) = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunCheckAndPrintOutputUnchanged(t *testing.T) {
	work := migrationQueueRunFixture(t)
	queue := queueFile{Version: 1, Updated: "2026-08-02", ReadyLimit: 1}
	fragment := renderQueue(queue)
	if err := os.WriteFile(
		filepath.Join(work, "docs/migration/PLAN.md"),
		[]byte("before\n"+fragment+"after\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var checkOut, checkErr bytes.Buffer
	if code := run([]string{"check"}, &checkOut, &checkErr); code != 0 {
		t.Fatalf("run(check) = %d, stderr = %q", code, checkErr.String())
	}
	if got, want := checkOut.String(), "validated 0 active slices and 0 deferred decisions\n"; got != want {
		t.Fatalf("check stdout = %q, want %q", got, want)
	}

	var printOut, printErr bytes.Buffer
	if code := run([]string{"print"}, &printOut, &printErr); code != 0 {
		t.Fatalf("run(print) = %d, stderr = %q", code, printErr.String())
	}
	if got := printOut.String(); got != fragment {
		t.Fatalf("print stdout = %q, want %q", got, fragment)
	}
}

func TestRunRejectsPlanOutsideRepositoryRoot(t *testing.T) {
	parent := t.TempDir()
	work := migrationQueueRunFixtureAt(t, filepath.Join(parent, "work"))
	outside := filepath.Join(parent, "outside.md")
	const original = "outside must not change\n"
	if err := os.WriteFile(outside, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	for _, plan := range []string{"../outside.md", outside} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--plan", plan, "render"}, &stdout, &stderr); code != 1 {
			t.Fatalf("run(--plan %q render) = %d, stderr = %q", plan, code, stderr.String())
		}
		got, err := os.ReadFile(outside)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != original {
			t.Fatalf("outside plan changed through %q: %q", plan, got)
		}
	}
}

func TestRunRejectsQueueOutsideRepositoryRoot(t *testing.T) {
	parent := t.TempDir()
	work := migrationQueueRunFixtureAt(t, filepath.Join(parent, "work"))
	outsideDir := filepath.Join(parent, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "queue.yaml"), []byte(runFixtureQueue), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--queue", "../outside/queue.yaml", "print"}, &stdout, &stderr); code != 1 {
		t.Fatalf("run(outside queue) = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunRejectsSymlinkEscapingRepositoryRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require additional privileges on Windows")
	}
	parent := t.TempDir()
	work := migrationQueueRunFixtureAt(t, filepath.Join(parent, "work"))
	outside := filepath.Join(parent, "outside.md")
	const original = "outside must not change\n"
	if err := os.WriteFile(outside, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "docs/migration/outside.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--plan", "docs/migration/outside.md", "render"}, &stdout, &stderr); code != 1 {
		t.Fatalf("run(symlink plan) = %d, stderr = %q", code, stderr.String())
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("outside plan changed through symlink: %q", got)
	}
}

func validQueue() queueFile {
	return queueFile{
		Version:    1,
		Updated:    "2026-08-02",
		ReadyLimit: 1,
		Slices: []queueSlice{
			{
				ID:       "P1.0",
				State:    "ready",
				Priority: 10,
				Gaps:     []string{"G1"},
				Promotion: promotion{
					ID: "first-gate", State: "satisfied", Label: "first gate", Link: "plans/contract.md#gate",
				},
				Contract: "plans/contract.md#slice",
				Outcome:  "Freeze the first contract.",
			},
			{
				ID:        "P2.0",
				State:     "queued",
				Priority:  20,
				Gaps:      []string{"G2"},
				DependsOn: []string{"P1.0"},
				BlockedBy: []string{"second-gate"},
				Promotion: promotion{
					ID: "second-gate", State: "pending", Label: "second gate", Link: "verification/gate.md",
				},
				Contract: "plans/contract.md#second",
				Outcome:  "Freeze the second contract.",
			},
		},
		Deferred: []deferredDecision{
			{ID: "P3", Gaps: []string{"G3"}, Gate: "verification/gate.md", Reason: "Evidence is absent."},
		},
	}
}

func queueFixtureRoot(t *testing.T) *os.Root {
	t.Helper()
	rootPath := t.TempDir()
	for _, path := range []string{"plans/contract.md", "verification/gate.md"} {
		full := filepath.Join(rootPath, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("# Fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inventory := "# Remaining gaps\n\n| ID | Gap |\n|---|---|\n| G1 | one |\n| G2 | two |\n| G3 | three |\n"
	if err := os.WriteFile(filepath.Join(rootPath, "REMAINING.md"), []byte(inventory), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

const runFixtureQueue = "version: 1\nupdated: 2026-08-02\nready_limit: 1\nslices: []\ndeferred: []\n"

const describeFixtureQueue = `version: 1
updated: 2026-08-02
ready_limit: 1
slices:
  - id: P1.0
    state: ready
    priority: 10
    gaps: [G1]
    depends_on: []
    blocked_by: []
    promotion: {id: first-gate, state: satisfied, label: first gate, link: plans/contract.md}
    contract: plans/contract.md#p1
    outcome: First outcome.
  - id: P2.0
    state: queued
    priority: 20
    gaps: [G2]
    depends_on: [P1.0]
    blocked_by: []
    promotion: {id: second-gate, state: pending, label: second gate, link: verification/gate.md}
    contract: plans/contract.md#p2
    outcome: Second outcome.
  - id: P3.0
    state: blocked
    priority: 30
    gaps: [G3]
    depends_on: [P2.0]
    blocked_by: [third-gate]
    promotion: {id: third-gate, state: pending, label: third gate, link: verification/gate.md}
    contract: plans/contract.md#p3
    outcome: Third outcome.
deferred:
  - id: P4.0
    gaps: [G4]
    gate: verification/gate.md
    reason: Deferred outcome.
`

func openTestRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func migrationQueueRunFixture(t *testing.T) string {
	t.Helper()
	return migrationQueueRunFixtureAt(t, t.TempDir())
}

func migrationQueueRunFixtureAt(t *testing.T, root string) string {
	t.Helper()
	migrationDir := filepath.Join(root, "docs/migration")
	if err := os.MkdirAll(migrationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"queue.yaml":   runFixtureQueue,
		"REMAINING.md": "# Remaining gaps\n\n| ID | Gap |\n|---|---|\n| G1 | one |\n| G2 | two |\n| G3 | three |\n| G4 | four |\n",
		"PLAN.md":      "before\n" + generatedBegin + "\nstale\n" + generatedEnd + "\nafter\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(migrationDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"plans/contract.md", "verification/gate.md"} {
		path := filepath.Join(migrationDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# Fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeRunFixtureQueue(t *testing.T, root, queue string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "docs/migration/queue.yaml"), []byte(queue), 0o600); err != nil {
		t.Fatal(err)
	}
}
