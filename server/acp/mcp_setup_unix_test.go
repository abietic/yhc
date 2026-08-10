//go:build !windows

package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestP235ACPStdioUnixEnvironmentIdentity(t *testing.T) {
	first := validACPStdioServer("unix-environment")
	first.Stdio.Env = []acpsdk.EnvVariable{
		{Name: "Path", Value: "one"},
		{Name: "PATH", Value: "two"},
	}
	firstSetup, err := validateACPSessionMCPSetup([]acpsdk.McpServer{first})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstSetup.servers[0].Config.Env["Path"]; got != "one" {
		t.Fatalf("mixed-case environment = %q, want %q", got, "one")
	}
	if got := firstSetup.servers[0].Config.Env["PATH"]; got != "two" {
		t.Fatalf("uppercase environment = %q, want %q", got, "two")
	}

	swapped := validACPStdioServer("unix-environment")
	swapped.Stdio.Env = []acpsdk.EnvVariable{
		{Name: "Path", Value: "two"},
		{Name: "PATH", Value: "one"},
	}
	swappedSetup, err := validateACPSessionMCPSetup([]acpsdk.McpServer{swapped})
	if err != nil {
		t.Fatal(err)
	}
	if firstSetup.fingerprint == swappedSetup.fingerprint {
		t.Fatal("distinct Unix environment mappings produced the same fingerprint")
	}
}
