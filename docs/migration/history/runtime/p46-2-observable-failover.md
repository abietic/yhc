# P46.2 Observable Attempt Discard And Switch

**Status:** historical
**Closed gaps:** G37
**Completed:** 2026-08-06
**Adoption:** `preserve`

> **Ownership:** completion record for P46.2/G37; current behavior belongs in
> [`model-providers.md`](../../../architecture/platform/model-providers.md) and
> [`runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md)

## Outcome

The existing attempt coordinator remains the sole failover authority. After it
confirms a switch-eligible overload and constructable alternate, it emits the
failed attempt as `discarded` with `never_started` or `discarded` output
disposition. An exact tombstone follows only when retractable TUI output was
offered. The next attempt then emits `started` before provider dispatch; the
old attempt does not also emit terminal `failed`.

Runtime state records the process-local phase and exact tombstone. TUI removes
only the tombstoned assistant, thinking, tool-index, active-tool, and progress
owners before showing one warning. Plain and Headless write the same safe
profile/switch notice only to stderr. ACP emits one `model_fallback`
`_session/status` update and no assistant chunk. Library callers receive the
typed events without a forced writer; standalone MCP remains unchanged because
it has no model runtime.

## Compatibility And Rollback

The repair changes no portfolio configuration, candidate order, provider
adapter, retry/switch/deadline budget, route construction, usage settlement,
public result schema, transcript, or durable Session state. Plain, Headless,
ACP, and default library entrypoints still refuse to switch after visible
assistant output; only TUI output is retractable.

The notice uses only the normalized configured fallback profile and switch
count. API model, provider endpoint, account, credential, route details, raw
error, prompt, and failed output remain excluded.

Rollback removes the four presentation projections and changes successful
switch disposal back to `failed` plus output disposition. It requires no data
migration but reopens G37 and makes successful fallback silent again.

## Evidence

Production-path zero/partial-output fixtures pin discarded/tombstone/started
ordering before fallback dispatch. Reducer/replay, exact TUI retraction,
representative-width warning, Plain/Headless channel separation, ACP extension,
unsafe-field suppression, Unix PTY, P29.4 regression, focused race, all four
Makefile gates, docs/queue/manifest checks, and `git diff --check` pass on the
closeout tree. Detailed commands and limitations are in the
[verification record](../../verification/p46-2-observable-failover.md).

No live-provider, physical-terminal, or remote-CI claim is made.
