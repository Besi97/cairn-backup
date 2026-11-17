# Lifecycle events

`cli/internal/lifecycle` is the shared internal event API for configured manual
and scheduled workflows and standalone backup, prune, compact, check, and restore runs. It is
not a JSON CLI mode or a persistent history store. Human output, Borg diagnostics,
exit codes, and file-lock coordination retain their existing contracts.

## Observing runs

Wrap the invocation context with `lifecycle.WithObserver(ctx, observer)`.
`Observer` receives an `Event` value and returns an error. Pass that context to
`cli.Run`, `agent.Agent.Run`, or `workflow.Executor.RunLocked`. The uncoordinated
`Executor.Run` primitive emits the same events for tests and internal callers.
No observer means no delivery, without changing execution.

`lifecycle.WithAdditionalObserver(ctx, observer)` adds a consumer without replacing
existing observers. Every consumer receives the same event even if another fails.
`lifecycle.Err` on the returned context includes failures from existing observers;
`WithObserver` still replaces observers when that is explicitly intended.

Callbacks are synchronous and serialized across runs sharing the observer.
They must return promptly and must not emit recursively. A failed callback does
not cancel work, alter outcomes,
disable subsequent delivery, or trigger retries. `lifecycle.Err(ctx)` returns
only the redacted `ErrDelivery` sentinel. Library callers must check it; the CLI
reports a safe warning when its invocation ends, including agent shutdown.
Callback error text is never put in events or CLI output.

## Event contract

- `RunID` is a random identity for each attempted run or skipped occurrence.
  `Sequence` increases from one within that run; `Time` is a UTC observation
  timestamp. Different concurrent runs may interleave.
- `Source` is `manual` or `scheduled`. `ScheduledAt` identifies the due instant
  for scheduled work; an expired-occurrence report can summarize several missed
  occurrences using the first expired instant. No missed-work queue is built.
- `Workflow` is the configured name. `Operation` is backup, prune, compact, check, or restore;
  task events also carry a configured `Repository` alias. Standalone runs omit
  repository attribution rather than recording a possibly sensitive location.
  `Preview` distinguishes standalone dry runs.
  Both check verification modes use `check` and the same normalized outcomes;
  events do not contain verification flags or imply that warnings are clean checks.
  Manual-only restore uses one run/task across preflight and extraction, with
  `Preview` for dry runs; it has no configured workflow counterpart.
- `ConfigID` is the opaque canonical-configuration digest from
  `runlock.Identity.Digest()`, captured at configured execution entry points.
  Scheduled skips and preparation failures carry the same identity. Standalone
  operations omit it; configuration paths and contents are never event fields.
- `Stage` and `Task` are one-based configuration indexes. Zero means not
  applicable. Standalone operations have task 1 and no stage.
- `Kind` distinguishes run, stage, and task starts, finishes, and skips.
  Started work finishes; unstarted work is skipped. A skipped run has no start
  and ends with `RunSkipped`, after its skipped stages and tasks.
- `Outcome` is empty on starts and otherwise success, warning, failure,
  interrupted, or skipped. `Reason` explains skipped work independently of
  outcomes: already running, expired occurrence, prior-stage warning/failure,
  or cancellation. Preparation failures use `preparation_failed`; lock/storage
  setup failures use `execution_unavailable` rather than implying a stage ran.

Task order inside a concurrent stage is not fixed. All task terminal events
precede the stage finish; a stage finishes before another starts. Later stages
and their tasks receive explicit skips after a warning, failure, or cancellation.
An independently signalled Borg child can be an interrupted task while its
workflow fails; only cancellation of Cairn interrupts the workflow.

Standalone events begin after argument validation and operation preparation.
Configured execution events begin at the workflow-lock attempt; preparation
errors during scheduled dispatch are reported as failed runs. Configuration
loading, help, listing, TUI browsing/reload, and invalid standalone inputs are not
runs. Reloading the read-only TUI never starts or changes workflow execution.

Events intentionally omit errors, command arguments, environment variables,
credentials, repository locations, source paths, archive names/selectors, rendered
scope/status text, and Borg output. Consumers must keep diagnostics separate and
must not reconstruct events by parsing human output.

CLI invocations also attach a lazy [local history](run-history.md) observer.
Library callers can opt into `history.Store.Observe`; persistence is not built
into lifecycle emission or execution locks.
