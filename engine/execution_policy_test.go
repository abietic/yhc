package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestSandboxSelectionRejectsImplicitAmbientAuthority(t *testing.T) {
	readOnlyA, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	readOnlyB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := NewSandboxSelection(
		containment.ProfileWorkspaceWrite,
		containment.SelectionDefault,
		[]string{readOnlyA, readOnlyB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Profile != containment.ProfileWorkspaceWrite || workspace.Source != containment.SelectionDefault || len(workspace.ExtraReadRoots) != 2 {
		t.Fatalf("workspace selection = %#v", workspace)
	}
	if _, err := NewSandboxSelection(containment.ProfileDangerFullAccess, containment.SelectionDefault, nil); err == nil {
		t.Fatal("default source selected danger-full-access")
	}
	if _, err := NewSandboxSelection(containment.ProfileDangerFullAccess, containment.SelectionChild, nil); err == nil {
		t.Fatal("child source selected danger-full-access")
	}
	for _, source := range []containment.SelectionSource{containment.SelectionUserConfig, containment.SelectionCLI} {
		if _, err := NewSandboxSelection(containment.ProfileDangerFullAccess, source, nil); err != nil {
			t.Fatalf("user-owned source %q rejected: %v", source, err)
		}
	}
	clone := workspace.Clone()
	wantFirstRoot := workspace.ExtraReadRoots[0]
	workspace.ExtraReadRoots[0] = "/mutated"
	if clone.ExtraReadRoots[0] != wantFirstRoot {
		t.Fatal("sandbox selection clone retained caller slice")
	}
}

func TestResolveDisabledExecutionBindingsKeepsProcessClassesAmbient(t *testing.T) {
	bindings, err := ResolveDisabledExecutionBindings(t.TempDir(), commands.EntrypointHeadless)
	if err != nil {
		t.Fatal(err)
	}
	got := []*containment.Binding{bindings.Guest(), bindings.ShellHooks(), bindings.StdioMCP()}
	want := []containment.ProcessClass{containment.ProcessClassGuest, containment.ProcessClassShellHooks, containment.ProcessClassStdioMCP}
	seen := make(map[string]struct{}, len(got))
	for index, binding := range got {
		if binding.ProcessClass() != want[index] || binding.AdapterFamily() != containment.AdapterAmbientHost || binding.Availability() != containment.BindingAvailable {
			t.Fatalf("binding[%d] = %#v", index, binding.Diagnostic())
		}
		if _, exists := seen[binding.Digest()]; exists {
			t.Fatalf("duplicate process-class binding digest %q", binding.Digest())
		}
		seen[binding.Digest()] = struct{}{}
	}
	if _, err := containment.NewExecutionProof(bindings.Guest(), containment.AxisWallTime); err == nil {
		t.Fatal("disabled Guest binding generated containment proof")
	}
}

func TestP511WorkspaceSelectionResolvesOrthogonalProcessBindings(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := ResolveExecutionBindings(context.Background(), root, commands.EntrypointHeadless, selection)
	if err != nil {
		t.Fatal(err)
	}
	guest, hookBinding, mcpBinding := bindings.Guest(), bindings.ShellHooks(), bindings.StdioMCP()
	if guest.AdapterFamily() != containment.AdapterDarwinSeatbelt || guest.ProcessClass() != containment.ProcessClassGuest {
		t.Fatalf("Guest binding = %#v", guest.Diagnostic())
	}
	for _, extension := range []*containment.Binding{hookBinding, mcpBinding} {
		if extension.AdapterFamily() != containment.AdapterAmbientHost || extension.Availability() != containment.BindingAvailable || extension.Policy().Diagnostic().State != containment.StateDisabled {
			t.Fatalf("extension binding = %#v", extension.Diagnostic())
		}
	}
	if guest.Digest() == hookBinding.Digest() || hookBinding.Digest() == mcpBinding.Digest() {
		t.Fatal("process-class binding identities collapsed")
	}
	if runtime.GOOS == "darwin" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		if info, statErr := os.Lstat("/usr/bin/sandbox-exec"); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 && guest.Availability() != containment.BindingAvailable {
			t.Fatalf("real Darwin Guest binding = %#v", guest.Diagnostic())
		}
	} else if guest.Availability() != containment.BindingUnavailable || guest.ReasonCode() != containment.ReasonUnsupportedPlatform {
		t.Fatalf("unsupported-host Guest binding = %#v", guest.Diagnostic())
	}
}

func TestP511EngineBindsEachOwnerToItsNamedClass(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	hookExecutor := hooks.NewExecutor()
	shellManager := tools.NewShellManager()
	mcpManager := tools.NewMCPToolManager()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "p51-owner-matrix", CWD: root, CommandEntrypoint: commands.EntrypointHeadless,
		SandboxSelection: selection, HookExecutor: hookExecutor, ShellManager: shellManager, MCPManager: mcpManager,
	})
	defer eng.Close()
	bindings := eng.ExecutionBindingMatrix()
	if bindings == nil || shellManager.GuestBindingDigest() != bindings.Guest().Digest() ||
		hookExecutor.ExecutionBindingDigest() != bindings.ShellHooks().Digest() ||
		mcpManager.ExecutionBindingDigest() != bindings.StdioMCP().Digest() {
		t.Fatalf("owner bindings shell=%q hook=%q mcp=%q", shellManager.GuestBindingDigest(), hookExecutor.ExecutionBindingDigest(), mcpManager.ExecutionBindingDigest())
	}
	if eng.ExecutionPolicySnapshot().Digest() != bindings.Guest().PolicyDigest() {
		t.Fatal("legacy policy accessor did not map to Guest policy")
	}
	if bindings.Guest().Availability() == containment.BindingAvailable {
		proof := eng.GuestExecutionProof()
		wantAxes := containment.AxisFilesystemRead | containment.AxisFilesystemWrite | containment.AxisNetworkDenied |
			containment.AxisRootIdentity | containment.AxisDescendantConfinement | containment.AxisDescendantCleanup |
			containment.AxisWallTime | containment.AxisOutput
		if proof.BindingDigest != bindings.Guest().Digest() || proof.Enforced != wantAxes {
			t.Fatalf("Guest proof = %#v", proof)
		}
		result, execErr := shellManager.Execute(context.Background(), "p51", "echo contained", time.Second)
		if execErr != nil || result.Stdout != "contained" {
			t.Fatalf("real Guest execution = %#v, %v", result, execErr)
		}
	} else {
		if _, execErr := shellManager.Execute(context.Background(), "p51", "true", time.Second); execErr == nil {
			t.Fatal("unavailable Guest executed")
		}
	}
}

