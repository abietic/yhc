# P29.2 Shared Inventory, Manual Switching, And Durable Binding

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for P29.2's shared configured inventory,
> active main-route control, additive Session binding, recovery admission, and
> safe entrypoint/session projections.

## Outcome

P29.2 completed the frozen `combine` slice. `engine/provider.Runtime` now owns
one detached, non-secret inventory for configured profiles and labelled
legacy compatibility selectors. `QueryEngine` owns the active binding,
model/reasoning control transaction, resume admission, and canonical
pre-dispatch guard. Commands, TUI, plain/headless startup, and ACP consume that
shared state instead of enumerating the static built-in registry.

The implementation:

- resolves exact profile IDs before the explicit `legacy:<selector>` grammar;
- validates a candidate outside the active-turn lock, rechecks generation and
  reasoning under the lock, durably checkpoints it, and only then mutates live
  route state;
- installs `model_binding_checkpoint_uncertain` when a recorder cannot prove
  whether a write committed, blocking later switches, compaction, and provider
  attempts until Session reload;
- persists strict `model_binding` v1 logical identity, resolved provider/API
  model, non-secret revision/digests, known limits, and applied reasoning;
- retains valid but invalid or unknown-version nested binding JSON opaquely so
  an additive field cannot make the enclosing Session unreadable;
- re-admits a binding before resume activation and distinguishes invalid,
  missing, identity drift, compatible revision/metadata change, context
  downshift, output-limit change, and unsupported reasoning;
- permits the existing compaction owner to clear only a context-only block
  after a fitting durable checkpoint; and
- copies the latest live binding during active fork while listing and export
  reveal only binding state, kind, and value.

## Security And Compatibility

The inventory excludes account IDs, endpoints, authentication
kind/reference/value, headers, clients, and route health. The persisted record
contains only one-way non-secret digests; listing/export exclude even those
digests and never reveal opaque invalid or unknown payloads.

Old Sessions without `model_binding` keep the legacy model/provider path.
Invalid or unknown records remain loadable but inert and fail closed before a
provider call. Missing profiles and provider/API-model or route-identity drift
require explicit rebind. Compatible portfolio or metadata changes warn
without silently changing the selected route; unsupported persisted reasoning
is cleared visibly rather than guessed.

P29.2 adds no role-specific call, failover attempt, provider adapter rewrite,
hot reload, or standalone-MCP model runtime.

## Verification And Rollback

Focused inventory, transaction, Session, recovery, compaction, projection,
entrypoint, race, and secret checks plus the repository and documentation
gates are recorded in
[`p29-2-shared-inventory-model-binding.md`](../../verification/p29-2-shared-inventory-model-binding.md).

Rollback stops inventory-backed entrypoint projection and new binding writes,
then restores the legacy model projection. Existing additive v1 and opaque
unknown records remain preserved for reader compatibility; rollback never
reinterprets an unknown version.

P29.3 remains queued until a separate production role-call inventory and root
promotion freeze its capability-admitted role-routing boundary. G31 remains
open.
