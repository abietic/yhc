package tools

import (
	"context"
	"os"
	"path/filepath"
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

func TestShellManagerTerminatedCommandRetiresShell(t *testing.T) {
	for _, command := range []string{"exit 7", "set -e; false"} {
		t.Run(command, func(t *testing.T) {
			manager := NewShellManager()
			t.Cleanup(func() { _ = manager.KillAll() })
			original, err := manager.GetOrCreate("")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Execute(context.Background(), "", command, time.Second); err == nil {
				t.Fatal("expected shell termination error")
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
