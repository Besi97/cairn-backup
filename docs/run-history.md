# Local run history

Cairn CLI invocations save small structured run summaries lazily, on the first
lifecycle event. Help, configuration validation, and browsing in the TUI do not
create history. TUI-started workflows, standalone operations, manual workflows,
and scheduled workflow attempts share the same per-user store:

- `$XDG_STATE_HOME/cairn/history` when `XDG_STATE_HOME` is absolute.
- Otherwise `$HOME/.local/state/cairn/history`, requiring an absolute `HOME`.

Use the same account and state environment for manual commands and the service.
The optional systemd unit already selects the account's `~/.local/state`.
History never replaces workflow, agent, or Borg locks and is not evidence that
an operation is currently running.

## Summaries and limits

Version 1 retains the **100 most recently observed runs**, ordered by their last
recorded event timestamp, with run ID as a deterministic tie-breaker. There is no
age limit or retention configuration. Incomplete runs count toward the same
limit and can be evicted. An evicted run that emits again can reappear as a
partial summary; records do not pin running work or reserve unbounded tombstones.

Summaries contain run ID, opaque configuration identity, workflow name,
standalone operation, manual/scheduled attribution, due time, preview status,
first/last observation and start/finish times, last sequence, terminal
outcome/reason, and aggregate successful, warning, failed, interrupted, and
skipped task counts. Task counts include only recorded terminal task events.
They are not byte/file statistics or proof of repository health.

`incomplete` means no terminal event was recorded. It does **not** mean active,
failed, or interrupted. `partial` means a sequence gap was observed, including
when the first retained event has sequence greater than one. A terminal partial
record has a known reported outcome but potentially incomplete counts.
Recorded outcomes are never rewritten after a restart. Read-time execution
observations are separate from these facts; inspection never recovers execution.

The snapshot payload is capped at 512 KiB. Atomic replacement uses at most one
512 KiB scratch payload, keeping Cairn-generated history file contents within
**1 MiB total**, excluding filesystem metadata/block allocation. Ordinarily 100
summaries use much less. Recovery evidence is also bounded as described below.
Workflow names and run
IDs exceeding 256 UTF-8 bytes are rejected for history rather than truncated.
An oversized or malformed store is reported, not silently erased or repaired.
The cap cannot constrain unrelated files or contents placed there externally.

## Recovery evidence

History can retain up to 16 recovery task records per run, identified by
one-based stage and task numbers. Each has a versioned projection of the report's
fixed step outcomes, aggregate sample statistics, and optional identifying details:
archive name and archive ID, scope paths and individual check outcomes. These
names and paths may be sensitive. They are quoted byte literals for display only,
never selectors. Repository IDs, expected contents and digests, raw diagnostics,
repository locations and credentials are excluded.

At most 32 paths and 32 checks are retained per task, with each name or path
limited to 256 input bytes. Counts report omitted items and a flag reports
truncated text. Additional or invalid reports increment `recovery_omitted`.
If the snapshot would exceed its cap, identifying details are discarded oldest
run first with `details_omitted` set, then complete recovery records if necessary;
run summaries remain. Evidence shares run retention and permissions.
Workflow recovery execution emits reports with terminal task events; invalid
evidence is omitted without changing the task summary or outcome. Recovery checks
can be configured for manual or scheduled workflows.

## CLI inspection

```sh
cairn history
cairn history --incomplete
cairn history --json
```

Show all retained runs, newest last-observation first, or only runs without a
recorded terminal event. No configuration or Borg installation is needed.
Inspection reads existing atomic snapshots without taking persistence or execution
locks, creating directories/files, or changing history. Missing storage succeeds
with “No recorded runs.” (or “No incomplete recorded runs.”); malformed, unsafe,
oversized, or unreadable storage fails with a redacted diagnostic, not empty history.

Human output quotes run IDs and configured workflow names to escape terminal
controls. It shows opaque configuration identity, workflow/operation,
manual/scheduled attribution, preview status, recorded timestamps and sequence,
outcome/reason, incomplete/partial flags, and terminal task counts. Owner and
lock observations are separate fields; raw owner identity and lock filenames are
not printed. Configured workflow names remain attribution, not secret-safe labels.
`--json` emits a version 1 object with `runs`, each containing a `summary` and
momentary `owner` and `lock` observations. It combines with `--incomplete`.
Recovery display literals must not be decoded and reused as restore selectors.

Successful inspection exits `0`, even when records report warning, failure,
interruption, or unknown outcomes. Storage/output failures exit `2`; invalid
arguments exit `3`. Diagnostics go to stderr, records to stdout. Missing execution
evidence is unknown, not an inspection failure. Read-time observations are
momentary, and age alone never establishes that a run stopped.

## TUI history

The **History** tab shows the same retained runs, newest last-event first.
Enter opens a run summary; Escape returns. The first visit reads history.
Press `g` to reopen and inspect it again; there is no automatic refresh or filter.
`r` still reloads only the TUI configuration. Inspection time labels the displayed
snapshot, not ongoing activity. A failed refresh reports unavailable history and
keeps any previous snapshot with its original inspection time.

Details show recorded timestamps, outcome, incomplete/partial flags, attribution,
and aggregate terminal task counts, separately from momentary owner/lock evidence.
Recovery task evidence is shown when retained; other per-stage/task results and
configuration contents are not retained. Configuration
identity identifies a path, not its contents: differences from the TUI or agent's
configuration snapshot cannot be determined. The tab reads local data only,
without contacting Borg, resolving secrets, taking locks, or rewriting history.
Leaving or closing the TUI cancels only its own read, not unrelated work.

