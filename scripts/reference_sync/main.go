package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath         = "docs/migration/reference/reference-repositories.yaml"
	defaultReference          = ".reference"
	defaultMemory             = ".sync-memory"
	defaultMaxSubjects        = 80
	defaultSummaryInputBytes  = 120000
	defaultSummaryTimeout     = 2 * time.Minute
	defaultSubagentInputBytes = 80000
	defaultSubagentTimeout    = 2 * time.Minute
	summaryBackendYHC         = "yhc"
	summaryBackendCodex       = "codex"
)

var frozenRepositories = map[string]string{
	"claude-code-ripe":            "the compatibility baseline is intentionally pinned",
	"grok-bot-0.18-reconstructed": "the newly added snapshot is intentionally pinned",
}

type Config struct {
	Version           int             `yaml:"version"`
	ReferenceDir      string          `yaml:"reference_dir"`
	MemoryDir         string          `yaml:"memory_dir"`
	MaxCommitSubjects int             `yaml:"max_commit_subjects"`
	ModelSummary      ModelSummary    `yaml:"model_summary"`
	SubagentSummary   SubagentSummary `yaml:"subagent_summary"`
	Repositories      []Repository    `yaml:"repositories"`
}

type ModelSummary struct {
	Backend        string `yaml:"backend"`
	Model          string `yaml:"model"`
	MaxInputBytes  int    `yaml:"max_input_bytes"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

func (s ModelSummary) normalized() ModelSummary {
	s.Backend = normalizeSummaryBackend(s.Backend)
	if s.MaxInputBytes <= 0 {
		s.MaxInputBytes = defaultSummaryInputBytes
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = int(defaultSummaryTimeout / time.Second)
	}
	return s
}

type SubagentSummary struct {
	Backend        string `yaml:"backend"`
	Model          string `yaml:"model"`
	MaxInputBytes  int    `yaml:"max_input_bytes"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

func (s SubagentSummary) normalized() SubagentSummary {
	s.Backend = normalizeSummaryBackend(s.Backend)
	if s.MaxInputBytes <= 0 {
		s.MaxInputBytes = defaultSubagentInputBytes
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = int(defaultSubagentTimeout / time.Second)
	}
	return s
}

func normalizeSummaryBackend(backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		return summaryBackendYHC
	}
	return backend
}

func validateSummaryBackend(name, backend, modelName string) error {
	backend = normalizeSummaryBackend(backend)
	switch backend {
	case summaryBackendYHC:
		return nil
	case summaryBackendCodex:
		if strings.TrimSpace(modelName) == "" {
			return fmt.Errorf("%s.model must be set when backend is %q", name, summaryBackendCodex)
		}
		return nil
	default:
		return fmt.Errorf("%s.backend must be %q or %q, got %q", name, summaryBackendYHC, summaryBackendCodex, backend)
	}
}

type Repository struct {
	ID       string `yaml:"id"`
	Path     string `yaml:"path"`
	Remote   string `yaml:"remote"`
	Upstream string `yaml:"upstream"`
	Sync     string `yaml:"sync"`
	Note     string `yaml:"note"`
}

type syncResult struct {
	Repository      string
	Path            string
	Remote          string
	Upstream        string
	Policy          string
	Status          string
	Detail          string
	Before          string
	After           string
	Branch          string
	CommitCount     int
	ShortStat       string
	DiffStat        string
	CommitSubjects  []string
	ChangeDiff      string
	DiffBytes       int
	DiffTruncated   bool
	ModelSummary    string
	SummaryStatus   string
	SummaryProvider string
	SummaryModel    string
	SummaryError    string
	ObservedAt      time.Time
}

