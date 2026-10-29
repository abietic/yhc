package cmd

import (
	"strings"
	"testing"
)

func TestExecRejectsRemovedCheckerFlags(t *testing.T) {
	for _, flag := range []string{
		"verification-turns", "verification-repairs", "verification-coverage-checks",
		"verification-coverage-review", "verification-effort", "verification-max-output-tokens",
		"verification-coverage-effort", "verification-coverage-max-output-tokens", "resume-verification",
	} {
		t.Run(flag, func(t *testing.T) {
			command := newExecCommand()
			if err := command.ParseFlags([]string{"--" + flag + "=1"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("removed flag must fail before execution, got %v", err)
			}
		})
	}
}
