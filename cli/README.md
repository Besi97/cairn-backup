# Cairn CLI

The CLI provides standalone backups, pruning, and compaction, help, and version
output, plus optional configuration checking, manual workflows, and a foreground
scheduler. See the [installation guide](../docs/installation.md) and optional
[systemd setup](../docs/systemd.md). Install stable Borg 1.x
(at least 1.2.8) separately; SSH repositories also require OpenSSH.

## Backup

Back up to an existing local or SSH repository without configuration or a running
scheduler. A backup never prunes, compacts, or initializes a repository.

```sh
cairn backup --repository /backups/repo --archive-prefix documents \
  --source-root "$HOME" --exclude '*.tmp' Documents

cairn backup --repository ssh://user@backup-host/./repo \
  --archive-name '{hostname}-before-upgrade' \
  --passphrase-file ./passphrase --identity-file ~/.ssh/backup "$HOME/Documents"

cairn backup --repository /backups/repo --archive-prefix documents \
  --dry-run --source-root "$HOME" Documents
```

Require an explicit nonempty `--repository`, at least one source path, and exactly
one of `--archive-prefix` or `--archive-name`. There is no `BORG_REPO` fallback.
A prefix generates `PREFIX-YYYYMMDDTHHMMSS.NNNNNNNNNZ` using UTC; an exact name
receives no timestamp. Borg expands placeholders such as `{hostname}` normally.
Names retain Borg's restrictions, and collisions fail without replacing an archive
or retrying with another name. The initial stderr status shows the unexpanded
archive template; native Borg diagnostics may show its expanded name.

`--source-root DIR` sets Borg's working directory. Without it, use Cairn's invocation
directory. Relative source paths resolve there; absolute sources are allowed.
Source paths are passed unchanged, with Borg's native archive layout. Source root
is not a sandbox. Relative local repositories, passphrase files, and identity files
always resolve from Cairn's original directory, not the source root. Borg SSH URLs
and scp-style locations retain their native remote-path semantics.

Repeat `--exclude PATTERN` to add Borg exclusion patterns. Quote shell globs and
placeholders to leave expansion to Borg. Use `--` before source names beginning
with a hyphen; arguments after it are sources, not extra Borg options. Compression
remains Borg's default; exclusion files and arbitrary Borg options are not exposed.

`--dry-run` lists files Borg would process on stderr, without creating an archive.
It does **not** establish repository access, working credentials, or that every
file's contents can be read. It is not a guarantee that a subsequent backup will
succeed or that no local filesystem state is touched.

## Prune

Prune an existing local or SSH repository without configuration, a scheduler, or
a preceding backup. No confirmation prompt or `--yes` flag is involved.

```sh
cairn prune --repository /backups/repo --archive-prefix documents \
  --keep-daily 7 --keep-weekly 4 --dry-run

cairn prune --repository /backups/repo --archive-prefix documents \
  --keep-daily 7 --keep-weekly 4

cairn prune --repository ssh://user@backup-host/./repo \
  --archive-prefix '{hostname}-documents' --keep-within 2d --keep-monthly 12 \
  --passphrase-file ./passphrase --identity-file ~/.ssh/backup
```

Require an explicit nonempty `--repository`, nonempty `--archive-prefix`, and at
least one effective retention rule. There is no repository environment fallback,
implicit all-archives mode, exact-name deletion option, or positional argument.
Relative local repositories and access files resolve from Cairn's invocation
directory, as for backup.

Cairn appends `-*` to the supplied prefix and passes that selector to Borg's
`--glob-archives`. For example, `documents` selects both timestamped backups and
`documents-before-upgrade`, but not `documents2-backup` or `documents` itself.
**Placeholders and glob metacharacters are not escaped.** Deliberately broader
patterns broaden deletion scope. Quote selectors to prevent shell expansion;
the initial stderr status shows the unexpanded selector.

Supported retention rules:

- `--keep-within INTERVAL`: keep all archives within a positive integer interval
  using Borg's units `H`, `d`, `w`, `m`, or `y`. A month is 31 days and a year
  is 365 days; this is not Go duration syntax.
