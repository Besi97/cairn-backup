# Configuration

Cairn configuration is YAML. The machine-readable contract is
[`cli/config.schema.yaml`](../cli/config.schema.yaml), written as YAML-formatted
JSON Schema Draft 2020-12. A complete generic example is in
[config.example.yaml](config.example.yaml). The example uses placeholder hosts,
paths, and job names; replace them with values appropriate to your installation.

The schema describes the configuration shape. Runtime validation must also check
cross-references and filesystem-dependent conditions, such as whether a named
repository exists and whether an SSH identity file was supplied for a local
repository. Those checks are performed by the configuration validator, not by
JSON Schema alone.

## Top-level structure

- `version` is currently `1`.
- `repositories` is a map of named, existing Borg repositories.
- `jobs` is a map of named backup jobs. Each job targets exactly one repository.
- Identifiers use lowercase letters, digits, and hyphens, begin with a letter,
  and are limited to 63 characters. Treat job identifiers as stable names for
  manual selection and execution records. Archive series are selected using the
  separate `archive_prefix` field.
- Unknown fields are errors. Cairn does not accept arbitrary Borg argument
  passthrough.

## Repositories

`location` is either an existing local repository path or a Borg SSH repository
location, such as `ssh://user@host.example/path/to/repository` or
`user@host.example:/path/to/repository`. SSH host aliases, ports, and jump hosts
can be supplied through ordinary OpenSSH configuration. `identity_file` is an
optional explicit private-key path for small or single-user installations; it
does not disable host-key verification. SSH repositories still require a trusted
host key.

A dedicated, least-privileged service account is recommended for unattended
agent operation, but is not required. A user can run Cairn under an existing
account and provide an SSH key readable by that account.

`passphrase_file` is optional and points to a file containing the repository
passphrase. Inline credentials are not supported. Protect the file so only the
manual or service account that runs Cairn can read it. Relative repository,
passphrase, identity, and `source_root` paths are resolved relative to the
configuration file; absolute paths are recommended for unattended service use.

`lock_wait` controls how long Borg waits for repository locks held outside
Cairn's local coordination. Use whole seconds or minutes, such as `30s` or `5m`.
The allowed range is `1s` through `10m`; omission defaults to `60s`. Cairn fails
after the bound rather than retrying indefinitely.

## Jobs and sources

Each job has one `repository`, a `source_root`, and one or more `sources`. Source
entries are relative paths beneath `source_root`; Borg runs with that root as its
working directory. This allows the archive to retain useful relative paths and
keeps the source root stable between runs. All listed sources are placed in the
same archive and share the job's schedule, repository, retention policy, and
outcome. Use separate jobs when sources need independent schedules or retention.

Each job has an `archive_prefix`. Prefixes must be unique among jobs using the
same repository; the same prefix may be used in different repositories. This
enables retention to select only the job's archive series. Changing a prefix
after creating archives starts a new series and leaves old archives outside
that job's retention selection.

## Schedules

`schedule` is optional. A job without one can be invoked manually and is not
scheduled by agent mode. When present, it requires `cron`. Expressions have six
fields in this order: second, minute, hour, day-of-month, month, and day-of-week.
`timezone` is optional. When supplied, it must be an IANA name such as `Etc/UTC`;
when omitted, Cairn uses the machine's local timezone. An explicit timezone is
recommended for predictable unattended scheduling.

For example, `0 0 4 * * *` means once daily at 04:00:00 in the selected
timezone. The scheduler skips occurrences missed while the agent is stopped; it
does not catch up or automatically retry failed work. Configuration changes take
effect after an explicit agent restart; 0.1.0 does not hot-reload configuration.

## Maintenance and Borg options

Maintenance is optional and runs only as part of a backup job; Cairn does not
provide independent prune or compact commands. `retention` supports `keep_within`
and positive hourly, daily, weekly, monthly, and yearly counts. At least one
retention rule is required when retention is configured. `compact: true` requests
compaction after that job's successful backup and, if configured, prune stage. It
is per-job, not a fleet-wide or end-of-batch operation. If omitted, compaction is
disabled.

If Borg archive creation reports a warning, maintenance is skipped by default.
Set `after_backup_warning: run` only when maintenance should proceed despite that
warning; the overall job result still preserves the warning. Prune failure must
prevent compaction.

Compression is not configurable in 0.1.0. Cairn uses Borg's default compression,
`lz4`; this matches Borg 1.4.5 and later supported 1.4.x clients. See the [Borg
1.4.5 `borg create` documentation](https://borgbackup.readthedocs.io/en/1.4.5/usage/create.html).

## Scope note

Cairn backs up the configured source paths. It does not stop applications,
create filesystem snapshots, run deploy hooks, or remove source snapshots. Ensure
the sources are ready and remain available for the duration of each job.
