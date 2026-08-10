package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const maxMetricArtifacts = 256

type MetricsReport struct {
	SchemaVersion int             `json:"schema_version"`
	State         string          `json:"state"`
	Outcomes      []MetricOutcome `json:"outcomes"`
}

type MetricOutcome struct {
	Target        string `json:"target"`
	Level         string `json:"level"`
	Pass          int    `json:"pass"`
	Fail          int    `json:"fail"`
	Blocked       int    `json:"blocked"`
	NotApplicable int    `json:"not_applicable"`
	Samples       int    `json:"samples"`
	P50Millis     *int64 `json:"p50_ms,omitempty"`
	P95Millis     *int64 `json:"p95_ms,omitempty"`

	durations []int64
}

type metricArtifact struct {
	name    string
	modTime time.Time
}

func collectMetrics(repositoryRoot, metricsRoot string) (MetricsReport, string, error) {
	report := MetricsReport{SchemaVersion: 1, Outcomes: []MetricOutcome{}}
	if err := validateRepositoryPath(metricsRoot); err != nil {
		return report, "", errors.New("invalid metrics root")
	}
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return report, "", errors.New("cannot open metrics root")
	}
	defer root.Close()
	dir, err := openStrictDir(root, metricsRoot, false)
	if errors.Is(err, os.ErrNotExist) {
		report.State = "no_data"
		return report, "", nil
	}
	if err != nil {
		return report, "", errors.New("cannot inspect metrics root")
	}
	defer dir.Close()

	artifacts, rejected, err := metricArtifacts(dir)
	if err != nil {
		return report, "", errors.New("cannot inspect metrics root")
	}
	byKey := make(map[string]*MetricOutcome)
	for _, artifact := range artifacts {
		if err := addMetricArtifact(dir, artifact.name, byKey); err != nil {
			rejected = true
		}
	}
	report.Outcomes = metricOutcomes(byKey)
	if len(report.Outcomes) == 0 {
		report.State = "no_data"
	} else if !metricsReady(report.Outcomes) {
		report.State = "insufficient_samples"
	} else {
		report.State = "ready"
	}
	diagnostic := ""
	if rejected {
		diagnostic = "metrics: skipped invalid evidence artifact"
	}
	return report, diagnostic, nil
}

func metricArtifacts(root *os.Root) ([]metricArtifact, bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, false, fmt.Errorf("open metrics artifacts: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, false, fmt.Errorf("list metrics artifacts: %w", err)
	}
	artifacts := make([]metricArtifact, 0, len(entries))
	rejected := false
	for _, entry := range entries {
		info, infoErr := root.Lstat(entry.Name())
		if infoErr != nil {
			rejected = true
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			rejected = true
			continue
		}
		if !info.IsDir() {
			continue
		}
		if !digestPattern.MatchString(entry.Name()) {
			if containsEvidenceFile(root, entry.Name()) {
				rejected = true
			}
			continue
		}
		modTime, artifactErr := metricArtifactModTime(root, entry.Name())
		if artifactErr != nil {
			rejected = true
			continue
		}
		artifacts = append(artifacts, metricArtifact{name: entry.Name(), modTime: modTime})
	}
	slices.SortFunc(artifacts, func(left, right metricArtifact) int {
		if order := right.modTime.Compare(left.modTime); order != 0 {
			return order
		}
		return cmp.Compare(left.name, right.name)
	})
	if len(artifacts) > maxMetricArtifacts {
		artifacts = artifacts[:maxMetricArtifacts]
	}
	return artifacts, rejected, nil
}