type gitClient struct {
	dir string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reference_sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "reference repository configuration")
	referenceDir := flags.String("reference-dir", "", "override the configured reference directory")
	memoryDir := flags.String("memory-dir", "", "override the configured memory directory")
	repoFilter := flags.String("repo", "", "only process this repository id")
	dryRun := flags.Bool("dry-run", false, "inspect without fetching, merging, or writing memory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: reference_sync [flags] check|sync|baseline")
		return 2
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "resolve working directory: %v\n", err)
		return 1
	}
	cfg, err := loadConfig(filepath.Join(root, *configPath))
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if err := validateConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "validate config: %v\n", err)
		return 1
	}

	refRoot := cfg.ReferenceDir
	if *referenceDir != "" {
		refRoot = *referenceDir
	}
	if refRoot == "" {
		refRoot = defaultReference
	}
	if !filepath.IsAbs(refRoot) {
		refRoot = filepath.Join(root, refRoot)
	}

	memRel := cfg.MemoryDir
	if *memoryDir != "" {
		memRel = *memoryDir
	}
	if memRel == "" {
		memRel = defaultMemory
	}
	memRoot := memRel
	if !filepath.IsAbs(memRoot) {
		memRoot = filepath.Join(refRoot, memRoot)
	}

	repositories, err := selectRepositories(cfg.Repositories, *repoFilter)
	if err != nil {
		fmt.Fprintf(stderr, "select repositories: %v\n", err)
		return 1
	}

	switch flags.Arg(0) {
	case "check":
		return runCheck(repositories, refRoot, stdout, stderr)
	case "sync":
		return runSync(root, repositories, refRoot, memRoot, cfg.MaxCommitSubjects, cfg.ModelSummary, cfg.SubagentSummary, *dryRun, stdout, stderr)
	case "baseline":
		return runBaseline(repositories, refRoot, memRoot, *dryRun, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", flags.Arg(0))
		return 2
	}
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, fmt.Errorf("decode trailing YAML: %w", err)
		}
		return Config{}, errors.New("configuration contains multiple YAML documents")
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", cfg.Version)
	}
	if cfg.ReferenceDir != "" && !isRelativePath(cfg.ReferenceDir) {
		return fmt.Errorf("reference_dir must be a relative path")
	}
	if cfg.MemoryDir != "" && !isRelativePath(cfg.MemoryDir) && !filepath.IsAbs(cfg.MemoryDir) {
		return fmt.Errorf("memory_dir must be a relative path or an absolute operator path")
	}
	if cfg.MaxCommitSubjects < 0 {
		return errors.New("max_commit_subjects must not be negative")
	}
	if cfg.ModelSummary.MaxInputBytes < 0 {
		return errors.New("model_summary.max_input_bytes must not be negative")
	}
	if cfg.ModelSummary.TimeoutSeconds < 0 {
		return errors.New("model_summary.timeout_seconds must not be negative")
	}
	if err := validateSummaryBackend("model_summary", cfg.ModelSummary.Backend, cfg.ModelSummary.Model); err != nil {
		return err
	}
	if cfg.SubagentSummary.MaxInputBytes < 0 {
		return errors.New("subagent_summary.max_input_bytes must not be negative")
	}
	if cfg.SubagentSummary.TimeoutSeconds < 0 {
		return errors.New("subagent_summary.timeout_seconds must not be negative")
	}
	if err := validateSummaryBackend("subagent_summary", cfg.SubagentSummary.Backend, cfg.SubagentSummary.Model); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(cfg.Repositories))
	seenPaths := make(map[string]struct{}, len(cfg.Repositories))
	for _, repo := range cfg.Repositories {
		if repo.ID == "" {
			return errors.New("repository id must not be empty")
		}
		if _, ok := seen[repo.ID]; ok {
			return fmt.Errorf("duplicate repository id %q", repo.ID)
		}
		seen[repo.ID] = struct{}{}
		if !isRelativePath(repo.Path) {
			return fmt.Errorf("repository %q path must be a relative path", repo.ID)
		}
		cleanPath := filepath.Clean(repo.Path)
		if _, ok := seenPaths[cleanPath]; ok {
			return fmt.Errorf("duplicate repository path %q", cleanPath)
		}
		seenPaths[cleanPath] = struct{}{}
		if strings.TrimSpace(repo.Remote) == "" {
			return fmt.Errorf("repository %q remote must not be empty", repo.ID)
		}
		if !strings.HasPrefix(repo.Upstream, "origin/") || repo.Upstream == "origin/" {
			return fmt.Errorf("repository %q upstream must be an origin branch", repo.ID)
		}
		if repo.Sync != "enabled" && repo.Sync != "frozen" {
			return fmt.Errorf("repository %q sync must be enabled or frozen", repo.ID)
		}
		if reason, frozen := frozenRepositories[repo.ID]; frozen && repo.Sync != "frozen" {
			return fmt.Errorf("repository %q must remain frozen: %s", repo.ID, reason)
		}
	}
	return nil
}

