package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BenchmarkProcessRunner interface {
	Run(context.Context, string, []byte, string, ...string) ([]byte, error)
}

type commandBenchmarkProcessRunner struct{}

func (commandBenchmarkProcessRunner) Run(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = bytes.NewReader(stdin)
	return command.Output()
}

type HookBenchmarkReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Runs          int                 `json:"runs"`
	Modes         []HookBenchmarkMode `json:"modes"`
}

type HookBenchmarkMode struct {
	Mode        string `json:"mode"`
	SampleCount int    `json:"sample_count"`
	P50Millis   int64  `json:"p50_ms"`
	P95Millis   int64  `json:"p95_ms"`
}

const (
	hookWrapperPrefix         = "#!/usr/bin/env bash\nset -euo pipefail\n\nreadonly repository_root=\"$(git rev-parse --show-toplevel)\"\n"
	hookBenchmarkMaxFileBytes = 64 << 20
)

func hookWrapper(finalExec string) []byte { return []byte(hookWrapperPrefix + finalExec + "\n") }

func validateHookWrappers(production, candidate []byte) error {
	prefix := []byte(hookWrapperPrefix)
	if !bytes.HasPrefix(production, prefix) || !bytes.HasPrefix(candidate, prefix) || bytes.Equal(production, candidate) {
		return errors.New("invalid benchmark hook wrappers")
	}
	productionFinal := bytes.TrimSuffix(production[len(prefix):], []byte("\n"))
	candidateFinal := bytes.TrimSuffix(candidate[len(prefix):], []byte("\n"))
	if len(productionFinal) == 0 || len(candidateFinal) == 0 || bytes.Contains(productionFinal, []byte("\n")) || bytes.Contains(candidateFinal, []byte("\n")) {
		return errors.New("invalid benchmark hook wrappers")
	}
	if !bytes.Equal(productionFinal, []byte(`exec go -C "$repository_root" run ./scripts/iteration hook "$@"`)) ||
		!bytes.HasPrefix(candidateFinal, []byte(`exec "`)) || !bytes.HasSuffix(candidateFinal, []byte(`" hook "$@"`)) {
		return errors.New("invalid benchmark hook wrappers")
	}
	return nil
}

func runHookBenchmark(ctx context.Context, root string, runs int, runner BenchmarkProcessRunner, clock func() time.Time) (HookBenchmarkReport, error) {
	if runs < 5 || runs > 100 || runner == nil || clock == nil {
		return HookBenchmarkReport{}, errors.New("invalid hook benchmark configuration")
	}
	status, err := runner.Run(ctx, root, nil, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil || len(bytes.TrimSpace(status)) != 0 {
		return HookBenchmarkReport{}, errors.New("hook benchmark requires a clean committed tracked tree")
	}
	archive, err := runner.Run(ctx, root, nil, "git", "archive", "HEAD")
	if err != nil {
		return HookBenchmarkReport{}, errors.New("create hook benchmark fixture")
	}
	temp, err := os.MkdirTemp("", "eino-agent-hook-benchmark-")
	if err != nil {
		return HookBenchmarkReport{}, errors.New("create hook benchmark fixture")
	}
	defer os.RemoveAll(temp)
	fixture := filepath.Join(temp, "repository")
	if err := extractBenchmarkArchive(archive, fixture); err != nil {
		return HookBenchmarkReport{}, errors.New("create hook benchmark fixture")
	}
	for _, command := range [][]string{{"init", "-b", "master", fixture}, {"-C", fixture, "config", "user.email", "benchmark@invalid"}, {"-C", fixture, "config", "user.name", "benchmark"}, {"-C", fixture, "add", "."}, {"-C", fixture, "commit", "-m", "fixture"}, {"-C", fixture, "update-ref", "refs/remotes/origin/master", "HEAD"}} {
		if _, err := runner.Run(ctx, root, nil, "git", command...); err != nil {
			return HookBenchmarkReport{}, errors.New("create hook benchmark fixture")
		}
	}
	binary := filepath.Join(temp, "iteration-hook-benchmark")
	if _, err := runner.Run(ctx, fixture, nil, "go", "build", "-o", binary, "./scripts/iteration"); err != nil {
		return HookBenchmarkReport{}, errors.New("build hook benchmark candidate")
	}
	productionPath := filepath.Join(fixture, ".codex", "hooks", "iteration.sh")
	production, err := os.ReadFile(productionPath)
	if err != nil {
		return HookBenchmarkReport{}, errors.New("create hook benchmark wrapper")
	}
	candidate := hookWrapper("exec \"" + binary + "\" hook \"$@\"")
	if err := validateHookWrappers(production, candidate); err != nil {
		return HookBenchmarkReport{}, err
	}
	candidatePath := filepath.Join(temp, "wrapper-prebuilt.sh")
	if err := os.WriteFile(candidatePath, candidate, 0o700); err != nil {
		return HookBenchmarkReport{}, errors.New("create hook benchmark wrapper")
	}
	goRun, err := benchmarkHookMode(ctx, runner, clock, fixture, productionPath, runs)
	if err != nil {
		return HookBenchmarkReport{}, errors.New("run hook benchmark")
	}
	prebuilt, err := benchmarkHookMode(ctx, runner, clock, fixture, candidatePath, runs)
	if err != nil {
		return HookBenchmarkReport{}, errors.New("run hook benchmark")
	}
	return HookBenchmarkReport{SchemaVersion: 1, Runs: runs, Modes: []HookBenchmarkMode{
		benchmarkMode("wrapper_go_run", goRun), benchmarkMode("wrapper_prebuilt_binary", prebuilt),
	}}, nil
}

func extractBenchmarkArchive(archive []byte, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return errors.New("create archive root")
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("invalid archive")
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("invalid archive")
		}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(path, 0o700); err != nil {
				return errors.New("extract archive")
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > hookBenchmarkMaxFileBytes {
				return errors.New("invalid archive")
			}
			parent, err := os.Stat(filepath.Dir(path))
			if err != nil || !parent.IsDir() {
				return errors.New("extract archive")
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return errors.New("extract archive")
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("extract archive")
			}
		default:
			return errors.New("invalid archive")
		}
	}
}

func benchmarkHookMode(ctx context.Context, runner BenchmarkProcessRunner, clock func() time.Time, fixture, wrapper string, runs int) ([]int64, error) {
	result := make([]int64, 0, runs)
	for index := 0; index < runs; index++ {
		input, _ := json.Marshal(HookInput{SessionID: fmt.Sprintf("benchmark-%d", index), CWD: fixture, HookEventName: HookSessionStart, Source: "startup"})
		started := clock()
		if _, err := runner.Run(ctx, fixture, input, "bash", wrapper, "session-start"); err != nil {
			return nil, err
		}
		elapsed := clock().Sub(started).Milliseconds()
		if elapsed < 0 {
			return nil, errors.New("invalid benchmark clock")
		}
		result = append(result, elapsed)
	}
	return result, nil
}

func benchmarkMode(mode string, samples []int64) HookBenchmarkMode {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return HookBenchmarkMode{Mode: mode, SampleCount: len(samples), P50Millis: benchmarkNearestRank(samples, 50), P95Millis: benchmarkNearestRank(samples, 95)}
}

func benchmarkNearestRank(samples []int64, percentile int) int64 {
	return samples[(len(samples)*percentile+99)/100-1]
}
