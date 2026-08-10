package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	responses map[string]commandResult
	calls     [][]string
}
type commandResult struct {
	stdout string
	err    error
}

type exitStatusError int

func (exitStatusError) Error() string        { return "exit status" }
func (status exitStatusError) ExitCode() int { return int(status) }

func (f *fakeRunner) run(_ context.Context, a ...string) (string, error) {
	f.calls = append(f.calls, a)
	r, ok := f.responses[strings.Join(a, "\x00")]
	if !ok {
		return "", errors.New("unexpected")
	}
	return r.stdout, r.err
}
func k(a ...string) string       { return strings.Join(a, "\x00") }
func p(records ...string) string { return strings.Join(records, "\x00\x00") + "\x00\x00" }

func TestSchemaAndReadOnlyArgv(t *testing.T) {
	r := &fakeRunner{responses: map[string]commandResult{
		k("worktree", "list", "--porcelain", "-z"):                                                                    {stdout: p("worktree /repo/a space\x00HEAD head\x00branch refs/heads/main")},
		k("rev-parse", "--verify", "--end-of-options", "base^{commit}"):                                               {stdout: "base\n"},
		k("-C", "/repo/a space", "status", "--porcelain=v1", "-z"):                                                    {stdout: "?? file with\nnewline\x00 M tracked\x00"},
		k("-C", "/repo/a space", "rev-parse", "--verify", "HEAD"):                                                     {stdout: "head\n"},
		k("-C", "/repo/a space", "symbolic-ref", "--quiet", "--short", "HEAD"):                                        {stdout: "main\n"},
		k("-C", "/repo/a space", "for-each-ref", "--format=%(upstream:short)%00%(upstream:track)", "refs/heads/main"): {stdout: "origin/main\x00\n"},
		k("-C", "/repo/a space", "rev-list", "--left-right", "--count", "origin/main...HEAD"):                         {stdout: "2\t3\n"},
		k("-C", "/repo/a space", "merge-base", "--is-ancestor", "head", "base"):                                       {err: exitStatusError(1)},
	}}
	var out bytes.Buffer
	if err := run(context.Background(), &out, r, "base", "json"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int    `json:"schema_version"`
		Base          string `json:"base"`
		Worktrees     []struct {
			Path          string   `json:"path"`
			Status        string   `json:"status"`
			Untracked     int      `json:"untracked_count"`
			Ahead         int      `json:"ahead"`
			Behind        int      `json:"behind"`
			BaseReachable bool     `json:"base_reachable"`
			ReviewHints   []string `json:"review_hints"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.Base != "base" || len(got.Worktrees) != 1 || got.Worktrees[0].Untracked != 1 || got.Worktrees[0].Ahead != 3 || got.Worktrees[0].Behind != 2 || got.Worktrees[0].BaseReachable || got.Worktrees[0].Status != "dirty" {
		t.Fatalf("%s", out.String())
	}
	if _, err := runGit(context.Background(), r, "branch", "--show-current"); err == nil {
		t.Fatal("branch command allowed")
	}
}

func TestUpstreamFailureIsUnreadable(t *testing.T) {
	r := &fakeRunner{responses: map[string]commandResult{k("worktree", "list", "--porcelain", "-z"): {stdout: p("worktree /x\x00HEAD h\x00branch refs/heads/x")}, k("rev-parse", "--verify", "--end-of-options", "base^{commit}"): {stdout: "base"}, k("-C", "/x", "status", "--porcelain=v1", "-z"): {}, k("-C", "/x", "rev-parse", "--verify", "HEAD"): {stdout: "h"}, k("-C", "/x", "symbolic-ref", "--quiet", "--short", "HEAD"): {stdout: "x"}, k("-C", "/x", "for-each-ref", "--format=%(upstream:short)%00%(upstream:track)", "refs/heads/x"): {err: context.Canceled}}}
	var out bytes.Buffer
	if err := run(context.Background(), &out, r, "base", "json"); err == nil || !strings.Contains(out.String(), "unreadable") {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestPorcelainRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", p("worktree /x\x00branch refs/heads/x"), p("worktree /x\x00HEAD h\x00branch refs/heads/x\x00detached")} {
		if _, err := parsePorcelain(s); err == nil {
			t.Fatal(s)
		}
	}
}

func TestGoneUpstreamIsValidWithoutRevList(t *testing.T) {
	r := &fakeRunner{responses: map[string]commandResult{k("worktree", "list", "--porcelain", "-z"): {stdout: p("worktree /gone\x00HEAD h\x00branch refs/heads/gone")}, k("rev-parse", "--verify", "--end-of-options", "base^{commit}"): {stdout: "base"}, k("-C", "/gone", "status", "--porcelain=v1", "-z"): {}, k("-C", "/gone", "rev-parse", "--verify", "HEAD"): {stdout: "h"}, k("-C", "/gone", "symbolic-ref", "--quiet", "--short", "HEAD"): {stdout: "gone"}, k("-C", "/gone", "for-each-ref", "--format=%(upstream:short)%00%(upstream:track)", "refs/heads/gone"): {stdout: "origin/gone\x00[gone]\n"}, k("-C", "/gone", "merge-base", "--is-ancestor", "h", "base"): {}}}
	report, err := buildReport(context.Background(), r, "base")
	if err != nil || len(report.Worktrees) != 1 || report.Worktrees[0].UpstreamState != "gone" || report.Worktrees[0].Ahead != 0 || report.Worktrees[0].Behind != 0 {
		t.Fatalf("%#v %v", report, err)
	}
}

func TestParseUpstreamTrackStates(t *testing.T) {
	for _, test := range []struct {
		name, output, upstream, track string
		valid                         bool
	}{
		{"equal", "origin/main\x00\n", "origin/main", "", true},
		{"ahead", "origin/main\x00[ahead 2]\n", "origin/main", "[ahead 2]", true},
		{"behind", "origin/main\x00[behind 3]\n", "origin/main", "[behind 3]", true},
		{"diverged", "origin/main\x00[ahead 2, behind 3]\n", "origin/main", "[ahead 2, behind 3]", true},
		{"gone", "origin/main\x00[gone]\n", "origin/main", "[gone]", true},
		{"empty-upstream-track", "\x00[ahead 2]\n", "", "[ahead 2]", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream, track, err := parseUpstream(test.output)
			if (err == nil) != test.valid || upstream != test.upstream || track != test.track {
				t.Fatalf("%q %q %v", upstream, track, err)
			}
		})
	}
}

func TestEmptyUpstreamWithTrackIsUnreadable(t *testing.T) {
	r := &fakeRunner{responses: map[string]commandResult{k("-C", "/bad", "status", "--porcelain=v1", "-z"): {}, k("-C", "/bad", "rev-parse", "--verify", "HEAD"): {stdout: "head"}, k("-C", "/bad", "symbolic-ref", "--quiet", "--short", "HEAD"): {stdout: "bad"}, k("-C", "/bad", "for-each-ref", "--format=%(upstream:short)%00%(upstream:track)", "refs/heads/bad"): {stdout: "\x00[ahead 1]\n"}}}
	if got := classifyRecord(context.Background(), r, porcelainRecord{path: "/bad", head: "head", branch: "refs/heads/bad"}, "base"); got.Status != "unreadable" {
		t.Fatalf("%#v", got)
	}
}

func TestParseStatusCountsOnlyStatusRecords(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        int
		valid       bool
	}{
		{"untracked-names", "?? ?? name\x00?? line\nname\x00", 2, true},
		{"rename-second-path", "R  new\x00?? old name\x00?? real\x00", 1, true},
		{"malformed", "?? file", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dirty, got, err := parseStatus(test.input)
			if (err == nil) != test.valid || (test.valid && (!dirty || got != test.want)) {
				t.Fatalf("dirty=%t count=%d err=%v", dirty, got, err)
			}
		})
	}
}

func TestParseUpstreamRejectsUnknownMarkers(t *testing.T) {
	for _, value := range []string{"origin/x\x00[unexpected]\n", "origin/x\x00[ahead 0]\n", "origin/x\x00[behind -1]\n", "origin/x\x00[ahead 1, behind 2, extra]\n", "origin/x\x00\x00extra\n"} {
		if _, _, err := parseUpstream(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
