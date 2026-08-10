package tools

import (
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func ownedTestTool(name, owner string, aliases ...string) ToolImpl {
	return ToolImpl{
		Info:              &schema.ToolInfo{Name: name},
		Aliases:           aliases,
		RegistrationOwner: owner,
	}
}

func TestRegistryReplaceOwnedToolsIsAtomicOnCollision(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ToolImpl{Info: &schema.ToolInfo{Name: "builtin"}})
	before := registry.Generation()

	_, err := registry.ReplaceOwnedTools(before, nil, []ToolImpl{
		ownedTestTool("candidate", "owner-a"),
		ownedTestTool("builtin", "owner-b"),
	})
	if !errors.Is(err, ErrRegistryNameCollision) {
		t.Fatalf("ReplaceOwnedTools error = %v", err)
	}
	if registry.Generation() != before {
		t.Fatalf(
			"generation changed on rejected batch: got %d, want %d",
			registry.Generation(),
			before,
		)
	}
	if registry.Resolve("candidate").Registered {
		t.Fatal("partial candidate was published")
	}
	if !registry.Resolve("builtin").Registered {
		t.Fatal("existing row was removed by rejected batch")
	}
}

func TestRegistryReplaceOwnedToolsComparesGenerationAndReplacesOneOwner(t *testing.T) {
	registry := NewRegistry()
	generation := registry.Generation()
	generation, err := registry.ReplaceOwnedTools(generation, nil, []ToolImpl{
		ownedTestTool("old", "owner-a", "old-alias"),
		ownedTestTool("other", "owner-b"),
	})
	if err != nil {
		t.Fatal(err)
	}

	stale := generation
	registry.Register(ToolImpl{Info: &schema.ToolInfo{Name: "unrelated"}})
	if _, err := registry.ReplaceOwnedTools(stale, []string{"owner-a"}, []ToolImpl{
		ownedTestTool("new", "owner-a"),
	}); !errors.Is(err, ErrRegistryGenerationChanged) {
		t.Fatalf("stale ReplaceOwnedTools error = %v", err)
	}
	if !registry.Resolve("old").Registered ||
		!registry.Resolve("old-alias").Registered ||
		registry.Resolve("new").Registered {
		t.Fatal("stale compare-and-replace mutated the owner generation")
	}

	current := registry.Generation()
	next, err := registry.ReplaceOwnedTools(current, []string{"owner-a"}, []ToolImpl{
		ownedTestTool("new", "owner-a", "new-alias"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next != current+1 {
		t.Fatalf("replacement generation = %d, want %d", next, current+1)
	}
	for _, removed := range []string{"old", "old-alias"} {
		if registry.Resolve(removed).Registered {
			t.Fatalf("%q remained after owner replacement", removed)
		}
	}
	for _, kept := range []string{"new", "new-alias", "other", "unrelated"} {
		if !registry.Resolve(kept).Registered {
			t.Fatalf("%q missing after owner replacement", kept)
		}
	}
}

func TestRegistryRemoveOwnedToolsCannotDeleteNewerOwner(t *testing.T) {
	registry := NewRegistry()
	generation := registry.Generation()
	_, err := registry.ReplaceOwnedTools(generation, nil, []ToolImpl{
		ownedTestTool("old", "owner-old"),
		ownedTestTool("new", "owner-new"),
	})
	if err != nil {
		t.Fatal(err)
	}

	before := registry.Generation()
	after := registry.RemoveOwnedTools("owner-old")
	if after != before+1 {
		t.Fatalf("remove generation = %d, want %d", after, before+1)
	}
	if registry.Resolve("old").Registered {
		t.Fatal("old owner row remained registered")
	}
	if !registry.Resolve("new").Registered {
		t.Fatal("new owner row was removed")
	}

	if unchanged := registry.RemoveOwnedTools("owner-old"); unchanged != after {
		t.Fatalf("idempotent removal generation = %d, want %d", unchanged, after)
	}
}
