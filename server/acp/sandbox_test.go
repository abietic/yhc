package acp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
)

func TestACPSandboxSelectionIsServerOwned(t *testing.T) {
	agent, err := NewAgent(Config{})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := agent.resolveSandboxSelection(nil)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile != containment.ProfileWorkspaceWrite || selection.Source != containment.SelectionDefault {
		t.Fatalf("ACP default selection = %#v", selection)
	}

	agent, err = NewAgent(Config{SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true})
	if err != nil {
		t.Fatal(err)
	}
	selection, err = agent.resolveSandboxSelection(&config.Config{Sandbox: &config.SandboxConfig{GuestProfile: "workspace-write"}})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile != containment.ProfileDangerFullAccess || selection.Source != containment.SelectionCLI {
		t.Fatalf("ACP CLI selection = %#v", selection)
	}
}

func TestP511ACPEmitsOneDangerDiagnostic(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		CWD: cwd, SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	var output bytes.Buffer
	agent.sandboxDiagnosticOut = &output
	first, err := agent.createEngine("p51-danger-one", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := agent.createEngine("p51-danger-two", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := strings.Count(output.String(), "sandbox-danger-full-access"); got != 1 {
		t.Fatalf("ACP danger diagnostics = %d, output %q", got, output.String())
	}
	if strings.Contains(output.String(), cwd) {
		t.Fatalf("ACP danger diagnostic leaked CWD: %q", output.String())
	}
}

func TestP511ACPEngineUsesServerOwnedBindingMatrix(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  Config
		profile containment.Profile
		adapter containment.AdapterFamily
	}{
		{name: "default workspace", config: Config{}, profile: containment.ProfileWorkspaceWrite, adapter: containment.AdapterDarwinSeatbelt},
		{name: "explicit danger", config: Config{SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true}, profile: containment.ProfileDangerFullAccess, adapter: containment.AdapterAmbientHost},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			test.config.CWD = cwd
			agent, err := NewAgent(test.config)
			if err != nil {
				t.Fatal(err)
			}
			agent.mockModel = &mockChatModel{}
			eng, err := agent.createEngine("p51-acp", cwd)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			bindings := eng.ExecutionBindingMatrix()
			if bindings.Guest().Policy().Spec().Profile != test.profile || bindings.Guest().AdapterFamily() != test.adapter ||
				bindings.ShellHooks().AdapterFamily() != containment.AdapterAmbientHost || bindings.StdioMCP().AdapterFamily() != containment.AdapterAmbientHost {
				t.Fatalf("ACP binding matrix Guest=%#v hook=%#v MCP=%#v", bindings.Guest().Diagnostic(), bindings.ShellHooks().Diagnostic(), bindings.StdioMCP().Diagnostic())
			}
			if test.profile == containment.ProfileDangerFullAccess && eng.GuestExecutionProof() != (containment.ExecutionProof{}) {
				t.Fatal("ACP danger Guest fabricated containment proof")
			}
		})
	}
}
