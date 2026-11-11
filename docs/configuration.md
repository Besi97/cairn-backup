# Configuration

Cairn uses YAML configuration, format version `1`. See the
[example](config.example.yaml) and the [JSON Schema](../cli/config.schema.yaml).
Replace the example's paths, repository locations, and job names with your own.

## Finding the configuration

Cairn looks for a configuration file in this order:

1. A file passed with `--config PATH`.
2. `$XDG_CONFIG_HOME/cairn/config.yaml`, or `~/.config/cairn/config.yaml` when
   `XDG_CONFIG_HOME` is unset or not absolute.
3. `/etc/cairn/config.yaml`.

An explicit `--config` is authoritative: if that file is missing or invalid,
Cairn fails instead of trying another file. Otherwise the user file wins when it
exists, and the system file is used only when the user file is absent. Cairn does
not merge files.

## Checking the configuration

```sh
cairn config check --config ./config.yaml
```

Omit `--config` to use normal discovery. A valid configuration prints a short
confirmation and exits with code `0`. Loading or validation failures are written
to stderr and exit with code `3`; invalid command usage exits with code `2`.
Independent semantic errors are reported together in a stable order. YAML and
loading errors stop the check immediately.

The check validates required fields, references, supported values, schedules,
retention rules, and combinations such as SSH-only identity files and unique
archive prefixes per repository. Local path references are expanded and resolved.
The configuration file itself must be readable and regular.

This is static validation, not an operational test. Cairn does not read
passphrase or SSH-key contents, check access to configured sources or files,
run Borg or SSH, or contact repositories. Missing source directories,
repositories, keys, and secret files do not make this check fail. Successful
validation does not establish that a backup can run or credentials will work.

## Repositories

Define each Borg repository once under `repositories`. `location` is an existing
local path or an SSH location such as
`ssh://user@host.example/path/to/repository`. Cairn uses your OpenSSH
configuration for aliases, ports, and jump hosts, and verifies SSH host keys.

- `passphrase_file` is an optional path to a file containing the repository
  passphrase. Inline passphrases are not supported; allow only the Cairn user to
  read this file.
- `identity_file` is an optional path to an SSH private key, used with SSH
  repositories. Cairn runs SSH with `-i` pointing at this key; other SSH settings
  come from your normal OpenSSH configuration, and host key verification stays on.
  A dedicated, least-privileged account is recommended for unattended use, but
  not required; you can run Cairn as your existing user if the key is readable by
  that account.
- `lock_wait` sets how long Borg waits for locks held outside Cairn. Use whole
  seconds or minutes, from `1s` to `10m`; the default is `60s`. If the lock is
  still held when the wait ends, the backup fails. Cairn does not retry.

Relative repository, passphrase, and identity paths are resolved from the
configuration file's directory, not from the directory where you run Cairn.
Paths may also start with `~/` or use `$VAR` and `${VAR}`; an unset or empty
variable is an error, and command substitution is not supported. SSH locations
are used unchanged. Absolute paths are recommended for unattended use.

`sources` entries stay relative to `source_root` and are not expanded.

## Secrets

`passphrase_file` must contain only the passphrase. One final line break (LF or
CRLF) is optional and removed; spaces and tabs are kept as part of the
passphrase. Cairn rejects empty files, empty passphrases, extra lines, and NUL
bytes, and never prints the secret in errors or logs.

Cairn reads the secret when a backup runs, not at startup, so a passphrase file
can be replaced without restarting the agent. Other configuration changes still
require an agent restart.

Cairn passes the passphrase to Borg in the `BORG_PASSPHRASE` environment
variable. Other processes running as the same user can read a process's
environment, so run Cairn under an account that only trusted processes share.
Borg's own output does not include the passphrase.

## Running a backup

```sh
cairn backup --job service-a-local --config ./config.yaml
```

Cairn runs the Borg executable found on `PATH`. Borg 1.4.5 and later below 1.5.0
is verified. Other Borg 1 versions, including prereleases, print a warning and
still run. Borg 2 or later, a missing `borg`, or an unrecognizable version stops
the backup before any repository access. Install Borg yourself; Cairn does not
bundle it.

A backup creates one archive named `ARCHIVE_PREFIX-` followed by the local time
of the run. Borg output is shown as it happens, and the final line reports the
outcome. Press Ctrl-C (or send `SIGTERM`) to stop a running backup: Cairn stops
Borg, waits for it to exit, and exits with code `130`.

Only the named job runs. Repositories must already exist; Cairn does not create
them. Local repositories and SSH repositories both use the existing Borg
repository format.

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

Cron fields support lists, ranges, steps, and month/day names. Five-field cron,
shorthand such as `@daily`, and embedded `TZ=` or `CRON_TZ=` directives are not
supported; use `timezone` instead. An explicit timezone must be a recognized IANA
name, not an empty string or `Local`. Timezone data is bundled with Cairn for
systems without an installed timezone database.

## Maintenance

`maintenance` is optional. If present, include `retention` or set
`compact: true` (or both). Retention accepts `keep_within` intervals (for
example, `7d` or `4w`) and positive `keep_hourly`, `keep_daily`, `keep_weekly`,
`keep_monthly`, or `keep_yearly` counts. At least one retention rule is required
when using `retention`. Compaction is disabled unless `compact` is `true`.

Prune runs only when `retention` is configured, and it considers only this job's
archives. Compaction runs after a clean backup and, when `retention` is
configured, after a clean prune. A prune warning or failure skips compaction.
Compaction works on the whole repository, so it also reclaims space from other
jobs' deleted archives.

`after_backup_warning` controls maintenance when archive creation reports a
warning. It accepts `skip` (the default) or `run`. A backup failure never runs
maintenance.

Compression is not configurable; Cairn uses Borg's default, `lz4`. Arbitrary
Borg arguments are not supported.

## Agent configuration changes

Configuration changes require an explicit agent restart. Cairn 0.1.0 does not
hot-reload configuration. Replacing the contents of a configured passphrase file
is the exception: the next backup reads the replacement on demand.
