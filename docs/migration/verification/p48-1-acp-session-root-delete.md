# P48.1 ACP Observed Session-Root Delete Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that ACP inactive deletion selects one
> exact process-local observed project root without moving filesystem ownership
> out of `engine/session`

## Contract

Successful new, load, resume, fork, and returned list rows remember the
canonical effective project root for their Session ID. Close retains that
observation. Inactive delete selects the exact observation, or the canonical
default CWD for an ID never observed, and delegates the transcript directory to
`session.DeleteSession`.

Two canonical roots observed for one ID are permanently ambiguous in that
agent process. Delete returns `CodeSessionConflict` containing only the Session
ID and performs no filesystem mutation. Success or `os.ErrNotExist` forgets
only the matching non-ambiguous observation. Unsafe IDs, active targets, and
other filesystem errors retain the existing failure behavior.

## Deterministic Evidence

The public ACP regression creates an Agent rooted at project A, creates and
closes a Session in project B, and proves delete removes B's transcript and
owned sidecars while preserving an A sentinel. A fresh A-rooted Agent then
lists a valid B Session and proves the returned row reconstructs enough
process-local correlation for exact deletion.

A third fixture persists the same safe ID under B and C, lists both roots, and
requires a typed conflict. Byte snapshots prove that neither transcript nor
owned sidecar changes, while privacy assertions exclude both roots from the
error. Locator unit tests pin canonical clean/symlink equivalence, repeated
observations, monotonic ambiguity, default fallback, retained close-style
state, exact guarded forget, and race-safe concurrent remember/resolve.

The existing ACP delete suite continues to prove active rejection under the
lifecycle lock, registration serialization, unsafe-ID containment, default-root
deletion, unrelated-file preservation, and idempotent absence. Full ACP,
Session contract, focused race, repository race, and official SDK v1.3.0 wire
checks cover the surrounding lifecycle and protocol boundary.

## Commands

```bash
go test ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
go test -race ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
go test ./server/acp/ -count=1
make test-contract
make test-race
./scripts/verify-p23-5-acp-sdk.sh
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree.

## Evidence Limits

The locator is intentionally process-local and non-durable. A fresh Agent
cannot delete an unobserved cross-project Session without first seeing it
through a supported lifecycle/list operation, and ACP v1 delete still carries
no CWD. List/delete interleaving may conservatively retain an observed root and
later force conflict; it does not authorize guessing or deleting another root.
No remote-CI result, multi-process coordination, network provider, or global
Session discovery is claimed by this local verification.