func metricArtifactModTime(root *os.Root, digest string) (time.Time, error) {
	dir, err := root.OpenRoot(digest)
	if err != nil {
		return time.Time{}, err
	}
	defer dir.Close()
	evidence, err := strictRegularFile(dir, "evidence.json")
	if err != nil {
		return time.Time{}, err
	}
	defer evidence.Close()
	info, err := evidence.Stat()
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func containsEvidenceFile(root *os.Root, name string) bool {
	dir, err := root.OpenRoot(name)
	if err != nil {
		return false
	}
	defer dir.Close()
	_, err = dir.Lstat("evidence.json")
	return err == nil
}

func addMetricArtifact(root *os.Root, digest string, outcomes map[string]*MetricOutcome) error {
	dir, err := root.OpenRoot(digest)
	if err != nil {
		return err
	}
	defer dir.Close()
	plan, err := readPlan(dir)
	if err != nil || plan.DiffDigest != digest {
		return errors.New("invalid stored metrics plan")
	}
	evidence, err := readEvidence(dir)
	if err != nil || !samePlan(evidence.Plan, plan) {
		return errors.New("invalid stored metrics evidence")
	}
	if err := validateStoredEvidence(evidence, plan); err != nil {
		return errors.New("invalid stored metrics evidence")
	}
	for _, gate := range evidence.Gates {
		level := VerifyLevel(gate.Level)
		if !safeTarget(gate.Target) || !slices.Contains(expectedTargets(plan, level), gate.Target) {
			return errors.New("invalid stored metrics gate")
		}
	}
	for _, gate := range evidence.Gates {
		key := gate.Level + "\x00" + gate.Target
		outcome := outcomes[key]
		if outcome == nil {
			outcome = &MetricOutcome{Target: gate.Target, Level: gate.Level}
			outcomes[key] = outcome
		}
		switch gate.Status {
		case GatePass:
			outcome.Pass++
		case GateFail:
			outcome.Fail++
		case GateBlocked:
			outcome.Blocked++
		case GateNotApplicable:
			outcome.NotApplicable++
		}
		if (gate.Status == GatePass || gate.Status == GateFail) && gate.DurationMillis > 0 {
			outcome.durations = append(outcome.durations, gate.DurationMillis)
		}
	}
	return nil
}

func metricOutcomes(byKey map[string]*MetricOutcome) []MetricOutcome {
	outcomes := make([]MetricOutcome, 0, len(byKey))
	for _, outcome := range byKey {
		outcome.Samples = len(outcome.durations)
		if outcome.Samples >= 5 {
			slices.Sort(outcome.durations)
			p50 := nearestRank(outcome.durations, 50)
			p95 := nearestRank(outcome.durations, 95)
			outcome.P50Millis, outcome.P95Millis = &p50, &p95
		}
		outcome.durations = nil
		outcomes = append(outcomes, *outcome)
	}
	slices.SortFunc(outcomes, func(left, right MetricOutcome) int {
		if order := strings.Compare(left.Level, right.Level); order != 0 {
			return order
		}
		return strings.Compare(left.Target, right.Target)
	})
	return outcomes
}

func metricsReady(outcomes []MetricOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.P50Millis != nil && outcome.P95Millis != nil {
			return true
		}
	}
	return false
}

func nearestRank(values []int64, percentile int) int64 {
	index := (len(values)*percentile + 99) / 100
	return values[index-1]
}

func renderMetricsMarkdown(report MetricsReport, writer io.Writer) error {
	var output strings.Builder
	fmt.Fprintf(&output, "# Iteration Metrics\n\n- State: `%s`\n\n", report.State)
	output.WriteString("| Level | Target | Pass | Fail | Blocked | Not applicable | Samples | p50 ms | p95 ms |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, outcome := range report.Outcomes {
		p50, p95 := "", ""
		if outcome.P50Millis != nil {
			p50 = fmt.Sprintf("%d", *outcome.P50Millis)
		}
		if outcome.P95Millis != nil {
			p95 = fmt.Sprintf("%d", *outcome.P95Millis)
		}
		fmt.Fprintf(&output, "| %s | %s | %d | %d | %d | %d | %d | %s | %s |\n", outcome.Level, outcome.Target, outcome.Pass, outcome.Fail, outcome.Blocked, outcome.NotApplicable, outcome.Samples, p50, p95)
	}
	_, err := io.WriteString(writer, output.String())
	return err
}
