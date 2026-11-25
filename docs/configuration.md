# Configuration contract

This is the version 1 configuration contract agreed in
[#52](https://github.com/Besi97/cairn-backup/issues/52). Configuration loading and
checking, manual and scheduled workflow execution, and overlap protection are
implemented. Standalone `backup`, `prune`, `compact`, and `check` remain
usable without configuration or a scheduler and do not discover config files.

- [Example YAML](../cli/examples/config.yaml)
- [Version 1 JSON Schema for YAML](../cli/schema/config-v1.schema.json)
- [Decision and rationale](decisions/0003-configuration-and-workflows.md)

## Check a configuration

```sh
cairn config check
cairn config check --config ./config.yaml
```

Checking validates the entire selected file, not just a selected workflow. Success
prints the selected absolute path and exits with `0`; missing, unreadable, or
invalid configuration exits with `3`. An explicitly empty `--config` is an error.
The terminal interface uses the same selection and validation rules. Standalone
operations do not discover configuration.

### Browse configuration in the terminal

Run `cairn` or `cairn tui` in a terminal, optionally with `--config PATH`.
Tab switches between Home, Workflows, Configuration, Archives, History, and Help. In Workflows,
use the arrows or `j`/`k` to select a workflow, then Enter to browse its ordered
stages and tasks. Select a task and press Enter to follow its repository reference.
`y` opens the workflow or repository YAML; Esc returns. Page Up/Down or `b`/`f`
scroll long details.

Press `s` on a workflow to review its prepared stages, then `y` to start or
Esc to cancel. Prune and compact are included when configured. Only one
TUI-started workflow runs at a time; an already-running workflow is not queued.
The interface shows a running state and a basic outcome, but does not retain Borg
output; use the CLI when diagnostics are needed. Quitting with `q` or Ctrl+C cancels a TUI-started
run and waits for it to stop. Other runs and the agent are unaffected.

The YAML is a sanitized, normalized snapshot of the validated input, not a
verbatim file or a dump of resolved runtime defaults. Relative paths remain as
configured. Comments and anchors are omitted, inline passphrases and embedded
location passwords are redacted, and terminal controls are escaped. Ordinary
repository locations and credential-file paths remain visible; credential files
are never opened for display.

Press `r` to reread and validate only the selected path. A successful reload
replaces the TUI snapshot; failure retains the previous one and shows a safe
error without rejected values. There are no automatic rereads, configuration edits,
or individual operation controls. Reloading the TUI does **not** reload or restart an agent: its startup
snapshot is separate, and the TUI does not observe its status.

No Borg installation or network access is needed. Checking does not open
passphrase/identity files or require repository/source paths to exist. It cannot
verify credentials or repository health. Inline passphrases necessarily enter
memory when the configuration itself is read, but never appear in diagnostics.
Errors identify field paths (JSON Pointer notation) and source positions where
available, without quoting rejected values or YAML snippets.

Checking also validates cron syntax/ranges and timezone names offline.
Schedules run only while the foreground agent is active.

## Editor validation

The example associates its schema using a relative path:

```yaml
# yaml-language-server: $schema=../schema/config-v1.schema.json
```

When copying the example elsewhere, adjust the path or use the published URL:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Besi97/cairn-backup/main/cli/schema/config-v1.schema.json
```

Editors with YAML language-server support can validate the document and suggest
properties. Set a task's `type` first to narrow completion to its operation;
exact completion behavior depends on the editor. The comment is editor metadata,
not a Cairn setting, and does not replace `version`.

The schema uses JSON Schema draft 2020-12. It describes YAML's JSON-compatible
data model; it is deliberately JSON-encoded for editor compatibility. CI compiles
the schema and tests both the example and valid/invalid configuration cases.

## Top-level fields

All three fields are required. Unknown fields are errors at every object level,
except user-defined repository and workflow names.

| Field | Meaning |
| --- | --- |
| `version` | Integer `1`. Missing, string, or unsupported versions are errors; never infer the latest version. |
| `repositories` | Map of nonempty names to repository objects. `{}` is allowed. |
| `workflows` | Map of nonempty names to workflow objects. `{}` is allowed. |

Names are case-sensitive strings used as exact references, not filesystem paths.
Quote numeric-looking names so YAML treats them as strings. No lowercase-only
identifier restriction is imposed.

## Repositories

Repositories must already exist; Cairn does not initialize them.

| Field | Required | Meaning |
| --- | --- | --- |
| `location` | Yes | Nonempty local repository path or Borg SSH location, including native scp-style syntax. Must identify a repository, not an archive. |
| `passphrase` | No | Literal inline passphrase. Empty string is an explicit empty passphrase, not absence. |
| `passphrase_file` | No | Nonempty path to a passphrase file. Mutually exclusive with `passphrase`. |
| `identity_file` | No | Nonempty SSH identity-file path, valid only for SSH repositories. |

Credential selection depends on field presence. Either configured passphrase
form overrides inherited `BORG_PASSPHRASE`, `BORG_PASSCOMMAND`, and
`BORG_PASSPHRASE_FD`. With neither configured form, retain standalone Borg
credential precedence and non-interactive behavior. Unencrypted repositories do
not require a credential field. Compaction needs no repository encryption key or
passphrase; SSH authentication still applies.

Passphrases are secrets, not templates. Never expand them or include their
contents in logs, validation errors, diagnostic config dumps, or process
arguments. A loader must not echo rejected values or raw YAML snippets that
could contain secrets. Inline passphrases are stored in plaintext: restrict
configuration permissions (for example, `chmod 600`) and do not commit real
secrets. Schema validation does not inspect file permissions or secret contents.

Repository fields are not overridable on tasks. There are no inheritance,
defaults-map, or environment-interpolation mechanisms in version 1.

## Workflows, stages, and tasks

A workflow has:

- Optional `cron`: a five-field expression or supported alias; omit for manual-only execution.
- Optional `timezone`: an IANA timezone name or `UTC`; omit for the agent process local timezone.
- Required `stages`: a nonempty ordered list of objects.

Each stage has only `tasks`, a nonempty list of task objects. Stages and tasks
have no names or IDs. Each task has a required `type`; Borg tasks also require
`repository`, which references a key in `repositories`, not a repository location.

Tasks run concurrently within a stage. Wait for all tasks before evaluating the
stage result. Any warning or failure prevents subsequent stages from starting,
but does not cancel already-running sibling tasks. Cancellation is separate
from warning/failure handling and must stop and reap owned subprocesses.

Operations never imply another operation. To require backup before prune and
prune before compact, place them in separate stages. Tasks in the same stage
have no ordering guarantee, even when they reference the same repository.
Borg remains responsible for repository/cache locking: parallel tasks may contend
or time out; Cairn does not serialize them implicitly.

The manual command contract is:

```sh
cairn workflow list [--config PATH]
cairn workflow run NAME [--config PATH]
```

`workflow list` validates the entire selected configuration and prints workflow
names in lexicographical order, one per line. It does not execute Borg, access
repositories, open credential files, or check source paths. An empty workflows
map produces no output and succeeds. It exits `3` for invalid arguments or
configuration and `2` if output cannot be written.

`workflow run` runs the workflow without starting a scheduler and does not
require `cron`. Standalone command
flags do not override workflow task fields. Persisted `dry_run` settings,
confirmation flags, arbitrary Borg options, and task-level credential/location
overrides are not supported.

The entire selected configuration is validated before any operation starts.
Workflow names are case-sensitive. All task requests are prepared before
execution; generated archive-prefix timestamps use one UTC time sampled for the
run. Credential files are opened only when their operations execute, not during
preparation. Compact ignores configured encryption credentials, including
passphrase files, while retaining SSH authentication.

Task status and native Borg diagnostic chunks are labelled with the workflow,
one-based stage/task positions, operation, and repository name. Task completion
and the workflow summary go to stdout; scopes and Borg diagnostics go to stderr.
Concurrent output order is not guaranteed. A warning or failure summary reports
how many later stages were not started. Completed operations are not rolled back.

Exit codes are `0` for success, `1` for warnings without failures, `2` for any
execution failure, and `3` for invalid arguments/configuration or an unknown
workflow. A Borg child interrupted independently of Cairn is a task failure, with
its signal retained in diagnostics. SIGINT/SIGTERM cancellation of Cairn stops
active tasks, waits for owned subprocess cleanup, and exits `130`/`143`.
Warnings/failures do not cancel siblings or trigger retries.

### Executable commands

```yaml
type: command
program: /usr/local/bin/prepare-backup
args: ["--target", "data"]
timeout_seconds: 300
```

`program` is an executable name (looked up in Cairn's PATH) or path; `args` is
a required list of literal strings (which may be empty). No shell, expansion, or interpolation is
performed. The working directory is the configuration directory, stdin is closed,
and the process inherits Cairn's environment and permissions. The required
timeout is 1–9223372036 seconds (the maximum representable duration). Exit 0
succeeds; every nonzero exit, signal, startup error, or timeout fails the task
and prevents later stages. Workflow cancellation
interrupts the task. Cairn stops the owned process group on exit or cancellation,
escalating to SIGKILL after its grace period; detached descendants are not owned.

Standard output and error are streamed in labelled chunks to their respective
streams. Partial chunks are displayed promptly as separate labelled lines, so
a single program line can span multiple display lines. Incomplete UTF-8 bytes
wait for the next chunk; an incomplete sequence at EOF is replaced. Output is
not stored in history and may contain sensitive data. Command arguments are visible in
configuration browsing and may be visible to other local
processes. Do not put secrets in arguments or print them. Configuration is trusted
code: anyone able to change it can run arbitrary programs as Cairn, including
through scheduled workflows. Restrict write access and review configuration before
running it. Containers may not include the executable.

Cross-process workflow overlap protection rejects a duplicate manual invocation
with exit code `2`, without waiting or starting Borg. Manual runs start immediately
regardless of cron or timezone. Borg's repository/cache locks remain authoritative.

### Schedules

Start the foreground scheduler with `cairn agent run [--config PATH]`. It uses
the same configuration selection rules as manual workflows and loads one
validated snapshot at startup. Restart to apply changes; no reload/watch or
background daemon mode is provided. At least one workflow must have `cron`;
otherwise startup fails with exit `3`. A syntactically valid but impossible
calendar schedule still counts as scheduled and remains idle.

One agent lock covers the canonical config path. A second agent fails immediately
with exit `2`; manual runs do not acquire that lock and may run while the agent
is active. Different workflows can execute concurrently. Each occurrence uses
the shared staged executor and workflow locks, with no repository queue.

Agent lifecycle and workflow summaries are timestamped in UTC on stderr,
alongside labelled Borg diagnostics; task outcomes remain on stdout. Warnings
and failures stop later stages of that run but do not terminate the agent.
Output failures stop dispatch, cancel active work, and exit `2`. SIGINT/SIGTERM
stop dispatching and wait for active-work cancellation cleanup before returning
`130`/`143` and releasing the agent lock. See [local run history](run-history.md)
for retained outcomes and recovery evidence.

An optional top-level `metrics: {listen: "127.0.0.1:9090"}` enables the agent's
Prometheus endpoint. It is disabled by default and has no authentication or
TLS. See [workflow metrics](workflow-metrics.md) for address requirements,
security, metric semantics, and limitations.

`cron` has five whitespace-separated fields:

```text
minute  hour  day-of-month  month  day-of-week
0–59    0–23  1–31          1–12   0–6 (Sunday = 0)
```

Fields support `*`, comma-separated lists, inclusive ascending ranges, and
positive steps, for example `*/15`, `8-18/2`, or `5/20` (from 5 through the field
maximum, stepping by 20). Month and weekday names use case-insensitive
three-letter English abbreviations (`JAN`–`DEC`, `SUN`–`SAT`). Sunday is not `7`.
When both day-of-month and day-of-week are restricted, either can match;
a wildcard day field leaves the other field to determine matching days.

Supported aliases are:

| Alias | Equivalent |
| --- | --- |
| `@hourly` | `0 * * * *` |
| `@daily`, `@midnight` | `0 0 * * *` |
| `@weekly` | `0 0 * * 0` |
| `@monthly` | `0 0 1 * *` |
| `@yearly`, `@annually` | `0 0 1 1 *` |

Aliases are lowercase. Seconds, `@every`, `@reboot`, `?`, Quartz extensions,
and inline `TZ=`/`CRON_TZ=` directives are not supported. Leading/trailing
whitespace is ignored. Syntax and field ranges are validated, not calendar
feasibility: `0 0 30 2 *` is valid syntax but never runs. The parser's next-time
search returns no occurrence if none is found within five years.

Set `timezone: Europe/Berlin`, for example, to make scheduling independent of the
agent's local timezone. Named zone data is embedded for minimal installations.
Omitting the field uses the process local timezone (including its `TZ`
environment); an explicit empty value or `Local` is invalid. A timezone may be
set on a manual-only workflow and is still validated, but has no scheduling
effect until `cron` is added. It never changes Borg retention's timezone or
Cairn's UTC archive-prefix timestamps.

The agent applies these dispatch rules:

- A nonexistent local time during a DST jump is skipped. A repeated local time
  may run twice, once at each real instant, subject to overlap protection.
- Start with the next future occurrence: no startup run or catch-up backlog.
  If the agent wakes after an occurrence's minute has expired, skip that
  occurrence. Do not replay an already-dispatched real occurrence after a
  backward clock adjustment.
- Use one-second waits to recheck wall time rather than relying on a long
  monotonic timer. Missed occurrences are skipped in bulk, not queued.
  A zero next-time search result is revisited daily so a future occurrence can
  enter the parser's bounded search horizon.
- Do not retry warnings, failures, or overlaps automatically; later cron
  occurrences remain independent attempts.

Backup archive-prefix timestamps use actual dispatch time in UTC, not the
scheduled minute or agent startup time.

### Backup task

| Field | Meaning |
| --- | --- |
| `type` | Required literal `backup`. |
| `repository` | Required nonempty repository name. |
| `archive_prefix` | Nonempty prefix; append `-YYYYMMDDTHHMMSS.NNNNNNNNNZ` in UTC. |
| `archive_name` | Nonempty exact archive name; no timestamp added. |
| `source_root` | Optional nonempty Borg working directory. Defaults to the configuration directory. |
| `sources` | Required nonempty list of nonempty native Borg source arguments. |
| `exclude` | Optional list of native Borg exclusion patterns; defaults to none. |
| `lock_wait` | Optional nonnegative integer seconds; defaults to `60`. Explicit `0` does not wait. |

Require exactly one of `archive_prefix` and `archive_name`. Preserve Borg
placeholders such as `{hostname}`, archive naming restrictions, collision
behavior, source path layout, and exclusion semantics. Do not expand or clean
source arguments or exclusion patterns. The source root is not a sandbox.

### Prune task

| Field | Meaning |
| --- | --- |
| `type` | Required literal `prune`. |
| `repository` | Required nonempty repository name. |
| `archive_prefix` | Required nonempty prefix; select `PREFIX-*` with native Borg placeholder/glob semantics, including matching exact-name archives. |
| `keep_within` | Optional positive integer plus `H`, `d`, `w`, `m`, or `y`, using Borg's interval semantics. |
| `keep_secondly`, `keep_minutely`, `keep_hourly`, `keep_daily`, `keep_weekly`, `keep_monthly`, `keep_yearly` | Optional integer counts; zero disables, negative means unlimited periods. Omitted counts behave as zero. |
| `lock_wait` | Optional nonnegative integer seconds; defaults to `60`. Explicit `0` does not wait. |

Require at least one effective retention rule: a positive `keep_within` interval
or a nonzero count. The schema rejects absent policies and all-zero policies.
Retain the standalone interval syntax, including optional leading `+` and leading
zeros. Borg owns retention ordering, interactions, archive metadata timestamps,
local calendar timezone, and checkpoint handling. Do not interpret timestamps
from archive names or escape selectors to literals. Broader patterns can broaden
deletion scope.

### Compact task

Only `type: compact`, the required `repository` reference, and optional
`lock_wait` are allowed. `lock_wait` defaults to `60` seconds; zero is valid.
Compaction is repository-wide, never prefix-scoped, and keeps Borg's default
threshold. It does not delete archives.

### Check task

| Field | Meaning |
| --- | --- |
| `type` | Required literal `check`. |
| `repository` | Required nonempty repository name. |
| `verify_data` | Optional boolean, defaults to `false`. Also cryptographically verify archived file contents. |
| `lock_wait` | Optional nonnegative integer seconds; defaults to `60`. Explicit `0` does not wait. |

Uses the same preparation, Borg execution, and normalized outcomes as standalone
`cairn check`. By default, Borg checks repository consistency (including segment
checksums) and all archive metadata, not cryptographic file-content integrity.
With `verify_data: true`, Borg also reads, decrypts, and decompresses all archived
data; this can take a long time and transfer substantial data over SSH. Encrypted
archives need their key and passphrase, using the repository's usual credentials.

Checks never request repair, but may acquire locks and update Borg state.
Repair, partial checks, archive selectors, repository-only, and archives-only
switches are not supported. Borg warnings can indicate corruption or skipped
checks: inspect diagnostics. As with other tasks, warnings and failures stop later
stages without cancelling siblings or triggering retries. Add `cron` to the
workflow to schedule checks, or use `cairn workflow run` for an immediate run.

## Configuration selection

Configured commands select exactly one file, in this order:

1. Explicit `--config PATH`, if supplied. A relative path uses Cairn's invocation
   directory. An explicit path disables discovery, including when it is missing.
2. `$XDG_CONFIG_HOME/cairn/config.yaml`. If `XDG_CONFIG_HOME` is unset or empty,
   use `~/.config/cairn/config.yaml`. Per XDG conventions, ignore a relative
   `XDG_CONFIG_HOME` and use that default.
3. `/etc/cairn/config.yaml`.

Do not merge configurations. During default discovery only an absent candidate
permits trying the next location. An existing but unreadable, malformed, or
invalid selected file is an error, not permission to fall back. If none is found,
report the searched locations. Do not read other candidates after selection.

Resolve home and environment settings for the executing OS user. A service may
have a different home/config location than an interactive shell; explicit
`--config` is recommended for service setup.

## Configured path handling

Use the selected configuration's containing directory as the base for relative
local repository locations, passphrase files, SSH identities, and source roots.
If the selected file is a symlink, use the selected path's directory rather than
the target's directory. This path base is separate from canonical overlap-lock
identity.
For these local path fields, expand a value equal to `~` or starting with `~/`
using the executing user's home directory before resolving relative paths.
Apply the same local-path handling to the path portion of Borg `file://`
locations. Fail if required home expansion cannot be resolved.

Do not support `~otheruser` expansion or `${VARIABLE}` / `$VARIABLE`
interpolation. Other string contents remain literal. Do not apply local home
expansion to SSH repository locations: their remote paths retain Borg's
semantics. Source arguments stay unchanged; relative sources are interpreted
from the resolved source root, while absolute sources are allowed. To back up a
home directory, use `source_root: "~"` (bare `~` is YAML null) and
`sources: ["."]`.

Local relative paths do not depend on the caller's working directory, and a
source root never changes the base for repository or credential paths.

## Validation boundary

The published schema enforces structure, versions, allowed fields and types,
credential exclusivity, required operation inputs, effective retention, and
nonempty lists/strings where specified. `default` entries describe runtime
defaults; schema validators do not insert values.

The loader also rejects unknown repository references,
duplicate YAML mapping keys, non-string mapping keys, multiple YAML documents,
and non-JSON YAML values (including timestamps, custom tags, and nonfinite
numbers). YAML merge keys are not supported. Ordinary aliases are supported with
bounded nesting/expansion (100 levels and 100,000 visited nodes).

Validation checks repository-only locations, statically invalid archive names,
SSH-only identity settings, and integer ranges supported on both Linux targets
(signed 64-bit). Borg retains expanded-name validation, placeholder expansion,
and collision handling. The loader also checks cron syntax/ranges and timezone names.
The published schema is embedded in the binary, so structural validation is
offline and uses the same contract as editors. Standard JSON Schema cannot derive
valid repository names from this document's dynamic map or check filesystem access.
It cannot provide dynamic repository-name completion either.

Configuration checking must not execute Borg or read secret files unnecessarily.
Execution-time repository, credential, and filesystem access checks remain
separate. Schema tests do not substitute for runtime validation.

## Workflow overlap

Prevent concurrent runs of the same workflow across scheduler and manual CLI
processes for the same OS user on the same host. Identity is the canonical
configuration-file path (resolving symlinks) plus workflow name. Separate copies
of a configuration are independent. Different workflows may run concurrently,
including those sharing a repository; Borg locking remains authoritative.

Use a nonblocking advisory workflow lock. A manual duplicate reports an
already-running error; a scheduled duplicate is skipped and reported by
the agent. Do not queue or retry automatically because of overlap. Hold
ownership through execution and cancellation cleanup, accounting for Borg
children surviving a Cairn crash.
This is not a distributed or cross-user lock and is not a repository lock.

Lock files live under `$XDG_RUNTIME_DIR/cairn/locks` when the runtime directory
is absolute, owned by the executing user, and mode `0700`. Otherwise Cairn uses
`$XDG_STATE_HOME/cairn/locks`, or `~/.local/state/cairn/locks` if the state setting
is absent or relative. Cairn's directories must be private (`0700`) and its lock
files must be user-owned regular files (`0600`), not symlinks or hardlinks.
The state parent must be user-owned and not group/world writable.
An unsafe existing Cairn directory or lock file is an execution error, not a
reason to run without coordination.

Use the same runtime/state environment for all invocations that should
coordinate, including systemd services. Do not move, delete, or change permissions
on lock directories/files while Cairn or its Borg children are active.
Lock files have hashed identities and remain after release; their existence
does not mean a run is active. There are no PID files to clear or stale locks to
break: the kernel releases the advisory lock when its final descriptor closes.

Workflow locks are inherited by Borg, including compatibility probes, so killing
only Cairn does not permit a duplicate while Borg survives. The agent
holds an additional config-level lock, inherited by its Borg children as well,
preventing a replacement agent while that work is still active. Manual runs do
not acquire the agent lock. These locks do not track remote SSH server lifetime
after the local Borg process exits.

The schema checks schedule field types and nonempty strings. Full cron and
timezone validation belongs to the loader; editor/schema success alone is not
enough.
# Inline recovery checks

For a complete example and results walkthrough, see
[recovery check walkthrough](recovery-check.md).

`recovery_check` tasks select exactly one literal `archive_name` or
`archive_prefix` (the latter selects the latest matching archive, excluding
generated checkpoints). Specify either nonempty `paths` or `whole_archive: true`,
and at least one assertion in `checks` or a `sample`. The task uses temporary
extraction, not a Borg repository consistency check. The system temporary
directory must be trusted and have sufficient free space.

```yaml
- type: recovery_check
  repository: local
  archive_name: snapshot-2026
  paths: [documents]
  checks:
    - {kind: path_type, path: documents, type: directory}
    - {kind: file_size, path: documents/a, size: 12}
    - {kind: file_bytes, path: documents/marker, bytes: "ready\n"}
    - {kind: file_count, path: documents, count: 2}
    - {kind: file_sha256, path: documents/a, sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
  sample: {live_root: ./live, limit: 100}
```

Path types are `file`, `directory`, `symlink`, `fifo`, `socket`,
`block_device`, and `char_device`. Assertion paths are literal relative
slash-separated paths; `.` means the extracted root. Extraction paths follow
the same rules except `.` is not accepted. `bytes` is the literal UTF-8 bytes
of the YAML string, including any newlines; it is redacted in the TUI.
Sample limits range from 1 to 10000 and bound comparisons, not extraction or
traversal. `live_root` resolves relative to the configuration directory (or
uses local `~` expansion); sampling compares against current live files, not a
snapshot. Differences or incomplete sampling stop later stages as warnings.
Optional `lock_wait` has the same meaning as for other tasks.