func TestP511PermissionModesCannotChangeBindingMatrix(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	modes := []permission.Mode{permission.ModeDefault, permission.ModePlan, permission.ModeAcceptEdits, permission.ModeAuto, permission.ModeBypassPermissions, permission.ModeDontAsk}
	var want [3]string
	for index, mode := range modes {
		eng := NewQueryEngine(QueryEngineConfig{SessionID: "p51-mode-" + string(mode), CWD: root, CommandEntrypoint: commands.EntrypointHeadless, SandboxSelection: selection, PermissionMode: mode})
		bindings := eng.ExecutionBindingMatrix()
		got := [3]string{bindings.Guest().Digest(), bindings.ShellHooks().Digest(), bindings.StdioMCP().Digest()}
		eng.Close()
		if index == 0 {
			want = got
		} else if got != want {
			t.Fatalf("mode %q changed bindings: %v != %v", mode, got, want)
		}
	}
}

func TestP511DangerSelectionIsExplicitAndCarriesNoProof(t *testing.T) {
	selection, err := NewSandboxSelection(containment.ProfileDangerFullAccess, containment.SelectionCLI, nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := ResolveExecutionBindings(context.Background(), t.TempDir(), commands.EntrypointPlain, selection)
	if err != nil {
		t.Fatal(err)
	}
	guest := bindings.Guest()
	if guest.AdapterFamily() != containment.AdapterAmbientHost || guest.Policy().Spec().SelectionSource != containment.SelectionCLI || guest.Availability() != containment.BindingAvailable {
		t.Fatalf("danger Guest = %#v", guest.Diagnostic())
	}
	if guest.PolicyDigest() == bindings.ShellHooks().PolicyDigest() {
		t.Fatal("user-selected Guest authority collapsed into extension compatibility identity")
	}
	if _, err := containment.NewExecutionProof(guest, containment.AxisWallTime); err == nil {
		t.Fatal("ambient danger Guest produced an execution proof")
	}
}

func TestP511DangerStartupDiagnosticIsVisibleAndRedacted(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileDangerFullAccess, containment.SelectionCLI, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "p511-danger-diagnostic",
		CWD:               root,
		SandboxSelection:  selection,
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	defer eng.Close()
	code, message := eng.ExecutionContainmentStartupDiagnostic()
	if code != "sandbox-danger-full-access" || !strings.Contains(message, "ambient host authority") || !strings.Contains(message, "permission mode") {
		t.Fatalf("danger startup diagnostic = %q %q", code, message)
	}
	if strings.Contains(message, root) {
		t.Fatalf("danger startup diagnostic leaked root: %q", message)
	}
}

func TestP511ChildGuestNarrowsWriteRootAndRetainsExtensionBindings(t *testing.T) {
	root := t.TempDir()
	childDir := filepath.Join(root, "child")
	if err := os.Mkdir(childDir, 0o700); err != nil {
		t.Fatal(err)
	}
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := ResolveExecutionBindings(context.Background(), root, commands.EntrypointHeadless, selection)
	if err != nil {
		t.Fatal(err)
	}
	child, err := DeriveChildExecutionBindings(context.Background(), parent, childDir, "child-a")
	if err != nil {
		t.Fatal(err)
	}
	childSpec := child.Guest().Policy().Spec()
	if child.ShellHooks() != parent.ShellHooks() || child.StdioMCP() != parent.StdioMCP() ||
		childSpec.Lineage.ParentDigest != parent.Guest().Digest() || childSpec.Entrypoint != containment.EntrypointChildAgent ||
		len(childSpec.WriteRoots) != 1 || childSpec.WriteRoots[0] != childSpec.CWD {
		t.Fatalf("child binding matrix = %#v", childSpec)
	}
	outside := t.TempDir()
	if _, err := DeriveChildExecutionBindings(context.Background(), parent, outside, "escape"); err == nil {
		t.Fatal("child Guest widened its parent write root")
	}
}

func TestP511WorkspaceGuestIncludesRuntimeRootsAndControlPlaneDenies(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := resolveWorkspaceGuestBinding(t.Context(), root, containment.EntrypointHeadless, selection)
	if err != nil {
		t.Fatal(err)
	}
	spec := binding.Policy().Spec()
	canonicalRoot, err := canonicalExistingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{
		filepath.Join(canonicalRoot, ".eino-agent"),
		filepath.Join(canonicalRoot, ".claude", "settings.json"),
		filepath.Join(canonicalRoot, ".claude", "settings.local.json"),
		filepath.Join(canonicalRoot, ".agents", "skills"),
		filepath.Join(canonicalRoot, ".codex", "agents"),
		filepath.Join(canonicalRoot, ".git", "config"),
		filepath.Join(canonicalRoot, ".git", "hooks"),
	} {
		if !slices.Contains(spec.DeniedRoots, denied) {
			t.Fatalf("control-plane deny %q missing from %#v", denied, spec.DeniedRoots)
		}
	}
	if goroot, gorootErr := canonicalExistingDirectory(goToolchainRoot()); gorootErr == nil && !slices.Contains(spec.ReadRoots, goroot) {
		t.Fatalf("Go toolchain root %q missing from read roots %#v", goroot, spec.ReadRoots)
	}
	tempRoot, err := canonicalExistingDirectory(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.TempRoots, tempRoot) {
		t.Fatalf("temporary root %q missing from writable temp roots %#v", tempRoot, spec.TempRoots)
	}
}

func TestP511RestoreReprobesSameSeatbeltRoot(t *testing.T) {
	root := t.TempDir()
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := ResolveExecutionBindings(t.Context(), root, commands.EntrypointHeadless, selection)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DeriveRestoredExecutionBindings(t.Context(), parent, root, "same-root")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Guest().Digest() == parent.Guest().Digest() {
		t.Fatal("same-root restore reused the old Guest binding without a new probe identity")
	}
	if restored.Guest().Policy().Spec().Lineage.ParentDigest != parent.Guest().Digest() {
		t.Fatal("same-root restore lost parent binding lineage")
	}
	if restored.ShellHooks().Digest() != parent.ShellHooks().Digest() || restored.StdioMCP().Digest() != parent.StdioMCP().Digest() {
		t.Fatal("same-root restore replaced independent extension bindings")
	}
}

func TestP511RestoreRejectsSamePathRootReplacement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin root identity is required")
	}
	parentDir := t.TempDir()
	root := filepath.Join(parentDir, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := ResolveExecutionBindings(t.Context(), root, commands.EntrypointHeadless, selection)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(parentDir, "workspace-old")
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveRestoredExecutionBindings(t.Context(), parent, root, "replaced-root"); err == nil {
		t.Fatal("same-path root replacement produced a restored Guest binding")
	}
}

