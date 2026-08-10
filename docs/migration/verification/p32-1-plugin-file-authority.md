# P32.1 Plugin File Authority Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P32.1 only

> **Ownership:** reproducible acceptance evidence for root-bound plugin
> discovery, manifest and command reads, plugin-skill identity, portable paths,
> atomic generation retention, entrypoint exclusions, and G4 closure.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Configured and child roots | A configured-root symlink remains bound after retargeting. Child links are undiscovered, and a real child replaced between enumeration and opening must retain the exact `os.SameFile` identity. |
| Manifest authority | `plugin.json` is opened beneath the child `os.Root`, must be regular on the descriptor read, and an outside link is rejected without leaking its bytes. |
| Portable paths | Slash and backslash relatives normalize to one slash form. Unix absolute, drive absolute/relative, UNC, parent escape, cleaned parent escape, empty, NUL, and invalid local paths reject before opening. |
| Prompt files | Relative contained links and regular hard links are accepted. Absolute, broken, outside, and non-regular command paths reject. An already opened descriptor retains its original bytes after ambient replacement. |
| Directory replacement | A child root acquired before ambient rename/replacement continues to read the original directory; a replacement between enumeration and child acquisition fails identity comparison. |
| Materialization | Prompt closures perform no filesystem read. Ambient file mutation leaves the live output, revision, and digest stable until an explicit successful reload publishes new bytes. |
| Skills | Explicit file and directory contained links load through the child root. Outside explicit/default/nested links reject before the target registry changes. `RegisterSkills` rejects a post-`Load` directory identity replacement. |
| Precedence and rollback | An invalid higher-precedence duplicate rejects the candidate; the exact previous revision, digest, command count, and dispatch output remain live. |
| Entrypoints | TUI and Plain dispatch the configured command. ACP, ordinary headless, and headless Goal discovery remain absent. Standalone MCP still constructs no conversation command or plugin loader. |
| Cross-platform and concurrency | Darwin/Linux descriptor and replacement tests pass, Windows amd64 package tests compile, and the focused plugin/skill race run passes. Windows runtime link behavior is not claimed by the local cross-compile. |
| Review | Independent review found one child-directory replacement-to-contained-link window. The implementation added enumeration/open identity comparison and a deterministic regression; re-review reported no findings. |

## Source Gate

```text
test -z "$(rg -n 'os\\.ReadFile|filepath\\.Walk|registry\\.LoadFromDirectory|skills\\.ParseSkillFile' engine/plugins/loader.go engine/plugins/file_authority.go || true)"
```

The gate prevents the plugin authority path from silently regaining an ambient
read or direct registry traversal. Direct user/project skill loading remains
owned by `engine/skills` and is intentionally outside this source gate.

## Focused Commands

```text
go test ./engine/plugins ./engine/skills -count=1
go test -race ./engine/plugins ./engine/skills -count=1
go test ./engine/plugins -run 'TestNormalizePluginLocalPathPortableRules|TestLoaderRejectsManifestSymlinkOutsidePluginRoot|TestLoaderCommandSymlinkPolicy|TestLoaderRejectsNonRegularManifestAndCommand|TestPluginAuthorityPinsOpenedFileDescriptor|TestPluginAuthorityPinsOpenedDirectory|TestPluginSourceRejectsChildReplacementWithContainedDirectoryLink|TestConfiguredRootSymlinkBindsOpenedTarget|TestLoaderRegisterSkillsSymlinkAndAtomicity|TestLoaderRegisterSkillsRejectsDirectoryIdentityReplacement|TestLoaderInvalidHigherPrecedenceRetainsLiveGeneration|TestPluginGenerationMaterializesBytesAndKeepsEntrypointScope' -count=1
GOOS=windows GOARCH=amd64 go test -c -o /tmp/eino-agent-p32-plugins.test.exe ./engine/plugins
GOOS=windows GOARCH=amd64 go test -c -o /tmp/eino-agent-p32-skills.test.exe ./engine/skills
go test ./...
```

## Repository Closeout

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands passed. GitHub Actions billing or usage failures may be waived
only after the exact job annotation proves that no runner started; they are
never described as green CI.
