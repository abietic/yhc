# P43.0 Real-Repository Evaluation Promotion Evidence

**Status:** verification
**Snapshot:** `03faac2575a0de6e17c54d1c310cfd4eba081649`
**Measured:** 2026-08-02

> **Ownership:** the test-only evidence that makes the P43.0 isolated baseline
> harness executable. The accepted implementation and rollback contract are in
> [`p43-real-repository-evaluation.md`](../plans/p43-real-repository-evaluation.md).

## Result

P43.0 is ready because one test-only prototype now drives the public headless
command across two clean, disposable Git repositories and produces the same
canonical outcome grade. It proves a bounded minimum rather than claiming a
complete evaluator:

- `newRootCommand` dispatches the public `exec --output-format json` path;
- a local scripted provider requires no live provider or credential;
- only `Write` is model-visible under `acceptEdits`;
- one root-escape write reaches the headless denial fallback and leaves its
  outside sentinel unchanged;
- one contained write creates the expected implementation;
- public and grader-owned hidden behavior checks pass;
- the product residual contains only the declared relative change while the
  public command's repository-local `.eino-agent` Session metadata is
  classified separately; and
- the two canonical reports are byte-identical and contain none of the prompt,
  fake credential, provider request, repository-content, or absolute-path
  sentinels.

This prototype remains in `_test.go` and `testdata`. It is promotion evidence,
not the reusable P43.0 harness, report CLI, or current product behavior.

## Reproduce

```bash
go test ./cmd/eino-agent/cmd \
  -run '^TestP430LocalizedWriteFixPromotion$' \
  -count=1
```

The test performs both replays in one run and compares canonical encoded
reports. Repeating the test checks fresh process-local state:

```bash
go test ./cmd/eino-agent/cmd \
  -run '^TestP430LocalizedWriteFixPromotion$' \
  -count=10
```

## What The Fixture Proves

| Evidence | Claim |
|---|---|
| Public Cobra route | The current production composition and one-shot QueryEngine path, not a direct engine fixture, perform the change. |
| Pinned repository | Each replay copies the same committed fixture and records the same clean input digest. |
| Deterministic provider | Ordered loopback responses and fixed usage remove live-provider/model variance while retaining the public adapter boundary. |
| Outcome grader | Public compile/smoke behavior and an external hidden regression pass after the change. |
| Policy probe | The outside Write is denied and unchanged; the contained Write succeeds through the current `acceptEdits` fast path. |
| Residual grader | Only the declared relative source addition and final product-tree digest remain; repository-local Session metadata is classified separately. |
| Redaction | Canonical report bytes exclude raw task, fake key, temp roots, request body, repository content, and outside sentinel values. |
| Honest coverage | `headless.exec` is evaluated; TUI, Plain, ACP, standalone MCP, OS process containment, and recovery are explicitly `not_evaluated` or `not_exercised`. |

## Evidence Limits

The fixture does not expose Bash, process hooks, WebFetch, MCP, Agent, Read, or
search tools. Its filesystem isolation claim is therefore limited to the
admitted `Write` surface and the existing resolved-root `acceptEdits` policy.
It does not prove an OS filesystem, network, syscall, credential-socket, or
resource sandbox, and it does not close G28.

The scripted provider proves reproducibility and exact fixture usage, not model
quality, provider reliability, monetary cost, or representative production
latency. Wall duration is diagnostic and excluded from canonical equality.
Recovery is present in the grade schema as `not_exercised`; a later recovery
scenario needs its own restart oracle.

Raw prompt and repository bytes are committed fixture inputs and remain local.
They are never report fields. No score from this promotion is a CI, release,
permission-review, or Goal-default gate.

## Reference Decision

P43 keeps canonical traces for compatibility and adapts three verified
patterns: Crush's production-constructor plus offline-provider tests,
OpenCode's ordered replay and disposable temporary roots, and Codex's separation
between the tested binary and benchmark data. The scenario/report schema and
truthful isolation matrix remain project-owned. The resulting decision is
`combine`, not a port of any reference harness.
