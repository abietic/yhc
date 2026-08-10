# P43.0 Real-Repository Evaluation Baseline

**Status:** historical
**Closed gaps:** G29
**Completed:** 2026-08-02
**Adoption:** `combine`

> **Ownership:** completion record for the first opt-in real-repository
> evaluation baseline. The frozen contract remains
> [`p43-real-repository-evaluation.md`](../../plans/p43-real-repository-evaluation.md).
> This record does not make a score authoritative or turn evaluation into a
> release gate.

## Outcome

`make eval-baseline` now builds the current public `eino-agent` executable and
runs one standalone harness around `exec --output-format json`. The target is
outside `verify`, required GitHub workflows, cross-platform release builds,
and production scheduling. A source gate rejects any harness dependency on a
production project package, so the evaluated subject cannot silently become
an in-process `QueryEngine` shortcut.

One invocation owns two fresh private repositories. Each replay also owns
separate HOME, XDG, temporary-artifact, grader, and provider roots. On macOS,
the harness canonicalizes the temporary root before permission evaluation, so
the `/var` to `/private/var` alias cannot turn a contained Write into a false
escape. Cleanup finishes before a replay can receive a passing cleanup grade;
an injected cleanup failure prevents report publication.

## Scenario And Grade

The committed `localized-write-fix/v1` manifest lists exactly three regular
fixture files, one external hidden grader, and one three-step provider script.
Closed JSON decoding rejects unknown or trailing input. Duplicate, absolute,
traversing, linked, non-regular, undeclared, or over-budget fixture paths fail
before the public command starts.

The loopback provider requires one fake credential, one OpenAI-compatible
model identity, and exactly the `Write` function. Its ordered script proves:

1. a Write outside the disposable repository is denied by the public headless
   fallback and leaves the outside sentinel unchanged;
2. one contained `greet/decorate.go` Write returns the exact success result;
3. a final assistant response settles the public JSON envelope.

Public `go test` and a grader-owned overlay test cover the ordinary, empty,
and non-ASCII cases under an explicit 60-second per-command timeout. Git
status must contain only repository-local
`.eino-agent` Session metadata plus the declared product addition. The
canonical grade records exact provider/model/tool counts, scripted usage of
3 input and 3 output tokens, unavailable cost, unexercised recovery, and the
final source-tree digest. Measured replay duration remains diagnostic and is
excluded from the byte-equality comparison.

## Isolation And Report Safety

The scenario exposes no Read, Bash, WebFetch, MCP, Agent, hook, or process
tool. Environment inheritance is replaced by an allowlist; stdout and stderr
have independent limits; Unix process groups and Windows Job Objects own
timeout/cancellation cleanup. The report therefore marks host checkout,
workspace Write, model-visible host read, and credential handling according to
their tested mechanisms. OS-wide network and process/syscall/resource
containment remain `not_evaluated`; TUI, Plain, ACP, and standalone MCP remain
`not_evaluated`. P43.0 closes G29 but does not close G28.

The versioned report is capped at 64 KiB and scanned for the prompt, fake key,
provider call IDs, repository bytes, and absolute private paths. Publication
requires an existing 0700 non-link parent and creates one 0600 file without
replacement. Unix binds publication to the originally approved directory
identity with `O_NOFOLLOW`, `openat`, `linkat`, `unlinkat`, and directory
`fsync`; Windows holds a non-delete-sharing directory handle. Target creation,
symlink replacement, post-validation parent replacement, and pre-open
replacement by another real 0700 directory all fail without publishing a
passing report.

## Verification And Review

Default focused tests cover closed manifests and scripts, path/file/byte bounds,
provider auth/tool/order drift, malformed envelopes, bounded buffers, public
and hidden graders, exact residuals, report redaction/size/mode/collision,
target and parent races, and timeout/cancellation process-tree cleanup. The
external double replay and cleanup-precedence test runs only with
`EINO_EVAL_EXTERNAL_TEST=1`, keeping it outside the required suite. The
evaluation package passes race tests and compiles for Windows amd64. Two fresh
opt-in invocations produce passing reports through the built product executable.

Repository gates `make fmt`, `make lint`, `make lint-new`, `make test`,
`make build`, documentation and migration-ledger checks, plus
`git diff --check`, passed for the candidate.

Independent review first found that path-based report publication could write
through a parent-directory replacement. A first fd-relative repair still did
not bind the fd to the directory identity approved before `open`. The final
implementation carries that original identity into platform publication and
tests both race windows. Follow-up review found no remaining issue.

## Owners And Rollback

- [`scripts/evaluation`](../../../../scripts/evaluation/main.go) owns the
  command, manifest, scripted provider, replay lifecycle, graders, report, and
  tests.
- [`Makefile`](../../../../Makefile) owns the opt-in `eval-baseline` invocation
  and product-binary build dependency.
- [`p43_0_characterization_test.go`](../../../../cmd/yhc/cmd/p43_0_characterization_test.go)
  remains promotion evidence only; it is not the reusable harness.

Rollback removes `scripts/evaluation`, the opt-in Make target, this closeout,
and G29 closure. It changes no production provider, permission, Session,
QueryEngine, CI-required, or release behavior. Generated reports and private
repositories are disposable; rollback has no durable data migration.
