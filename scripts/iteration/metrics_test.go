package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetricsNoDataAndInsufficientSamples(t *testing.T) {
	root := t.TempDir()
	report, diagnostics, err := collectMetrics(root, "build/iteration")
	if err != nil || diagnostics != "" || report.State != "no_data" || len(report.Outcomes) != 0 {
		t.Fatalf("empty metrics = %#v, diagnostics %q, err %v", report, diagnostics, err)
	}
	var rendered bytes.Buffer
	if err := renderJSON(report, &rendered); err != nil {
		t.Fatal(err)
	}
	data := rendered.Bytes()
	if !strings.Contains(string(data), `"outcomes": []`) {
		t.Fatalf("no_data JSON = %q", data)
	}
	var encoded MetricsReport
	if err := json.Unmarshal(data, &encoded); err != nil || encoded.Outcomes == nil {
		t.Fatalf("no_data JSON outcomes = %#v, err %v", encoded.Outcomes, err)
	}
	for _, fixture := range []struct {
		digest string
		gate   GateEvidence
	}{
		{"a", GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: 1}},
		{"b", GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GateFail, DurationMillis: 2}},
		{"c", GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GateBlocked, DurationMillis: 99}},
	} {
		plan := metricsPlan(fixture.digest)
		writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{fixture.gate}})
	}
	report, _, err = collectMetrics(root, "build/iteration")
	if err != nil || report.State != "insufficient_samples" || len(report.Outcomes) != 1 {
		t.Fatalf("insufficient metrics = %#v, err %v", report, err)
	}
	if got := report.Outcomes[0]; got.Pass != 1 || got.Fail != 1 || got.Blocked != 1 || got.Samples != 2 || got.P50Millis != nil || got.P95Millis != nil {
		t.Fatalf("outcome = %#v", got)
	}
}

func TestMetricsDispersedSamplesRemainInsufficient(t *testing.T) {
	root := t.TempDir()
	for index, fixture := range []struct {
		target string
		level  VerifyLevel
	}{{"test-contract", VerifyFocused}, {"test-contract", VerifyFocused}, {"test-contract", VerifyFocused}, {"fmt", VerifyMerge}, {"fmt", VerifyMerge}} {
		plan := metricsPlanFor(index)
		writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{
			Target: fixture.target, Level: string(fixture.level), Status: GatePass, DurationMillis: 1,
		}}})
	}
	report, _, err := collectMetrics(root, "build/iteration")
	if err != nil || report.State != "insufficient_samples" {
		t.Fatalf("dispersed metrics = %#v, err %v", report, err)
	}
}

func TestMetricsNearestRankMixedOutcomesAndStableOrder(t *testing.T) {
	root := t.TempDir()
	for index, duration := range []int64{1, 2, 3, 4, 100} {
		plan := metricsPlan(string(rune('a' + index)))
		writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{
			Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: duration,
		}, {
			Target: "fmt", Level: string(VerifyMerge), Status: GateNotApplicable,
		}}})
	}
	report, _, err := collectMetrics(root, "build/iteration")
	if err != nil || report.State != "ready" || len(report.Outcomes) != 2 {
		t.Fatalf("metrics = %#v, err %v", report, err)
	}
	if report.Outcomes[0].Level != "focused" || report.Outcomes[0].Target != "test-contract" || *report.Outcomes[0].P50Millis != 3 || *report.Outcomes[0].P95Millis != 100 {
		t.Fatalf("focused outcome = %#v", report.Outcomes[0])
	}
	if report.Outcomes[1].Level != "merge" || report.Outcomes[1].Target != "fmt" || report.Outcomes[1].Samples != 0 {
		t.Fatalf("merge outcome = %#v", report.Outcomes[1])
	}
}

