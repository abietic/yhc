# Bounded concurrent map

Repair `Map` in `fixture/batch.go`. Preserve its public signature; use only the
Go standard library. The public example is intentionally insufficient.

- Invoke each input's callback once on successful execution, with at most
  `limit` callbacks active. Independent callbacks must progress while another
  is blocked, including starting a queued input when a worker becomes free.
- Return results in input order regardless of callback completion order.
- Reject a non-positive limit before invoking a callback. Empty input succeeds
  without invoking callbacks. An already-cancelled context invokes none.
- Give callbacks a context derived from the caller. On the first observed
  callback error, cancel that context and return that error after all started
  callbacks have exited. Callbacks cooperate with cancellation; the caller may
  also cancel. Inputs not yet started may be skipped after cancellation.
- A successful return acknowledges completion of all callbacks. Error and
  cancellation returns also join every started callback. Do not mutate input.

Only this task and `fixture/` belong in an independent agent workspace.
`controls/` and `grader/` are evaluator inputs and must remain outside it.
Offline calibration proves the tests distinguish trusted controls, not that
an agent or model can solve the task.
