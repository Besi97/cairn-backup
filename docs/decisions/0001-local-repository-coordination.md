# Local repository coordination

Status: deferred. Not part of 0.1.0. Borg's own repository locks are the only
repository serialization in 0.1.0.

Date: 8 November 2026. Updated: 10 November 2026.

## Context

Independent manual commands and the scheduler can run Borg commands against the
same repository on one host. Borg already takes an exclusive repository lock for
`create`, `prune`, and `compact`, so overlapping writes are serialized by Borg.
Cairn-level coordination (lock files, a queue, workflow reservations, cross-process
admission, and cancellation of waiting work) would add substantial implementation
and safety validation work that early use does not need.

## Decision for 0.1.0

- Cairn adds no repository lock files, lock namespace, or queue.
- Each Borg command takes Borg's repository lock and waits up to the configured
  `lock_wait`. If the lock is not acquired in time, the run fails with exit code
  `4`. There is no retry loop.
- Backup, prune, and compact are not reserved as one workflow. Another job may run
  a Borg command between stages; Borg serializes each command. A failed backup
  still stops maintenance (see [supported environment](0002-supported-environment.md)).
- Scheduled runs of one job do not overlap within one agent process (agent work
  in [#8](https://github.com/Besi97/cairn-backup/issues/8)).
- Cross-process admission, which skips a scheduled run while the same job is
  waiting or running, is tracked in
  [#30](https://github.com/Besi97/cairn-backup/issues/30).

## Limits

- No queue and no fairness: a waiting run competes with others for Borg's lock.
- A waiting run can be stopped only by interrupting it (Ctrl-C or SIGTERM). That
  stops the Borg process if it has started.
- Scheduled runs are not skipped across processes while the same job is waiting
  or running (see #30).
- No reservation spans backup, prune, and compact.
- Other hosts, external processes, and unrecognized repository aliases rely on
  Borg's own locking.
- Cairn never runs `borg break-lock` automatically.

## Deferred design

The earlier direction proposed a per-repository lock file with an exclusive OS
advisory lock, held across the whole workflow. The following points remain
relevant if coordination is revisited, and must be validated before any guarantee
is claimed:

1. Repository identity: hashing and normalization rules, and alias limitations.
2. Lock directory: a per-account default under `$XDG_STATE_HOME/cairn/locks`, with
   a system path that services set explicitly and provision. Safe opening, owner
   and mode checks, and symlink rejection. Never fall back silently to a private
   directory after an access error.
3. Lock files: release the OS lock rather than the file. Never unlink or replace
   files while a process may hold or await them.
4. Cancellation that affects only the requesting execution, and shutdown that
   waits for Borg to exit before releasing any reservation.
5. Crash recovery without permanent blockage and without overlap with a surviving
   Borg subprocess. Parent exit alone is not sufficient.

Revisit this decision when concurrency problems appear in real use, or before
cross-process admission (#30), which needs shared state. Only claim these
guarantees after validation. The open coordination spike is tracked in
[#7](https://github.com/Besi97/cairn-backup/issues/7).