func isRelativePath(value string) bool {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func selectRepositories(repositories []Repository, filter string) ([]Repository, error) {
	if filter == "" {
		return repositories, nil
	}
	for _, repo := range repositories {
		if repo.ID == filter {
			return []Repository{repo}, nil
		}
	}
	return nil, fmt.Errorf("repository %q is not in the configuration", filter)
}

func runCheck(repositories []Repository, referenceRoot string, stdout, stderr io.Writer) int {
	failed := false
	for _, repo := range repositories {
		result := inspectRepository(repo, referenceRoot)
		printResult(stdout, result)
		if result.Status == "error" {
			failed = true
			fmt.Fprintf(stderr, "%s: %s\n", repo.ID, result.Detail)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func runSync(projectDir string, repositories []Repository, referenceRoot, memoryRoot string, maxSubjects int, summaryConfig ModelSummary, subagentConfig SubagentSummary, dryRun bool, stdout, stderr io.Writer) int {
	if maxSubjects == 0 {
		maxSubjects = defaultMaxSubjects
	}
	summaryConfig = summaryConfig.normalized()
	var release func()
	if !dryRun {
		var err error
		release, err = acquireLock(memoryRoot)
		if err != nil {
			fmt.Fprintf(stderr, "acquire sync lock: %v\n", err)
			return 1
		}
		defer release()
	}

	now := time.Now().UTC()
	results := make([]syncResult, 0, len(repositories))
	for _, repo := range repositories {
		result := syncRepository(repo, referenceRoot, maxSubjects, summaryConfig.MaxInputBytes, dryRun)
		result.ObservedAt = now
		results = append(results, result)
	}

	if dryRun {
		for _, result := range results {
			printResult(stdout, result)
		}
		return 0
	}
	summaryFactory := newConfiguredModelSummaryGenerator
	if summaryConfig.Backend == summaryBackendCodex {
		summaryFactory = func(ctx context.Context, projectDir string) (modelSummaryGenerator, error) {
			return newCodexModelSummaryGenerator(ctx, projectDir, summaryConfig.Model)
		}
	} else if strings.TrimSpace(summaryConfig.Model) != "" {
		summaryFactory = func(ctx context.Context, projectDir string) (modelSummaryGenerator, error) {
			return newConfiguredModelSummaryGeneratorForSelector(ctx, projectDir, summaryConfig.Model)
		}
	}
	if err := generateModelSummaries(context.Background(), projectDir, summaryConfig, results, summaryFactory); err != nil {
		fmt.Fprintf(stderr, "model summary: %v\n", err)
	}
	finalizePreparedUpdates(repositories, referenceRoot, results)
	for _, result := range results {
		printResult(stdout, result)
	}
	updateFiles, err := writeSyncUpdates(memoryRoot, now, results)
	if err != nil {
		fmt.Fprintf(stderr, "write sync memory: %v\n", err)
		return 1
	}
	subagentConfig = subagentConfig.normalized()
	subagentFactory := newConfiguredSubagentSummaryGenerator
	if subagentConfig.Backend == summaryBackendCodex {
		subagentFactory = func(ctx context.Context, projectDir, _ string) (modelSummaryGenerator, error) {
			return newCodexModelSummaryGenerator(ctx, projectDir, subagentConfig.Model)
		}
	}
	postSummary, postSummaryErr := summarizeUpdateFiles(
		context.Background(),
		projectDir,
		memoryRoot,
		now,
		updateFiles,
		subagentConfig,
		subagentFactory,
	)
	if postSummaryErr != nil {
		fmt.Fprintf(stderr, "post-update subagent summary: %v\n", postSummaryErr)
	}
	if err := writeRunMemory(memoryRoot, now, results, postSummary); err != nil {
		fmt.Fprintf(stderr, "write sync run memory: %v\n", err)
		return 1
	}
	for _, result := range results {
		if result.Status == "error" || isBlockingStatus(result.Status) {
			fmt.Fprintf(stderr, "%s: %s\n", result.Repository, result.Detail)
		}
	}
	printSubagentSummary(stdout, postSummary)
	if postSummaryErr != nil {
		return 1
	}
	for _, result := range results {
		if result.Status == "error" || isBlockingStatus(result.Status) {
			return 1
		}
	}
	return 0
}

func runBaseline(repositories []Repository, referenceRoot, memoryRoot string, dryRun bool, stdout, stderr io.Writer) int {
	if dryRun {
		fmt.Fprintln(stderr, "baseline does not support --dry-run")
		return 2
	}
	release, err := acquireLock(memoryRoot)
	if err != nil {
		fmt.Fprintf(stderr, "acquire sync lock: %v\n", err)
		return 1
	}
	defer release()

	now := time.Now().UTC()
	results := make([]syncResult, 0, len(repositories))
	for _, repo := range repositories {
		result := baselineRepository(repo, referenceRoot)
		result.ObservedAt = now
		results = append(results, result)
		printResult(stdout, result)
	}
	if err := writeBaselineMemories(memoryRoot, now, results); err != nil {
		fmt.Fprintf(stderr, "write baseline memory: %v\n", err)
		return 1
	}
	for _, result := range results {
		if result.Status == "error" {
			return 1
		}
	}
	return 0
}

func inspectRepository(repo Repository, referenceRoot string) syncResult {
	result := syncResult{
		Repository: repo.ID,
		Path:       filepath.Join(referenceRoot, repo.Path),
		Remote:     repo.Remote,
		Upstream:   repo.Upstream,
		Policy:     repo.Sync,
	}
	if _, err := os.Stat(result.Path); err != nil {
		result.Status = "error"
		result.Detail = fmt.Sprintf("checkout is unavailable: %v", err)
		return result
	}
	if repo.Sync == "frozen" {
		result.Status = "frozen"
		result.Detail = repo.Note
		return result
	}
	client := gitClient{dir: result.Path}
	if _, err := client.run("rev-parse", "--is-inside-work-tree"); err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	if err := validateRemote(client, repo.Remote); err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.Branch, _ = client.run("rev-parse", "--abbrev-ref", "HEAD")
	result.Before, _ = client.run("rev-parse", "HEAD")
	result.Status = "ready"
	return result
}

func syncRepository(repo Repository, referenceRoot string, maxSubjects, maxDiffBytes int, dryRun bool) syncResult {
	result := inspectRepository(repo, referenceRoot)
	if result.Status == "error" || result.Status == "frozen" {
		return result
	}
	client := gitClient{dir: result.Path}
	status, err := client.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	if strings.TrimSpace(status) != "" {
		result.Status = "blocked_dirty"
		result.Detail = summarizeLines(status, 12)
		return result
	}
	if dryRun {
		result.Status = "not_fetched"
		result.Detail = "dry-run skipped fetch and merge"
		return result
	}
	if _, err := client.run("fetch", "--prune", "origin"); err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	if _, err := client.run("rev-parse", "--verify", repo.Upstream); err != nil {
		result.Status = "error"
		result.Detail = fmt.Sprintf("upstream %s is unavailable: %v", repo.Upstream, err)
		return result
	}
	counts, err := client.run("rev-list", "--left-right", "--count", "HEAD..."+repo.Upstream)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	ahead, behind, err := parseAheadBehind(counts)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	if ahead > 0 {
		result.Status = "blocked_diverged"
		result.Detail = fmt.Sprintf("local branch is ahead by %d commit(s); fast-forward-only sync refused", ahead)
		return result
	}
	if behind == 0 {
		result.Status = "unchanged"
		result.After = result.Before
		return result
	}
	result.After, err = client.run("rev-parse", "--verify", repo.Upstream)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.CommitCount, err = parseCount(client.run("rev-list", "--count", result.Before+".."+result.After))
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.ShortStat, err = client.run("diff", "--no-renames", "--shortstat", result.Before, result.After)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.DiffStat, err = client.run("diff", "--no-renames", "--stat", result.Before, result.After)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.CommitSubjects, err = commitSubjects(client, result.Before, result.After, maxSubjects)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	diff, err := client.run("diff", "--no-ext-diff", "--no-renames", "--no-color", "--unified=3", result.Before, result.After)
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return result
	}
	result.DiffBytes = len(diff)
	result.ChangeDiff, result.DiffTruncated = truncateText(diff, maxDiffBytes)
	result.Status = "pending_summary"
	return result
}

func mergePreparedRepository(repo Repository, referenceRoot string, result *syncResult) {
	if result == nil || result.Status != "pending_summary" {
		return
	}
	client := gitClient{dir: filepath.Join(referenceRoot, repo.Path)}
	current, err := client.run("rev-parse", "HEAD")
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return
	}
	if current != result.Before {
		result.Status = "blocked_changed"
		result.Detail = fmt.Sprintf("HEAD changed during model analysis: expected %s, found %s", result.Before, current)
		return
	}
	status, err := client.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return
	}
	if strings.TrimSpace(status) != "" {
		result.Status = "blocked_dirty"
		result.Detail = summarizeLines(status, 12)
		return
	}
	if _, err := client.run("merge", "--ff-only", result.After); err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return
	}
	current, err = client.run("rev-parse", "HEAD")
	if err != nil {
		result.Status = "error"
		result.Detail = err.Error()
		return
	}
	if current != result.After {
		result.Status = "error"
		result.Detail = fmt.Sprintf("fast-forward landed at unexpected commit %s; expected %s", current, result.After)
		return
	}
	result.Status = "updated"
}