- `--keep-secondly COUNT`, `--keep-minutely COUNT`, `--keep-hourly COUNT`,
  `--keep-daily COUNT`, `--keep-weekly COUNT`, `--keep-monthly COUNT`, and
  `--keep-yearly COUNT`: keep Borg's selected archives for those periods.
  Zero disables a rule; negative counts mean unlimited periods for that rule,
  not necessarily every archive in those periods. A policy of only zeros is
  rejected.

Borg owns retention ordering, rule interaction, and checkpoint handling. It uses
**archive metadata timestamps**, not dates parsed from archive names. Calendar
retention follows Borg's local timezone; UTC timestamps in Cairn-generated names
do not change that. Newer quarterly rules and arbitrary Borg flags are not exposed.

Both pruning and previews show native keep/prune lists on stderr. `--dry-run`
does not delete archives, but unlike backup previews it requires repository
access and working credentials. It may acquire locks and update local Borg state;
it is not a guarantee that a subsequent prune will succeed or make the same
choices after archives or time have changed.

Prune never invokes compact. Deleting archives does not reclaim eligible
repository disk space until a separate compaction operation.

## Compact

Reclaim eligible space across an existing local or SSH repository without
configuration, a scheduler, or a preceding backup or prune.

```sh
cairn compact --repository /backups/repo

cairn compact --repository ssh://user@backup-host/./repo \
  --identity-file ~/.ssh/backup --lock-wait 60
```

Require an explicit nonempty `--repository`; there is no `BORG_REPO` fallback.
Compaction applies to the **whole repository**, not an archive prefix or job.
It does not delete archives, initialize repositories, or invoke backup or prune.
There is no confirmation prompt or `--yes` flag.

Borg retains its default compaction threshold (10%). Native progress and verbose
space-reclamation estimates go to stderr; success does not promise that any space
was eligible to reclaim. Cairn does not expose threshold tuning, legacy commit
cleanup, or arbitrary Borg options.

Compaction does not need the repository encryption key or passphrase, even for
encrypted repositories. SSH authentication and host verification still apply.
The optional `--passphrase-file` remains available for interface consistency;
explicitly supplied files and inherited credential descriptors receive the same
validation as other operations. Relative repository and access-file paths resolve
from Cairn's invocation directory.

`--dry-run` is rejected: Borg 1.2.8 has no compaction preview, and Borg 1.4.5's
option merely does nothing rather than providing a useful preview.

## Integrity checks

Check an existing local or SSH repository without configuration or a scheduler:

```sh
cairn check --repository /backups/repo
cairn check --repository /backups/repo --verify-data
cairn check --repository ssh://user@backup-host/./repo \
  --passphrase-file ./passphrase --identity-file ~/.ssh/backup --lock-wait 60
```

Require an explicit nonempty `--repository`; there is no `BORG_REPO` fallback.
Default checks cover **whole-repository consistency**, including segment sizes
and checksums, plus **all archive metadata** and referenced-chunk existence.
They do **not cryptographically verify file contents**. `--verify-data` also
reads, decrypts, decompresses, and cryptographically verifies archived data.
This can take a long time and transfer substantial data from SSH repositories.
Remote repository consistency checks run on the server; archive checks run on
the client and need the encryption key and working credentials.

Checks never request repair or change archive contents, but Borg can acquire
locks and update its own state; this is not a promise of no filesystem writes.
There are no repair, partial-duration, repository-only, archives-only, archive
selector, dry-run, or arbitrary Borg options. A successful check is not a restore
test or a guarantee against future damage. Investigate warnings and failures
using the streamed Borg diagnostics; Cairn does not attempt repairs or retries.

Relative repository and access-file paths, credential precedence, SSH host
verification, locking, cancellation, and exit codes follow the shared contracts
below. Scope and Borg diagnostics go to stderr; the final check outcome goes to
stdout. Exit `1` means warnings, not a clean check; `2` means failure (including
lock timeout), `3` invalid inputs, and `130`/`143` interruption.

## Configuration

