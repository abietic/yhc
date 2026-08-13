# Desktop Session Creation Activation Implementation Plan

**Status:** active-plan
**Plan state:** Implementation and focused verification complete; committed-tree and physical acceptance pending
**Accepted contract:**
[`2026-08-13-desktop-session-creation-activation-design.md`](../specs/2026-08-13-desktop-session-creation-activation-design.md)

> **Ownership:** executable test-first renderer repair for the accepted Desktop
> session creation activation contract

> **Goal:** make a newly created Desktop session visible immediately after the
> server returns its identity, while keeping hydration target-scoped and
> preventing overlapping workspace selections.

## Task 1: Add deterministic renderer orchestration tests

**Files:**

- Create `internal/webui/assets/session_creation.mjs`
- Create `desktop/test/session_creation.test.mjs`

Write tests with deferred Promises that prove:

1. activation occurs before hydration completes;
2. hydration completion cannot reclaim a selection the user changed;
3. a second creation attempt reuses the in-flight Promise without invoking the
   picker/create callback again;
4. busy state begins before picker/create work and clears after success,
   cancellation, and failure; and
5. hydration failure preserves the already activated session identity.

Run the focused test before implementation and retain its expected failure as
the regression oracle:

```bash
node --test desktop/test/session_creation.test.mjs
```

## Task 2: Implement the ordering helper

**Files:**

- Modify `internal/webui/assets/session_creation.mjs`
- Verify `desktop/test/session_creation.test.mjs`

Implement two narrow helpers:

- `activateCreatedSession(summary, { activate, hydrate })` invokes `activate`
  synchronously before awaiting target-scoped `hydrate` and never performs a
  later selection;
- `createSessionCreationGate(run, onBusyChange)` exposes one in-flight Promise
  and a renderer-local busy predicate, beginning before `run` is invoked and
  clearing in `finally`.

Run:

```bash
node --test desktop/test/session_creation.test.mjs
```

## Task 3: Wire immediate activation into the renderer

**Files:**

- Modify `internal/webui/assets/app.mjs`
- Modify `desktop/test/onboarding_structure.test.mjs`

Split existing session synchronization into explicit phases:

1. prepare/upsert the target summary as `restoring`;
2. select the new session immediately for the create-session path only;
3. hydrate transcript, snapshot, execution settings, and stream for the target
   ID; and
4. finalize that summary without dispatching `SESSION_SELECT`.

Wrap the full native flow—from before workspace picker invocation through
hydration—in the single-flight gate. While busy, disable both New session entry
points, expose `aria-busy`, and show a visible creation label. Preserve existing
provider-setup and cancellation behavior before a session summary exists.

Run:

```bash
node --test desktop/test/session_creation.test.mjs \
  desktop/test/onboarding_structure.test.mjs
node --check internal/webui/assets/app.mjs
```

## Task 4: Run focused and publication verification

**Files:**

- Update `quality/publication.yaml` only for exact reviewed findings introduced
  by this plan or implementation
- Regenerate `PUBLICATION_MANIFEST.json`

Run:

```bash
node --test desktop/test/*.test.mjs
make desktop-check
make docs-check-ci
make publication-check-policy
make publication-scan-expression PUBLICATION_ROOT=.
make change-plan
make verify-focused
```

Do not claim provider or packaging acceptance from these source-level gates.

## Task 5: Commit and verify the reviewed tree

Commit only the plan, renderer helper/wiring, focused tests, and required
publication evidence. Then run:

```bash
make verify-merge
make change-evidence
make change-evidence-ready
```

If any committed-tree gate changes generated evidence, review and commit that
evidence before rerunning the gate.

## Task 6: Package and physically accept the fresh app

Build a fresh unsigned macOS package from the final committed tree and launch
that artifact after fully quitting stale YHC processes. Verify with Computer
Use:

1. selecting the `yhc` workspace shows the `yhc` label without waiting for
   hydration;
2. repeated New session activation is unavailable while creation is busy;
3. Markdown input sends and renders as heading/list/bold content;
4. Activity and Changes remain semantic; and
5. selecting saved history does not resume until the first user send.

Report unsigned/notarized status separately from local usability. Push only the
topic branch after local committed-tree evidence and physical acceptance pass.
