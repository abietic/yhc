# P31.1b Authoritative WorkBoard

**Status:** historical
**Completed:** 2026-07-31

> **Ownership:** completion evidence for the authoritative WorkBoard v2
> cutover, exact Task/Todo compatibility adapters, Session lifecycle binding,
> forward-only reader floor, local recovery, and rollback boundary. P31.2 owns
> any runtime replay or explorer projection.

## Outcome

P31.1b completed the accepted `combine` slice. Each durable root Session
lineage now owns one `workboard.LogicalWorkAdapter` and one serialized
WorkBoard mutation gate. `tools.TaskManager` remains the stable compatibility
facade, while Task and exact `(SessionID, AgentID)` Todo operations read and
write one canonical board after cutover. Children share the root adapter;
independent roots do not. Explicit non-Session tool embeddings and standalone
MCP retain only opaque process-local compatibility scope.

The compatibility payload preserves numeric Task IDs, arbitrary accepted
legacy statuses, unresolved dependencies, metadata, output, timestamps,
lifecycle events, Todo full replacement, all-complete clearing, duplicate
rows, and existing tool result text. Canonical WorkItems retain stable
identity and durable completed/cancelled evidence without making
`AgentRunner` an execution child of the board.

## Authority And Failure Boundary

The first accepted mutation freezes the complete legacy Task snapshot and
every registered Todo partition, writes the immutable backup and v2 seed, and
commits the marker last. Marker absence remains legacy authority; a supported
marker establishes the forward-only `workboard/v2` reader floor. The P31.1a
shadow is never promoted or restored.

All three authoritative files are exact mode-0600 regular files under a
mode-0700 transcript directory. Reads and writes are bounded and reject links,
non-regular targets, unsafe modes, replacement races, corruption, unknown
versions, Session mismatch, invalid compatibility state, and over-budget
input. Replacement uses same-directory temp creation, full write, file sync,
close, rename, and parent-directory sync.

A historical transcript directory with no marker or prepared WorkBoard files
may still start in legacy mode without an implicit permission rewrite. Its
first cutover validates private containment before creating any artifact;
marker-visible or interrupted prepared state always uses the strict reader.

A post-rename sync failure compensates to the prior complete file or absence
before returning an error. If compensation also fails, the newly installed
artifact or existing marker is synced as mode 0400. Both marked and
pre-marker restart paths reject that quarantine before model or tool dispatch,
so a caller cannot turn durability uncertainty into a duplicate mutation.

## Session Lifecycle And Recovery

Resume and restore staging validate the complete authority before activation.
Authoritative fork creates a child-specific Session ID, fresh BoardID, cloned
board, immutable fork-time backup, and marker before publishing the child
transcript. Legacy fork publishes no WorkBoard files. Compaction holds the
lifecycle gate and revalidates BoardID/revision without rewriting the board.
Export remains presentation-only and excludes the authority, marker, backup,
private identity, and recovery data.

Deletion preflights every exact owned artifact, removes the transcript first,
then marker, authority, backup, shadow, and media, and reports typed
cleanup-pending state for an exact retry. Active deletion detaches the
recorder so close cannot recreate the transcript.

Destructive backup recovery is local CLI/session-service only. It requires the
exact current Session ID, BoardID, revision, and explicit data-loss
acknowledgement, restores the immutable cutover baseline under a fresh
BoardID, and retains the reader-floor marker. TUI, ACP, model tools, hooks, and
standalone MCP have no recovery entrypoint.

## Verification And Rollback

Compatibility, strict codec/resource, cutover-stage, post-rename
compensation/quarantine, marker-floor, root/child, standalone fallback,
resume, fork, delete retry, compaction, export, restore staging, recovery,
concurrency, and source-owner tests pass. The complete commands and independent
review evidence are recorded in
[`p31-1b-authoritative-workboard.md`](../../verification/p31-1b-authoritative-workboard.md).

There is no supported downgrade to a binary that cannot read WorkBoard v2
after a marker is committed. Normal rollback may remove later replay or
presentation, but must retain the v2 reader, marker handling, compatibility
adapters, Session lifecycle containment, and WorkBoard authority. P31.2-P31.5
remain separately queued; no successor became `Ready`.
