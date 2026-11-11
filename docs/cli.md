# Command-line reference

`cairn backup` (including `--dry-run`), `cairn config check`, help, and version
output are available. The scheduler agent is not available yet.

## Commands

```text
cairn backup --job NAME [--config PATH] [--dry-run]
cairn config check [--config PATH]
cairn agent [--config PATH]
```

- `backup` requires `--job NAME` to choose which job to run. It creates one
  archive for that job using the local Borg executable. Cairn checks the Borg
  version first; see [Running a backup](configuration.md#running-a-backup).
  With `--dry-run`, it previews the run instead; see [Dry run](#dry-run).
- `--config PATH` selects a configuration file. Without it, Cairn discovers the
  configuration using the documented system or user default.
- `config check` checks the configuration and reports problems. It does not run
  backups or contact repositories. See [validation limits](configuration.md#checking-the-configuration).
- `agent` stays running and starts jobs with schedules in the configuration.
- `--help` shows help for Cairn or a command. `--version` prints the version.
  Both work without Borg installed.

## Dry run

`cairn backup --job NAME --dry-run` previews a backup without changing the
repository. The preview:

- Lists the source paths that Borg would archive, using `borg create --dry-run`.
  Archive creation opens no repository and takes no lock, so it ignores a lock
  held by another process.
- When `retention` is configured, lists the archives that `borg prune --dry-run`
  would remove. This step opens the repository, checks the passphrase, and waits
  for Borg's repository lock up to `lock_wait`, like a real prune. If the lock is
  not acquired in time, the preview fails with exit code `4`.
- Never runs or previews compaction. The output says so.
- Applies the same maintenance rules as a real run. A failed backup previews no
  maintenance. After a backup warning, maintenance is skipped unless
  `after_backup_warning` is `run`.

The preview is an estimate, not a promise of what a later backup or prune will
do. Every completed dry run prints these limits:

```text
Preview limits:
- Backup: Borg lists every source path it would consider. It does not compare
  them with the repository, so it cannot tell new, changed, or unchanged files
  apart, and it cannot show archive size, compression, or deduplication.
- Backup: the preview does not read file contents, so it cannot report
  unreadable files that a real backup would warn about. It does not contact the
  repository, check the passphrase, or check SSH access.
- Prune: the result reflects only archives that exist now. The archive a real
  backup would create is not included, so the real prune may keep or remove
  different archives.
- Prune (when retention is configured): the preview opens the repository, checks
  the passphrase, and waits for the repository lock, like a real prune.
- Compaction is not previewed.
- Later backups and prunes can differ from this preview.
```

Prune and compact are maintenance steps, not standalone commands.

## Output and exit codes

Cairn prints readable progress and results. Warnings and errors include enough
context to help diagnose the problem, but never print secret values.

| Code | Meaning |
| --- | --- |
| `0` | Completed successfully. |
| `1` | Completed with a warning. |
| `2` | Invalid command or usage. |
| `3` | Configuration is invalid. |
| `4` | Backup, dry run, or agent execution failed. |
| `130` | Interrupted by the user or system. |

For `backup`, Borg's own warnings and errors appear in the output. Exit code `1`
means Borg created the archive but reported warnings; exit code `4` means the
archive was not created. Specific causes, such as a lock timeout or a rejected
SSH host key, are described in the message rather than a separate exit code.

A dry run uses the same codes. Exit code `1` means the create or prune preview
reported warnings, and `4` means a preview step failed, such as a lock timeout.
Compaction is not previewed, so a dry run that would compact still exits with
`0` when nothing else warned; its output says compaction was not run.

## Examples

```sh
cairn backup --job service-a-local
cairn backup --job service-a-local --config ./config.yaml
cairn backup --job service-a-local --dry-run --config ./config.yaml
cairn config check --config ./config.yaml
cairn agent --config /etc/cairn/config.yaml
```
