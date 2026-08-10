# P38.0 Provider Reasoning Origin

**Status:** historical
**Closed gaps:** G34
**Completed:** 2026-08-02
**Adoption:** `adapt`

> **Ownership:** delivery evidence for exact-origin Agentic OpenAI private
> continuation and G34 closure. Current behavior belongs in
> [`model-providers.md`](../../../architecture/platform/model-providers.md).

## Outcome

P38.0 closes G34 without making provider-private state public. A completed
Agentic OpenAI assistant response now receives one transcript-private origin
binding. A later request may reuse its reasoning/signature only when the
restored physical record, message index, logical assistant ID, complete payload
digest, provider, account, API family, API model, route digest, credential
origin, and current publication all match.

Every absent, legacy, malformed, recovered-with-drift, rotated, switched, or
stale path strips reasoning text, structured reasoning/signature parts, and
provider metadata from an attempt-local clone before conversion. Public text
and tool history remain canonical. Other providers keep their prior transport
behavior.

## Persistence And Dispatch Authority

`assistant-origin-binding/v1` is stored beside the assistant message but is not
part of `schema.Message`, durable public entries, Session replay snapshots,
exports, TUI/Plain history, or ACP wire output. Its canonical SHA-256 binding
covers the complete persisted Message JSON after the internal logical ID is
assigned. Load, branch, exact rewrite, repair, and Resume rebind only from a
verified physical source; malformed or partial sidecars leave the Session
readable and make the assistant ineligible.

The route registry re-resolves credentials for every dispatch. A monotonic
resolution attempt and account-level current publication prevent a slower old
credential, auth source, endpoint, or route identity from reviving after a
newer resolution succeeds. Published client objects remain immutable. The
pre-conversion fence verifies both the RouteIdentity publication and its
account publication before issuing an internal client-bound proof. Caller
`Message.Extra`, transcript fields, response metadata, and model options cannot
mint that proof.

Only a fully aggregated successful canonical assistant response stages an
origin. Failed, cancelled, partial, or persistence-failed rounds mint none and
cannot continue to tool execution. Generate and Stream share route
preparation, exact comparison, marker injection, stripping, and redacted
diagnostics.

## Panic Containment

The same registry settlement also repairs the reported
`assignment to entry in nil map` failure. Publication and named-profile paths
self-initialize the model, publication, account-route, account-publication,
and resolution-fence maps before mutation. A focused regression deliberately
nils those maps and proves the next route preparation repairs them without a
panic.

## Proof And Review

Focused tests cover exact Generate/Stream continuation, real Agentic OpenAI
request conversion, every rejection reason, credential rotation, same-identity
and cross-identity publication races, endpoint/auth-source changes, completed
versus failed streams, canonical sidecar encoding, reload/fork/rewrite and
recovery mismatch, Session export, and ACP New/Load/Resume zero leakage.
Focused provider, transcript, engine, Session, and ACP race tests pass.

Independent concurrency review found two variants of one P1 publication race:
first an old credential snapshot could overwrite a newer same-identity route;
then a changed RouteIdentity could escape that fence. The final account-scoped
monotonic publication and pre-leaf account check close both variants. The
review also drove explicit ACP sidecar leak coverage rather than relying only
on the older public-replay fixtures.

Repository closeout passes formatting, full lint, new-finding lint, tests,
build, documentation, migration-manifest/ledger, and diff gates.

## Compatibility And Rollback

Legacy transcripts and credentials without a durable opaque origin remain
readable and usable for ordinary calls; they conservatively omit private
continuation. Existing credential records gain an additive opaque ID/revision,
and reads return detached copies. No public protocol or Session export schema
changes.

A squash revert removes origin capture, sidecar validation, route proof, and
diagnostics and restores unconditional private stripping. It may leave unknown
optional sidecar JSON for newer readers to ignore. Rollback must retain
alternate-route stripping and the rule that persisted assistant `Extra` is not
provider-origin authority.
