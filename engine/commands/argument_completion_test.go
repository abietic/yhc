package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

func TestArgumentCompletionSelectionAndQuotedReplacement(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{Name: "inspect", Completion: []ArgumentCompletion{{Choices: []string{"中文 folder", "other"}}}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return &CommandResult{}, nil }}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"/inspect 中", "/inspect \"中"} {
		result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, &CommandContext{})
		if len(result.Candidates) != 1 {
			t.Fatalf("%q: %+v", input, result)
		}
		accepted := ApplyCompletion(input, result, result.Candidates[0])
		_, args := ParseCommandInput(accepted)
		if len(args) != 1 || args[0] != "中文 folder" {
			t.Fatalf("accepted %q parsed %q", accepted, args)
		}
	}
	input := "/inspect 中 suffix"
	result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune("/inspect 中"))}, &CommandContext{})
	if got := ApplyCompletion(input, result, result.Candidates[0]); got != "/inspect \"中文 folder\" suffix" {
		t.Fatal(got)
	}
}

type completionRuntime struct{}

func (completionRuntime) GetModelName() string { return "profile:fast" }

func (completionRuntime) ModelInventory() provider.RuntimeInventorySnapshot {
	return provider.RuntimeInventorySnapshot{Entries: []provider.RuntimeInventoryEntry{{Selector: "profile:fast", DisplayName: "Fast"}}}
}

func (completionRuntime) ReasoningEffortCapability(context.Context) (bool, string, error) {
	return true, "", nil
}
func (completionRuntime) ReasoningEffort() string { return "low" }
func (completionRuntime) ReasoningEffortOptions(context.Context) ([]string, error) {
	return []string{"low", "high"}, nil
}

func (completionRuntime) MCPInventorySnapshot() tools.MCPInventorySnapshot {
	return tools.MCPInventorySnapshot{Servers: []tools.MCPServerSnapshot{{Name: "local-server"}}}
}

func (completionRuntime) ListResumableSessions(int) ([]session.SessionInfo, error) {
	return []session.SessionInfo{{SessionID: "session-1", CustomTitle: "Fix input"}}, nil
}

func TestArgumentCompletionRuntimeSourcesAndPathScope(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "中文 folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ordinary.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &CommandContext{CWD: root, Engine: completionRuntime{}}
	for input, want := range map[string]string{"/model profile:": "profile:fast", "/effort h": "high", "/mcp local": "local-server", "/sessions resume session": "session-1", "/add-dir 中": "中文 folder" + string(filepath.Separator), "/sessions export session-1 ordinary": "ordinary.txt"} {
		result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, ctx)
		if len(result.Candidates) != 1 || result.Candidates[0].Value != want {
			t.Fatalf("%q: %+v", input, result)
		}
		_, args := ParseCommandInput(ApplyCompletion(input, result, result.Candidates[0]))
		if args[len(args)-1] != want {
			t.Fatalf("wrong dispatch bytes: %q", args)
		}
	}
	input := "/add-dir ordinary"
	if result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, ctx); len(result.Candidates) != 0 {
		t.Fatal("file offered as directory")
	}
	input = "/sessions rename session-1 name"
	if result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, ctx); len(result.Candidates) != 0 {
		t.Fatal("session IDs offered for title argument")
	}
}

func FuzzArgumentCompletionInsertion(f *testing.F) {
	for _, value := range []string{"中文 folder", "a\\b", "quote\"value", "single'quote", "emoji👩‍💻"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return
		}
		input := "/inspect "
		result := CompletionResult{Start: len([]rune(input)), End: len([]rune(input))}
		accepted := ApplyCompletion(input, result, ArgumentCandidate{Value: value})
		_, args, err := parseCommandInputStrict(accepted)
		if err != nil || len(args) != 1 || args[0] != value {
			t.Fatalf("%q -> %q -> %q (%v)", value, accepted, args, err)
		}
	})
}

func TestArgumentCompletionBuiltinsAndSnapshotIsolation(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	for _, input := range []string{"/diff st", "/theme ", "/permissions mode "} {
		result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, &CommandContext{})
		if len(result.Candidates) == 0 {
			t.Fatalf("no candidates for %q", input)
		}
	}
	cmd := r.Get("diff")
	cmd.Completion[0].Choices[0] = "corrupt"
	if r.Get("diff").Completion[0].Choices[0] == "corrupt" {
		t.Fatal("snapshot mutated registry")
	}
	input := "/diff stat "
	if result := r.CompleteArguments(context.Background(), CompletionRequest{Input: input, Cursor: len([]rune(input))}, &CommandContext{}); len(result.Candidates) != 0 {
		t.Fatal("completed argument reopened")
	}
}
