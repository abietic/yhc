package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatShellResultLongCommandPreservesOutcome(t *testing.T) {
	for _, test := range []struct {
		name   string
		result ShellResult
		status string
	}{
		{name: "failure", result: ShellResult{ExitCode: 7}, status: "[exit code: 7]"},
		{name: "timeout", result: ShellResult{TimedOut: true}, status: "[timeout]"},
		{name: "cancellation", result: ShellResult{Canceled: true}, status: "[canceled]"},
		{name: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.result.Stdout = "actual stdout"
			test.result.Stderr = "actual stderr"
			got := formatShellResult("# "+strings.Repeat("payload ", 5000), &test.result)
			for _, want := range []string{"actual stdout", "actual stderr", test.status} {
				if !strings.Contains(got, want) {
					t.Errorf("long command concealed %q", want)
				}
			}
			if len(got) > 30000 {
				t.Errorf("result exceeds byte limit: %d", len(got))
			}
		})
	}
}

func TestFormatShellResultPreservesStreamEnds(t *testing.T) {
	result := &ShellResult{
		Stdout:   "stdout start!\n" + strings.Repeat("界", 5000) + "\nstdout final assertion",
		Stderr:   "stderr start!\n" + strings.Repeat("错", 5000) + "\nstderr final diagnostic",
		ExitCode: 9,
	}
	got := formatShellResult("run-checks", result)
	for _, want := range []string{
		"stdout start", "stdout final assertion", "stderr start", "stderr final diagnostic", "[exit code: 9]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("truncation concealed %q", want)
		}
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a UTF-8 code point")
	}
	if len(got) > 30000 {
		t.Errorf("result exceeds byte limit: %d", len(got))
	}
}

func TestFormatShellResultSmallOutputCompatibility(t *testing.T) {
	got := formatShellResult("check", &ShellResult{Stdout: "stdout", Stderr: "stderr\n", ExitCode: 3})
	if got != "$ check\nstdout\nstderr\n[exit code: 3]\n" {
		t.Fatalf("small result changed: %q", got)
	}
}

func TestFormatShellResultBoundsRepeatedCommand(t *testing.T) {
	for _, command := range []string{
		"# begin\n" + strings.Repeat("long script body\n", 200) + "# finish",
		"# begin\n" + strings.Repeat("脚本🙂\n", 200) + "# finish",
	} {
		result := &ShellResult{Stdout: "execution stdout", Stderr: "execution stderr", ExitCode: 7}
		got := formatShellResult(command, result)
		const evidence = "execution stdout\nexecution stderr\n[exit code: 7]\n"
		if !strings.HasSuffix(got, evidence) {
			t.Fatalf("command preview changed execution evidence: %q", got)
		}
		echo := strings.TrimSuffix(strings.TrimPrefix(got, "$ "), "\n"+evidence)
		if len(echo) > 512 {
			t.Errorf("repeated command exceeds 512-byte preview: %d", len(echo))
		}
		if !strings.HasPrefix(echo, "# begin\n") || !strings.HasSuffix(echo, "# finish") ||
			!strings.Contains(echo, "[command truncated; showing head and tail]") {
			t.Errorf("command preview lost identity or truncation notice: %q", echo)
		}
		if !utf8.ValidString(got) {
			t.Error("command preview split a UTF-8 code point")
		}
	}
}

func TestFormatShellResultCommandPreviewBoundary(t *testing.T) {
	for _, size := range []int{511, 512, 513} {
		command := strings.Repeat("x", size)
		got := formatShellResult(command, &ShellResult{Stdout: "done"})
		if size <= 512 {
			if want := "$ " + command + "\ndone\n"; got != want {
				t.Errorf("%d-byte command changed: %q", size, got)
			}
		} else if strings.Contains(got, command) || !strings.Contains(got, "[command truncated;") {
			t.Errorf("%d-byte command was repeated in full: %q", size, got)
		}
	}
}

func FuzzFormatShellResultPreservesUTF8AndOutcome(f *testing.F) {
	f.Add("check", "stdout", "stderr")
	f.Add(strings.Repeat("🙂", 10000), "head!"+strings.Repeat("界", 5000), "head!"+strings.Repeat("错", 5000))
	f.Fuzz(func(t *testing.T, command, stdout, stderr string) {
		if len(command)+len(stdout)+len(stderr) > 256<<10 ||
			!utf8.ValidString(command) || !utf8.ValidString(stdout) || !utf8.ValidString(stderr) {
			t.Skip()
		}
		got := formatShellResult(command, &ShellResult{Stdout: stdout, Stderr: stderr, ExitCode: 7})
		if !utf8.ValidString(got) || len(got) > 30000 || !strings.Contains(got, "[exit code: 7]") {
			t.Fatalf("invalid result: utf8=%v bytes=%d exit_marker=%v", utf8.ValidString(got), len(got), strings.Contains(got, "[exit code: 7]"))
		}
	})
}
