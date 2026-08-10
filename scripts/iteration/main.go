package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"
)

type dependencies struct {
	git              GitSource
	tree             TreeSource
	slices           SliceResolver
	openRoot         func(string) (*os.Root, error)
	goos             string
	root             string
	hookInput        io.Reader
	branchName       func(context.Context, string) (string, error)
	hookStoreFactory func(string) HookStateStore
	trackedFiles     func(context.Context) ([]string, error)
	runnerFactory    func(Plan) TargetRunner
	storeFactory     func(string) EvidenceStore
	deepStoreFactory func(string) DeepIntakeStore
	benchmarkRunner  BenchmarkProcessRunner
	benchmarkClock   func() time.Time
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()))
}

func run(args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("iteration", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "quality/iteration.yaml", "iteration policy path")
	baseRef := flags.String("base", "origin/master", "base Git ref")
	head := flags.String("head", "", "committed head object")
	sliceID := flags.String("slice-id", "", "accepted migration slice identifier")
	format := flags.String("format", "markdown", "output format: json or markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: iteration [flags] plan|evidence|policy-check|boundaries|deep|metrics|hook-benchmark|verify --level focused|merge|hook <event>")
		return 2
	}
	command := flags.Arg(0)
	if !oneOf(command, "plan", "evidence", "policy-check", "boundaries", "deep", "verify", "hook", "metrics", "hook-benchmark") {
		fmt.Fprintf(stderr, "unknown command %q\n", command)
		return 2
	}
	if !oneOf(*format, "json", "markdown") {
		fmt.Fprintf(stderr, "unknown format %q\n", *format)
		return 2
	}
	var verifyLevel VerifyLevel
	var hookEvent HookEventName
	boundaryAll := false
	requireReady := false
	metricsRoot := ""
	if command == "verify" {
		verifyFlags := flag.NewFlagSet("verify", flag.ContinueOnError)
		verifyFlags.SetOutput(stderr)
		level := verifyFlags.String("level", "", "verification level: focused or merge")
		if err := verifyFlags.Parse(flags.Args()[1:]); err != nil || verifyFlags.NArg() != 0 || !oneOf(*level, string(VerifyFocused), string(VerifyMerge)) {
			fmt.Fprintln(stderr, "usage: iteration [flags] verify --level focused|merge")
			return 2
		}
		verifyLevel = VerifyLevel(*level)
	} else if command == "evidence" {
		evidenceFlags := flag.NewFlagSet("evidence", flag.ContinueOnError)
		evidenceFlags.SetOutput(stderr)
		evidenceFlags.BoolVar(&requireReady, "require-ready", false, "require current evidence_ready state")
		if err := evidenceFlags.Parse(flags.Args()[1:]); err != nil || evidenceFlags.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: iteration [flags] evidence [--require-ready]")
			return 2
		}
		if requireReady && strings.TrimSpace(*head) == "" {
			fmt.Fprintln(stderr, "evidence --require-ready requires explicit --head")
			return 2
		}
	} else if command == "boundaries" {
		boundaryFlags := flag.NewFlagSet("boundaries", flag.ContinueOnError)
		boundaryFlags.SetOutput(stderr)
		boundaryFlags.BoolVar(&boundaryAll, "all", false, "include current baseline diagnostics")
		boundaryFlags.StringVar(format, "format", *format, "output format: json or markdown")
		if err := boundaryFlags.Parse(flags.Args()[1:]); err != nil || boundaryFlags.NArg() != 0 ||
			!oneOf(*format, "json", "markdown") {
			fmt.Fprintln(stderr, "usage: iteration [flags] boundaries [--all] [--format json|markdown]")
			return 2
		}
	} else if command == "deep" {
		deepFlags := flag.NewFlagSet("deep", flag.ContinueOnError)
		deepFlags.SetOutput(stderr)
		deepFlags.StringVar(format, "format", *format, "output format: json or markdown")
		if err := deepFlags.Parse(flags.Args()[1:]); err != nil || deepFlags.NArg() != 0 ||
			!oneOf(*format, "json", "markdown") {
			fmt.Fprintln(stderr, "usage: iteration [flags] deep [--format json|markdown]")
			return 2
		}
	} else if command == "hook" {
		if flags.NArg() != 2 {
			fmt.Fprintln(stderr, "usage: iteration [flags] hook session-start|post-tool-use|subagent-start|subagent-stop|stop|session-end")
			return 2
		}
		var ok bool
		hookEvent, ok = parseHookEvent(flags.Arg(1))
		if !ok {
			fmt.Fprintln(stderr, "unknown hook event")
			return 2
		}
	} else if command == "metrics" {
		metricsFlags := flag.NewFlagSet("metrics", flag.ContinueOnError)
		metricsFlags.SetOutput(stderr)
		metricsFlags.StringVar(&metricsRoot, "root", "build/iteration", "local iteration evidence root")
		metricsFlags.StringVar(format, "format", *format, "output format: json or markdown")
		if err := metricsFlags.Parse(flags.Args()[1:]); err != nil || metricsFlags.NArg() != 0 ||
			!oneOf(*format, "json", "markdown") {
			fmt.Fprintln(stderr, "usage: iteration metrics [--root build/iteration] [--format json|markdown]")
			return 2
		}
	} else if command == "hook-benchmark" {
		benchmarkFlags := flag.NewFlagSet("hook-benchmark", flag.ContinueOnError)
		benchmarkFlags.SetOutput(io.Discard)
		runs := benchmarkFlags.Int("runs", 0, "benchmark runs (5..100)")
		benchmarkFormat := benchmarkFlags.String("format", "", "output format: json")
		if err := benchmarkFlags.Parse(flags.Args()[1:]); err != nil || benchmarkFlags.NArg() != 0 || *benchmarkFormat != "json" || *runs < 5 || *runs > 100 {
			fmt.Fprintln(stderr, "usage: iteration hook-benchmark --runs 5..100 --format json")
			return 2
		}
		root := deps.root
		if strings.TrimSpace(root) == "" {
			root = "."
		}
		report, benchmarkErr := runHookBenchmark(context.Background(), root, *runs, deps.benchmarkRunner, deps.benchmarkClock)
		if benchmarkErr != nil {
			reportRunError(stderr, benchmarkErr)
			return 1
		}
		if benchmarkErr = renderJSON(report, stdout); benchmarkErr != nil {
			reportRunError(stderr, benchmarkErr)
			return 1
		}
		return 0
	} else if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "unexpected command arguments")
		return 2
	}
	if command == "metrics" {
		repositoryPath := deps.root
		if strings.TrimSpace(repositoryPath) == "" {
			repositoryPath = "."
		}
		report, diagnostic, metricsErr := collectMetrics(repositoryPath, metricsRoot)
		if metricsErr != nil {
			reportRunError(stderr, metricsErr)
			return 1
		}
		var output bytes.Buffer
		if *format == "json" {
			metricsErr = renderJSON(report, &output)
		} else {
			metricsErr = renderMetricsMarkdown(report, &output)
		}
		if metricsErr != nil {
			reportRunError(stderr, metricsErr)
			return 1
		}
		if _, metricsErr = stdout.Write(output.Bytes()); metricsErr != nil {
			reportRunError(stderr, metricsErr)
			return 1
		}
		if diagnostic != "" {
			fmt.Fprintln(stderr, diagnostic)
		}
		return 0
	}
	if err := validateRepositoryPath(*policyPath); err != nil {
		reportRunError(stderr, err)
		return 1
	}
	repositoryPath := deps.root
	if strings.TrimSpace(repositoryPath) == "" {
		repositoryPath = "."
	}
	if deps.openRoot == nil {
		reportRunError(stderr, errors.New("repository root opener is unavailable"))
		return 1
	}
	repositoryRoot, err := deps.openRoot(repositoryPath)
	if err != nil {
		reportRunError(stderr, err)
		return 1
	}
	defer repositoryRoot.Close()
	policy, err := loadPolicy(repositoryRoot, *policyPath)
	if err != nil {
		reportRunError(stderr, err)
		return 1
	}

	ctx := context.Background()
	if command == "policy-check" {
		if deps.trackedFiles == nil {
			reportRunError(stderr, errors.New("tracked file source is unavailable"))
			return 1
		}
		tracked, listErr := deps.trackedFiles(ctx)
		if listErr != nil {
			reportRunError(stderr, listErr)
			return 1
		}
		unknown, classifyErr := unclassifiedTrackedPaths(policy, tracked)
		if classifyErr != nil {
			reportRunError(stderr, classifyErr)
			return 1
		}
		if len(unknown) > 0 {
			reportRunError(stderr, fmt.Errorf("unclassified tracked paths: %s", strings.Join(unknown, ", ")))
			return 1
		}
		return 0
	}

	var acceptedSlice *SliceRef
	if *sliceID != "" {
		if deps.slices == nil {
			reportRunError(stderr, errors.New("slice resolver is unavailable"))
			return 1
		}
		acceptedSlice, err = deps.slices.Resolve(ctx, *sliceID)
		if err != nil {
			reportRunError(stderr, err)
			return 1
		}
	}
	if deps.git == nil {
		reportRunError(stderr, errors.New("git source is unavailable"))
		return 1
	}
	snapshot, err := resolveSnapshot(ctx, repositoryPath, *baseRef, *head, deps.git)
	if err != nil {
		reportRunError(stderr, err)
		return 1
	}
	plan, err := buildPlan(policy, snapshot, deps.goos, acceptedSlice)
	if err != nil {
		reportRunError(stderr, err)
		return 1
	}

	var output bytes.Buffer
	exitCode := 0
	switch command {
	case "plan":
		if *format == "json" {
			err = renderJSON(plan, &output)
		} else {
			err = renderPlanMarkdown(plan, &output)
		}
	case "evidence":
		if deps.storeFactory == nil {
			err = errors.New("evidence store factory is unavailable")
			break
		}
		evidence, loadErr := deps.storeFactory(repositoryPath).Load(plan)
		if loadErr != nil {
			err = loadErr
			break
		}
		if len(evidence.Gates) == 0 {
			evidence = initialEvidence(plan)
		}
		if requireReady {
			if evidence.State != "evidence_ready" {
				err = errors.New("committed iteration evidence is not ready")
			}
			break
		}
		if *format == "json" {
			err = renderJSON(evidence, &output)
		} else {
			err = renderEvidenceMarkdown(evidence, &output)
		}
	case "verify":
		if deps.runnerFactory == nil || deps.storeFactory == nil {
			err = errors.New("verification factories are unavailable")
			break
		}
		store := deps.storeFactory(repositoryPath)
		replan := func(replanCtx context.Context) (Plan, error) {
			snapshot, snapshotErr := resolveSnapshot(replanCtx, repositoryPath, *baseRef, *head, deps.git)
			if snapshotErr != nil {
				return Plan{}, snapshotErr
			}
			return buildPlan(policy, snapshot, deps.goos, acceptedSlice)
		}
		evidence, verifyErr := verify(ctx, repositoryPath, VerifyOptions{Level: verifyLevel, Plan: plan}, deps.runnerFactory(plan), store, replan)
		if verifyErr != nil {
			err = verifyErr
			break
		}
		if verifyLevel == VerifyMerge {
			if _, coverageErr := writeCoverageAdvisory(repositoryPath, evidence.Plan); coverageErr != nil {
				fmt.Fprintf(stderr, "coverage advisory: %v\n", coverageErr)
			}
		}
		if *format == "json" {
			err = renderJSON(evidence, &output)
		} else {
			err = renderEvidenceMarkdown(evidence, &output)
		}
		if err == nil {
			exitCode = verificationExitCode(evidence, verifyLevel)
		}
	case "boundaries":
		if deps.tree == nil {
			err = errors.New("tree source is unavailable")
			break
		}
		tree := deps.tree
		if strings.TrimSpace(*head) == "" {
			changed := make(map[string]struct{}, len(snapshot.Changed))
			for _, change := range snapshot.Changed {
				changed[change.Path] = struct{}{}
			}
			tree = worktreeHeadTreeSource{source: tree, head: plan.Head, changed: changed}
		}
		analysis, boundaryErr := buildBoundaryAnalysis(ctx, plan, policy, tree)
		if boundaryErr != nil {
			err = boundaryErr
			break
		}
		if *format == "json" {
			err = renderBoundaryJSON(analysis, boundaryAll, &output)
		} else {
			err = renderBoundaryMarkdown(analysis, boundaryAll, &output)
		}
		if err == nil && boundaryFailed(analysis.report) {
			exitCode = 1
		}
	case "deep":
		if deps.runnerFactory == nil || deps.deepStoreFactory == nil {
			err = errors.New("deep verification factories are unavailable")
			break
		}
		result, deepErr := runDeep(
			ctx,
			repositoryPath,
			plan,
			policy,
			deps.goos,
			deps.runnerFactory(plan),
			deps.deepStoreFactory(repositoryPath),
		)
		if deepErr != nil {
			err = deepErr
			break
		}
		if *format == "json" {
			err = renderJSON(result, &output)
		} else {
			err = renderDeepMarkdown(result, &output)
		}
		if err == nil && result.Intake != nil {
			exitCode = 1
		}
	case "hook":
		if deps.hookInput == nil || deps.branchName == nil || deps.hookStoreFactory == nil || deps.storeFactory == nil {
			err = errors.New("hook adapter dependencies are unavailable")
			break
		}
		evidence, loadErr := deps.storeFactory(repositoryPath).Load(plan)
		if loadErr != nil {
			err = loadErr
			break
		}
		branch, branchErr := deps.branchName(ctx, repositoryPath)
		if branchErr != nil {
			err = branchErr
			break
		}
		err = runHook(hookEvent, deps.hookInput, &output, repositoryPath, HookSnapshot{
			Plan: plan, Evidence: evidence, Branch: branch,
		}, deps.hookStoreFactory(repositoryPath))
	}
	if err != nil {
		reportRunError(stderr, err)
		return 1
	}
	if _, err := stdout.Write(output.Bytes()); err != nil {
		reportRunError(stderr, err)
		return 1
	}
	return exitCode
}

