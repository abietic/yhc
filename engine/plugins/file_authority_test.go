package plugins

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/skills"
)

func requireSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func dispatchPluginCommand(
	t *testing.T,
	registry *commands.Registry,
	entrypoint commands.Entrypoint,
	input string,
) string {
	t.Helper()
	result, err := registry.Dispatch(
		context.Background(),
		entrypoint,
		&commands.CommandContext{},
		input,
	)
	if err != nil {
		t.Fatalf("dispatch %q: %v", input, err)
	}
	return result.Output
}

func TestNormalizePluginLocalPathPortableRules(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "slash relative", input: "commands/review.md", want: "commands/review.md"},
		{name: "backslash relative", input: `commands\review.md`, want: "commands/review.md"},
		{name: "clean internal parent", input: "commands/../review.md", want: "review.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizePluginLocalPath(test.input)
			if err != nil {
				t.Fatalf("normalize %q: %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalize %q = %q, want %q", test.input, got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "empty"},
		{name: "unix absolute", input: "/tmp/review.md"},
		{name: "drive absolute", input: `C:\tmp\review.md`},
		{name: "drive relative", input: `C:review.md`},
		{name: "UNC", input: `\\server\share\review.md`},
		{name: "parent", input: "../review.md"},
		{name: "cleaned parent", input: "commands/../../review.md"},
		{name: "NUL", input: "commands/review\x00.md"},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			if got, err := normalizePluginLocalPath(test.input); err == nil {
				t.Fatalf("normalize %q unexpectedly succeeded as %q", test.input, got)
			}
		})
	}
}

func TestLoaderRejectsManifestSymlinkOutsidePluginRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.json")
	writeFile(t, outside, `{"name":"outside"}`)
	pluginDir := filepath.Join(root, "linked-manifest")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, "../outside.json", filepath.Join(pluginDir, "plugin.json"))

	loader := NewLoader(root)
	err := loader.Load()
	if err == nil {
		t.Fatal("outside manifest symlink was accepted")
	}
	if strings.Contains(err.Error(), `{"name":"outside"}`) {
		t.Fatalf("manifest bytes leaked through diagnostic: %v", err)
	}
	if len(loader.List()) != 0 {
		t.Fatalf("rejected manifest changed live loader: %#v", loader.List())
	}
}

func TestLoaderCommandSymlinkPolicy(t *testing.T) {
	t.Run("contained relative accepted", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "contained")
		writePluginManifest(t, pluginDir, `{
			"name":"contained",
			"commands":[{"name":"review","filePath":"commands/review.md"}]
		}`)
		writeFile(t, filepath.Join(pluginDir, "templates", "body.md"), "contained body")
		if err := os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755); err != nil {
			t.Fatal(err)
		}
		requireSymlink(
			t,
			"../templates/body.md",
			filepath.Join(pluginDir, "commands", "review.md"),
		)

		registry := commands.NewRegistry()
		loader := NewLoader(root)
		if err := loader.RegisterCommands(registry); err != nil {
			t.Fatalf("register contained command link: %v", err)
		}
		if output := dispatchPluginCommand(
			t,
			registry,
			commands.EntrypointTUI,
			"/contained:review",
		); output != "contained body" {
			t.Fatalf("contained command output = %q", output)
		}
	})

	t.Run("outside rejected without bytes", func(t *testing.T) {
		root := t.TempDir()
		const secret = "outside-secret-prompt"
		writeFile(t, filepath.Join(root, "outside.md"), secret)
		pluginDir := filepath.Join(root, "outside-link")
		writePluginManifest(t, pluginDir, `{
			"name":"outside-link",
			"commands":[{"name":"review","filePath":"commands/review.md"}]
		}`)
		if err := os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755); err != nil {
			t.Fatal(err)
		}
		requireSymlink(
			t,
			"../../outside.md",
			filepath.Join(pluginDir, "commands", "review.md"),
		)

		candidate, err := NewLoader(root).BuildCommandGeneration()
		if err == nil {
			t.Fatal("outside command link was accepted")
		}
		for _, diagnostic := range candidate.Diagnostics {
			if strings.Contains(diagnostic.Message, secret) {
				t.Fatalf("outside bytes leaked through diagnostic: %#v", diagnostic)
			}
		}
	})

	t.Run("broken link rejected", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "broken-link")
		writePluginManifest(t, pluginDir, `{
			"name":"broken-link",
			"commands":[{"name":"review","filePath":"review.md"}]
		}`)
		requireSymlink(t, "missing.md", filepath.Join(pluginDir, "review.md"))
		if _, err := NewLoader(root).BuildCommandGeneration(); err == nil {
			t.Fatal("broken command link was accepted")
		}
	})

	t.Run("absolute contained target rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("absolute symlink semantics are platform-specific")
		}
		root := t.TempDir()
		pluginDir := filepath.Join(root, "absolute-link")
		writePluginManifest(t, pluginDir, `{
			"name":"absolute-link",
			"commands":[{"name":"review","filePath":"review.md"}]
		}`)
		target := filepath.Join(pluginDir, "body.md")
		writeFile(t, target, "inside")
		requireSymlink(t, target, filepath.Join(pluginDir, "review.md"))

		if _, err := NewLoader(root).BuildCommandGeneration(); err == nil {
			t.Fatal("absolute symlink was accepted")
		}
	})

	t.Run("regular hard link accepted", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "hard-link")
		writePluginManifest(t, pluginDir, `{
			"name":"hard-link",
			"commands":[{"name":"review","filePath":"review.md"}]
		}`)
		target := filepath.Join(root, "hard-link-source.md")
		writeFile(t, target, "hard-linked body")
		if err := os.Link(target, filepath.Join(pluginDir, "review.md")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}

		registry := commands.NewRegistry()
		if err := NewLoader(root).RegisterCommands(registry); err != nil {
			t.Fatalf("register hard-linked command: %v", err)
		}
		if output := dispatchPluginCommand(
			t,
			registry,
			commands.EntrypointTUI,
			"/hard-link:review",
		); output != "hard-linked body" {
			t.Fatalf("hard-linked command output = %q", output)
		}
	})
}

