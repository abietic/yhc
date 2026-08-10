//go:build parity

package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aymanbagabas/go-udiff"
)

// ComparePairwise compares captures from multiple projects for the same scenario/capture point.
func ComparePairwise(captures map[Project]*Capture) []ComparisonResult {
	var results []ComparisonResult
	projects := make([]Project, 0, len(captures))
	for p := range captures {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })

	for i := 0; i < len(projects); i++ {
		for j := i + 1; j < len(projects); j++ {
			a := captures[projects[i]]
			b := captures[projects[j]]
			normA := NormalizeStructural(a.Plain)
			normB := NormalizeStructural(b.Plain)

			result := ComparisonResult{
				Scenario:  a.Scenario,
				CaptureID: a.CaptureID,
				ProjectA:  projects[i],
				ProjectB:  projects[j],
				Match:     normA == normB,
			}

			if !result.Match {
				result.Diff = udiff.Unified(string(projects[i]), string(projects[j]), normA, normB)
			}

			results = append(results, result)
		}
	}
	return results
}

// CompareToGolden compares a capture against its golden file.
func CompareToGolden(capture *Capture, goldenDir string) (bool, string) {
	goldenPath := GoldenPath(goldenDir, capture.Project, capture.Scenario, capture.CaptureID)
	content, err := os.ReadFile(goldenPath)
	if err != nil {
		return false, fmt.Sprintf("golden file not found: %s", goldenPath)
	}

	normalized := NormalizeForCompare(capture.Plain)
	expected := string(content)

	if normalized == expected {
		return true, ""
	}

	diff := udiff.Unified(goldenPath, "actual", expected, normalized)
	return false, diff
}

// UpdateGolden writes or updates the golden file for a capture.
func UpdateGolden(capture *Capture, goldenDir string) error {
	goldenPath := GoldenPath(goldenDir, capture.Project, capture.Scenario, capture.CaptureID)
	dir := filepath.Dir(goldenPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	normalized := NormalizeForCompare(capture.Plain)
	return os.WriteFile(goldenPath, []byte(normalized), 0o644)
}

// GoldenPath returns the path to a golden file for a given project/scenario/capture.
func GoldenPath(baseDir string, project Project, scenario, captureID string) string {
	return filepath.Join(baseDir, string(project), scenario+"_"+captureID+".golden")
}

// GenerateReport produces a markdown report of all comparison results.
func GenerateReport(results []ComparisonResult) string {
	var sb strings.Builder
	sb.WriteString("# TUI Parity Report\n\n")
	sb.WriteString(fmt.Sprintf("**Generated**: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	total := len(results)
	matching := 0
	for _, r := range results {
		if r.Match {
			matching++
		}
	}
	sb.WriteString("## Summary\n\n| Metric | Value |\n|---|---:|\n")
	sb.WriteString(fmt.Sprintf("| Total comparisons | %d |\n", total))
	sb.WriteString(fmt.Sprintf("| Matching | %d |\n", matching))
	sb.WriteString(fmt.Sprintf("| Gaps | %d |\n\n", total-matching))

	if total-matching > 0 {
		sb.WriteString("## Parity Gaps\n\n")
		for _, r := range results {
			if r.Match {
				continue
			}
			sb.WriteString(fmt.Sprintf("### %s / %s: %s vs %s\n\n", r.Scenario, r.CaptureID, r.ProjectA, r.ProjectB))
			sb.WriteString("```diff\n")
			sb.WriteString(r.Diff)
			sb.WriteString("\n```\n\n")
		}
	}

	if matching > 0 {
		sb.WriteString("## Matching Captures\n\n")
		for _, r := range results {
			if !r.Match {
				continue
			}
			sb.WriteString(fmt.Sprintf("- %s / %s: %s = %s\n", r.Scenario, r.CaptureID, r.ProjectA, r.ProjectB))
		}
	}

	return sb.String()
}
