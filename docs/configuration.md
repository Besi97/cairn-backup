# Configuration

Cairn uses YAML configuration, format version `1`. See the
[example](config.example.yaml) and the [JSON Schema](../cli/config.schema.yaml).
Replace the example's paths, repository locations, and job names with your own.

## Repositories

Define each Borg repository once under `repositories`. `location` is an existing
local path or an SSH location such as
`ssh://user@host.example/path/to/repository`. Cairn uses your OpenSSH
configuration for aliases, ports, and jump hosts, and verifies SSH host keys.

- `passphrase_file` is an optional path to a file containing the repository
  passphrase. Inline passphrases are not supported; allow only the Cairn user to
  read this file.
- `identity_file` is an optional path to an SSH private key, used with SSH
  repositories. A dedicated, least-privileged account is recommended for
  unattended use, but not required; you can run Cairn as your existing user if
  the key is readable by that account.
- `lock_wait` sets how long Borg waits for locks held outside Cairn. Use whole
  seconds or minutes, from `1s` to `10m`; the default is `60s`.

Relative repository, passphrase, and identity paths are resolved from the
configuration file. Absolute paths are recommended for unattended use.

## Jobs and sources

Add each backup job under `jobs`. Job and repository names use lowercase
letters, digits, and hyphens, start with a letter, and are at most 63 characters.
Each job requires:

- `repository`: a name from `repositories`.
- `source_root`: a filesystem path. Relative paths are resolved from the
  configuration file.
- `sources`: one or more relative paths beneath `source_root`. All sources in a
  job go into the same archive.
- `archive_prefix`: a lowercase prefix. Prefixes must be unique among jobs that
  use the same repository; they may be reused for a different repository.

## Schedules

`schedule` is optional; without it, the job is not scheduled and can be run
manually. When setting it, provide `cron` as a six-field expression in this order:
second, minute, hour, day of month, month, day of week. `timezone` is an
optional IANA timezone such as `Etc/UTC`. If omitted, Cairn uses the machine's
local timezone. For example, `0 0 4 * * *` runs daily at 04:00.

## Maintenance

`maintenance` is optional. If present, include `retention` or set
`compact: true` (or both). Retention accepts `keep_within` intervals (for
example, `7d` or `4w`) and positive `keep_hourly`, `keep_daily`, `keep_weekly`,
`keep_monthly`, or `keep_yearly` counts. At least one retention rule is required
when using `retention`. Compaction is disabled unless `compact` is `true`.

`after_backup_warning` controls maintenance when archive creation reports a
warning. It accepts `skip` (the default) or `run`.

Compression is not configurable; Cairn uses Borg's default, `lz4`. Arbitrary
Borg arguments are not supported.
