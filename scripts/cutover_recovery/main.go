package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()))
}

func run(args []string, stdout, stderr io.Writer, deps dependencies) int {
	return runWithProcessCollector(args, stdout, stderr, deps, collectProcessOccupancy)
}

type processCollector func(context.Context, processReader, []string) ([]processRecord, error)

func runWithProcessCollector(args []string, stdout, stderr io.Writer, deps dependencies, collectProcesses processCollector) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cutover_recovery capture|verify [flags]")
		return 2
	}
	switch args[0] {
	case "capture":
		return runCapture(context.Background(), args[1:], stdout, stderr, deps, collectProcesses)
	case "verify":
		return runVerify(context.Background(), args[1:], stdout, stderr, deps)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runCapture(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies, collectProcesses processCollector) int {
	flags := flag.NewFlagSet("cutover_recovery capture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	privateFlag := newUniqueStringFlag("private-root")
	publicFlag := newUniqueStringFlag("public-root")
	archiveFlag := newUniqueStringFlag("archive-root")
	inputFlag := newUniqueStringFlag("input")
	outputFlag := newUniqueStringFlag("output")
	flags.Var(privateFlag, "private-root", "private-history checkout root")
	flags.Var(publicFlag, "public-root", "public canonical checkout root")
	flags.Var(archiveFlag, "archive-root", "private archive destination")
	flags.Var(inputFlag, "input", "strict cutover input JSON")
	flags.Var(outputFlag, "output", "sealed manifest output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths := []*uniqueStringFlag{privateFlag, publicFlag, archiveFlag, inputFlag, outputFlag}
	if flags.NArg() != 0 || !allAbsoluteFlags(paths) {
		fmt.Fprintln(stderr, "usage: cutover_recovery capture --private-root ABS --public-root ABS --archive-root ABS --input ABS --output ABS")
		return 2
	}
	if filepath.Clean(privateFlag.value) == filepath.Clean(archiveFlag.value) {
		fmt.Fprintln(stderr, "archive root must differ from every source root")
		return 2
	}
	if deps.Git == nil || deps.Processes == nil || deps.Now == nil || collectProcesses == nil {
		fmt.Fprintln(stderr, "capture dependencies are incomplete")
		return 1
	}
	privateRoot, err := canonicalExistingPath(privateFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "canonicalize private root: %v\n", err)
		return 1
	}
	publicRoot, err := canonicalExistingPath(publicFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "canonicalize public root: %v\n", err)
		return 1
	}
	archiveRoot, err := canonicalDestinationPath(archiveFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "canonicalize archive root: %v\n", err)
		return 1
	}
	if archiveRoot == privateRoot || overlappingPaths(publicRoot, privateRoot) {
		fmt.Fprintln(stderr, "public, private, and archive roots must be distinct")
		return 1
	}
	input, err := readCutoverInput(inputFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "read cutover input: %v\n", err)
		return 1
	}
	if err := validateExpectedRepositories(input); err != nil {
		fmt.Fprintf(stderr, "validate repository identities: %v\n", err)
		return 1
	}
	privateInventory, err := collectPrivateInventory(ctx, deps.Git, privateRoot, input)
	if err != nil {
		fmt.Fprintf(stderr, "capture private inventory: %v\n", err)
		return 1
	}
	if err := validateMappingTopology(privateInventory.ArchiveMapping); err != nil {
		fmt.Fprintf(stderr, "validate archive mapping: %v\n", err)
		return 1
	}
	if err := validatePublicSeparation(publicRoot, privateInventory.ArchiveMapping); err != nil {
		fmt.Fprintf(stderr, "validate public separation: %v\n", err)
		return 1
	}
	main, err := mainMapping(privateInventory.ArchiveMapping)
	if err != nil {
		fmt.Fprintf(stderr, "validate main archive mapping: %v\n", err)
		return 1
	}
	if main.Source != privateRoot || main.Destination != archiveRoot {
		fmt.Fprintln(stderr, "main checkout mapping must match private-root and archive-root")
		return 1
	}
	publicRecord, err := collectRepositoryRecord(ctx, deps.Git, publicRoot, "public")
	if err != nil {
		fmt.Fprintf(stderr, "capture public identity: %v\n", err)
		return 1
	}
	if publicRecord.OriginRepository != input.ExpectedPublicRepository {
		fmt.Fprintf(stderr, "public origin %q does not match expected public repository %q\n", publicRecord.OriginRepository, input.ExpectedPublicRepository)
		return 1
	}
	roots := mappingSources(privateInventory.ArchiveMapping)
	processes, err := collectProcesses(ctx, deps.Processes, roots)
	if err != nil {
		fmt.Fprintf(stderr, "capture process occupancy: %v\n", err)
		return 1
	}
	m := manifest{
		SchemaVersion:   schemaVersion,
		CapturedAt:      deps.Now().UTC().Format(time.RFC3339),
		Public:          publicRecord,
		Private:         privateInventory.Repository,
		ArchiveMapping:  privateInventory.ArchiveMapping,
		Refs:            privateInventory.Refs,
		Worktrees:       privateInventory.Worktrees,
		DirtyPaths:      privateInventory.DirtyPaths,
		Stashes:         privateInventory.Stashes,
		Processes:       processes,
		Classifications: privateInventory.Classifications,
	}
	m.Aggregates = aggregateRecord{
		ArchiveMappings: len(m.ArchiveMapping),
		Refs:            len(m.Refs),
		Worktrees:       len(m.Worktrees),
		DirtyPaths:      len(m.DirtyPaths),
		Stashes:         len(m.Stashes),
		Processes:       len(m.Processes),
		Classifications: len(m.Classifications),
	}
	if err := writeManifestAtomic(outputFlag.value, m); err != nil {
		fmt.Fprintf(stderr, "write recovery manifest: %v\n", err)
		return 1
	}
	written, err := readManifest(outputFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "strictly re-read recovery manifest: %v\n", err)
		return 1
	}
	writeStatus(stdout, "capture", written.Aggregates)
	return 0
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("cutover_recovery verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestFlag := newUniqueStringFlag("manifest")
	phaseFlag := newUniqueStringFlag("phase")
	flags.Var(manifestFlag, "manifest", "sealed recovery manifest")
	flags.Var(phaseFlag, "phase", "pre-move, post-move, pre-rollback, or rollback")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !allAbsoluteFlags([]*uniqueStringFlag{manifestFlag}) || !phaseFlag.set {
		fmt.Fprintln(stderr, "usage: cutover_recovery verify --manifest ABS --phase pre-move|post-move|pre-rollback|rollback")
		return 2
	}
	phase := validationPhase(phaseFlag.value)
	if phase != phasePreMove && phase != phasePostMove && phase != phasePreRollback && phase != phaseRollback {
		fmt.Fprintf(stderr, "unknown verification phase %q\n", phaseFlag.value)
		return 2
	}
	if deps.Git == nil || deps.Processes == nil || deps.Now == nil {
		fmt.Fprintln(stderr, "verification dependencies are incomplete")
		return 1
	}
	m, err := readManifest(manifestFlag.value)
	if err != nil {
		fmt.Fprintf(stderr, "read recovery manifest: %v\n", err)
		return 1
	}
	if err := verifyLiveState(ctx, deps, m, phase); err != nil {
		fmt.Fprintf(stderr, "verify %s: %v\n", phase, err)
		return 1
	}
	writeStatus(stdout, string(phase), m.Aggregates)
	return 0
}