func TestLoaderRejectsNonRegularManifestAndCommand(t *testing.T) {
	t.Run("manifest directory", func(t *testing.T) {
		root := t.TempDir()
		manifestPath := filepath.Join(root, "bad-manifest", "plugin.json")
		if err := os.MkdirAll(manifestPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := NewLoader(root).Load(); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("non-regular manifest error = %v", err)
		}
	})

	t.Run("command directory", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "bad-command")
		writePluginManifest(t, pluginDir, `{
			"name":"bad-command",
			"commands":[{"name":"review","filePath":"review.md"}]
		}`)
		if err := os.MkdirAll(filepath.Join(pluginDir, "review.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLoader(root).BuildCommandGeneration(); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("non-regular command error = %v", err)
		}
	})
}

func TestPluginAuthorityPinsOpenedFileDescriptor(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "stable")
	writePluginManifest(t, pluginDir, `{"name":"stable"}`)
	commandPath := filepath.Join(pluginDir, "review.md")
	writeFile(t, commandPath, "stable bytes")
	const outsideBytes = "outside replacement bytes"
	writeFile(t, filepath.Join(root, "outside.md"), outsideBytes)

	source, err := openPluginSourceAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	authority, err := source.openPlugin("stable")
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	opened, err := authority.openRegularFile("review.md")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()

	if err := os.Rename(commandPath, commandPath+".old"); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, "../outside.md", commandPath)
	data, err := io.ReadAll(opened)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "stable bytes" {
		t.Fatalf("opened descriptor read %q", data)
	}
	if escaped, err := authority.readRegularFile("review.md"); err == nil {
		t.Fatalf("replacement outside link was read: %q", escaped)
	} else if strings.Contains(err.Error(), outsideBytes) {
		t.Fatalf("replacement bytes leaked through error: %v", err)
	}
}

func TestPluginAuthorityPinsOpenedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot reliably rename an open directory")
	}
	root := t.TempDir()
	pluginDir := filepath.Join(root, "stable")
	writePluginManifest(t, pluginDir, `{
		"name":"stable",
		"commands":[{"name":"review","filePath":"review.md"}]
	}`)
	writeFile(t, filepath.Join(pluginDir, "review.md"), "original")

	source, err := openPluginSourceAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	authority, err := source.openPlugin("stable")
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()

	if err := os.Rename(pluginDir, pluginDir+".moved"); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, pluginDir, `{
		"name":"replacement",
		"commands":[{"name":"review","filePath":"review.md"}]
	}`)
	writeFile(t, filepath.Join(pluginDir, "review.md"), "replacement")

	plugin, err := NewLoader().loadPluginFromAuthority(authority)
	if err != nil {
		t.Fatalf("load through pinned directory: %v", err)
	}
	if plugin.Name != "stable" ||
		len(plugin.materializedCommands) != 1 ||
		plugin.materializedCommands[0].err != nil {
		t.Fatalf("pinned plugin = %#v", plugin)
	}
	result, err := plugin.materializedCommands[0].command.Execute(
		context.Background(),
		&commands.CommandContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "original" {
		t.Fatalf("pinned directory output = %q", result.Output)
	}
}

func TestLoaderIgnoresPluginChildDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writePluginManifest(t, filepath.Join(outside, "linked"), `{"name":"linked"}`)
	requireSymlink(t, filepath.Join(outside, "linked"), filepath.Join(root, "linked"))

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("load root containing child symlink: %v", err)
	}
	if len(loader.List()) != 0 {
		t.Fatalf("child directory symlink was discovered: %#v", loader.List())
	}
}

func TestPluginSourceRejectsChildReplacementWithContainedDirectoryLink(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "stable")
	writePluginManifest(t, pluginDir, `{"name":"stable"}`)
	replacementDir := filepath.Join(root, "replacement")
	writePluginManifest(t, replacementDir, `{"name":"replacement"}`)

	source, err := openPluginSourceAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	entries, err := source.readDir()
	if err != nil {
		t.Fatal(err)
	}
	var expected os.FileInfo
	for _, entry := range entries {
		if entry.Name() != "stable" {
			continue
		}
		expected, err = entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if expected == nil {
		t.Fatal("stable plugin entry not found")
	}

	if err := os.Rename(pluginDir, pluginDir+".moved"); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, "replacement", pluginDir)
	if authority, err := source.openPluginWithExpectedIdentity(
		"stable",
		expected,
	); err == nil {
		_ = authority.Close()
		t.Fatal("contained child-directory replacement link was accepted")
	}
}

func TestConfiguredRootSymlinkBindsOpenedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root symlink replacement requires Unix link semantics")
	}
	parent := t.TempDir()
	first := t.TempDir()
	second := t.TempDir()
	writePluginManifest(t, filepath.Join(first, "stable"), `{"name":"stable"}`)
	writePluginManifest(t, filepath.Join(second, "replacement"), `{"name":"replacement"}`)
	configured := filepath.Join(parent, "configured")
	requireSymlink(t, first, configured)

	source, err := openPluginSourceAuthority(configured)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.Remove(configured); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, second, configured)

	entries, err := source.readDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "stable" {
		t.Fatalf("opened configured root followed replacement: %#v", entries)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	authority, err := source.openPluginWithExpectedIdentity("stable", info)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	plugin, err := NewLoader().loadPluginFromAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Name != "stable" {
		t.Fatalf("configured root rebound to %q", plugin.Name)
	}
}

func TestLoaderAcceptsBackslashCommandRelativePath(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "portable")
	writePluginManifest(t, pluginDir, `{
		"name":"portable",
		"commands":[{"name":"review","filePath":"commands\\review.md"}]
	}`)
	writeFile(t, filepath.Join(pluginDir, "commands", "review.md"), "portable body")

	registry := commands.NewRegistry()
	if err := NewLoader(root).RegisterCommands(registry); err != nil {
		t.Fatalf("register backslash-relative command: %v", err)
	}
	if output := dispatchPluginCommand(
		t,
		registry,
		commands.EntrypointPlain,
		"/portable:review",
	); output != "portable body" {
		t.Fatalf("portable command output = %q", output)
	}
}