func TestP511DarwinGuestRunsGoAndProtectsControlPlaneWrites(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real Seatbelt acceptance requires Darwin")
	}
	t.Setenv("P511_EXACT_ENV", "inherited=value")
	root := t.TempDir()
	t.Setenv("GOCACHE", filepath.Join(root, ".cache", "go-build"))
	gitInit := exec.Command("git", "init", "-q", root)
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for path, contents := range map[string]string{
		"Makefile":     "test:\n\tgo test ./...\n",
		"go.mod":       "module example.com/p511\n\ngo 1.26\n",
		"main.go":      "package p511\n\nfunc answer() int { return 42 }\n",
		"main_test.go": "package p511\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(answer()) } }\n",
		".eino-agent/transcripts/foreign-session.workboard.json": "workboard sentinel\n",
		".claude/settings.json":                                  "{}\n",
		".claude/settings.local.json":                            "{}\n",
		".git/hooks/pre-commit":                                  "#!/bin/sh\nexit 0\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	selection, err := NewSandboxSelection(containment.ProfileWorkspaceWrite, containment.SelectionDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "p511-real-product", CWD: root,
		SandboxSelection: selection, CommandEntrypoint: commands.EntrypointHeadless,
	})
	defer eng.Close()
	guest := eng.ExecutionBindingMatrix().Guest()
	if guest.Availability() != containment.BindingAvailable {
		t.Fatalf("real Darwin Guest unavailable: %#v", guest.Diagnostic())
	}
	canonicalRoot := guest.Policy().Spec().CWD
	for name, command := range map[string]string{
		"background write": "(printf background > background.tmp) & wait; mv background.tmp background.txt",
		"environment":      `[ "$P511_EXACT_ENV" = "inherited=value" ]`,
		"git read":         "git --no-optional-locks status --short",
		"go test":          "go test ./...",
		"make":             "make test",
	} {
		result, executeErr := eng.shellManager.ExecuteAt(t.Context(), "p511-product", canonicalRoot, command, 30*time.Second)
		if executeErr != nil || result.ExitCode != 0 {
			t.Fatalf("%s failed: result=%#v err=%v", name, result, executeErr)
		}
	}
	if contents, err := os.ReadFile(filepath.Join(root, "background.txt")); err != nil || string(contents) != "background" {
		t.Fatalf("background create/rename = %q, %v", contents, err)
	}
	for _, path := range []string{
		".eino-agent/transcripts/foreign-session.workboard.json",
		".claude/settings.json",
		".claude/settings.local.json",
		".git/config",
		".git/hooks/pre-commit",
	} {
		before, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		command := fmt.Sprintf("printf changed > %q", filepath.ToSlash(path))
		result, executeErr := eng.shellManager.ExecuteAt(t.Context(), "p511-product", canonicalRoot, command, 5*time.Second)
		if executeErr != nil {
			t.Fatalf("control-plane write %s lost persistent shell: %v", path, executeErr)
		}
		if result.ExitCode == 0 {
			t.Fatalf("control-plane write %s succeeded: %#v", path, result)
		}
		after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("control-plane path %s changed from %q to %q", path, before, after)
		}
	}
}

