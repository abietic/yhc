package acp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func validACPStdioServer(name string) acpsdk.McpServer {
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    name,
		Command: "/absolute/test-mcp",
		Args:    []string{"--mode", "test"},
		Env: []acpsdk.EnvVariable{
			{Name: "PUBLIC_SENTINEL", Value: "value"},
		},
	}}
}

func TestP235ACPStdioDescriptorValidationFailsBeforeLaunch(t *testing.T) {
	tests := []struct {
		name       string
		servers    func() []acpsdk.McpServer
		wantReason string
		wantIndex  int
	}{
		{
			name: "server limit",
			servers: func() []acpsdk.McpServer {
				servers := make([]acpsdk.McpServer, maxACPSessionMCPServers+1)
				for index := range servers {
					servers[index] = validACPStdioServer(fmt.Sprintf("server-%d", index))
				}
				return servers
			},
			wantReason: "server_limit",
			wantIndex:  maxACPSessionMCPServers,
		},
		{
			name: "transport union empty",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{{}}
			},
			wantReason: "transport_union_invalid",
		},
		{
			name: "transport union ambiguous",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("ambiguous")
				server.Http = &acpsdk.McpServerHttpInline{}
				return []acpsdk.McpServer{server}
			},
			wantReason: "transport_union_invalid",
		},
		{
			name: "unsupported transport",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{{Http: &acpsdk.McpServerHttpInline{}}}
			},
			wantReason: "unsupported_transport",
		},
		{
			name: "name required",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{validACPStdioServer("")}
			},
			wantReason: "server_name_required",
		},
		{
			name: "name too long",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{
					validACPStdioServer(strings.Repeat("n", maxACPSessionMCPServerName+1)),
				}
			},
			wantReason: "server_name_too_long",
		},
		{
			name: "raw name duplicate",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{
					validACPStdioServer("same"),
					validACPStdioServer("same"),
				}
			},
			wantReason: "server_name_duplicate",
			wantIndex:  1,
		},
		{
			name: "normalized name collision",
			servers: func() []acpsdk.McpServer {
				return []acpsdk.McpServer{
					validACPStdioServer("same name"),
					validACPStdioServer("same@name"),
				}
			},
			wantReason: "server_name_normalization_collision",
			wantIndex:  1,
		},
		{
			name: "command required",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("missing")
				server.Stdio.Command = ""
				return []acpsdk.McpServer{server}
			},
			wantReason: "command_required",
		},
		{
			name: "command absolute",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("relative")
				server.Stdio.Command = "relative-command"
				return []acpsdk.McpServer{server}
			},
			wantReason: "command_not_absolute",
		},
		{
			name: "command too long",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("long")
				server.Stdio.Command = "/" + strings.Repeat("c", maxACPSessionMCPCommand)
				return []acpsdk.McpServer{server}
			},
			wantReason: "command_too_long",
		},
		{
			name: "argument limit",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("args")
				server.Stdio.Args = make([]string, maxACPSessionMCPArguments+1)
				return []acpsdk.McpServer{server}
			},
			wantReason: "argument_limit",
		},
		{
			name: "environment limit",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("env")
				server.Stdio.Env = make([]acpsdk.EnvVariable, maxACPSessionMCPEnvironment+1)
				return []acpsdk.McpServer{server}
			},
			wantReason: "environment_limit",
		},
		{
			name: "environment name",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("env-name")
				server.Stdio.Env = []acpsdk.EnvVariable{{Name: "BAD-NAME", Value: "private"}}
				return []acpsdk.McpServer{server}
			},
			wantReason: "environment_name_invalid",
		},
		{
			name: "environment duplicate",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("env-duplicate")
				server.Stdio.Env = []acpsdk.EnvVariable{
					{Name: "TOKEN", Value: "private-a"},
					{Name: "TOKEN", Value: "private-b"},
				}
				return []acpsdk.McpServer{server}
			},
			wantReason: "environment_name_duplicate",
		},
		{
			name: "nul",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("nul")
				server.Stdio.Args = []string{"private\x00argument"}
				return []acpsdk.McpServer{server}
			},
			wantReason: "nul_not_allowed",
		},
		{
			name: "aggregate bytes",
			servers: func() []acpsdk.McpServer {
				server := validACPStdioServer("bytes")
				server.Stdio.Args = []string{strings.Repeat("p", maxACPSessionMCPDescriptor)}
				return []acpsdk.McpServer{server}
			},
			wantReason: "descriptor_size_limit",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateACPSessionMCPSetup(test.servers())
			var requestErr *acpsdk.RequestError
			if !errors.As(err, &requestErr) {
				t.Fatalf("validation error = %#v", err)
			}
			data, ok := requestErr.Data.(map[string]any)
			if !ok ||
				data["input"] != acpSessionMCPDescriptorInput ||
				data["reason"] != test.wantReason ||
				data["descriptor"] != test.wantIndex {
				t.Fatalf("request error data = %#v", requestErr.Data)
			}
			for _, private := range []string{
				"private",
				strings.Repeat("p", 64),
				"/absolute/test-mcp",
			} {
				if containsRequestErrorFact(requestErr, private) {
					t.Fatalf("validation error leaked descriptor fact: %v", requestErr)
				}
			}
		})
	}
}

func TestP235ACPStdioFingerprintIsOrderStableAndValueSensitive(t *testing.T) {
	first := validACPStdioServer("alpha")
	first.Stdio.Env = []acpsdk.EnvVariable{
		{Name: "B", Value: "two"},
		{Name: "A", Value: "one"},
	}
	second := validACPStdioServer("beta")

	setupA, err := validateACPSessionMCPSetup([]acpsdk.McpServer{first, second})
	if err != nil {
		t.Fatal(err)
	}
	first.Stdio.Env[0], first.Stdio.Env[1] = first.Stdio.Env[1], first.Stdio.Env[0]
	setupB, err := validateACPSessionMCPSetup([]acpsdk.McpServer{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if setupA.fingerprint != setupB.fingerprint {
		t.Fatal("equivalent descriptor set produced different fingerprint")
	}

	first.Stdio.Env[0].Value = "changed"
	setupC, err := validateACPSessionMCPSetup([]acpsdk.McpServer{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if setupA.fingerprint == setupC.fingerprint {
		t.Fatal("descriptor value change did not change fingerprint")
	}
}