The [configuration contract](../docs/configuration.md),
[version 1 YAML validation schema](schema/config-v1.schema.json), and
[example configuration](examples/config.yaml) define the agreed repository and
workflow format.

```sh
cairn config check
cairn config check --config ./config.yaml
```

Checking discovers and validates one file without executing Borg, contacting a
repository, opening credential files, or checking source paths. It exits with
`0` on success or `3` for invalid/missing configuration. Without `--config`, Cairn
tries the user's XDG configuration location, then `/etc/cairn/config.yaml`.
Selected invalid/unreadable files are errors; files are never merged.

Checking also validates five-field cron schedules, supported calendar aliases,
and optional workflow timezones. See the [schedule contract](../docs/configuration.md#schedules)
for the dialect and clock behavior. Schedules run only while
[`cairn agent run`](#scheduled-workflows) is active. Standalone operations, help,
and version never load configuration.

## Configured workflows

List configured workflow names in lexicographical order, then run one immediately
without a scheduler:

```sh
cairn config check --config ./config.yaml
cairn workflow list --config ./config.yaml
cairn workflow run nightly --config ./config.yaml
```

Omit `--config` to discover user then system configuration. Listing validates
the whole selected file but does not execute Borg or access repositories,
credential files, or source paths. An empty workflow map prints nothing and
succeeds. Workflow names are case-sensitive.

The listing command exits `0` on success, `3` for invalid arguments/configuration,
and `2` if output cannot be written.

For execution, the whole selected file is validated before tasks start.
Stages run in order; tasks within each stage run concurrently. Warnings or
failures stop subsequent stages only after all siblings finish. Cancellation
stops active operations and waits for subprocess cleanup.

Workflows reuse the standalone backup, prune, and compact operations. Their
fields cannot be overridden with standalone flags, and workflow dry-run is not
supported. Standalone commands still never discover configuration.

Exit codes: `0` success, `1` warnings, `2` execution failure, `3` invalid input;
Cairn cancellation exits `130` (SIGINT) or `143` (SIGTERM).
Manual workflows start immediately, regardless of schedule, and reject duplicate
runs of the same workflow/config with exit code `2`. Locks remain held by Borg
children that survive a Cairn crash. See
[workflow overlap](../docs/configuration.md#workflow-overlap) for lock locations
and the shared runtime/state environment required for coordination.
See the [configuration contract](../docs/configuration.md) and
[example configuration](examples/config.yaml).

## Scheduled workflows

```sh
cairn agent run --config ./config.yaml
```

The agent runs in the foreground until interrupted. Omit `--config` to use the
same user-then-system discovery as manual workflows. It validates and loads the
whole configuration once; restart to apply edits. At least one workflow must
have `cron`. Manual-only workflows are ignored by the agent.

There is no startup backup or catch-up backlog: scheduling starts at the next
future occurrence. Expired minutes are skipped; a late wakeup can still dispatch
the current minute. Clock adjustments do not replay already-consumed occurrences.
See the [schedule contract](../docs/configuration.md#schedules) for timezones and DST.

Different workflows run concurrently, without a repository queue. Duplicate runs
are skipped without waiting or retrying, including when a manual run holds the
workflow lock. Warnings/failures stop later stages of that run, not the agent;
later cron occurrences remain independent attempts.

Only one agent may run for a canonical config path. Use the same runtime/state
environment for manual and agent invocations, including service managers; see
[workflow overlap](../docs/configuration.md#workflow-overlap). Manual invocations
are still allowed while an agent is idle.

Agent lifecycle and workflow summaries go to stderr with UTC timestamps; task
outcomes go to stdout and labelled Borg diagnostics to stderr. No persistent
run history is stored. Output failure stops the agent and cancels active work.
SIGINT/SIGTERM stop dispatching, cancel active workflows, and wait for subprocess
cleanup before releasing the agent lock.

Exit codes: `3` for invalid arguments/configuration or no scheduled workflows,
`2` for agent startup/runtime failures (including a duplicate agent or output
failure), and `130`/`143` for SIGINT/SIGTERM. Individual workflow warnings/failures
are reported without terminating the process.

## Recovery without Cairn

Cairn creates ordinary Borg 1.x archives. Use native Borg to list, inspect, and
restore them even if Cairn or its configuration is unavailable. Follow the
[recovery guide](../docs/recovery.md) to restore into a separate directory,
including passphrase and SSH setup.

## Shell completion

Cairn can generate completion scripts for Bash, Zsh, Fish, and PowerShell:

```sh
cairn completion bash
cairn completion zsh
cairn completion fish
cairn completion powershell
```

Use `cairn completion SHELL --help` for setup instructions. Running the command
without `--help` prints the completion script. Bash completion requires the
`bash-completion` package. For PowerShell, add
`cairn completion powershell | Out-String | Invoke-Expression` to your PowerShell
profile. Completion is available wherever the Cairn binary runs.

Completions include commands, flags, local paths for path-valued options and
backup sources, and directories for `--source-root`. Relative backup source
suggestions follow the selected `--source-root`. SSH-style repository values do
not trigger local path suggestions. Archive names and patterns are treated as
literal Borg inputs rather than filesystem paths.

For `cairn workflow run`, Cairn reads and validates the selected configuration
to suggest matching workflow names. It uses `--config` when supplied, otherwise
the normal user/system discovery order. Put `--config` before the workflow name
when completing from a specific file:

```sh
cairn workflow run --config ./config.yaml <TAB>
```

Workflow completion does not execute Borg or access repositories, credential
files, or source paths. Missing or invalid configuration produces no workflow
suggestions; use `cairn config check` to see configuration diagnostics.

## Output and exit codes

Operation status, Borg progress/statistics, file/archive lists, warnings, and errors go
to stderr. The final human-readable outcome goes to stdout. Output wording is not
a machine-readable API.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Completed with warnings |
| 2 | Execution failure, including missing/unsupported Borg or lock timeout |
| 3 | Invalid invocation or command inputs |
| 128 + signal | Interrupted, including 130 for SIGINT and 143 for SIGTERM |

Borg's modern exit-code mode does not change these Cairn meanings. Source access,
repository access, and native Borg validation errors are reported as execution
outcomes rather than Cairn argument-parsing failures.

## Development

Use Go 1.27.4 or later and Make. Run from this directory:

```sh
make build
./bin/cairn --help
./bin/cairn --version
make check
make cross-build
```

`make check` checks formatting, runs `go vet`, tests with the race detector,
and runs a pinned version of `govulncheck` against the current vulnerability
database. Go downloads dependencies and the scanner as needed; no global tool
installation is required. Race tests require a C compiler.
`make test` and `make check` explicitly disable real-Borg tests, even if
`CAIRN_TEST_BORG` is inherited from the environment. Process-runner tests still
execute a compiled fake Borg binary to test subprocess behavior.

Builds default to version `dev`. Set `VERSION` for a versioned binary:

```sh
make build VERSION=0.1.0
```

`cmd/cairn` contains the process entry point. `internal/cli` handles arguments
and output; `internal/backup`, `internal/prune`, and `internal/compact` prepare
operation requests without Cobra or configuration. Help and version output do
not require Borg or configuration.

`internal/borg` provides the shared process runner. It checks the selected
executable's version before each operation, accepts stable Borg 1.x from 1.2.8,
and streams operation diagnostics to a supplied writer. Callers prepare argument
arrays, environment overrides, and the working directory; the runner neither
loads configuration nor invokes a shell. Results distinguish success, warnings,
failures, and interruption while retaining the process exit code.

## Locking

Borg's repository and cache locks remain authoritative across Cairn invocations,
scheduled operations, and external Borg processes. `LockWaitArgs` prepares
an explicit native timeout: 60 seconds by default, with zero requesting no waiting.
Backup, prune, and compact expose `--lock-wait SECONDS`. This is not an
overall operation or SSH connection timeout. Cairn does not queue operations,
retry failures, bypass locks, or delete lock files. A lock timeout is an execution
failure; native Borg diagnostics are streamed to the caller.

Borg handles stale local locks on subsequent acquisition; zero wait may fail even
after Borg removes a stale lock. Borg also needs a stable host identity to recognize
stale local locks. In containers without a stable MAC, configure Borg's
`BORG_HOST_ID` consistently; Cairn preserves it rather than inventing a host
identity or removing foreign locks.

## Cancellation

Both the version probe and operation run in separate local Linux process groups.
Cancellation forwards the initiating SIGINT or SIGTERM to the group; ordinary
context cancellation and deadlines use SIGTERM. Remaining group members receive
SIGKILL after a five-second grace period. Cairn waits for execution to stop,
reaps its direct child, and closes owned descriptors before returning. Linux
`/proc` is used to distinguish running descendants from orphan zombies awaiting
system reaping. A functioning system reaper is needed for orphan zombies; Borg
can still consider a zombie lock owner alive until it is reaped. The leader
remains unreaped during cleanup to prevent reuse of its process-group ID.
No deferred signal timers remain after an invocation.

The CLI retains the initiating signal for exit 130 (SIGINT) or 143 (SIGTERM),
even when child cleanup requires SIGKILL. Runner results retain raw process status;
callers should use the cancellation context for the initiating signal rather than
mistaking a cleanup signal for the user's request.

This covers ordinary Borg, SSH, and passcommand descendants in the owned local
group, not programs that deliberately detach or remote server processes. Cairn
cannot guarantee graceful cleanup after its own SIGKILL or a host crash.
Interrupted backups may leave checkpoint archives. Interrupted pruning may have
already committed some deletions, and interrupted compaction may have reclaimed
some space; cancellation is not a rollback guarantee.

## Repository access

`Request.Access` supplies an optional passphrase file, optional SSH identity, and
whether the repository uses SSH. Resolve these inputs before running an operation;
file paths are relative to Cairn's original directory, not `Request.Dir`.

An explicit passphrase file overrides `BORG_PASSPHRASE`, `BORG_PASSCOMMAND`, and
`BORG_PASSPHRASE_FD`. Cairn opens the file and forwards a descriptor without copying
the secret into arguments or environment values. Otherwise Borg's precedence is
preserved, including empty values. Selected inherited descriptors are duplicated
and remapped into the child; the original descriptor is not closed, though reading
the duplicate advances their shared file offset. Recognized inherited credential
descriptors are marked close-on-exec to prevent accidental inheritance; only the
selected duplicate is forwarded to the operation, not the version probe.
Cairn closes owned descriptors after each run.
Borg reads descriptor contents and strips trailing newlines in its normal way.

Without credential inputs, Cairn supplies an empty passphrase instead of allowing
Borg to open a terminal prompt. Unencrypted repositories, empty-passphrase keys,
and operations such as compact remain usable without supplying a secret. Missing
or incorrect required credentials fail. `BORG_PASSCOMMAND` remains a trusted
user-provided program: Borg runs it when needed, and users must ensure it is
non-interactive and does not print secrets to stderr.

SSH uses OpenSSH with `BatchMode=yes` and `StrictHostKeyChecking=yes`, overriding
`BORG_RSH`. Standard SSH configuration, agents, known-host files, and URL options
remain available; an identity adds `-i` without restricting other configured keys.
Unknown or changed host keys require explicit setup outside Cairn. Encrypted SSH
keys must already be usable non-interactively, for example through an agent.
Local operations do not require OpenSSH. Cairn does not automatically set Borg
repository-trust approvals; explicit inherited Borg trust settings remain in effect.

## Integration tests

CI exercises standalone backups, pruning, and compaction, access, contention,
cancellation, and subsequent lock acquisition against Borg 1.2.8 and 1.4.5 using
disposable repositories and a fake SSH transport
(not a live SSH server). To run these checks locally, install
a supported Borg binary named `borg` and run:

```sh
make integration-test CAIRN_TEST_BORG="$(command -v borg)"
```

The integration target enables real-Borg tests and runs the entire Go test suite
without package or test-name filters. Plain `go test ./...` also opts into those
tests when `CAIRN_TEST_BORG` is set; without it, they skip.