func TestP420EngineBindsOneDisabledPolicyToProcessOwners(t *testing.T) {
	cwd := t.TempDir()
	policy := ResolveDisabledExecutionPolicy(cwd, commands.EntrypointHeadless)
	hookExecutor := hooks.NewExecutor()
	shellManager := tools.NewShellManager()
	mcpManager := tools.NewMCPToolManager()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:         "p42-root",
		CWD:               cwd,
		CommandEntrypoint: commands.EntrypointHeadless,
		ExecutionPolicy:   policy,
		HookExecutor:      hookExecutor,
		ShellManager:      shellManager,
		MCPManager:        mcpManager,
	})
	defer engine.Close()

	digest := policy.Digest()
	if digest == "" || engine.ExecutionPolicySnapshot().Digest() != digest {
		t.Fatalf("engine execution policy = %q, want %q", engine.ExecutionPolicySnapshot().Digest(), digest)
	}
	if shellManager.ExecutionPolicyDigest() != digest ||
		hookExecutor.ExecutionPolicyDigest() != digest ||
		mcpManager.ExecutionPolicyDigest() != digest {
		t.Fatalf(
			"owner digests shell=%q hook=%q mcp=%q, want %q",
			shellManager.ExecutionPolicyDigest(),
			hookExecutor.ExecutionPolicyDigest(),
			mcpManager.ExecutionPolicyDigest(),
			digest,
		)
	}
	if err := shellManager.BindExecutionPolicy(nil); err == nil {
		t.Fatal("shell manager accepted nil policy replacement")
	}
	if err := hookExecutor.BindExecutionPolicy(nil); err == nil {
		t.Fatal("hook executor accepted nil policy replacement")
	}
	if err := mcpManager.BindExecutionPolicy(nil); err == nil {
		t.Fatal("MCP manager accepted nil policy replacement")
	}
	diagnostic := policy.Diagnostic()
	if diagnostic.Profile != containment.ProfileDangerFullAccess ||
		diagnostic.State != containment.StateDisabled {
		t.Fatalf("compatibility diagnostic = %#v", diagnostic)
	}
}

