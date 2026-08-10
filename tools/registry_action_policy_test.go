package tools

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRegistryResolveReturnsCanonicalAliasSnapshot(t *testing.T) {
	registry := NewRegistry()
	implementation := ToolImpl{
		Info:    &schema.ToolInfo{Name: "canonical", Desc: "test tool"},
		Aliases: []string{"alias"},
		Capabilities: ToolCapabilities{
			Declared:   true,
			Origin:     ToolOriginBuiltin,
			ActionKind: ToolActionRead,
		},
	}
	registry.Register(implementation)

	resolved := registry.Resolve("alias")
	if resolved.RequestedName != "alias" || resolved.CanonicalName != "canonical" {
		t.Fatalf("alias resolution names = requested %q, canonical %q", resolved.RequestedName, resolved.CanonicalName)
	}
	if !resolved.Registered || !resolved.Enabled {
		t.Fatalf("alias resolution availability = registered %t, enabled %t", resolved.Registered, resolved.Enabled)
	}
	if resolved.Implementation.Info != implementation.Info {
		t.Fatal("alias resolution did not retain the registered implementation")
	}
	if resolved.Generation != registry.Generation() {
		t.Fatalf("alias resolution generation = %d, registry generation = %d", resolved.Generation, registry.Generation())
	}

	registry.Disable("canonical")
	resolved = registry.Resolve("alias")
	if !resolved.Registered || resolved.Enabled {
		t.Fatalf("disabled alias resolution availability = registered %t, enabled %t", resolved.Registered, resolved.Enabled)
	}
	if resolved.CanonicalName != "canonical" {
		t.Fatalf("disabled alias canonical name = %q", resolved.CanonicalName)
	}

	registry.Enable("alias")
	updated := implementation
	updated.Info = &schema.ToolInfo{Name: "canonical", Desc: "updated"}
	updated.Capabilities.ActionKind = ToolActionWrite
	registry.Update("alias", updated)
	resolved = registry.Resolve("alias")
	if resolved.Implementation.Info.Desc != "updated" ||
		resolved.Implementation.Capabilities.ActionKind != ToolActionWrite {
		t.Fatalf("alias did not observe canonical update: %+v", resolved)
	}

	missing := registry.Resolve("missing")
	if missing.RequestedName != "missing" || missing.Registered || missing.Enabled || missing.CanonicalName != "" {
		t.Fatalf("missing resolution = %+v", missing)
	}
}

func TestRegistryUnregisterByAliasRemovesCanonicalAndEveryAlias(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ToolImpl{
		Info:    &schema.ToolInfo{Name: "canonical"},
		Aliases: []string{"alias-one", "alias-two"},
	})

	registry.Unregister("alias-one")

	for _, name := range []string{"canonical", "alias-one", "alias-two"} {
		if resolution := registry.Resolve(name); resolution.Registered {
			t.Fatalf("%q remains registered after unregister by alias: %+v", name, resolution)
		}
	}
}

func TestRegistryGenerationIncrementsForMutations(t *testing.T) {
	registry := NewRegistry()
	implementation := ToolImpl{Info: &schema.ToolInfo{Name: "mutable", Desc: "test tool"}}

	assertGenerationIncrease := func(operation string, mutate func()) {
		t.Helper()
		before := registry.Generation()
		mutate()
		if after := registry.Generation(); after != before+1 {
			t.Fatalf("%s generation = %d, want %d", operation, after, before+1)
		}
	}

	assertGenerationIncrease("register", func() { registry.Register(implementation) })
	assertGenerationIncrease("disable", func() { registry.Disable("mutable") })
	assertGenerationIncrease("enable", func() { registry.Enable("mutable") })
	assertGenerationIncrease("update", func() { registry.Update("mutable", implementation) })
	assertGenerationIncrease("unregister", func() { registry.Unregister("mutable") })
}

func TestRegisterDefaultsDeclaresCapabilitiesForEveryBuiltin(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)

	for _, name := range registry.Names() {
		resolved := registry.Resolve(name)
		if !resolved.Registered {
			t.Errorf("built-in %q was not registered", name)
			continue
		}
		if !resolved.Implementation.Capabilities.Declared {
			t.Errorf("built-in %q has no declared capabilities", name)
		}
	}
}

func TestMCPRegistrationDeclaresDynamicNetworkCapabilities(t *testing.T) {
	implementation := registeredMCPToolImpl(nil, &MCPToolInfo{
		ServerName:  "demo",
		ToolName:    "status",
		Description: "reports status",
	}, "test-owner")
	capabilities := implementation.Capabilities
	if !capabilities.Declared || capabilities.Origin != ToolOriginMCP || capabilities.ActionKind != ToolActionDynamic || !capabilities.Network || !capabilities.Dynamic {
		t.Fatalf("MCP capabilities = %+v", capabilities)
	}
}
