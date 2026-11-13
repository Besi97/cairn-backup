# ADR 0002: Configuration-free manual CLI

- Status: Accepted
- Date: 2026-11-13
- Issue: [#44](https://github.com/Besi97/cairn-backup/issues/44)
- Follows: [ADR 0001](0001-supported-environment.md)

## Context

Users should be able to run a backup or maintain an existing Borg repository
without first defining repositories, jobs, or workflows in a configuration file.
Backup, prune, and compact are separate operations, not stages that can only be
reached through a backup job.

The CLI needs enough structure to make these operations usable from a terminal
or a script, without trying to replace Borg's naming, pattern, or path semantics.
Configured workflows and scheduling will be designed separately and will reuse
these operations.

## Decision

### Command shape

```text
cairn backup --repository LOCATION \
  (--archive-prefix PREFIX | --archive-name NAME) [OPTIONS] PATH...

cairn prune --repository LOCATION --archive-prefix PREFIX [RETENTION OPTIONS]

cairn compact --repository LOCATION
```

For example:

```sh
cairn backup --repository /backups/repo --archive-prefix documents \
  --source-root "$HOME" --exclude '*.tmp' Documents

cairn backup --repository /backups/repo \
  --archive-name documents-before-upgrade "$HOME/Documents"

cairn prune --repository /backups/repo --archive-prefix documents \
  --keep-daily 7 --keep-weekly 4

cairn compact --repository /backups/repo
```

These commands do not require a configuration file or a running scheduler.
They must not discover and load unrelated configuration when their inputs are
provided directly. Backup does not implicitly prune or compact, and prune does
not implicitly compact. None of these commands asks for confirmation or requires
a `--yes` flag.

All three require an explicit, nonempty `--repository LOCATION`. Support local
filesystem paths and Borg SSH URLs. Do not fall back to `BORG_REPO` when the flag
is missing. Repositories must already exist.

Use `--` to separate options from source paths when necessary, including paths
whose names start with a hyphen. It is not a mechanism for passing extra options
to Borg. Unknown options and options belonging to another command are errors.
Each command validates only its own inputs. Help and version output must work
without Borg, repository access, or credentials.

### Backup

Require at least one positional source path and exactly one of:

- `--archive-prefix PREFIX`: generate `PREFIX-YYYYMMDDTHHMMSS.NNNNNNNNNZ`, using
  UTC and nine fractional-second digits.
- `--archive-name NAME`: use the supplied name without adding a timestamp.

A name collision is an error; do not overwrite an archive or silently choose a
different name. Archive names and prefixes follow Borg's restrictions rather
than a Cairn-specific lowercase identifier format.

Pass user-supplied names and prefixes through without escaping Borg placeholders
such as `{hostname}`. Borg performs its normal expansion. Cairn's generated UTC
timestamp is the only naming addition it makes.

Support `--source-root DIR`. Without it, use the caller's current directory;
with it, run Borg from that directory and resolve relative source paths there.
Absolute source paths remain allowed. The source root is not a sandbox, and
Cairn does not impose a different archive path layout or normalize source paths
before handing them to Borg.

Resolve relative local repository, passphrase-file, and identity-file paths from
the caller's original directory, before changing Borg's working directory.

Support repeated `--exclude PATTERN` options with Borg's pattern semantics. Do
not add exclusion files, include/exclude rule files, compression selection, or
arbitrary Borg option passthrough in 0.1.0. Leave compression at Borg's default.

### Prune

Require `--archive-prefix PREFIX` and at least one effective retention rule.
There is no exact-name deletion command or implicit all-archives mode in 0.1.0.

Append `-*` to the supplied prefix and pass the result to Borg's
`--glob-archives` option. For an ordinary prefix such as `documents`, this selects
every archive starting with `documents-`, not just Cairn's timestamped names.
An exact-name archive such as `documents-before-upgrade` is also in that scope.

Do not escape placeholders or glob metacharacters in the prefix. Users may use
Borg's expansion and matching behavior, including deliberately broader selectors.
Cairn requires an explicit prefix but does not reinterpret it as a literal value.

Expose these retention options:

- `--keep-within INTERVAL`
- `--keep-secondly COUNT`
- `--keep-minutely COUNT`
- `--keep-hourly COUNT`
- `--keep-daily COUNT`
- `--keep-weekly COUNT`
- `--keep-monthly COUNT`
- `--keep-yearly COUNT`

Use Borg's interval syntax, retention ordering, and count semantics, including
negative counts meaning unlimited retention. Reject a missing policy or one
that consists only of zero counts. A keep-within interval must be positive.
Do not expose newer quarterly rules as part of this initial interface.

Borg applies retention to archive metadata timestamps, not timestamps parsed
from archive names. Calendar retention uses Borg's local timezone behavior;
UTC archive names do not change that. Preserve Borg's handling of checkpoint
archives within the selected scope.

### Compact

Run compaction independently of backup or prune. It reclaims eligible space
across the repository, not just within one archive prefix, and does not require
the repository encryption key or passphrase. Keep Borg's default compaction
threshold rather than exposing another option in 0.1.0.

### Previews

Support `--dry-run` for backup and prune, with useful lists of the files or
archives Borg would process. A preview does not create an archive or delete
archives. It retains Borg's limitations rather than promising a complete test
of a future execution.

In particular, backup dry-run does not establish repository access, working
credentials, or that all file contents can be read. Prune previews require
repository access and may use Borg locks and local state. Do not promise that
previews have no local filesystem effects.

Reject `--dry-run` for compact. Newer Borg releases accept it as a no-op, but
that does not provide a useful compaction preview.

### Credentials and SSH

Support `--passphrase-file PATH` and Borg's existing passphrase environment
inputs: `BORG_PASSPHRASE`, `BORG_PASSCOMMAND`, and `BORG_PASSPHRASE_FD`. The
explicit file takes precedence over inherited inputs; otherwise retain Borg's
precedence. Forward inherited file descriptors deliberately rather than assuming
they survive process creation.

Do not accept an inline passphrase flag or print secret contents in normal
output or errors. Require credentials only when Borg needs them. Missing or
incorrect credentials must fail rather than fall back to an interactive prompt.

Support `--identity-file PATH` for SSH repositories. Other SSH settings come
from standard SSH configuration or the repository URL. Keep known-host
verification enabled and SSH execution non-interactive. Do not silently approve
unknown hosts or Borg repository trust warnings; report when explicit setup is
needed. Cairn does not prompt for repository passphrases or SSH authentication.

### Locking and cancellation

Use Borg's repository and cache locking rather than adding a separate Cairn
cross-process queue. Expose `--lock-wait SECONDS` on all three commands as a
nonnegative whole number, defaulting to 60. Zero requests an immediate attempt
without waiting. This controls Borg lock acquisition, not an overall operation
or SSH connection timeout.

A lock timeout is an execution failure. Do not retry automatically or bypass
locks. Different invocations can contend through Borg without a scheduler
process being present.

On cancellation, stop Borg and its owned subprocesses, wait for them to exit,
and report interruption rather than success. Cancellation must work during lock
waiting as well as during execution. Do not signal unrelated operations or
delete another process's lock. Interruption may leave Borg checkpoint archives;
it is not a promise that no partial work was recorded.

### Output and exit codes

Provide human-readable output only in 0.1.0. Show useful Borg diagnostics and
operation status as the command runs, followed by a final outcome. Put progress,
warnings, and errors on stderr, and the final human-readable result on stdout.
Output wording is not a stable machine-readable API; defer JSON output.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Completed with warnings |
| 2 | Execution failure, including lock timeout |
| 3 | Invalid invocation, inputs, or configuration |
| 128 + signal | Interrupted by a signal, including 130 for SIGINT and 143 for SIGTERM |

Normalize Borg's outcomes to these codes rather than forwarding all exit codes
unchanged. In particular, an inherited `BORG_EXIT_CODES=modern` must not change
Cairn's public exit-code meanings. Missing or unsupported Borg is an execution
failure, not successful validation of an invocation.

### Borg compatibility baseline

The selected command interfaces are present from Borg 1.2.0, including separate
compaction, archive filtering, the listed retention rules, and lock waiting.
Borg 1.1 cannot provide the required separation of prune and compact.

Set the supported baseline to stable Borg 1.2.8 and compatible later stable
Borg 1.x releases, with 1.4.5 as the current primary target. The higher baseline
accounts for relevant fixes, not additional CLI options: 1.2.5 fixed archive
spoofing, 1.2.7 fixed compaction-related behavior, and 1.2.8 fixed Ctrl-C handling
in packaged binaries. Prefer current upstream patch releases; this baseline is
not a claim that every older patch release is free of security issues.

CI must exercise 1.2.8 and the latest stable Borg 1.x release. Runtime
verification remains implementation work; the baseline here comes from upstream
documentation and tagged source review. This resolves ADR 0001's open question
about the minimum version without promising identical native Borg behavior
across versions.

## Consequences

Users can perform each operation without first adopting Cairn's configuration
format or scheduler. Later configuration and scheduling work must resolve into
the same operations, not introduce separate command behavior.

Explicit repository, naming, and retention inputs make direct calls suitable
for scripts. Warning and failure outcomes remain distinguishable without parsing
human-readable output.

Cairn remains a thin wrapper around Borg. Users are responsible for the Borg
placeholders, patterns, and path semantics they choose. Broader archive selectors
can broaden prune scope; the wrapper does not escape them or ask for confirmation.

## References

- [Borg 1.2 prune](https://borgbackup.readthedocs.io/en/1.2.0/usage/prune.html)
- [Borg 1.2 compact](https://borgbackup.readthedocs.io/en/1.2.0/usage/compact.html)
- [Borg general usage, environment, and exit codes](https://borgbackup.readthedocs.io/en/stable/usage/general.html)
- [Borg release history and upgrade notes](https://borgbackup.readthedocs.io/en/stable/changes.html)
