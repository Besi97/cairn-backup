# Local repository coordination

Status: agreed direction; implementation and safety validation pending.

Date: 8 November 2026.

## Context

Independent manual commands and the scheduler must serialize workflows targeting
one repository on the same host, without requiring a running agent. Only
repository coordination needs sharing; configuration, secrets, and history can
remain account-local.

## Direction

- Use a per-repository file with an exclusive OS advisory lock, such as Linux
  `flock`. Contenders wait without starting Borg.
- Hold the lock throughout **backup → prune → compact**.
- Use a dedicated local-filesystem namespace, proposed at `/run/lock/cairn`,
  with explicitly managed access and provisioning after reboot.
- Derive filenames from stable repository identities. CLI and agent must use
  consistent identities and the same namespace, independent of private state.
- Release the OS lock, not the filename. Never unlink or replace files while
  processes may hold or await their locks: replacement creates a separate lock
  object and can permit overlapping workflows.

Avoid `/tmp`: predictable files invite pre-creation/symlink attacks and ownership
conflicts; cleanup can unlink active locks; differing temporary-directory settings
can split coordination. A dedicated directory still needs safe creation/opening
and ownership checks.

Shared lock access does not grant credential or source access, but participants
can deny service by holding locks. Prevent untrusted replacement or redirected
privileged opens. Never silently fall back to private locks after access errors.

## Limits

These locks cover cooperating local Cairn processes only. External commands,
other hosts, separate namespaces, and unrecognized repository aliases still rely
on Borg locking. Use Borg's bounded external-lock wait, then fail without an
automatic retry loop.

File locks alone do not provide per-job duplicate suppression, FIFO fairness,
durable queues, history, or safe recovery from surviving Borg subprocesses.
Additional metadata or process controls may be necessary; do not assume a shared
database is required.

## Open design and validation

The prototype must define and test:

1. Repository identity, hashing/normalization rules, and alias limitations.
2. Boot/install provisioning, safe file opening, ownership/modes, authorized
   cross-account access, and rejection of symlinks/replacement attempts.
3. CLI/agent serialization across full workflows; multiple distinct jobs may wait.
4. Atomic scheduled admission: skip a job already waiting/running, including
   relevant manual work. Repository contention alone cannot identify the job.
5. Cancellation affecting only the requesting execution, and shutdown that stops
   remaining stages and waits for Borg before releasing the reservation.
6. Shutdown timeout/escalation and crash recovery without permanent blockage or
   overlap with a surviving Borg subprocess. Parent exit alone is not sufficient.

Only claim these guarantees after validation. See the
[supported environment](0002-supported-environment.md) for the release contract.