## Concurrency, safety, and failures

Cairn directories use `0700`; files use `0600`, owned by the invoking user.
Unsafe Cairn child symlinks, file symlinks/hardlinks, nonregular files, and
insecure modes are refused. Directory descriptors anchor subsequent file access.
The XDG state parent must be user-owned and not group/world writable.

`history.lock` is a stable **persistence-only** flock. Each event update holds it
only while reading, updating, syncing, and atomically replacing `runs.json`.
Contending processes wait up to **100 ms**, then report unavailable history;
they do not wait for the other backup to finish. Normal filesystem IO and an
invocation's serialized observers can take additional time. Sustained contention
or slow storage can therefore lose history events. This is not an execution
queue, and failed events are never retried or caught up. Never delete this lock
while writers are active.

`runs.pending` is a fixed scratch file, safely reused after a crash and never
automatically promoted. A process crash leaves either the old or new complete
snapshot, not a partially rewritten snapshot. Files and the directory are synced
before success is reported; this is modest local history, not an audit log.

History failures produce a redacted warning immediately, at most once per minute
per invocation, and lifecycle delivery failures are also reported when the CLI
ends. Later events are still attempted. No filesystem error text or path is
printed by history reporting. Failure never cancels work, changes operation
outcomes/exit codes, disables another observer, or stops agent dispatch.

No Borg output, errors, command arguments, environment, credentials, repository
locations, or source/credential paths are recorded. Recovery archive names and
scope/check paths are identifying display evidence and may be sensitive.
Configured names are attribution, so do not use secrets as workflow names.
Configuration identity is a domain-separated SHA-256 digest of the canonical
configuration path, matching execution's symlink resolution. It is not a content
hash, an authorization token, or encryption against guessed paths. Standalone
operations have no configuration identity and do not discover configuration.

## Crash and restart observations

New locked workflow runs can also retain optional execution evidence: the opaque
workflow-lock filename and the Cairn owner's Linux boot ID, PID, and process-start
ticks. Capture failures leave that evidence absent without affecting execution.
No canonical configuration path is saved. Older records, standalone operations,
uncoordinated library runs, and attempts that did not acquire a workflow lock have
no execution evidence and remain **unknown** to inspection.

Inspection uses the current user's runtime/state environment and Linux `/proc`;
use the same environment and process namespace as execution. It only opens
existing execution directories/files. It never acquires a workflow or agent lock,
creates execution state, dispatches work, or contacts Borg.

Owner and lock evidence are independent, momentary observations, not an atomic
snapshot of execution:

- Owner **present** means boot ID, PID, and start ticks match a non-zombie process.
  It does not establish that a task is running or that Borg is alive.
- Owner **gone** means a different boot, absent PID, reused PID, or zombie/dead
  owner was observed. It says nothing about surviving Borg children.
- Owner **unknown** covers absent evidence and unreadable/uncertain process data.
- Lock **held** means a matching flock was positively observed in `/proc/locks`.
  Its holder could be an inherited Borg child or a later invocation; it does not
  establish that the recorded owner is alive or that this run is active.
- Lock **unknown** includes missing/unsafe/inaccessible files, unavailable kernel
  data, and no matching kernel entry. There is deliberately no “free” state:
  Linux can omit inherited flocks after the original Cairn owner disappears, even
  while they still prevent execution. Namespace visibility also limits evidence.

Consequently even a gone owner with an unknown lock leaves an incomplete run's
outcome unknown, not failed, interrupted, finished, or definitively stopped.
Age is not liveness evidence. Terminal outcomes, incomplete/partial flags, task
counts, sequence numbers, timestamps, ordering, and retention do not change during
inspection. A later run cannot finish an earlier record. Observations are not
persisted, and history remains neither execution authority nor a queue.

## Internal consumers

`internal/history.Open()` opens the shared writable store; `Store.Observe(lifecycle.Event)`
is a lifecycle observer. `Store.List()` returns a newest-first snapshot and
never infers liveness. `Store.Inspect()` returns newest-first `history.Observation`
values containing `Summary` and separate `Owner`/`Lock` fields
(`runlock.OwnerState`/`runlock.LockState`). Storage failures return
`history.ErrUnavailable`; unavailable execution evidence returns unknown fields,
not an inspection failure. No configuration file is needed.
`internal/history.OpenReadOnly()` provides the same `List`/`Inspect` API without
creating storage or taking any locks. Missing directories/snapshot return an empty
list; unsafe or unreadable storage still returns `history.ErrUnavailable`.
Read-only stores reject `Observe`. Atomic snapshot replacement allows readers to
see a complete old or new snapshot while a writer updates history. If storage was
missing at open time, reopen on refresh to discover newly created directories.
CLI and TUI consumers can use these structured observations without parsing text.
`Store.Close()` releases directory resources. All public
storage failures return only `history.ErrUnavailable`. The JSON envelope has
`version: 1` and `runs`; the optional `execution` object preserves readability of
older summaries by current readers. Older binaries may refuse the extended
snapshot rather than silently erase evidence. Unsupported versions fail closed.

`lifecycle.Event.ConfigID` is populated from `runlock.Identity.Digest()` at the
configured execution boundary, including scheduled preparation failures and
skipped occurrences. `lifecycle.Event.Execution` carries optional
`runlock.Evidence` only after a workflow lock is acquired. Consumers must keep
missing identity, partial summaries, and absent terminal events explicit.
Existing execution locks remain the sole Cairn authority for overlap.
