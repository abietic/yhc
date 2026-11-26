package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellManagerCompleteCommandBoundary(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		stdout   string
		exitCode int
	}{
		{
			name: "empty command",
		},
		{
			name:    "quoted heredoc without final newline",
			command: "cat <<'END'\nliteral $HOME $(printf unwanted) 'quoted'\nEND",
			stdout:  "literal $HOME $(printf unwanted) 'quoted'",
		},
		{
			name:    "heredoc with final newline",
			command: "cat <<'END'\ncomplete\nEND\n",
			stdout:  "complete",
		},
		{
			name:    "tab stripped heredoc",
			command: "cat <<-END\n\tcomplete\n\tEND",
			stdout:  "complete",
		},
		{
			name:     "trailing comment preserves exit status",
			command:  "(exit 7) # no final newline",
			exitCode: 7,
		},
		{
			name:    "comment only",
			command: "# complete comment",
		},
		{
			name:     "complete list merges stderr and preserves failure",
			command:  "printf 'first\\n' >&2\nprintf 'second\\n'\n(exit 9)",
			stdout:   "first\nsecond",
			exitCode: 9,
		},
		{
			name:    "alias becomes available on the next line",
			command: "shopt -s expand_aliases\nalias yhc_boundary_alias='printf ready'\nyhc_boundary_alias",
			stdout:  "ready",
		},
		{
			name:    "extended glob becomes available on the next line",
			command: "shopt -s extglob\ncase ready in @(ready|other)) printf ready;; esac",
			stdout:  "ready",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewShellManager()
			t.Cleanup(func() { _ = manager.KillAll() })
			result, err := manager.ExecuteAt(context.Background(), "", t.TempDir(), test.command, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if result.TimedOut || result.Canceled || result.ExitCode != test.exitCode {
				t.Fatalf("command did not return expected status: %+v", result)
			}
			if result.Stderr != "" {
				t.Fatalf("stderr escaped the command result: %q", result.Stderr)
			}
			if result.Stdout != test.stdout {
				t.Fatalf("stdout = %q, want %q", result.Stdout, test.stdout)
			}
			assertShellCommandCompletes(t, manager, "printf 'next command\\n'")
		})
	}
}

func TestShellManagerCommandBoundaryWithOutputOverrides(t *testing.T) {
	tests := []struct {
		name    string
		setup   string
		command string
		stdout  string
	}{
		{
			name:    "echo alias from a previous command",
			setup:   "shopt -s expand_aliases\nalias echo='builtin printf alias:%s'",
			command: "echo diagnostic; (exit 9)",
			stdout:  "alias:diagnostic",
		},
		{
			name:    "printf function defined in the same command",
			command: "printf() { builtin printf 'function:%s' \"$1\"; }\nprintf diagnostic; (exit 9)",
			stdout:  "function:diagnostic",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewShellManager()
			t.Cleanup(func() { _ = manager.KillAll() })
			if test.setup != "" {
				assertShellCommandCompletes(t, manager, test.setup)
			}
			result, err := manager.Execute(context.Background(), "", test.command, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if result.TimedOut || result.Canceled || result.ExitCode != 9 || result.Stdout != test.stdout {
				t.Fatalf("command feedback = %+v, want %q and exit code 9", result, test.stdout)
			}
			assertShellCommandCompletes(t, manager, "builtin printf 'next command\\n'")
		})
	}
}

func TestShellManagerInitialCWDWithStartupFunction(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	startup := filepath.Join(cwd, "startup.sh")
	if err := os.WriteFile(startup, []byte("pwd() { builtin printf 'custom-pwd\\n'; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", filepath.ToSlash(startup))
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	shell, err := manager.GetOrCreateAt("", cwd)
	if err != nil {
		t.Fatal(err)
	}
	initialCWD := shell.cwd
	result, err := manager.Execute(context.Background(), "", "pwd", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimedOut || result.ExitCode != 0 || result.Stdout != "custom-pwd" {
		t.Fatalf("explicit user pwd did not use the configured function: %+v", result)
	}
	if result.CWD == "" || initialCWD != result.CWD {
		t.Fatalf("initial shell directory = %q, command terminal reported %q", initialCWD, result.CWD)
	}
}

func TestShellManagerTerminatedCommandRetiresShell(t *testing.T) {
	for _, test := range []struct {
		name, command string
		want          []string
	}{
		{name: "empty", command: "exit 7"},
		{
			name: "errexit with unterminated diagnostic", command: "set -e; printf 'missing synthetic header' >&2; false",
			want: []string{"[partial shell output]", "missing synthetic header"},
		},
		{
			name: "bounded preview", command: "printf 'diagnostic head\\n'; printf '%12000s' ' '; printf '\\ndiagnostic tail'; exit 7",
			want: []string{"diagnostic head", "[stdout truncated; showing head and tail]", "diagnostic tail"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewShellManager()
			t.Cleanup(func() { _ = manager.KillAll() })
			original, err := manager.GetOrCreate("")
			if err != nil {
				t.Fatal(err)
			}
			result, err := manager.Execute(context.Background(), "", test.command, time.Second)
			if result != nil || !errors.Is(err, io.EOF) {
				t.Fatalf("expected an unverified EOF, result=%+v err=%v", result, err)
			}
			message := err.Error()
			if len(test.want) == 0 && message != "read from shell: EOF" {
				t.Fatalf("unexpected diagnostic for empty output: %q", message)
			}
			for _, marker := range test.want {
				if !strings.Contains(message, marker) {
					t.Errorf("error missing diagnostic marker %q", marker)
				}
			}
			if len(message) > bashMaxOutputPerStream+len("read from shell: EOF\n[partial shell output]\n") || strings.Contains(message, "[exit code:") {
				t.Fatal("shell error exceeded the preview budget or reported an unverified exit code")
			}
			assertShellCommandCompletes(t, manager, "printf 'recovered\\n'")
			replacement, err := manager.GetOrCreate("")
			if err != nil || replacement == original {
				t.Fatalf("terminated shell was not replaced: %v", err)
			}
		})
	}
}

func TestShellManagerHeredocPreservesShellState(t *testing.T) {
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	command := "cd child\nexport YHC_BOUNDARY_VALUE='preserved value'\nyhc_boundary_function() { printf '%s\\n' \"$YHC_BOUNDARY_VALUE\"; }\ncat <<'END'\nready\nEND"
	result, err := manager.ExecuteAt(context.Background(), "", root, command, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimedOut || result.ExitCode != 0 || result.CWD != child || result.Stdout != "ready" {
		t.Fatalf("heredoc result = %+v", result)
	}
	shell, err := manager.GetOrCreate("")
	if err != nil {
		t.Fatal(err)
	}
	next, err := manager.Execute(context.Background(), "", "yhc_boundary_function", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next.TimedOut || next.ExitCode != 0 || next.CWD != child || next.Stdout != "preserved value" {
		t.Fatalf("persistent state = %+v", next)
	}
	reused, err := manager.GetOrCreate("")
	if err != nil || reused != shell {
		t.Fatalf("persistent shell replaced: %v", err)
	}
}

func TestShellManagerMalformedCommandReportsFailure(t *testing.T) {
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	result, err := manager.Execute(context.Background(), "", "if then", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimedOut || result.Canceled || result.ExitCode == 0 || result.Stdout == "" {
		t.Fatalf("malformed command did not report a shell error: %+v", result)
	}
	assertShellCommandCompletes(t, manager, "printf 'recovered\\n'")
}
