# Configuration contract

This is the version 1 configuration contract agreed in
[#52](https://github.com/Besi97/cairn-backup/issues/52). Configuration loading and
checking and manual workflow execution are implemented; scheduling follows in
#55. Standalone `backup`, `prune`, and `compact` remain
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
The flag belongs to `config check`, not standalone operations.

No Borg installation or network access is needed. Checking does not open
passphrase/identity files or require repository/source paths to exist. It cannot
verify credentials or repository health. Inline passphrases necessarily enter
memory when the configuration itself is read, but never appear in diagnostics.
Errors identify field paths (JSON Pointer notation) and source positions where
available, without quoting rejected values or YAML snippets.

Cron is currently validated only as a nonempty string; syntax and scheduling
semantics remain deferred to #55.

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

- Optional nonempty `cron`: omit for manual-only execution.
- Required `stages`: a nonempty ordered list of objects.

Each stage has only `tasks`, a nonempty list of task objects. Stages and tasks
have no names or IDs. Each task has a required `type` and `repository`; the
repository value references a key in `repositories`, not a repository location.

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

Cross-process workflow overlap protection is **not implemented yet**; it follows
in #55. For now, simultaneous manual invocations can run the same workflow.
Borg's repository/cache locks remain authoritative.

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
and collision handling. Cron expressions will be checked once defined in #55.
The published schema is embedded in the binary, so structural validation is
offline and uses the same contract as editors. Standard JSON Schema cannot derive
valid repository names from this document's dynamic map or check filesystem access.
It cannot provide dynamic repository-name completion either.

Configuration checking must not execute Borg or read secret files unnecessarily.
Execution-time repository, credential, and filesystem access checks remain
separate. Schema tests do not substitute for runtime validation.

## Workflow overlap (implementation in #55)

Prevent concurrent runs of the same workflow across scheduler and manual CLI
processes for the same OS user on the same host. Identity is the canonical
configuration-file path (resolving symlinks) plus workflow name. Separate copies
of a configuration are independent. Different workflows may run concurrently,
including those sharing a repository; Borg locking remains authoritative.

Use a nonblocking advisory workflow lock. A manual duplicate reports an
already-running error; a scheduled duplicate is skipped and reported. Do not
queue or retry automatically because of overlap. Hold ownership through execution
and cancellation cleanup, accounting for Borg children surviving a Cairn crash.
This is not a distributed or cross-user lock and is not a repository lock.

Cron dialect, timezone, missed occurrences, and general retry policies are
deliberately deferred to #55. The schema accepts nonempty cron strings without
claiming to validate those semantics.