func verificationExitCode(evidence Evidence, level VerifyLevel) int {
	if targetsSatisfied(evidence, string(level), expectedTargets(evidence.Plan, level)) {
		return 0
	}
	return 1
}

func defaultDependencies() dependencies {
	const root = "."
	git := commandGitSource{root: root}
	return dependencies{
		git:              git,
		tree:             git,
		slices:           commandSliceResolver{root: root},
		openRoot:         os.OpenRoot,
		goos:             runtime.GOOS,
		root:             root,
		hookInput:        os.Stdin,
		branchName:       commandBranchName,
		hookStoreFactory: func(root string) HookStateStore { return newFileHookStateStore(root) },
		runnerFactory:    func(plan Plan) TargetRunner { return newCommandTargetRunner(plan) },
		storeFactory:     func(root string) EvidenceStore { return newFileEvidenceStore(root) },
		deepStoreFactory: func(root string) DeepIntakeStore { return newFileDeepIntakeStore(root) },
		benchmarkRunner:  commandBenchmarkProcessRunner{},
		benchmarkClock:   time.Now,
		trackedFiles: func(ctx context.Context) ([]string, error) {
			return repositoryReviewPaths(ctx, root)
		},
	}
}

func commandTrackedFiles(ctx context.Context, root string) ([]string, error) {
	output, err := (commandGitSource{root: root}).runUnchecked(ctx, "ls-files", "--full-name", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("list tracked files: truncated NUL-delimited output")
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	paths := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		name, normalizeErr := normalizeGitPath(string(field))
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate tracked path %q", name)
		}
		seen[name] = struct{}{}
		paths = append(paths, name)
	}
	slices.Sort(paths)
	return paths, nil
}

func unclassifiedTrackedPaths(policy Policy, tracked []string) ([]string, error) {
	var unknown []string
	for _, name := range tracked {
		if strings.HasPrefix(name, "build/") {
			continue
		}
		if err := validateRepositoryPath(name); err != nil {
			return nil, err
		}
		covered, err := policyCoversPath(policy, name)
		if err != nil {
			return nil, err
		}
		if !covered {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	return unknown, nil
}

func reportRunError(stderr io.Writer, err error) {
	diagnostic := strings.TrimSpace(err.Error())
	diagnostic = strings.Join(strings.FieldsFunc(diagnostic, func(character rune) bool {
		return character == '\n' || character == '\r'
	}), "; ")
	fmt.Fprintln(stderr, diagnostic)
}
