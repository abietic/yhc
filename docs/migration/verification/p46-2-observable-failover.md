# P46.2 Observable Model Failover Verification

**Status:** verification
**Last verified:** 2026-08-06

> **Ownership:** reproducible evidence that a successful overload switch
> disposes the failed attempt explicitly and projects one safe fallback notice
> without changing canonical output or durable state

## Contract

After the coordinator has confirmed a constructable alternate, the production
round emits the failed attempt as `discarded`, an exact tombstone only when the
TUI offered retractable output, and the alternate `started` event before its
provider dispatch. The switched attempt does not also emit `failed`.

The `started` fallback carries the already-normalized profile and switch count.
TUI projects one warning, Plain and Headless write the same notice to stderr,
ACP sends one `_session/status` extension, and library callers retain the typed
event. No adapter adds the notice to assistant text, canonical projection,
transcript state, or structured headless output.

## Deterministic Evidence

`TestP462DiscardedAttemptPrecedesTombstoneAndFallbackDispatch` drives the
production `Query` seam and records yielded events beside actual provider-call
entry. Its zero-output case pins:

```text
started primary -> dispatch primary -> discarded/never_started
-> started fallback -> dispatch fallback
```

Its retractable TUI case pins:

```text
assistant partial -> discarded/discarded -> exact tombstone
-> started fallback -> dispatch fallback
```

Both cases retain the original attempt identity, typed `overloaded` failure,
and provider-call/switch accounting. The reducer/replay fixture proves that the
discarded phase remains bounded process-local truth while the exact tombstone
removes only the matching live output.

Adapter fixtures prove:

- TUI removes the old attempt before showing one warning and does not create a
  chat item;
- Plain stdout and Headless structured output remain exact assistant content;
- Plain and Headless stderr contain the same bounded notice once;
- ACP emits one `model_fallback` `_session/status` update and no assistant
  chunk; and
- unsafe or incomplete synthetic profile facts are suppressed.

The Unix PTY fixture runs the Plain projection in a subprocess at 80 columns.
It pins one notice, one assistant payload, normal helper completion, and the
absence of API model, provider, route digest, and raw overload error.

## Commands

```bash
go test ./engine/ -run '^(TestP462.*|TestP294.*)$' -count=1
go test -race ./engine/ -run '^(TestP462.*|TestP294.*)$' -count=1
go test ./internal/tui/ -run '^(TestP462.*|TestP294.*)$' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP462.*$' -count=1
go test ./server/acp/ -run '^TestP462.*$' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP462PlainFallbackNoticePTY$' -count=1
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree.

## Evidence Limits

The provider calls are in-process deterministic seams with literal configured
profile identity. The PTY proves terminal protocol/process behavior, not a
physical font or pixel grid. No live-provider tokenization, remote endpoint,
remote-CI availability, or restart replay of process-local attempt events is
claimed.
