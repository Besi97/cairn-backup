# Local run history

Cairn CLI invocations save small structured run summaries lazily, on the first
lifecycle event. Help, configuration validation, archive browsing, and the
read-only TUI do not create history. Standalone operations, manual workflows,
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
No restart reconciliation, lock probing, inspection command, or execution
recovery is performed by this store.

The snapshot payload is capped at 512 KiB. Atomic replacement uses at most one
512 KiB scratch payload, keeping Cairn-generated history file contents within
**1 MiB total**, excluding filesystem metadata/block allocation. Ordinarily 100
summaries use much less. Large workflows only increment fixed-size counters:
no per-task or per-stage list grows with the workflow. Workflow names and run
IDs exceeding 256 UTF-8 bytes are rejected for history rather than truncated.
An oversized or malformed store is reported, not silently erased or repaired.
The cap cannot constrain unrelated files or contents placed there externally.

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
locations, source/credential paths, or archive names/selectors are recorded.
Configured names are attribution, so do not use secrets as workflow names.
Configuration identity is a domain-separated SHA-256 digest of the canonical
configuration path, matching execution's symlink resolution. It is not a content
hash, an authorization token, or encryption against guessed paths. Standalone
operations have no configuration identity and do not discover configuration.

## Internal consumers

`internal/history.Open()` opens the shared store; `Store.Observe(lifecycle.Event)`
is a lifecycle observer. `Store.List()` returns a newest-first snapshot and
never infers liveness. `Store.Close()` releases directory resources. All public
storage failures return only `history.ErrUnavailable`. The JSON envelope has
`version: 1` and `runs`; unsupported versions fail closed.

`lifecycle.Event.ConfigID` is populated from `runlock.Identity.Digest()` at the
configured execution boundary, including scheduled preparation failures and
skipped occurrences. Consumers must keep missing identity, partial summaries,
and absent terminal events explicit. Existing execution locks remain the sole
Cairn authority for overlap and any future reconciliation.
