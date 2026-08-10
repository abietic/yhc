//go:build windows

package acp

import (
	"errors"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestP235ACPStdioWindowsEnvironmentIdentity(t *testing.T) {
	t.Run("semantic duplicate fails before setup", func(t *testing.T) {
		server := validACPStdioServer("windows-duplicate")
		server.Stdio.Env = []acpsdk.EnvVariable{
			{Name: "Path", Value: "one"},
			{Name: "PATH", Value: "two"},
		}

		_, err := validateACPSessionMCPSetup([]acpsdk.McpServer{server})
		var requestErr *acpsdk.RequestError
		if !errors.As(err, &requestErr) {
			t.Fatalf("validation error = %#v", err)
		}
		data, ok := requestErr.Data.(map[string]any)
		if !ok ||
			data["input"] != acpSessionMCPDescriptorInput ||
			data["reason"] != "environment_name_duplicate" ||
			data["descriptor"] != 0 {
			t.Fatalf("request error data = %#v", requestErr.Data)
		}
	})

	t.Run("fingerprint folds identity without rewriting config", func(t *testing.T) {
		mixed := validACPStdioServer("windows-fingerprint")
		mixed.Stdio.Env = []acpsdk.EnvVariable{{Name: "Path", Value: "same"}}
		upper := validACPStdioServer("windows-fingerprint")
		upper.Stdio.Env = []acpsdk.EnvVariable{{Name: "PATH", Value: "same"}}

		mixedSetup, err := validateACPSessionMCPSetup([]acpsdk.McpServer{mixed})
		if err != nil {
			t.Fatal(err)
		}
		upperSetup, err := validateACPSessionMCPSetup([]acpsdk.McpServer{upper})
		if err != nil {
			t.Fatal(err)
		}
		if mixedSetup.fingerprint != upperSetup.fingerprint {
			t.Fatal("equivalent Windows environment identities produced different fingerprints")
		}
		if got := mixedSetup.servers[0].Config.Env["Path"]; got != "same" {
			t.Fatalf("original environment spelling/value = %q, want %q", got, "same")
		}
		if _, rewritten := mixedSetup.servers[0].Config.Env["PATH"]; rewritten {
			t.Fatalf("admission rewrote environment key: %#v", mixedSetup.servers[0].Config.Env)
		}
	})
}