func TestP420PermissionModesAndExactGrantDoNotChangePolicy(t *testing.T) {
	cwd := t.TempDir()
	policy := ResolveDisabledExecutionPolicy(cwd, commands.EntrypointHeadless)
	modes := []permission.Mode{
		permission.ModeDefault,
		permission.ModePlan,
		permission.ModeAcceptEdits,
		permission.ModeAuto,
		permission.ModeBypassPermissions,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			approvals := permission.NewApprovalTracker()
			approvals.Approve(permission.ApprovalKey{
				ToolName: "Bash", CommandPattern: "printf ok", ExactCommand: true,
			}, "user", true)
			engine := NewQueryEngine(QueryEngineConfig{
				SessionID:         "p42-" + string(mode),
				CWD:               cwd,
				CommandEntrypoint: commands.EntrypointHeadless,
				ExecutionPolicy:   policy,
				PermissionMode:    mode,
				ApprovalTracker:   approvals,
			})
			defer engine.Close()
			if got := engine.ExecutionPolicySnapshot().Digest(); got != policy.Digest() {
				t.Fatalf("policy changed under mode/grant: %q != %q", got, policy.Digest())
			}
		})
	}
}

func TestP420EntrypointsAndConcurrentChildDerivation(t *testing.T) {
	cwd := t.TempDir()
	entrypoints := []commands.Entrypoint{
		commands.EntrypointTUI,
		commands.EntrypointPlain,
		commands.EntrypointHeadless,
		commands.EntrypointHeadlessGoal,
		commands.EntrypointACP,
	}
	seen := make(map[string]struct{}, len(entrypoints))
	for _, entrypoint := range entrypoints {
		policy := ResolveDisabledExecutionPolicy(cwd, entrypoint)
		if policy.Diagnostic().State != containment.StateDisabled {
			t.Fatalf("%s state = %q", entrypoint, policy.Diagnostic().State)
		}
		seen[policy.Digest()] = struct{}{}
	}
	if len(seen) != len(entrypoints) {
		t.Fatalf("entrypoint identities collapsed: %d digests for %d roots", len(seen), len(entrypoints))
	}

	parent := ResolveDisabledExecutionPolicy(cwd, commands.EntrypointHeadless)
	executor := &SubAgentExecutor{CWD: cwd, ExecutionPolicy: parent}
	const workers = 32
	digests := make(chan string, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			child, err := executor.childExecutionPolicy(context.Background(), cwd, "child-a")
			if err != nil {
				errs <- err
				return
			}
			digests <- child.Digest()
		}()
	}
	wait.Wait()
	close(errs)
	close(digests)
	for err := range errs {
		t.Fatal(err)
	}
	var want string
	for digest := range digests {
		if want == "" {
			want = digest
		}
		if digest != want {
			t.Fatalf("concurrent child digest = %q, want %q", digest, want)
		}
	}
	child, err := executor.childExecutionPolicy(context.Background(), cwd, "child-a")
	if err != nil {
		t.Fatal(err)
	}
	spec := child.Spec()
	if spec.Entrypoint != containment.EntrypointChildAgent ||
		spec.Lineage.ParentDigest != parent.Digest() ||
		spec.Lineage.ChildID == "" {
		t.Fatalf("child lineage = %#v", spec.Lineage)
	}
}

