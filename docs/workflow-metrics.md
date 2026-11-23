# Workflow metrics collection

`internal/metrics.New(configID, names)` fixes up to 256 distinct, nonempty
configured workflow names (each at most 256 UTF-8 bytes). Pass the configuration's
`runlock.Identity.Digest()` and attach `collector.Observe` with
`lifecycle.WithAdditionalObserver` before dispatch. `collector.Snapshot()` is
concurrency-safe and returns sorted `Workflow` values and `HistoryAvailable`.
There is no endpoint or automatic collector installation. A future transport must
retain these scope and unknown semantics and must not expose the identity.

Workflow names are labels by explicit choice: they may reveal sensitive business
information. Do not put secrets, paths, or other sensitive information in names;
protect any future metrics transport accordingly. No repository, archive, path,
configuration identity, run ID, task, or error becomes a label. The fixed name
set and fixed outcome fields bound series per collector; consumers must also
bound how many collectors/configurations they export.

Outcome counters and busy skips count lifecycle terminal events observed by this
collector since creation, not retained runs or a lifetime total. An expired skip
may represent multiple missed occurrences, so it counts as one event. Busy is
only a locally observed rejection, not a queue or global contention measurement.
Counters and observed in-flight runs reset on restart. In-flight counts only
starts observed by this collector that have not had a matching observed terminal
event; they say nothing about other processes or runs already in progress when
observation began. At most 1024 starts are tracked; after overflow the affected
workflow's `InFlightKnown` stays false and its value is only a lower bound until
the collector is replaced. Missing events can also make observations incomplete.

Latest outcome, finish and duration combine locally observed terminals with the
newest matching retained terminal, excluding previews. Duration is unknown when
there is no matching start or timestamps are inconsistent; a skip has no duration.
`SuccessKnown` and `LastSuccess` describe the newest observed or retained
successful **workflow** completion. This is workflow freshness, not backup age,
and does not prove a backup succeeded or even ran. Warning is not success.
Unknown fields are not zero-valued health claims. History is best effort and
shared only by processes using the same state root; it contains at most 100 runs
globally across configurations, so eviction or failed delivery can remove the
last success. It filters by configuration identity and name; replacing a config
at the same path is not distinguishable. Incomplete history is never treated as
running. `HistoryAvailable` is false on unsafe or unreadable storage, while a
missing store is available and empty. History and live observations are not an
atomic cross-process snapshot. Restart retains only what history still contains.
No lifetime counters are derived from history.

No overdue metric is defined: a threshold might be a fixed per-workflow maximum
age, derived from schedule and grace, or an external alert policy. A choice must
also specify unscheduled workflows, missed runs, clock changes, and unknown
freshness. Until that policy is agreed, consumers should not infer overdue from
an undocumented threshold.
