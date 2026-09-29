package e2e

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/scripts/internal/ownedprocess"
)

func TestInvocationFailurePreservesCauseWithoutDisclosingContent(t *testing.T) {
	const privateContent = "private-fixture-content"
	for _, tc := range []struct {
		name, phase, code string
		cause             error
		deadline          bool
		canceled          bool
	}{
		{name: "process", phase: "process", code: "unclassified", cause: errors.New(privateContent)},
		{name: "decoder", phase: "envelope", code: "invalid_envelope", cause: errors.New(privateContent)},
		{name: "timeout", phase: "process", code: "unclassified", cause: context.DeadlineExceeded, deadline: true},
		{name: "cancel", phase: "process", code: "unclassified", cause: context.Canceled, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr limitedBuffer
			_, _ = stdout.Write([]byte(privateContent))
			_, _ = stderr.Write([]byte(privateContent))
			failure := newInvocationError(tc.phase, tc.cause, time.Now(), &stdout, &stderr)
			if !errors.Is(failure, tc.cause) {
				t.Fatal("invocation diagnostic lost error identity")
			}
			if failure.stdoutBytes != len(privateContent) || failure.stderrBytes != len(privateContent) || failure.elapsed < 0 {
				t.Fatal("invocation diagnostic lost numeric metadata")
			}
			text := failure.Error()
			if strings.Contains(text, privateContent) ||
				!strings.Contains(text, "phase="+tc.phase) ||
				!strings.Contains(text, "code="+tc.code) ||
				strings.Contains(text, "deadline=true") != tc.deadline ||
				strings.Contains(text, "canceled=true") != tc.canceled {
				t.Fatal("invocation diagnostic disclosed content or omitted classification")
			}
		})
	}
}

func TestInvocationFailureReportsOwnedProcessCode(t *testing.T) {
	// An absent executable gives a deterministic process-start failure without
	// launching a child. Its path must never appear in the printable diagnostic.
	path := filepath.Join(t.TempDir(), "private-missing-executable")
	cause := ownedprocess.Run(context.Background(), exec.Command(path))
	if cause == nil {
		t.Fatal("absent executable unexpectedly started")
	}
	failure := &invocationError{cause: cause, phase: "process"}
	if code := ownedprocess.Code(failure); code != "process_start_failed" {
		t.Fatalf("wrapped process code = %q", code)
	}
	if text := failure.Error(); !strings.Contains(text, "code=process_start_failed") || strings.Contains(text, path) {
		t.Fatal("process diagnostic omitted code or disclosed executable path")
	}
}