func TestP420ChildPolicyRequiresExplicitParentAuthority(t *testing.T) {
	cwd := t.TempDir()
	missingParent := &SubAgentExecutor{CWD: cwd}
	if _, err := missingParent.childExecutionPolicy(
		context.Background(),
		cwd,
		"missing-parent",
	); err == nil {
		t.Fatal("child execution synthesized authority without a parent policy")
	}

	constructed := NewSubAgentExecutor(nil, nil, cwd)
	child, err := constructed.childExecutionPolicy(
		context.Background(),
		cwd,
		"constructor-child",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := child.Spec().Lineage.ParentDigest, constructed.ExecutionBindings.Guest().Digest(); got != want {
		t.Fatalf("constructor child parent digest = %q, want %q", got, want)
	}
}

func TestP420ChildReusesOnlyItsParentBoundMCPManager(t *testing.T) {
	rootCWD := t.TempDir()
	childCWD := t.TempDir()
	manager := tools.NewMCPToolManager()
	root := NewQueryEngine(QueryEngineConfig{
		CWD:               rootCWD,
		CommandEntrypoint: commands.EntrypointHeadless,
		ExecutionPolicy:   ResolveDisabledExecutionPolicy(rootCWD, commands.EntrypointHeadless),
		MCPManager:        manager,
	})
	defer root.Close()

	childBindings, err := DeriveChildExecutionBindings(context.Background(), root.ExecutionBindingMatrix(), childCWD, "child-mcp")
	if err != nil {
		t.Fatal(err)
	}
	child := NewQueryEngine(QueryEngineConfig{
		CWD:               childCWD,
		ExecutionBindings: childBindings,
		MCPManager:        manager,
	})
	defer child.Close()
	if got, want := manager.ExecutionBindingDigest(), root.ExecutionBindingMatrix().StdioMCP().Digest(); got != want {
		t.Fatalf("shared parent MCP binding = %q, want %q", got, want)
	}

	t.Run("unrelated root fails closed", func(t *testing.T) {
		unrelated := tools.NewMCPToolManager()
		unrelatedPolicy := ResolveDisabledExecutionPolicy(rootCWD, commands.EntrypointACP)
		if err := unrelated.BindExecutionPolicy(unrelatedPolicy); err != nil {
			t.Fatal(err)
		}
		defer func() {
			recovered := recover()
			if recovered == nil || !strings.Contains(fmt.Sprint(recovered), "stdio MCP binding replacement rejected") {
				t.Fatalf("unrelated manager construction panic = %v", recovered)
			}
		}()
		_ = NewQueryEngine(QueryEngineConfig{
			CWD:               childCWD,
			ExecutionBindings: childBindings,
			MCPManager:        unrelated,
		})
	})
}