func TestLoaderRegisterSkillsSymlinkAndAtomicity(t *testing.T) {
	t.Run("contained explicit file accepted", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "contained-skill")
		writePluginManifest(t, pluginDir, `{
			"name":"contained-skill",
			"skills":[{"name":"review","filePath":"links/review.md"}]
		}`)
		writeFile(
			t,
			filepath.Join(pluginDir, "skills-src", "review.md"),
			"---\nname: review\n---\ncontained skill",
		)
		if err := os.MkdirAll(filepath.Join(pluginDir, "links"), 0o755); err != nil {
			t.Fatal(err)
		}
		requireSymlink(
			t,
			"../skills-src/review.md",
			filepath.Join(pluginDir, "links", "review.md"),
		)
		loader := NewLoader(root)
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		registry := skills.NewSkillRegistry()
		if err := loader.RegisterSkills(registry); err != nil {
			t.Fatalf("register contained skill link: %v", err)
		}
		if skill, ok := registry.Get("review"); !ok ||
			!strings.Contains(skill.Content, "contained skill") {
			t.Fatalf("contained skill = %#v ok=%v", skill, ok)
		}
	})

	t.Run("contained explicit directory accepted", func(t *testing.T) {
		root := t.TempDir()
		pluginDir := filepath.Join(root, "contained-skill-dir")
		writePluginManifest(t, pluginDir, `{
			"name":"contained-skill-dir",
			"skills":[{"name":"bundle","filePath":"bundle"}]
		}`)
		writeFile(
			t,
			filepath.Join(pluginDir, "skill-source", "review.md"),
			"---\nname: review-dir\n---\ncontained directory",
		)
		requireSymlink(t, "skill-source", filepath.Join(pluginDir, "bundle"))
		loader := NewLoader(root)
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		registry := skills.NewSkillRegistry()
		if err := loader.RegisterSkills(registry); err != nil {
			t.Fatalf("register contained skill directory link: %v", err)
		}
		if skill, ok := registry.Get("review-dir"); !ok ||
			!strings.Contains(skill.Content, "contained directory") {
			t.Fatalf("contained directory skill = %#v ok=%v", skill, ok)
		}
	})

	for _, test := range []struct {
		name     string
		manifest string
		prepare  func(t *testing.T, root, pluginDir string)
	}{
		{
			name: "explicit file outside",
			manifest: `{
				"name":"bad-skill",
				"skills":[{"name":"escape","filePath":"escape.md"}]
			}`,
			prepare: func(t *testing.T, root, pluginDir string) {
				writeFile(
					t,
					filepath.Join(root, "outside.md"),
					"---\nname: escape\n---\noutside",
				)
				requireSymlink(
					t,
					"../outside.md",
					filepath.Join(pluginDir, "escape.md"),
				)
			},
		},
		{
			name: "explicit directory outside",
			manifest: `{
				"name":"bad-skill",
				"skills":[{"name":"bundle","filePath":"bundle"}]
			}`,
			prepare: func(t *testing.T, root, pluginDir string) {
				outside := t.TempDir()
				writeFile(
					t,
					filepath.Join(outside, "escape.md"),
					"---\nname: escape\n---\noutside",
				)
				relative, err := filepath.Rel(pluginDir, outside)
				if err != nil {
					t.Fatal(err)
				}
				requireSymlink(t, relative, filepath.Join(pluginDir, "bundle"))
			},
		},
		{
			name:     "default directory outside",
			manifest: `{"name":"bad-skill"}`,
			prepare: func(t *testing.T, root, pluginDir string) {
				outside := t.TempDir()
				writeFile(
					t,
					filepath.Join(outside, "escape.md"),
					"---\nname: escape\n---\noutside",
				)
				relative, err := filepath.Rel(pluginDir, outside)
				if err != nil {
					t.Fatal(err)
				}
				requireSymlink(t, relative, filepath.Join(pluginDir, "skills"))
			},
		},
		{
			name:     "nested outside link after valid file",
			manifest: `{"name":"bad-skill"}`,
			prepare: func(t *testing.T, root, pluginDir string) {
				writeFile(
					t,
					filepath.Join(pluginDir, "skills", "a-valid.md"),
					"---\nname: valid\n---\nvalid",
				)
				writeFile(
					t,
					filepath.Join(root, "outside.md"),
					"---\nname: escape\n---\noutside",
				)
				requireSymlink(
					t,
					"../../outside.md",
					filepath.Join(pluginDir, "skills", "z-outside.md"),
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			pluginDir := filepath.Join(root, "bad-skill")
			writePluginManifest(t, pluginDir, test.manifest)
			test.prepare(t, root, pluginDir)
			loader := NewLoader(root)
			if err := loader.Load(); err != nil {
				t.Fatalf("Load failed: %v", err)
			}
			registry := skills.NewSkillRegistry()
			registry.Register(&skills.Skill{Name: "sentinel", Content: "keep"})
			if err := loader.RegisterSkills(registry); err == nil {
				t.Fatal("outside skill link was accepted")
			}
			snapshot := registry.Snapshot()
			if len(snapshot.Skills) != 1 ||
				snapshot.Skills[0].Name != "sentinel" ||
				len(snapshot.Diagnostics) != 0 {
				t.Fatalf("failed skill registration partially mutated registry: %#v", snapshot)
			}
		})
	}
}

