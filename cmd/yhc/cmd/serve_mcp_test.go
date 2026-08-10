package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunServeMCPInvalidPermissionModeDoesNotClaimStarted(t *testing.T) {
	t.Setenv("MCP_PERMISSION_MODE", "Strict")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	err := runServeMCP(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "MCP_PERMISSION_MODE") {
		t.Fatalf("runServeMCP error = %v, want permission-mode configuration error", err)
	}
	if got, want := stderr.String(), "yhc MCP server starting (stdio)\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "server started") {
		t.Fatalf("invalid startup claimed success: %q", stderr.String())
	}
}