func finalizePreparedUpdates(repositories []Repository, referenceRoot string, results []syncResult) {
	for index := range results {
		if results[index].Status != "pending_summary" {
			continue
		}
		if results[index].SummaryStatus != "generated" {
			results[index].Status = "blocked_summary"
			results[index].Detail = "model summary unavailable: " + emptyAsNone(results[index].SummaryError)
			continue
		}
		if index >= len(repositories) {
			results[index].Status = "error"
			results[index].Detail = "repository/result alignment is invalid"
			continue
		}
		mergePreparedRepository(repositories[index], referenceRoot, &results[index])
	}
}

func baselineRepository(repo Repository, referenceRoot string) syncResult {
	result := inspectRepository(repo, referenceRoot)
	if result.Status == "error" {
		return result
	}
	client := gitClient{dir: result.Path}
	result.Status = "baseline"
	result.Before, _ = client.run("rev-parse", "HEAD")
	result.After = result.Before
	result.Branch, _ = client.run("rev-parse", "--abbrev-ref", "HEAD")
	if status, err := client.run("status", "--porcelain", "--untracked-files=all"); err == nil && strings.TrimSpace(status) != "" {
		result.Detail = "baseline captured with a dirty worktree: " + summarizeLines(status, 12)
	}
	return result
}