type uniqueStringFlag struct {
	name  string
	value string
	set   bool
}

func newUniqueStringFlag(name string) *uniqueStringFlag { return &uniqueStringFlag{name: name} }

func (f *uniqueStringFlag) String() string { return f.value }

func (f *uniqueStringFlag) Set(value string) error {
	if f.set {
		return fmt.Errorf("--%s may be provided only once", f.name)
	}
	f.value, f.set = value, true
	return nil
}

func allAbsoluteFlags(flags []*uniqueStringFlag) bool {
	for _, value := range flags {
		if !value.set || value.value == "" || !filepath.IsAbs(value.value) {
			return false
		}
	}
	return true
}

func validatePublicSeparation(publicRoot string, mappings []archiveMappingRecord) error {
	for _, mapping := range mappings {
		if overlappingPaths(publicRoot, mapping.Source) {
			return fmt.Errorf("public root overlaps private source %q", mapping.Source)
		}
		if overlappingPaths(publicRoot, mapping.Destination) {
			return fmt.Errorf("public root overlaps archive destination %q", mapping.Destination)
		}
	}
	return nil
}

func validateExpectedRepositories(input cutoverInput) error {
	public, err := canonicalRepositoryIdentity(input.ExpectedPublicRepository)
	if err != nil {
		return fmt.Errorf("expected public repository: %w", err)
	}
	private, err := canonicalRepositoryIdentity(input.ExpectedPrivateRepository)
	if err != nil {
		return fmt.Errorf("expected private repository: %w", err)
	}
	if public == private {
		return errors.New("public and private repository identities must differ")
	}
	return nil
}

func canonicalRepositoryIdentity(value string) (string, error) {
	if value == "" {
		return "", errors.New("repository identity is empty")
	}
	normalized, err := normalizeOrigin("https://github.com/" + value)
	if err != nil {
		return "", err
	}
	if normalized != value {
		return "", errors.New("repository identity must be canonical owner/name")
	}
	return normalized, nil
}

func readCutoverInput(path string) (cutoverInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return cutoverInput{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var input cutoverInput
	if err := decoder.Decode(&input); err != nil {
		return cutoverInput{}, fmt.Errorf("decode cutover input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return cutoverInput{}, errors.New("cutover input contains trailing JSON value")
		}
		return cutoverInput{}, fmt.Errorf("decode cutover input trailing value: %w", err)
	}
	return input, nil
}

func writeStatus(output io.Writer, phase string, counts aggregateRecord) {
	fmt.Fprintf(output, "command=%s status=ok archive_mappings=%d refs=%d worktrees=%d dirty_paths=%d stashes=%d processes=%d classifications=%d\n",
		phase, counts.ArchiveMappings, counts.Refs, counts.Worktrees, counts.DirtyPaths, counts.Stashes, counts.Processes, counts.Classifications)
}

func defaultDependencies() dependencies {
	return dependencies{Git: commandGitReader{}, Processes: commandProcessReader{}, Now: time.Now}
}

type commandGitReader struct{}

func (commandGitReader) Run(ctx context.Context, root string, argv ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, argv...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err == nil {
		if stderr.Len() != 0 {
			return nil, errors.New("git command wrote stderr")
		}
		return output, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, commandExitError{code: exit.ExitCode()}
	}
	return output, err
}

type commandExitError struct{ code int }

func (e commandExitError) Error() string { return fmt.Sprintf("command exited %d", e.code) }
func (e commandExitError) ExitCode() int { return e.code }

type commandProcessReader struct{}

func (commandProcessReader) Run(ctx context.Context, argv ...string) (commandResult, error) {
	if len(argv) == 0 {
		return commandResult{}, errors.New("process command is empty")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, err
}
