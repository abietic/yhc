package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputCompletionUsesEntrypointDiscoveryAndSharedArguments(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	ctx := &CommandContext{Engine: completionRuntime{}}
	request := func(input string) CompletionRequest {
		return CompletionRequest{Input: input, Cursor: len([]rune(input))}
	}
	for _, input := range []string{"/theme", "/sessions", "/resume", "/new"} {
		got := r.CompleteInputFor(context.Background(), EntrypointAppServer, request(input), ctx)
		if len(got.Candidates) != 0 {
			t.Fatalf("unsupported app-server command %q offered: %+v", input, got)
		}
	}
	for input, want := range map[string]string{"/mod": "/model ", "/model profile:": "profile:fast ", "/permissions mode def": "default "} {
		got := r.CompleteInputFor(context.Background(), EntrypointAppServer, request(input), ctx)
		if len(got.Candidates) != 1 || got.Candidates[0].Replacement != want {
			t.Fatalf("%q = %+v; want replacement %q", input, got, want)
		}
	}
	ctx.Environment.Phase = CommandPhaseActiveTurn
	if got := r.CompleteInputFor(context.Background(), EntrypointAppServer, request("/mod"), ctx); len(got.Candidates) != 0 {
		t.Fatalf("idle-only command offered during active turn: %+v", got)
	}
}

func TestInputCompletionNeverEnumeratesUnadmittedSources(t *testing.T) {
	r := NewRegistry()
	for _, source := range []string{"files", "directories", "sessions", "future-source"} {
		if err := r.Register(&Command{Name: source, Entrypoints: EntrypointsACP | EntrypointsTUI, Completion: []ArgumentCompletion{{Source: source}}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return &CommandResult{}, nil }}); err != nil {
			t.Fatal(err)
		}
		input := "/" + source + " "
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "private-sentinel"), 0o700); err != nil {
			t.Fatal(err)
		}
		commandContext := &CommandContext{CWD: root, Engine: completionRuntime{}}
		request := CompletionRequest{Input: input, Cursor: len([]rune(input))}
		got := r.CompleteInputFor(context.Background(), EntrypointAppServer, request, commandContext)
		if len(got.Candidates) != 0 {
			t.Fatalf("source %q enumerated: %+v", source, got)
		}
		if source != "future-source" {
			local := r.CompleteInputFor(context.Background(), EntrypointTUI, request, commandContext)
			if len(local.Candidates) == 0 {
				t.Fatalf("local TUI source %q unexpectedly suppressed", source)
			}
		}
	}
}

func TestInputCompletionBoundsAndCancellation(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	for _, req := range []CompletionRequest{{Input: "/mod", Cursor: -1}, {Input: "/mod", Cursor: 20}, {Input: "/mod\n", Cursor: 4}, {Input: "/" + strings.Repeat("a", 2048), Cursor: 1}, {Input: "/\xff", Cursor: 1}} {
		if got := r.CompleteInputFor(context.Background(), EntrypointAppServer, req, nil); len(got.Candidates) != 0 {
			t.Fatalf("invalid input produced candidates: %+v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := r.CompleteInputFor(ctx, EntrypointAppServer, CompletionRequest{Input: "/", Cursor: 1}, nil); len(got.Candidates) != 0 {
		t.Fatalf("cancelled input produced candidates: %+v", got)
	}
}
