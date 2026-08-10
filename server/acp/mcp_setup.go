package acp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/tools"
)

const (
	maxACPSessionMCPServers      = 16
	maxACPSessionMCPArguments    = 128
	maxACPSessionMCPEnvironment  = 128
	maxACPSessionMCPDescriptor   = 1 << 20
	maxACPSessionMCPServerName   = 128
	maxACPSessionMCPCommand      = 4096
	acpSessionMCPSetupTimeout    = 60 * time.Second
	acpSessionMCPDescriptorInput = "session.mcpServers"
)

var acpMCPEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type acpSessionMCPSetup struct {
	servers     []tools.SessionMCPServer
	fingerprint [sha256.Size]byte
}

type acpMCPEnvironmentFingerprintPair struct {
	name  string
	value string
}

func validateACPSessionMCPSetup(
	servers []acpsdk.McpServer,
) (*acpSessionMCPSetup, error) {
	if len(servers) == 0 {
		return &acpSessionMCPSetup{}, nil
	}
	if len(servers) > maxACPSessionMCPServers {
		return nil, invalidACPMCPSetup("server_limit", maxACPSessionMCPServers)
	}

	rawNames := make(map[string]struct{}, len(servers))
	normalizedNames := make(map[string]struct{}, len(servers))
	result := &acpSessionMCPSetup{
		servers: make([]tools.SessionMCPServer, 0, len(servers)),
	}
	totalBytes := 0
	for index, union := range servers {
		variants := 0
		if union.Stdio != nil {
			variants++
		}
		if union.Http != nil {
			variants++
		}
		if union.Sse != nil {
			variants++
		}
		if union.Acp != nil {
			variants++
		}
		if variants != 1 {
			return nil, invalidACPMCPSetup("transport_union_invalid", index)
		}
		if union.Stdio == nil {
			return nil, invalidACPMCPSetup("unsupported_transport", index)
		}
		stdio := union.Stdio
		if stdio.Name == "" {
			return nil, invalidACPMCPSetup("server_name_required", index)
		}
		if len(stdio.Name) > maxACPSessionMCPServerName {
			return nil, invalidACPMCPSetup("server_name_too_long", index)
		}
		if containsNUL(stdio.Name) {
			return nil, invalidACPMCPSetup("nul_not_allowed", index)
		}
		if _, duplicate := rawNames[stdio.Name]; duplicate {
			return nil, invalidACPMCPSetup("server_name_duplicate", index)
		}
		normalizedName := mcp.NormalizeNameForMCP(stdio.Name)
		if normalizedName == "" {
			return nil, invalidACPMCPSetup("server_name_invalid", index)
		}
		if _, duplicate := normalizedNames[normalizedName]; duplicate {
			return nil, invalidACPMCPSetup("server_name_normalization_collision", index)
		}
		rawNames[stdio.Name] = struct{}{}
		normalizedNames[normalizedName] = struct{}{}

		if stdio.Command == "" {
			return nil, invalidACPMCPSetup("command_required", index)
		}
		if len(stdio.Command) > maxACPSessionMCPCommand {
			return nil, invalidACPMCPSetup("command_too_long", index)
		}
		if containsNUL(stdio.Command) {
			return nil, invalidACPMCPSetup("nul_not_allowed", index)
		}
		if !filepath.IsAbs(stdio.Command) {
			return nil, invalidACPMCPSetup("command_not_absolute", index)
		}
		if len(stdio.Args) > maxACPSessionMCPArguments {
			return nil, invalidACPMCPSetup("argument_limit", index)
		}
		args := append([]string(nil), stdio.Args...)
		for _, argument := range args {
			if containsNUL(argument) {
				return nil, invalidACPMCPSetup("nul_not_allowed", index)
			}
		}
		if len(stdio.Env) > maxACPSessionMCPEnvironment {
			return nil, invalidACPMCPSetup("environment_limit", index)
		}
		environment := make(map[string]string, len(stdio.Env))
		seenEnvironment := make(map[string]struct{}, len(stdio.Env))
		for _, variable := range stdio.Env {
			if !acpMCPEnvironmentName.MatchString(variable.Name) {
				return nil, invalidACPMCPSetup("environment_name_invalid", index)
			}
			if containsNUL(variable.Name) || containsNUL(variable.Value) {
				return nil, invalidACPMCPSetup("nul_not_allowed", index)
			}
			canonicalName := mcp.CanonicalEnvironmentKey(variable.Name)
			if _, duplicate := seenEnvironment[canonicalName]; duplicate {
				return nil, invalidACPMCPSetup("environment_name_duplicate", index)
			}
			seenEnvironment[canonicalName] = struct{}{}
			environment[variable.Name] = variable.Value
		}

		totalBytes += len(stdio.Name) + len(stdio.Command)
		for _, argument := range args {
			totalBytes += len(argument)
		}
		for name, value := range environment {
			totalBytes += len(name) + len(value)
		}
		if totalBytes > maxACPSessionMCPDescriptor {
			return nil, invalidACPMCPSetup("descriptor_size_limit", index)
		}

		result.servers = append(result.servers, tools.SessionMCPServer{
			DescriptorIndex: index,
			Name:            stdio.Name,
			Config: mcp.ServerConfig{
				Name:    stdio.Name,
				Command: stdio.Command,
				Args:    args,
				Env:     environment,
				Type:    "stdio",
			},
		})
	}
	sort.Slice(result.servers, func(i, j int) bool {
		return result.servers[i].Name < result.servers[j].Name
	})
	result.fingerprint = fingerprintACPMCPServers(result.servers)
	return result, nil
}