func TestMetricsBoundsNewestArtifactsAt256(t *testing.T) {
	root := t.TempDir()
	for index := range 257 {
		plan := metricsPlanFor(index)
		writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: 1}}})
		when := time.Unix(int64(index), 0)
		if err := os.Chtimes(filepath.Join(root, "build", "iteration", plan.DiffDigest, "evidence.json"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	oldest := metricsPlanFor(0)
	writeRawEvidence(t, root, oldest.DiffDigest, `{"unknown":"METRICS_OLDEST_MARKER"}`)
	if err := os.Chtimes(filepath.Join(root, "build", "iteration", oldest.DiffDigest, "evidence.json"), time.Unix(0, 0), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	report, diagnostics, err := collectMetrics(root, "build/iteration")
	if err != nil || diagnostics != "" || len(report.Outcomes) != 1 || report.Outcomes[0].Pass != 256 {
		t.Fatalf("bounded metrics = %#v, diagnostics %q, err %v", report, diagnostics, err)
	}
}

func TestMetricsBoundsStablePathTieBreak(t *testing.T) {
	root := t.TempDir()
	for index := range 257 {
		plan := metricsPlanFor(index)
		writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: 1}}})
		if err := os.Chtimes(filepath.Join(root, "build", "iteration", plan.DiffDigest, "evidence.json"), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	excluded := metricsPlanFor(256)
	writeRawEvidence(t, root, excluded.DiffDigest, `{"unknown":"METRICS_TIE_MARKER"}`)
	if err := os.Chtimes(filepath.Join(root, "build", "iteration", excluded.DiffDigest, "evidence.json"), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	report, diagnostics, err := collectMetrics(root, "build/iteration")
	if err != nil || diagnostics != "" || len(report.Outcomes) != 1 || report.Outcomes[0].Pass != 256 {
		t.Fatalf("tied metrics = %#v, diagnostics %q, err %v", report, diagnostics, err)
	}
}

func TestMetricsRejectsInvalidArtifactsWithoutLeakingMarkers(t *testing.T) {
	const marker = "sk_live_METRICS_SECRET_MARKER"
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{"unknown evidence field", func(t *testing.T, root string) {
			plan := metricsPlan("b")
			writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed"})
			writeRawEvidence(t, root, plan.DiffDigest, `{"schema_version":1,"plan":`+mustRenderJSON(t, plan)+`,"state":"changed","gates":[],"unknown":"`+marker+`"}`)
		}},
		{"negative duration", func(t *testing.T, root string) {
			p := metricsPlan("c")
			writeMetricsArtifact(t, root, p, Evidence{SchemaVersion: 1, Plan: p, State: "changed", Gates: []GateEvidence{{Target: "test-contract", Level: "focused", Status: GatePass, DurationMillis: -1}}})
		}},
		{"symlink traversal", func(t *testing.T, root string) {
			p := metricsPlan("d")
			dir := filepath.Join(root, "build", "iteration", p.DiffDigest)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/dev/null", filepath.Join(dir, "evidence.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed digest", func(t *testing.T, root string) {
			writeRawMetricsArtifact(t, root, "not-a-digest", `{"marker":"`+marker+`"}`)
		}},
		{"non regular evidence file", func(t *testing.T, root string) {
			p := metricsPlan("e")
			dir := filepath.Join(root, "build", "iteration", p.DiffDigest)
			if err := os.MkdirAll(filepath.Join(dir, "evidence.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"plan directory mismatch", func(t *testing.T, root string) {
			p := metricsPlan("f")
			other := metricsPlan("0")
			writeMetricsArtifactAt(t, root, p.DiffDigest, other, Evidence{SchemaVersion: 1, Plan: other, State: "changed"})
		}},
		{"unexpected target", func(t *testing.T, root string) {
			p := metricsPlan("1")
			writeMetricsArtifact(t, root, p, Evidence{SchemaVersion: 1, Plan: p, State: "changed", Gates: []GateEvidence{{Target: "unexpected", Level: "focused", Status: GatePass, DurationMillis: 1}}})
		}},
		{"secret target", func(t *testing.T, root string) {
			p := metricsPlan("2")
			writeMetricsArtifact(t, root, p, Evidence{SchemaVersion: 1, Plan: p, State: "changed", Gates: []GateEvidence{{Target: marker, Level: "focused", Status: GatePass, DurationMillis: 1}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			valid := metricsPlan("a")
			writeMetricsArtifact(t, root, valid, Evidence{SchemaVersion: 1, Plan: valid, State: "changed", Gates: []GateEvidence{{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: 9}}})
			test.mutate(t, root)
			report, diagnostics, err := collectMetrics(root, "build/iteration")
			if err != nil || len(report.Outcomes) != 1 || report.Outcomes[0].Pass != 1 || diagnostics == "" {
				t.Fatalf("metrics = %#v, diagnostics %q, err %v", report, diagnostics, err)
			}
			if strings.Contains(diagnostics, marker) {
				t.Fatalf("diagnostics leaked marker: %q", diagnostics)
			}
		})
	}
}

func TestMetricsOutputProjectsOnlyAllowlistedFacts(t *testing.T) {
	marker := strings.Repeat("a", 64)
	root := t.TempDir()
	plan := metricsPlan("a")
	plan.Repository, plan.BaseRef, plan.Base, plan.Head, plan.DiffDigest = marker, marker, marker, marker, marker
	plan.Changed[0].Path = marker
	writeMetricsArtifact(t, root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{Target: "test-contract", Level: "focused", Status: GateFail, DurationMillis: 5, FailureLogPath: "build/iteration/" + marker + "/logs/test-contract.log"}}})
	report, diagnostic, err := collectMetrics(root, "build/iteration")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := renderJSON(report, &output); err != nil {
		t.Fatal(err)
	}
	if err := renderMetricsMarkdown(report, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String()+diagnostic, marker) {
		t.Fatalf("metrics output leaked marker: output %q, diagnostic %q", output.String(), diagnostic)
	}
}

func metricsPlan(letter string) Plan {
	plan := metricsPlanFor(0)
	plan.DiffDigest = strings.Repeat(letter, 64)
	return plan
}

func metricsPlanFor(index int) Plan {
	digest := fmt.Sprintf("%064x", index+1)
	return Plan{SchemaVersion: 1, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), DiffDigest: digest, Changed: []ChangedPath{{Path: "engine/engine.go", Status: "M", Owner: "engine-runtime", Kind: PathProduction}}, FocusedChecks: []FocusedCheck{{Owner: "engine-runtime"}}, RequiredTargets: []string{"fmt", "test-contract"}}
}

func writeMetricsArtifact(t *testing.T, root string, plan Plan, evidence Evidence) {
	writeMetricsArtifactAt(t, root, plan.DiffDigest, plan, evidence)
}

func writeMetricsArtifactAt(t *testing.T, root, digest string, plan Plan, evidence Evidence) {
	t.Helper()
	dir := filepath.Join(root, "build", "iteration", digest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var planData, evidenceData bytes.Buffer
	if err := renderJSON(plan, &planData); err != nil {
		t.Fatal(err)
	}
	if err := renderJSON(evidence, &evidenceData); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), planData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), evidenceData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRawMetricsArtifact(t *testing.T, root, digest, evidence string) {
	t.Helper()
	dir := filepath.Join(root, "build", "iteration", digest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), []byte(evidence), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRawEvidence(t *testing.T, root, digest, evidence string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "build", "iteration", digest, "evidence.json"), []byte(evidence), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRenderJSON(t *testing.T, value any) string {
	t.Helper()
	var output bytes.Buffer
	if err := renderJSON(value, &output); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(output.String())
}
