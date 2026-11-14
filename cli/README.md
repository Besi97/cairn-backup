# Cairn CLI

The CLI provides standalone backups, pruning, and compaction, help, and version
output. Configuration and scheduling will be added separately. Install stable Borg 1.x
(at least 1.2.8) separately; SSH repositories also require OpenSSH.

## Configuration design

The [configuration contract](../docs/configuration.md),
[version 1 YAML validation schema](schema/config-v1.schema.json), and
[example configuration](examples/config.yaml) define the agreed repository and
workflow format. Loading, workflow execution, and scheduling are not implemented
yet; the example is for schema validation and design review, not CLI execution.

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
future scheduler operations, and external Borg processes. `LockWaitArgs` prepares
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
CAIRN_TEST_BORG="$(command -v borg)" go test -race -count=1 -v ./...
```
