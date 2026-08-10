package ownedprocess

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestCodeReturnsOnlyOwnedProcessCodes(t *testing.T) {
	for _, want := range []string{
		codeTreeUnavailable,
		codeStartFailed,
		codeProcessFailed,
		codeTreeCloseFailed,
		codeTimeout,
		codeCanceled,
	} {
		if got := Code(fail(want, errors.New("cause"))); got != want {
			t.Fatalf("Code(%q) = %q, want %q", want, got, want)
		}
		var classified *Error
		if !errors.As(fail(want, errors.New("cause")), &classified) {
			t.Fatalf("failure %q is not exposed as *Error", want)
		}
	}
	if got := Code(nil); got != "" {
		t.Fatalf("Code(nil) = %q, want empty", got)
	}
	if got := Code(errors.New("untyped")); got != "" {
		t.Fatalf("Code(untyped) = %q, want empty", got)
	}
}

func TestRunClassifiesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Code(Run(ctx, exec.Command("sh", "-c", "exec sleep 10"))); got != codeCanceled {
		t.Fatalf("Code(Run()) = %q, want %q", got, codeCanceled)
	}
}

func TestRunClassifiesCommandOutcomes(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		cmd  *exec.Cmd
		want string
	}{
		{
			name: "start failure",
			ctx:  context.Background(),
			cmd:  exec.Command("/definitely-not-an-eino-agent-command"),
			want: "process_start_failed",
		},
		{
			name: "process failure",
			ctx:  context.Background(),
			cmd:  exec.Command("sh", "-c", "exit 7"),
			want: "process_failed",
		},
		{
			name: "timeout",
			ctx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			}(),
			cmd:  exec.Command("sh", "-c", "exec sleep 10"),
			want: "process_timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Code(Run(test.ctx, test.cmd)); got != test.want {
				t.Fatalf("Code(Run()) = %q, want %q", got, test.want)
			}
		})
	}
}