func containsNUL(value string) bool {
	return strings.IndexByte(value, 0) >= 0
}

func invalidACPMCPSetup(reason string, descriptorIndex int) *acpsdk.RequestError {
	data := map[string]any{
		"input":      acpSessionMCPDescriptorInput,
		"reason":     reason,
		"descriptor": descriptorIndex,
	}
	return acpsdk.NewInvalidParams(data)
}

func fingerprintACPMCPServers(
	servers []tools.SessionMCPServer,
) [sha256.Size]byte {
	digest := sha256.New()
	for _, server := range servers {
		writeMCPFingerprintString(digest, server.Name)
		writeMCPFingerprintString(digest, server.Config.Command)
		_ = binary.Write(digest, binary.BigEndian, uint32(len(server.Config.Args)))
		for _, argument := range server.Config.Args {
			writeMCPFingerprintString(digest, argument)
		}
		environment := make(
			[]acpMCPEnvironmentFingerprintPair,
			0,
			len(server.Config.Env),
		)
		for name, value := range server.Config.Env {
			environment = append(environment, acpMCPEnvironmentFingerprintPair{
				name:  mcp.CanonicalEnvironmentKey(name),
				value: value,
			})
		}
		sort.Slice(environment, func(i, j int) bool {
			if environment[i].name != environment[j].name {
				return environment[i].name < environment[j].name
			}
			return environment[i].value < environment[j].value
		})
		_ = binary.Write(digest, binary.BigEndian, uint32(len(environment)))
		for _, variable := range environment {
			writeMCPFingerprintString(digest, variable.name)
			writeMCPFingerprintString(digest, variable.value)
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeMCPFingerprintString(digest hash.Hash, value string) {
	_ = binary.Write(digest, binary.BigEndian, uint64(len(value)))
	_, _ = digest.Write([]byte(value))
}

func (s *acpSessionMCPSetup) forCWD(cwd string) []tools.SessionMCPServer {
	if s == nil || len(s.servers) == 0 {
		return nil
	}
	servers := make([]tools.SessionMCPServer, len(s.servers))
	for index, server := range s.servers {
		servers[index] = server
		servers[index].Config.Args = append([]string(nil), server.Config.Args...)
		servers[index].Config.CWD = cwd
		servers[index].Config.Env = make(map[string]string, len(server.Config.Env))
		for name, value := range server.Config.Env {
			servers[index].Config.Env[name] = value
		}
	}
	return servers
}

func acpSessionMCPContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(acpSessionMCPSetupTimeout)
	if current, ok := ctx.Deadline(); ok && current.Before(deadline) {
		return context.WithDeadline(ctx, current)
	}
	return context.WithDeadline(ctx, deadline)
}

func acpSessionMCPRequestError(err error) error {
	var setupErr *tools.SessionMCPSetupError
	if errors.As(err, &setupErr) {
		return invalidACPMCPSetup(setupErr.Reason, setupErr.DescriptorIndex)
	}
	return invalidACPMCPSetup("setup_failed", 0)
}