func (g gitClient) run(args ...string) (string, error) {
	cmdArgs := append([]string{"--no-pager"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", cmdArgs...)
	cmd.Dir = g.dir
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func validateRemote(client gitClient, configured string) error {
	actual, err := client.run("remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if normalizeRemote(actual) != normalizeRemote(configured) {
		return fmt.Errorf("origin mismatch: configured %q, actual %q", configured, actual)
	}
	return nil
}

func normalizeRemote(remote string) string {
	value := strings.TrimSpace(remote)
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "git://")
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "ssh://")
	value = strings.TrimPrefix(value, "git+ssh://")
	if strings.HasPrefix(value, "git@") {
		value = strings.TrimPrefix(value, "git@")
		value = strings.Replace(value, ":", "/", 1)
	}
	return strings.ToLower(strings.TrimSuffix(value, "/"))
}

func parseAheadBehind(value string) (int, int, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("parse ahead/behind %q: expected two counts", value)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse ahead count %q: %w", fields[0], err)
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse behind count %q: %w", fields[1], err)
	}
	return ahead, behind, nil
}

func parseCount(value string, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	count, parseErr := strconv.Atoi(strings.TrimSpace(value))
	if parseErr != nil {
		return 0, fmt.Errorf("parse commit count %q: %w", value, parseErr)
	}
	return count, nil
}

func commitSubjects(client gitClient, before, after string, maxSubjects int) ([]string, error) {
	if maxSubjects <= 0 {
		return nil, nil
	}
	value, err := client.run("log", "--reverse", fmt.Sprintf("--max-count=%d", maxSubjects), "--date=short", "--format=%h%x09%ad%x09%s", before+".."+after)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return strings.Split(value, "\n"), nil
}

func summarizeLines(value string, max int) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[:max], "; ") + fmt.Sprintf("; ... (%d more)", len(lines)-max)
}

func truncateText(value string, maxBytes int) (string, bool) {
	return truncateTextWithMarker(value, maxBytes, "\n\n[content truncated before model analysis]\n")
}

func truncateTextWithMarker(value string, maxBytes int, marker string) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	if maxBytes <= len(marker) {
		return marker[:maxBytes], true
	}
	limit := maxBytes - len(marker)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + marker, true
}

