# P48.3 ACP String Raw Output

**Status:** historical
**Closed gaps:** G44
**Completed:** 2026-08-07
**Adoption:** `preserve`

> **Ownership:** completion record for P48.3/G44; current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md)

## Outcome

ACP replay now passes validated durable tool-result text directly to the SDK
as `rawOutput`. The unchanged live lifecycle still decodes the canonical engine
JSON string to the same Go string. JSON-looking objects, arrays, numbers,
booleans, `null`, quoted strings, and empty output therefore retain exact type
and bytes before and after Session load.

The removed replay-only decoder no longer treats transcript text as an ad hoc
result schema. Rendered tool content, completed/failed status, update order,
malformed replay rejection, transcript bytes, canonical live redaction, and
tool `rawInput` remain unchanged.

## Compatibility And Rollback

Clients that previously observed replay-only object, array, numeric, boolean,
or null `rawOutput` now receive the same string type used by live delivery.
This is the accepted compatibility correction; no public request shape,
persisted schema, transcript migration, or ACP version changes.

A squash revert restores only replay JSON-shape inference. It requires no data
rollback, but reopens G44 and again makes one durable tool result change wire
type after load.

## Evidence

The closeout tree contains table-driven independent live/replay SDK-wire
checks for every accepted value, the corrected replay-order expectation,
focused race, full ACP, runtime contract/race packs, and the official ACP SDK
v1.3.0 harness. Detailed repository commands and limitations are in the
[verification record](../../verification/p48-3-acp-string-raw-output.md). All
listed local commands pass on the closeout tree.

No typed tool-result schema, transcript rewrite, remote-CI, live-provider, ACP
v2, or third-party client-rendering claim is made.
