# P51.2 Proof-Bound Auto Bash Core Verification

**Status:** verification
**Verified:** 2026-08-19
**Platform:** Darwin arm64

> **Ownership:** reproducible local evidence for proof-bound ordinary Auto
> Bash, critical live AllowOnce, durable constraints, final dispatch fencing,
> and supported Core client projections

## Accepted result

P51.2 admits an ordinary canonical built-in Bash invocation without a
permission request through its new automatic path only when QueryEngine binds
it to the complete available Darwin Seatbelt Guest proof. Exact local/user
allow rules remain a separate explicit authority and do not receive the
proof-bound admission marker. The narrow literal critical `rm`/`rmdir` corpus
instead enters one engine-owned `AllowOnce`-only interaction before any
positive rule, grant, Bypass, classifier, reviewer, or coalescing authority.
DontAsk denies that corpus without prompting.

This evidence covers the master-native Core: QueryEngine/tools, ProjectGraph,
Plain, TUI, ACP, and foreground/background Child. It does not claim AppServer,
Desktop, Web UI, general shell-risk classification, Linux or Windows
containment, credential isolation, hard memory/file-descriptor/process limits,
reviewer accuracy, remote CI, code signing, notarization, or physical UI
acceptance. Environment and `TMPDIR` remain inherited unchanged.

## Contract-to-evidence map

| Contract | Production owner | Deterministic or real oracle |
|---|---|---|
| Exact complete Guest proof only | `ExecutionIdentityFor`, `completeContainedAutoBashProof` | `TestP512ExecutionIdentity*`, `TestP512ContainedAutoBashProof*`, `TestP512PermissionAction*` |
| Exact user authority stays separate | rule evaluation and admission marker | `TestP512ExactUserAuthorityRemainsSeparateFromProofShortcut` |
| Narrow critical recognition | `ClassifyBashCriticalPath` | `TestP512ClassifyBashCriticalPathLiteralCorpus` |
| Fresh AllowOnce before non-live authority | `evaluateInvocationPolicy`, `promptForCriticalBash`, `PermissionCoordinator` | `TestP512ContainedAutoBashOrder`, `TestP512CriticalPath*`, `TestP512AllowOnceOnly*` |
| Hook rewrites restart policy | `executeToolCall`, QueryEngine descriptor rebuild | `TestP512PreToolRewriteRestartsPermissionAuthority` |
| Interactive ordinary-to-critical rewrites fail closed | `settlePermissionInteraction` final-action classification | `TestP512PermissionRewriteCannotEscalateOrdinaryBashToCritical` |
| Durable request and decision identity | `projectGraphHITLRequest`, `RuntimePermissionDecision` | `TestP512ProjectGraphConstraint*`, `TestP512ProjectGraphFirstInterruptProjectsConstraint`, `TestP512ProjectGraphColdRestartRetainsAllowOnceConstraint` |
| Root/binding/registry drift rejects execution | `toolExecutor`, `ShellManager.ExecuteAt` | `TestP512ContainedAutoBashDispatchRejects*`, `TestP512GuestSubmissionRejectsExpiredIdentity` |
| Plain/TUI/ACP choices | adapter reconstruction plus engine settlement | package-local `TestP512*` |
| Foreground/background Child authority | derived child Guest binding and child QueryEngine | `TestP512ProjectGraphChildBashUsesDerivedGuestAuthority` |
| Real Darwin contained execution | Seatbelt-backed Core entrypoints | ACP and child inside-root writes, child parent-root escape denial, real root replacement |

## Commands and observed results

The following focused commands passed on the Darwin arm64 implementation
candidate:

```bash
go test ./engine ./tools -run 'TestP512|TestPermissionDecisionConstraint|TestPermissionCoordinator|TestPermissionAction|TestShellManager' -count=1
go test ./cmd/yhc/cmd ./internal/tui ./server/acp -run 'TestP512|TestPlainPermission|TestPermission|TestACP' -count=1
go test ./engine -run '^TestP512ProjectGraphChildBashUsesDerivedGuestAuthority$' -count=1
go test ./engine -run '^TestP512PreToolRewriteRestartsPermissionAuthority$' -count=1
go test ./server/acp -run '^TestP512ACPAutoOrdinaryBashEntrypoint$' -count=1
go test ./engine -run '^TestP512ExactUserAuthorityRemainsSeparateFromProofShortcut$' -count=1
go test ./engine ./tools ./cmd/yhc/cmd ./internal/tui ./server/acp -count=1
go test ./internal/tui -run '^TestTUIHotPathPerformanceBudgets$' -count=3
go test -race ./engine ./tools ./server/acp -run '^(TestP512|TestPermissionDecisionConstraint|TestPermissionCoordinator|TestShellManager)' -count=1 -timeout=10m
```

Repository-owned focused, merge, evidence, documentation, build, publication,
and remote-CI results are separate evidence classes and must be appended only
after they run on the corresponding tree.

## Failure and skip interpretation

- `sandbox_binding_expired` is the exact proof-bound dispatch failure for
  action, root, binding, or registry drift. It is returned before tool
  acquisition or submission, and ShellManager repeats the root check at the
  final persistent-shell write boundary.
- Unsupported or malformed shell syntax is a classifier non-match. P51.2 does
  not add a sandbox denial or prompt solely because the recognizer is unsure.
- Real containment tests may skip when the Darwin Seatbelt Guest binding is
  unavailable. A skip proves no containment; only available-binding runs
  establish Darwin product behavior.
- Remote CI and packaged or physical Desktop acceptance are independent and
  are not inferred from local Core checks.

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
