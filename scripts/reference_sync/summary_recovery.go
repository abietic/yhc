package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const summaryBatchTimeLayout = "20060102T150405.000000000Z"

type summaryInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// A job is durable before model dispatch. Successful fast-forwards are never replayed.
type summaryJob struct {
	Version int            `json:"version"`
	Batch   string         `json:"batch"`
	Status  string         `json:"status"`
	Inputs  []summaryInput `json:"inputs"`
	Summary string         `json:"summary,omitempty"`
}

func (j *summaryJob) save(root string) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeMemoryFile(filepath.Join(root, "summary-jobs", j.Batch+".json"), append(data, '\n'))
}

func updateInput(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || filepath.ToSlash(filepath.Clean(relative)) != relative || !strings.HasPrefix(relative, "updates/") || filepath.Ext(relative) != ".md" {
		return "", errors.New("summary job input must be a canonical updates/*.md path")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	realRoot, err := filepath.EvalSymlinks(filepath.Join(root, "updates"))
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("summary job input escapes updates directory")
	}
	return path, nil
}

func newSummaryJob(root string, now time.Time, paths []string) (*summaryJob, error) {
	j := &summaryJob{Version: 1, Batch: now.UTC().Format(summaryBatchTimeLayout), Status: "pending"}
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		path, err = updateInput(root, rel)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		j.Inputs = append(j.Inputs, summaryInput{rel, hex.EncodeToString(digest[:])})
	}
	// Never re-hash changed evidence into an already registered batch.
	if data, err := os.ReadFile(filepath.Join(root, "summary-jobs", j.Batch+".json")); err == nil {
		var existing summaryJob
		if err := json.Unmarshal(data, &existing); err != nil {
			return nil, err
		}
		if existing.Version != j.Version || existing.Batch != j.Batch || len(existing.Inputs) != len(j.Inputs) {
			return nil, errors.New("summary batch registration mismatch")
		}
		for i := range j.Inputs {
			if existing.Inputs[i] != j.Inputs[i] {
				return nil, errors.New("summary batch inputs changed; registration refused")
			}
		}
		if _, err := existing.paths(root); err != nil {
			return nil, err
		}
		return &existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := j.save(root); err != nil {
		return nil, err
	}
	return j, nil
}

// The path was durably reserved before dispatch; only the controlled header is
// checked here, never model text. This repairs the result/completion write gap.
func reconcileSavedSummary(root string, job *summaryJob) (bool, error) {
	if job.Summary == "" {
		return false, nil
	}
	if filepath.ToSlash(filepath.Dir(job.Summary)) != "summaries" || filepath.Ext(job.Summary) != ".md" || filepath.IsAbs(job.Summary) {
		return false, errors.New("invalid reserved summary path")
	}
	path := filepath.Join(root, filepath.FromSlash(job.Summary))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("reserved summary is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	header, _, ok := strings.Cut(string(data), "\n## Subagent analysis\n")
	if !ok || !strings.Contains(header, "\n- Status: `generated`\n") {
		return false, nil
	}
	if !strings.Contains(header, "\n- Source batch: `"+job.Batch+"`\n") {
		return false, errors.New("reserved summary batch mismatch")
	}
	for _, input := range job.Inputs {
		if !strings.Contains(header, "\n- Source update: `"+input.Path+"`\n") {
			return false, errors.New("reserved summary input mismatch")
		}
	}
	job.Status = "completed"
	if err := job.save(root); err != nil {
		return false, err
	}
	return true, nil
}

func (j *summaryJob) paths(root string) ([]string, error) {
	if j.Version != 1 || len(j.Inputs) == 0 || len(j.Inputs) > 1000 || (j.Status != "pending" && j.Status != "completed") {
		return nil, errors.New("invalid summary job metadata")
	}
	if _, err := time.Parse(summaryBatchTimeLayout, j.Batch); err != nil {
		return nil, errors.New("invalid summary batch timestamp")
	}
	var paths []string
	seen := make(map[string]bool)
	for _, input := range j.Inputs {
		if seen[input.Path] {
			return nil, errors.New("duplicate summary input")
		}
		seen[input.Path] = true
		path, err := updateInput(root, input.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != input.SHA256 {
			return nil, errors.New("summary update input changed; recovery refused")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// Import only explicit legacy failures, not arbitrary old updates or successful runs.
func importLegacySummaryFailures(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "summaries", "*-updates-subagent.md"))
	if err != nil {
		return err
	}
	for _, file := range files {
		stamp := strings.TrimSuffix(filepath.Base(file), "-updates-subagent.md")
		when, err := time.Parse(summaryBatchTimeLayout, stamp)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "summary-jobs", stamp+".json")); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "\n- Status: `failed`\n") || strings.Contains(string(data), "\n- Source batch:") {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(root, "updates", stamp+"-*.md"))
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return fmt.Errorf("legacy failed summary %s has no update evidence", filepath.Base(file))
		}
		var count int
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "- Update files: ") {
				_, _ = fmt.Sscanf(line, "- Update files: %d", &count)
			}
		}
		if count != len(paths) {
			return fmt.Errorf("legacy failed summary %s update evidence count mismatch", filepath.Base(file))
		}
		if _, err := newSummaryJob(root, when, paths); err != nil {
			return err
		}
	}
	return nil
}

// Recover at most one old batch per sync, so a backlog cannot starve fresh syncs.
func recoverPendingSummaries(ctx context.Context, projectDir, root string, current []string, cfg SubagentSummary, factory subagentSummaryFactory, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := importLegacySummaryFailures(root); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(root, "summary-jobs", "*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var job summaryJob
		if err := json.Unmarshal(data, &job); err != nil {
			return fmt.Errorf("decode summary job: %w", err)
		}
		if job.Batch+".json" != filepath.Base(file) {
			return errors.New("summary job filename/batch mismatch")
		}
		if job.Status == "completed" {
			continue
		}
		paths, err := job.paths(root)
		if err != nil {
			return err
		}
		isCurrent := false
		for _, path := range paths {
			for _, fresh := range current {
				if path == fresh {
					isCurrent = true
				}
			}
		}
		if isCurrent {
			continue
		} // Already exhausted this batch's budget this run.
		settled, err := reconcileSavedSummary(root, &job)
		if err != nil {
			return err
		}
		if settled {
			result := postUpdateSummaryResult{Status: "generated", Path: job.Summary, SourceBatch: job.Batch, InputFiles: len(job.Inputs)}
			for _, input := range job.Inputs {
				result.SourceFiles = append(result.SourceFiles, input.Path)
			}
			if err := writeSummaryRecoveryRun(root, result); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "post-update summary recovery: recovered - source batch %s, saved summary %s (no model redispatch)\n", job.Batch, job.Summary)
			return nil
		}
		result, err := summarizeUpdateFiles(ctx, projectDir, root, time.Now().UTC(), paths, cfg, factory, &job)
		recordErr := writeSummaryRecoveryRun(root, result)
		if recordErr != nil {
			return errors.Join(err, recordErr)
		}
		if err != nil {
			printSubagentSummary(stdout, result)
			return err
		}
		fmt.Fprintf(stdout, "post-update summary recovery: recovered - source batch %s, %s\n", job.Batch, result.Path)
		return nil
	}
	return nil
}

func writeSummaryRecoveryRun(root string, result postUpdateSummaryResult) error {
	now := time.Now().UTC()
	return writeMemoryFile(filepath.Join(root, "runs", memoryFilename(now, "summary-recovery")), renderRunMemory(now, nil, &result))
}
