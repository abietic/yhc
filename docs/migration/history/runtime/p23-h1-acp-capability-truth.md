# P23.H1 ACP Capability Truth

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for truthful ACP baseline capabilities and
> fail-closed input admission. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.H1 was delivered under the **`combine`** decision:

- preserve ACP v1, `coder/acp-go-sdk v0.13.5`, QueryEngine, and the verified
  new/resume/list/close session paths;
- adapt capability advertisement to current behavior by disabling load until
  durable replay exists and adding process-owned agent identity;
- add an engine-owned ordered `PromptInput` fallback for the required Text and
  ResourceLink baseline instead of an ACP-only string extractor; and
- reject unsupported rich content, additional directories, MCP setup, and
  non-empty list cursors before model, registry, or filesystem state changes.

## Outcome

`Initialize` now advertises `loadSession=false` and reports the stable
`eino-agent` name, `Eino Agent` title, and current build version. The retained
unadvertised load handler preserves the narrow existing integration path, but
it is not a portable or conformant load claim until P23.4 adds staged durable
replay.

ACP converts ordered Text and ResourceLink blocks into engine-owned
`PromptInput`. Text bytes retain the prior single-newline inter-block rule.
Each ResourceLink becomes one deterministic JSON descriptor inside a
`resource_link` marker, preserving URI, name, title, description, MIME type,
size, and available standard annotations. The descriptor is limited to
16 KiB, never dereferences the URI, and grants no filesystem or network
authority.

Optional image, audio, and embedded-resource blocks return one structured
unsupported-input error. Non-empty additional directories and MCP arrays fail
before new/resume/load can create or register an engine. A non-empty list
cursor fails before store enumeration. Unsupported input uses code `-32006`
and bounded field-only data; malformed prompt input uses standard invalid
params without echoing prompt or resource content.

The pinned Go SDK's agent-side `ContentBlock` unmarshal accepts annotations and
reserved `_meta`, but its generated client-side `ContentBlock.MarshalJSON`
omits both even though the variant type contains those fields. The adapter
preserves received standard annotations and excludes reserved `_meta`; this
slice does not patch or upgrade the SDK. P23.1 must retain this exact
client/server asymmetry instead of generalizing from the generated struct
alone.

## Evidence

Engine fixtures prove:

- ResourceLink-only input renders a non-empty deterministic descriptor;
- mixed Text/ResourceLink input preserves order, text bytes, standard fields,
  and annotations;
- invalid unions, missing URI/name, negative size, and an oversized descriptor
  fail with content-free typed validation errors; and
- reserved protocol extension metadata is not made model-visible.

ACP SDK fixtures prove:

- capability and agent-identity fields match the production path;
- ResourceLink-only and mixed-order prompts reach the normal QueryEngine and
  model path;
- image, audio, and embedded-resource requests preserve structured unsupported
  errors across the SDK wire and do not call the model;
- malformed ResourceLink input wins over an unknown session, remains standard
  invalid params across the SDK wire, and leaks neither resource nor session
  input;
- every new/resume/load additional-directory and MCP case leaves the session
  registry unchanged; and
- non-empty cursor rejection retains empty-cursor first-page compatibility.

Closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
go test -race ./server/acp
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan -reference .reference/claude-code-ripe -json
git diff --check
```

## Compatibility And Failure Boundary

Clients lose the false load capability and silent-success behavior for
unsupported inputs. Existing empty-MCP new/resume/list/close and text-prompt
behavior remains. The legacy load handler remains callable only as an
unadvertised compatibility path; clients receive no replay guarantee from it.

The first list page remains unbounded, and stdio MCP remains unimplemented.
Explicit cursor/MCP rejection closes silent loss but does not close those ACP
v1 conformance gaps. Optional rich media remains outside this slice and is
owned by the later P30 contract.

Rollback is one code-and-test unit: remove engine PromptInput conversion and
the new admission errors, restore the old text extractor and load claim, and
remove the identity fields. That rollback would reopen ResourceLink loss,
capability falsehood, and silent setup-input acceptance, so the safer
operational fallback is to disable the ACP entrypoint.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| transport-neutral prompt input | [`engine/user_input.go`](../../../../engine/user_input.go) | Owns ordered baseline blocks, deterministic ResourceLink fallback, bounds, and content-free validation |
| ACP capability and admission | [`server/acp/agent.go`](../../../../server/acp/agent.go) | Owns agent identity, capability truth, protocol-block conversion, and pre-mutation rejection |
| engine fixtures | [`engine/user_input_test.go`](../../../../engine/user_input_test.go) | Proves descriptor identity, ordering, bounds, metadata, and no content leakage |
| ACP fixtures | [`agent_capability_truth_test.go`](../../../../server/acp/agent_capability_truth_test.go) | Proves wire errors, no mutation/model call, ResourceLink reachability, and capability fields |

## Next State

G17 is narrowed but remains open for isolated stdio MCP and bounded listing.
P23.1 is the sole root-plan `Ready` slice and owns SDK/schema characterization,
v2 guards, exact current wire errors, byte/order fixtures, and the internal
canonical lifecycle envelope without changing client output.
