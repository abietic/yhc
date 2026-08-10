# Typed Error Package

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** `engine/errors` package API and its narrow CLI abort-classification wiring

## Current Wiring

`engine/errors` defines `AgentError`, stable category codes, constructors,
error unwrapping, and classification helpers. It is inside the released CLI
dependency closure because the process boundary calls `IsAbort` when mapping
errors to cancellation exit status. That narrow call does not make the package
the error authority of `QueryEngine`, recovery, tools, or transports.

Other production paths still use standard wrapped errors plus local classifiers
and terminal enums. No production caller currently constructs the package's
typed errors or uses its other category helpers. Broadening that wiring would
be a public contract decision because it could change retry, user-facing text,
protocol mapping, and `errors.Is` / `errors.As` behavior.

## Package contract

- `AgentError` carries code, message, optional cause, retryability, and
  user-facing flags.
- `Unwrap` exposes the cause to the standard error chain.
- Constructors cover abort, overflow, shell, tool, permission, rate limit,
  network, model, configuration, session, and max-turn categories.
- Helpers classify an error only when an `AgentError` exists in its chain.

## Invariants and edge cases

- `IsRetryable` and `IsUserFacing` return false for ordinary errors.
- Model retryability depends on the status code supplied to `NewModelError`.
- Error messages may contain command or provider detail; callers must still
  redact secrets before presenting them.
- Do not infer recovery or runtime-wide taxonomy integration from the single
  CLI `IsAbort` call.

## Code references

- [`AgentError`](../../../engine/errors/errors.go)
- [`NewAbortError`](../../../engine/errors/errors.go)
- [`NewToolError`](../../../engine/errors/errors.go)
- [`NewModelError`](../../../engine/errors/errors.go)
- [`IsRetryable`](../../../engine/errors/errors.go)
- [`GetCode`](../../../engine/errors/errors.go)
- [CLI abort-to-exit mapping](../../../cmd/yhc/cmd/cli_errors.go)

## Related tracking

Any decision to make this the runtime-wide taxonomy belongs in
[`migration/PLAN.md`](../../migration/PLAN.md). Current recovery behavior is documented in
[`recovery.md`](recovery.md).
