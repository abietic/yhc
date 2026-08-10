// Command worktree_audit reports Git worktree state without changing Git state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const schemaVersion = 1

type commandRunner interface {
	run(context.Context, ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, argv ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", argv...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.Output()
	return string(output), err
}

type report struct {
	SchemaVersion int      `json:"schema_version"`
	Base          string   `json:"base"`
	Worktrees     []record `json:"worktrees"`
}

type record struct {
	Path           string   `json:"path"`
	Head           string   `json:"head"`
	Branch         string   `json:"branch"`
	Detached       bool     `json:"detached"`
	Locked         bool     `json:"locked"`
	Prunable       bool     `json:"prunable"`
	Status         string   `json:"status"`
	UntrackedCount int      `json:"untracked_count"`
	UpstreamState  string   `json:"upstream_state"`
	Ahead          int      `json:"ahead"`
	Behind         int      `json:"behind"`
	BaseReachable  bool     `json:"base_reachable"`
	ReviewHints    []string `json:"review_hints"`
	Diagnostics    []string `json:"diagnostics"`
}

type porcelainRecord struct {
	path        string
	head        string
	branch      string
	detached    bool
	locked      bool
	prunable    bool
	diagnostics []string
}

func main() {
	base := flag.String("base", "origin/master", "read-only Git base ref")
	format := flag.String("format", "text", "output format: text or json")
	flag.Parse()

	if err := run(context.Background(), os.Stdout, execRunner{}, *base, *format); err != nil {
		fmt.Fprintln(os.Stderr, "worktree-audit:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer, runner commandRunner, base, format string) error {
	report, auditErr := buildReport(ctx, runner, base)
	if err := writeReport(output, report, format); err != nil {
		return err
	}
	return auditErr
}

func buildReport(ctx context.Context, runner commandRunner, base string) (report, error) {
	report := report{SchemaVersion: schemaVersion, Worktrees: []record{}}
	if strings.TrimSpace(base) == "" {
		return report, errors.New("base is required")
	}
	porcelain, err := runGit(ctx, runner, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return report, fmt.Errorf("list worktrees: %w", err)
	}
	parsed, err := parsePorcelain(porcelain)
	if err != nil {
		return report, err
	}
	baseCommit, err := runGit(ctx, runner, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return report, fmt.Errorf("resolve base %q: %w", base, err)
	}
	report.Base = strings.TrimSpace(baseCommit)
	valid := true
	for _, item := range parsed {
		record := classifyRecord(ctx, runner, item, report.Base)
		if record.Status == "unreadable" {
			valid = false
		}
		report.Worktrees = append(report.Worktrees, record)
	}
	slices.SortFunc(report.Worktrees, func(left, right record) int { return strings.Compare(left.Path, right.Path) })
	if !valid {
		return report, errors.New("one or more worktrees are unreadable")
	}
	return report, nil
}

func classifyRecord(ctx context.Context, runner commandRunner, item porcelainRecord, base string) record {
	result := record{Path: item.path, Head: item.head, Branch: shortBranch(item.branch), Detached: item.detached, Locked: item.locked, Prunable: item.prunable, Status: "clean", UpstreamState: "detached", ReviewHints: []string{}, Diagnostics: append([]string{}, item.diagnostics...)}
	if item.locked {
		result.Status = "locked"
		result.ReviewHints = []string{"preserve_locked"}
		return result
	}
	if item.prunable {
		result.Status = "prunable"
		result.ReviewHints = []string{"inspect_prunable"}
		return result
	}
	status, err := runGit(ctx, runner, "-C", item.path, "status", "--porcelain=v1", "-z")
	if err != nil {
		return unreadable(result)
	}
	dirty, untracked, statusErr := parseStatus(status)
	if statusErr != nil {
		return unreadable(result)
	}
	if dirty {
		result.Status, result.UntrackedCount = "dirty", untracked
	}
	head, err := runGit(ctx, runner, "-C", item.path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return unreadable(result)
	}
	result.Head = strings.TrimSpace(head)
	if !item.detached {
		branch, branchErr := runGit(ctx, runner, "-C", item.path, "symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr != nil {
			return unreadable(result)
		}
		result.Branch = strings.TrimSpace(branch)
		upstream, upstreamErr := runGit(ctx, runner, "-C", item.path, "for-each-ref", "--format=%(upstream:short)%00%(upstream:track)", "refs/heads/"+result.Branch)
		if upstreamErr != nil {
			return unreadable(result)
		}
		upstream, track, upstreamParseErr := parseUpstream(upstream)
		if upstreamParseErr != nil {
			return unreadable(result)
		}
		if upstream == "" && track == "" {
			result.UpstreamState = "missing"
		} else if upstream != "" && track == "[gone]" {
			result.UpstreamState = "gone"
		} else if upstream == "" {
			return unreadable(result)
		} else {
			result.UpstreamState = "tracked"
			counts, countsErr := runGit(ctx, runner, "-C", item.path, "rev-list", "--left-right", "--count", upstream+"...HEAD")
			if countsErr != nil {
				return unreadable(result)
			}
			behind, ahead, parseErr := parseAheadBehind(counts)
			if parseErr != nil {
				return unreadable(result)
			}
			result.Ahead, result.Behind = ahead, behind
		}
	}
	_, reachableErr := runGit(ctx, runner, "-C", item.path, "merge-base", "--is-ancestor", result.Head, base)
	if reachableErr == nil {
		result.BaseReachable = true
	} else if exitCode(reachableErr) == 1 {
		result.BaseReachable = false
	} else {
		return unreadable(result)
	}
	if result.Status == "dirty" {
		result.ReviewHints = []string{"preserve_dirty"}
	} else if result.Status == "clean" && result.BaseReachable && result.UpstreamState == "tracked" && result.Ahead == 0 && result.Behind == 0 && len(result.Diagnostics) == 0 {
		result.ReviewHints = []string{"review_clean_base_reachable"}
	} else {
		result.ReviewHints = []string{"inspect"}
	}
	return result
}

func unreadable(result record) record {
	result.Status, result.ReviewHints = "unreadable", []string{"preserve_unreadable"}
	result.Diagnostics = append(result.Diagnostics, "git metadata unreadable")
	return result
}

func parsePorcelain(output string) ([]porcelainRecord, error) {
	if output == "" || !strings.HasSuffix(output, "\x00") {
		return nil, errors.New("porcelain must be non-empty and NUL terminated")
	}
	entries := strings.Split(output, "\x00")
	records := make([]porcelainRecord, 0, len(entries)/4)
	var current *porcelainRecord
	for index, line := range entries {
		if line == "" {
			if current == nil {
				if index == len(entries)-1 {
					continue
				}
				return nil, errors.New("porcelain contains an empty record")
			}
			if current.head == "" {
				return nil, fmt.Errorf("worktree %q lacks HEAD", current.path)
			}
			if current.detached == (current.branch != "") {
				return nil, fmt.Errorf("worktree %q must have exactly one of branch or detached", current.path)
			}
			records = append(records, *current)
			current = nil
			continue
		}
		key, value, hasValue := strings.Cut(line, " ")
		if key == "worktree" {
			if current != nil {
				return nil, errors.New("porcelain record lacks NUL separator")
			}
			if !hasValue || value == "" {
				return nil, errors.New("porcelain record lacks worktree path")
			}
			current = &porcelainRecord{path: value}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("porcelain key %q appears before worktree", key)
		}
		switch key {
		case "HEAD":
			if !hasValue || current.head != "" {
				return nil, errors.New("duplicate HEAD or HEAD without value")
			}
			current.head = value
		case "branch":
			if !hasValue || current.branch != "" {
				return nil, errors.New("duplicate branch or branch without value")
			}
			current.branch = value
		case "detached":
			if hasValue || current.detached {
				return nil, errors.New("duplicate detached or detached with value")
			}
			current.detached = true
		case "locked":
			if current.locked {
				return nil, errors.New("duplicate locked")
			}
			current.locked = true
		case "prunable":
			if current.prunable {
				return nil, errors.New("duplicate prunable")
			}
			current.prunable = true
		default:
			current.diagnostics = append(current.diagnostics, "unknown porcelain key: "+key)
		}
	}
	if current != nil {
		return nil, errors.New("porcelain record lacks NUL terminator")
	}
	if len(records) == 0 {
		return nil, errors.New("porcelain contains no records")
	}
	return records, nil
}

func runGit(ctx context.Context, runner commandRunner, argv ...string) (string, error) {
	if !allowlisted(argv) {
		return "", fmt.Errorf("git command is not allowlisted: %q", argv)
	}
	return runner.run(ctx, argv...)
}

func allowlisted(argv []string) bool {
	if slices.Equal(argv, []string{"worktree", "list", "--porcelain", "-z"}) {
		return true
	}
	if len(argv) == 4 && argv[0] == "rev-parse" && argv[1] == "--verify" && argv[2] == "--end-of-options" && strings.HasSuffix(argv[3], "^{commit}") {
		return true
	}
	if len(argv) < 4 || argv[0] != "-C" || argv[1] == "" {
		return false
	}
	command := argv[2:]
	return slices.Equal(command, []string{"status", "--porcelain=v1", "-z"}) ||
		slices.Equal(command, []string{"rev-parse", "--verify", "HEAD"}) ||
		slices.Equal(command, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}) ||
		(len(command) == 3 && command[0] == "for-each-ref" && command[1] == "--format=%(upstream:short)%00%(upstream:track)" && strings.HasPrefix(command[2], "refs/heads/")) ||
		(len(command) == 4 && command[0] == "rev-list" && command[1] == "--left-right" && command[2] == "--count" && strings.HasSuffix(command[3], "...HEAD")) ||
		(len(command) == 4 && command[0] == "merge-base" && command[1] == "--is-ancestor")
}

func writeReport(output io.Writer, report report, format string) error {
	switch format {
	case "json":
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	case "text":
		if _, err := fmt.Fprintf(output, "base=%s\n", report.Base); err != nil {
			return err
		}
		for _, record := range report.Worktrees {
			if _, err := fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%d\t%d\t%t\t%s\t%s\n", record.Path, record.Status, record.Branch, record.UpstreamState, record.Ahead, record.Behind, record.BaseReachable, textValue(record.ReviewHints), textValue(record.Diagnostics)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func textValue(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func shortBranch(branch string) string { return strings.TrimPrefix(branch, "refs/heads/") }

func parseAheadBehind(value string) (int, int, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 {
		return 0, 0, errors.New("ahead/behind must contain two integers")
	}
	behind, behindErr := strconv.Atoi(fields[0])
	ahead, aheadErr := strconv.Atoi(fields[1])
	if behindErr != nil || aheadErr != nil || behind < 0 || ahead < 0 {
		return 0, 0, errors.New("ahead/behind must be non-negative integers")
	}
	return behind, ahead, nil
}

func parseUpstream(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSuffix(value, "\n"), "\x00")
	if len(parts) != 2 {
		return "", "", errors.New("upstream output is malformed")
	}
	if strings.Contains(parts[0], "\n") || strings.Contains(parts[1], "\n") || !validTrack(parts[1]) {
		return "", "", errors.New("upstream output contains newline")
	}
	return parts[0], parts[1], nil
}

var trackPattern = regexp.MustCompile(`^\[(ahead [1-9][0-9]*|behind [1-9][0-9]*|ahead [1-9][0-9]*, behind [1-9][0-9]*|gone)\]$`)

func validTrack(track string) bool { return track == "" || trackPattern.MatchString(track) }

func parseStatus(output string) (bool, int, error) {
	if output == "" {
		return false, 0, nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return false, 0, errors.New("status is not NUL terminated")
	}
	dirty, untracked, skip := false, 0, false
	for _, entry := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
		if skip {
			skip = false
			continue
		}
		if len(entry) < 3 || entry[2] != ' ' || !statusCode(entry[0]) || !statusCode(entry[1]) {
			return false, 0, errors.New("status record is malformed")
		}
		dirty = true
		if entry[:3] == "?? " {
			untracked++
		}
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			skip = true
		}
	}
	if skip {
		return false, 0, errors.New("status rename lacks second path")
	}
	return dirty, untracked, nil
}

func statusCode(value byte) bool {
	return value == ' ' || value == '?' || strings.ContainsRune("MADRCUT", rune(value))
}

type exitCoder interface{ ExitCode() int }

func exitCode(err error) int {
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}