func TestLoaderRegisterSkillsRejectsDirectoryIdentityReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot reliably rename an open directory")
	}
	root := t.TempDir()
	pluginDir := filepath.Join(root, "identity")
	writePluginManifest(t, pluginDir, `{
		"name":"identity",
		"skills":[{"name":"stable","filePath":"stable.md"}]
	}`)
	writeFile(
		t,
		filepath.Join(pluginDir, "stable.md"),
		"---\nname: stable\n---\nstable",
	)
	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(pluginDir, pluginDir+".old"); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, pluginDir, `{
		"name":"identity",
		"skills":[{"name":"replacement","filePath":"replacement.md"}]
	}`)
	writeFile(
		t,
		filepath.Join(pluginDir, "replacement.md"),
		"---\nname: replacement\n---\nreplacement",
	)

	registry := skills.NewSkillRegistry()
	if err := loader.RegisterSkills(registry); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("identity replacement error = %v", err)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("identity replacement partially registered skills: %#v", registry.List())
	}
}

func TestLoaderInvalidHigherPrecedenceRetainsLiveGeneration(t *testing.T) {
	low := t.TempDir()
	high := filepath.Join(t.TempDir(), "plugins")
	lowPlugin := filepath.Join(low, "shared")
	writePluginManifest(t, lowPlugin, `{
		"name":"shared",
		"version":"1",
		"commands":[{"name":"review","filePath":"review.md"}]
	}`)
	writeFile(t, filepath.Join(lowPlugin, "review.md"), "low")

	loader := NewLoader(low, high)
	registry := commands.NewRegistry()
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	before := registry.PromptCommandGeneration()
	if output := dispatchPluginCommand(
		t,
		registry,
		commands.EntrypointTUI,
		"/shared:review",
	); output != "low" {
		t.Fatalf("initial output = %q", output)
	}

	highPlugin := filepath.Join(high, "shared")
	writePluginManifest(t, highPlugin, `{
		"name":"shared",
		"version":"2",
		"commands":[{"name":"review","filePath":"../outside.md"}]
	}`)
	writeFile(t, filepath.Join(high, "outside.md"), "high outside")
	if err := loader.RegisterCommands(registry); err == nil {
		t.Fatal("invalid higher-precedence plugin replaced generation")
	}
	after := registry.PromptCommandGeneration()
	if after.Revision != before.Revision ||
		after.Digest != before.Digest ||
		after.Commands != before.Commands {
		t.Fatalf("generation changed: before=%#v after=%#v", before, after)
	}
	if output := dispatchPluginCommand(
		t,
		registry,
		commands.EntrypointTUI,
		"/shared:review",
	); output != "low" {
		t.Fatalf("retained output = %q", output)
	}
}

func TestPluginGenerationMaterializesBytesAndKeepsEntrypointScope(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "stable")
	writePluginManifest(t, pluginDir, `{
		"name":"stable",
		"commands":[{"name":"review","filePath":"review.md"}]
	}`)
	commandPath := filepath.Join(pluginDir, "review.md")
	writeFile(t, commandPath, "version one")
	loader := NewLoader(root)
	registry := commands.NewRegistry()
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	before := registry.PromptCommandGeneration()

	writeFile(t, commandPath, "version two")
	for _, entrypoint := range []commands.Entrypoint{
		commands.EntrypointTUI,
		commands.EntrypointPlain,
	} {
		if output := dispatchPluginCommand(
			t,
			registry,
			entrypoint,
			"/stable:review",
		); output != "version one" {
			t.Fatalf("%s output before reload = %q", entrypoint, output)
		}
	}
	for _, entrypoint := range []commands.Entrypoint{
		commands.EntrypointACP,
		commands.EntrypointHeadless,
		commands.EntrypointHeadlessGoal,
	} {
		if command := registry.GetFor(entrypoint, "stable:review"); command != nil {
			t.Fatalf("%s unexpectedly exposes plugin command: %#v", entrypoint, command)
		}
	}
	if current := registry.PromptCommandGeneration(); current.Revision != before.Revision ||
		current.Digest != before.Digest {
		t.Fatalf("ambient replacement changed generation: before=%#v current=%#v", before, current)
	}

	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	after := registry.PromptCommandGeneration()
	if after.Revision <= before.Revision || after.Digest == before.Digest {
		t.Fatalf("explicit reload did not publish new material: before=%#v after=%#v", before, after)
	}
	if output := dispatchPluginCommand(
		t,
		registry,
		commands.EntrypointPlain,
		"/stable:review",
	); output != "version two" {
		t.Fatalf("output after reload = %q", output)
	}
}
