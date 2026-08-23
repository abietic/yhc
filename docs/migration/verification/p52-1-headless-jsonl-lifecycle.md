# P52.1 Headless JSONL Lifecycle Verification

**Status:** verification
**Verified:** 2026-08-24
**Platform:** Darwin arm64

> **Ownership:** reproducible local evidence for the versioned bounded
> headless lifecycle stream and its compatibility boundary

## Accepted result

The public headless composition root emits version-1 JSONL lifecycle records
from committed engine projections and finishes with one classified result.
The projection rejects malformed canonical data and invalid UTF-8, does not
emit a duplicate engine terminal, and retains existing text/JSON and
permission behavior.

This evidence does not claim a live provider, remote CI, PTY or physical UI,
long-lived reconnect, replay cursor, SDK, daemon, or Host Session API.

## Contract-to-evidence map

| Contract | Production owner | Oracle |
|---|---|---|
| Versioned mutually exclusive records | `LifecycleWriter` | `TestLifecycleWriterProjectsCanonicalEventAndResult` |
| Validated canonical payload and UTF-8 fail-closed behavior | `ProjectLifecycleEvent` | `TestLifecycleWriterProjectsOnlyValidatedSafePayloads` |
| Engine terminal skipped; one final result | headless observer and renderer | `TestHeadlessJSONLLifecycleStreamClosesWithOneResult` |
| Public CLI composition and model-order identity | `runHeadless` | `TestHeadlessJSONLPublicExecStreamsCommittedLifecycle` |
| Pre-turn failure does not invent turn identity | early `SubmitMessage` terminal and headless renderer | `TestHeadlessJSONLPreTurnFailureClosesWithSessionIdentity` |
| Pre-engine usage failure has no runtime identity | headless failure renderer | `TestHeadlessJSONLUsageFailureHasNoRuntimeIdentity` |
| Cancellation closes after the last committed event | headless classifier and result renderer | `TestHeadlessJSONLCancellationClosesAfterLastCommittedEvent` |
| Broken event sink cancels the query but drains terminal settlement | headless observer | `TestHeadlessJSONLWriteFailureCancelsAndDrainsTerminal` |
| Existing format parsing and redacted failure | headless format/error owners | `TestParseOutputFormat`, `TestHeadlessJSONEnvelopeAndDiagnosticsAreRedacted` |

## Focused commands and observed result

These commands passed on the implementation candidate:

```bash
make fmt
go test ./engine/transport ./cmd/yhc/cmd -run 'TestLifecycle|TestHeadlessJSONL' -count=1
go test ./engine/transport ./cmd/yhc/cmd -count=1
```

The focused public-entrypoint test uses a loopback OpenAI Responses fixture.
It exercises an outside-root Write denial, an in-root Write success, committed
tool start/input/terminal events, assistant delta, monotonic event identity,
and one final result. It makes no network call to a live provider.

## Failure interpretation

- Invalid canonical data is a transport failure, not an omitted best-effort
  event. Payload bytes are never interpolated into validation diagnostics.
- A failure before turn admission carries Session identity but no fabricated
  thread, turn, sequence, timestamp, or causation value.
- A usage or configuration failure before engine construction carries no
  fabricated Session or turn identity.
- A stdout write failure cancels the query and is returned as an I/O failure;
  no exactly-once claim is possible for a broken output sink.
- Unsupported engine event families are omitted from schema version 1. That is
  distinct from silently accepting a malformed supported event.
- Headless permission denial remains visible on stderr and as the canonical
  failed tool terminal; JSONL does not become an approval channel.

## Repository closure

Run the diff-selected and clean committed-tree gates from the topic branch:

```bash
make change-plan
make verify-focused
make verify-merge
make change-evidence
make change-evidence-ready
```

Local focused, merge, publication, remote-CI, live-provider, PTY, and physical
UI evidence are separate classes and must be reported separately.
