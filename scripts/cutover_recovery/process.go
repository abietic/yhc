package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const lsofTimeout = 30 * time.Second

type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type processReader interface {
	Run(context.Context, ...string) (commandResult, error)
}

func collectProcessOccupancy(ctx context.Context, reader processReader, roots []string) ([]processRecord, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("live process occupancy capture is supported only on darwin")
	}
	return collectLsofOccupancy(ctx, reader, roots)
}

// collectLsofOccupancy is pure with respect to the host platform so fake
// readers can exercise the lsof contract on every supported build target.
func collectLsofOccupancy(ctx context.Context, reader processReader, roots []string) ([]processRecord, error) {
	canonicalRoots, err := canonicalProcessRoots(roots)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]processRecord)
	for _, root := range canonicalRoots {
		rootCtx, cancel := context.WithTimeout(ctx, lsofTimeout)
		result, runErr := reader.Run(rootCtx, "lsof", "-nP", "-Fpfn", "+D", root)
		timedOut := rootCtx.Err()
		cancel()
		if timedOut != nil {
			return nil, fmt.Errorf("lsof for %q exceeded %s: %w", root, lsofTimeout, timedOut)
		}
		if runErr != nil {
			return nil, fmt.Errorf("run lsof for %q: %w", root, runErr)
		}
		if result.ExitCode == 1 && len(result.Stdout) == 0 && len(result.Stderr) == 0 {
			continue
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("lsof for %q exited %d", root, result.ExitCode)
		}
		if len(result.Stderr) != 0 {
			return nil, fmt.Errorf("lsof for %q wrote stderr", root)
		}
		if len(result.Stdout) == 0 {
			return nil, fmt.Errorf("lsof for %q exited successfully with empty output", root)
		}
		records, err := parseLsofOccupancy(result.Stdout, root)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if prior, exists := seen[record.RecordID]; exists && prior != record {
				return nil, fmt.Errorf("conflicting process record %q", record.RecordID)
			}
			seen[record.RecordID] = record
		}
	}
	records := make([]processRecord, 0, len(seen))
	for _, record := range seen {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RecordID < records[j].RecordID })
	return records, nil
}

func canonicalProcessRoots(roots []string) ([]string, error) {
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" {
			return nil, errors.New("process occupancy root is empty")
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve process occupancy root %q: %w", root, err)
		}
		if !filepath.IsAbs(resolved) {
			return nil, fmt.Errorf("process occupancy root %q is not absolute", root)
		}
		seen[filepath.Clean(resolved)] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for root := range seen {
		result = append(result, root)
	}
	sort.Strings(result)
	return result, nil
}

func parseLsofOccupancy(output []byte, root string) ([]processRecord, error) {
	if len(output) != 0 && output[len(output)-1] != '\n' {
		return nil, errors.New("lsof output is truncated without a final newline")
	}
	rootRecordID := makeRecordID("process_root", root, root)
	var records []processRecord
	var pid int
	var descriptor string
	completedPID := false
	lines := strings.Split(string(output), "\n")
	for index, line := range lines {
		if line == "" {
			if index == len(lines)-1 {
				continue
			}
			return nil, errors.New("lsof output contains empty field")
		}
		if len(line) < 2 {
			return nil, fmt.Errorf("malformed lsof field %q", line)
		}
		switch line[0] {
		case 'p':
			if descriptor != "" || (pid != 0 && !completedPID) {
				return nil, errors.New("lsof output changed PID before completing descriptor")
			}
			value, err := strconv.Atoi(line[1:])
			if err != nil || value <= 0 {
				return nil, fmt.Errorf("invalid lsof PID %q", line)
			}
			pid, descriptor, completedPID = value, "", false
		case 'f':
			if pid == 0 || descriptor != "" || len(line) == 1 {
				return nil, fmt.Errorf("lsof descriptor without PID %q", line)
			}
			descriptor = line[1:]
		case 'n':
			if pid == 0 || descriptor == "" || len(line) == 1 {
				return nil, fmt.Errorf("lsof path without PID and descriptor %q", line)
			}
			path := line[1:]
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || !pathWithin(path, root) {
				return nil, fmt.Errorf("lsof path %q is outside root %q", path, root)
			}
			kind := "open_file"
			if descriptor == "cwd" {
				kind = "cwd"
			}
			record := processRecord{RootRecordID: rootRecordID, PID: pid, OccupancyKind: kind, Path: path}
			record.RecordID = makeRecordID("process", record.RootRecordID, processIdentity(record))
			records = append(records, record)
			descriptor, completedPID = "", true
		default:
			return nil, fmt.Errorf("unsupported lsof field %q", line)
		}
	}
	if pid == 0 && len(output) != 0 {
		return nil, errors.New("lsof output has no process PID")
	}
	if descriptor != "" {
		return nil, errors.New("lsof output ended with partial descriptor")
	}
	if pid != 0 && !completedPID {
		return nil, errors.New("lsof output ended without a completed descriptor for PID")
	}
	return records, nil
}