func acquireLock(memoryRoot string) (func(), error) {
	if err := os.MkdirAll(memoryRoot, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(memoryRoot, ".lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("another reference sync appears to be running (%s exists)", lockPath)
		}
		return nil, err
	}
	return func() { _ = os.Remove(lockPath) }, nil
}

func writeSyncUpdates(memoryRoot string, now time.Time, results []syncResult) ([]string, error) {
	if err := ensureMemoryReadme(memoryRoot); err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, result := range results {
		if result.Status != "updated" {
			continue
		}
		name := filepath.Join("updates", memoryFilename(now, result.Repository))
		path := filepath.Join(memoryRoot, name)
		if err := writeMemoryFile(path, renderUpdateMemory(result)); err != nil {
			return nil, fmt.Errorf("%s: %w", result.Repository, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func writeRunMemory(memoryRoot string, now time.Time, results []syncResult, postSummary postUpdateSummaryResult) error {
	return writeMemoryFile(filepath.Join(memoryRoot, "runs", memoryFilename(now, "run")), renderRunMemory(now, results, &postSummary))
}

func writeBaselineMemories(memoryRoot string, now time.Time, results []syncResult) error {
	if err := ensureMemoryReadme(memoryRoot); err != nil {
		return err
	}
	for _, result := range results {
		name := filepath.Join("baselines", memoryFilename(now, result.Repository))
		if err := writeMemoryFile(filepath.Join(memoryRoot, name), renderBaselineMemory(result)); err != nil {
			return fmt.Errorf("%s: %w", result.Repository, err)
		}
	}
	return writeMemoryFile(filepath.Join(memoryRoot, "runs", memoryFilename(now, "baseline")), renderRunMemory(now, results, nil))
}

func ensureMemoryReadme(memoryRoot string) error {
	readme := filepath.Join(memoryRoot, "README.md")
	content := referenceMemoryReadme()
	if existing, err := os.ReadFile(readme); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeMemoryFile(readme, content)
}

func referenceMemoryReadme() []byte {
	return []byte("# Reference sync memory\n\n" +
		"This ignored directory is the local memory for reference checkout synchronization. " +
		"`runs/` records every scheduled pass; `updates/` records one model-generated analysis " +
		"per fast-forward update; `summaries/` records a read-only subagent synthesis of the " +
		"new updates from each pass; `baselines/` records initial or manually captured snapshots.\n\n" +
		"The sync prepares the exact commit range, asks the configured YHC model to analyze " +
		"the bounded source diff, and only then performs the fast-forward merge. A missing or " +
		"failed model summary blocks that merge and is recorded as `blocked_summary`; commit " +
		"metadata and diff stats are evidence, not a substitute for model analysis.\n\n" +
		"After updates are written, a separate read-only subagent reads only the new `updates/` " +
		"records and writes a consolidated summary under `summaries/`. A failed post-update " +
		"summary is recorded and makes the sync run fail, but does not roll back an already " +
		"completed fast-forward.\n\n" +
		"The memory is operational evidence, not a product backlog. Frozen repositories " +
		"are never fetched or merged by the scheduled sync.\n")
}

func writeMemoryFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func memoryFilename(now time.Time, subject string) string {
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	return stamp + "-" + subject + ".md"
}

func renderUpdateMemory(result syncResult) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reference update: %s\n\n", result.Repository)
	fmt.Fprintf(&b, "- Observed: %s\n", result.ObservedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Policy: `%s`\n", result.Policy)
	fmt.Fprintf(&b, "- Remote: `%s`\n", result.Remote)
	fmt.Fprintf(&b, "- Upstream: `%s`\n", result.Upstream)
	fmt.Fprintf(&b, "- Range: `%s..%s`\n", result.Before, result.After)
	fmt.Fprintf(&b, "- Commits: %d\n", result.CommitCount)
	fmt.Fprintf(&b, "- Short stat: %s\n\n", emptyAsNone(result.ShortStat))
	fmt.Fprintf(&b, "- Model summary status: `%s`\n", emptyAsNone(result.SummaryStatus))
	if result.SummaryProvider != "" || result.SummaryModel != "" {
		fmt.Fprintf(&b, "- Summary model: `%s:%s`\n", emptyAsNone(result.SummaryProvider), emptyAsNone(result.SummaryModel))
	}
	fmt.Fprintf(&b, "- Diff supplied to model: %d/%d bytes", len(result.ChangeDiff), result.DiffBytes)
	if result.DiffTruncated {
		b.WriteString(" (truncated)")
	}
	b.WriteString("\n\n## Model analysis\n\n")
	if result.SummaryStatus == "generated" && strings.TrimSpace(result.ModelSummary) != "" {
		b.WriteString(strings.TrimSpace(result.ModelSummary))
		b.WriteString("\n")
	} else {
		b.WriteString("_Model analysis was not generated; this record must not be treated as a semantic change summary._\n")
		if result.SummaryError != "" {
			fmt.Fprintf(&b, "\nModel error: `%s`\n", result.SummaryError)
		}
	}
	b.WriteString("\n## Commit metadata (evidence only)\n\n")
	if len(result.CommitSubjects) == 0 {
		b.WriteString("_No commit subjects captured._\n")
	} else {
		for _, subject := range result.CommitSubjects {
			fmt.Fprintf(&b, "- `%s`\n", subject)
		}
	}
	b.WriteString("\n## Diff stat\n\n```text\n")
	b.WriteString(emptyAsNone(result.DiffStat))
	b.WriteString("\n```\n")
	return []byte(b.String())
}

func renderBaselineMemory(result syncResult) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reference baseline: %s\n\n", result.Repository)
	fmt.Fprintf(&b, "- Observed: %s\n", result.ObservedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Policy: `%s`\n", result.Policy)
	fmt.Fprintf(&b, "- Remote: `%s`\n", result.Remote)
	fmt.Fprintf(&b, "- Branch: `%s`\n", emptyAsNone(result.Branch))
	fmt.Fprintf(&b, "- Commit: `%s`\n", emptyAsNone(result.Before))
	fmt.Fprintf(&b, "- State: %s\n", emptyAsNone(result.Detail))
	b.WriteString("\nThis is an initial snapshot record. It is not an upstream change summary.\n")
	return []byte(b.String())
}

func renderRunMemory(now time.Time, results []syncResult, postSummary *postUpdateSummaryResult) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reference sync run: %s\n\n", now.UTC().Format(time.RFC3339))
	b.WriteString("| Repository | Policy | Status | Range / detail |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, result := range results {
		detail := result.Detail
		if result.Status == "updated" {
			detail = fmt.Sprintf("%s..%s (%d commit(s), model_summary=%s)", result.Before, result.After, result.CommitCount, emptyAsNone(result.SummaryStatus))
		}
		if detail == "" {
			detail = "-"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n", result.Repository, result.Policy, result.Status, strings.ReplaceAll(detail, "|", "\\|"))
	}
	if postSummary != nil {
		b.WriteString("\n## Post-update subagent summary\n\n")
		fmt.Fprintf(&b, "- Status: `%s`\n", emptyAsNone(postSummary.Status))
		if postSummary.Provider != "" || postSummary.Model != "" {
			fmt.Fprintf(&b, "- Model: `%s:%s`\n", emptyAsNone(postSummary.Provider), emptyAsNone(postSummary.Model))
		}
		fmt.Fprintf(&b, "- Update files: %d\n", postSummary.InputFiles)
		fmt.Fprintf(&b, "- Input supplied: %d bytes", postSummary.InputBytes)
		if postSummary.InputTruncated {
			b.WriteString(" (truncated)")
		}
		b.WriteByte('\n')
		if postSummary.Path != "" {
			fmt.Fprintf(&b, "- Summary file: `%s`\n", postSummary.Path)
		}
		if postSummary.Error != "" {
			fmt.Fprintf(&b, "- Error: `%s`\n", postSummary.Error)
		}
	}
	b.WriteString("\nFrozen repositories are intentionally not fetched or merged. `blocked_dirty`, `blocked_diverged`, `blocked_changed`, and `blocked_summary` are safe no-op outcomes that require operator review or a later retry before the checkout can update.\n")
	return []byte(b.String())
}

func emptyAsNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return strings.TrimSpace(value)
}

func isBlockingStatus(status string) bool {
	return strings.HasPrefix(status, "blocked_")
}

func printResult(w io.Writer, result syncResult) {
	detail := result.Detail
	if result.Status == "updated" {
		detail = fmt.Sprintf("%s..%s (%d commit(s), model_summary=%s)", result.Before, result.After, result.CommitCount, emptyAsNone(result.SummaryStatus))
	}
	if detail == "" {
		detail = "-"
	}
	fmt.Fprintf(w, "%s: %s - %s\n", result.Repository, result.Status, detail)
}
