# P51.2 Proof-Bound Auto Bash Verification

**Status:** verification
**Verified:** 2026-08-13
**Platform:** Darwin arm64

> **Ownership:** reproducible local evidence for proof-bound ordinary Auto
> Bash, critical live AllowOnce, durable constraints, final dispatch fencing,
> and supported client projections

## Accepted result

P51.2 admits an ordinary canonical built-in Bash invocation without a
permission request only when QueryEngine binds it to the complete available
Darwin Seatbelt Guest proof. The narrow literal critical `rm`/`rmdir` corpus
instead enters one engine-owned `AllowOnce`-only interaction before Bypass,
grants, classifier, reviewer, or coalescing can authorize it. DontAsk denies
that corpus without prompting.

This evidence does not claim general shell-risk classification, Linux or
Windows containment, credential isolation, hard memory/file-descriptor/process
limits, reviewer accuracy, remote CI, code signing, notarization, or physical
UI acceptance. Environment and `TMPDIR` remain inherited unchanged.

## Contract-to-evidence map

| Contract | Production owner | Deterministic or real oracle |
|---|---|---|
| Exact complete Guest proof only | `ExecutionIdentityFor`, `completeContainedAutoBashProof` | `TestP512ExecutionIdentity*`, `TestP512ContainedAutoBashProof*`, `TestP512PermissionAction*` |
| Narrow critical recognition | `ClassifyBashCriticalPath` | `TestP512ClassifyBashCriticalPathLiteralCorpus` |
| Fresh AllowOnce before non-live authority | `evaluateInvocationPolicy`, `promptForCriticalBash`, `PermissionCoordinator` | `TestP512ContainedAutoBashOrder`, `TestP512CriticalPath*`, `TestP512AllowOnceOnly*` |
| Hook rewrites restart policy | `executeToolCall`, QueryEngine descriptor rebuild | `TestP512PreToolRewriteRestartsPermissionAuthority` |
| Durable request and decision identity | `projectGraphHITLRequest`, `RuntimePermissionDecision` | `TestP512ProjectGraphConstraint*`, `TestP512ProjectGraphFirstInterruptProjectsConstraint`, `TestP512ProjectGraphColdRestartRetainsAllowOnceConstraint` |
| Root/binding/registry drift rejects execution | `toolExecutor`, `ShellManager.ExecuteAt` | `TestP512ContainedAutoBashDispatchRejects*`, `TestP512GuestSubmissionRejectsExpiredIdentity` |
| Plain/TUI/ACP/AppServer/Web UI scopes | adapter reconstruction plus engine settlement | package-local `TestP512*`, broker digest/forgery test, Desktop view-model tests |
| Foreground/background Child authority | derived child Guest binding and child QueryEngine | `TestP512ProjectGraphChildBashUsesDerivedGuestAuthority` |
| Real Darwin contained execution | Seatbelt-backed public entrypoints | ACP and child inside-root writes, child parent-root escape denial, real root replacement |

## Commands and observed results

The following commands passed on the implementation candidate:

```bash
go test ./engine/permission -count=1
go test ./engine/containment ./tools ./engine ./internal/tui \
  ./server/acp ./server/appserver ./cmd/yhc/cmd -run '^TestP512' -count=1
go test -race ./engine ./tools ./server/appserver \
  -run '^TestP512.*(Constraint|Permission|Drift|Submission|Rewrite)' -count=10
go test ./engine -count=1
node --test desktop/test/*.test.mjs
```

The full `engine` package passed after updating the pre-P51.2 Bypass test to
use a non-critical command. A separate critical Bypass regression requires a
fresh constrained decision, so the compatibility edit does not weaken the new
exception.

## Failure and skip interpretation

- `sandbox_binding_expired` is the exact proof-bound dispatch failure for
  action, root, binding, or registry drift. It is returned before tool
  acquisition or submission, and ShellManager repeats the root check at the
  final persistent-shell write boundary.
- Unsupported or malformed shell syntax is a classifier non-match. P51.2 does
  not add a sandbox denial or prompt solely because the recognizer is unsure.
- Real containment tests may skip when the Darwin Seatbelt Guest binding is
  unavailable. A skip proves no containment; only the available-binding runs
  above establish the Darwin product behavior.
- Remote CI and packaged/physical Desktop acceptance are independent evidence
  classes and are not inferred from these local checks.

## Reproduction

Run focused proof and race checks first, then the repository-owned evidence
workflow from a clean committed tree:

```bash
make change-plan
make verify-focused
make verify-merge
make change-evidence
make change-evidence-ready
```

Publication safety remains a separate required gate because this is a public
forward-port and no non-public Git object or implementation provenance may be
published.
