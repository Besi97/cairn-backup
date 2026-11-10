# Command-line reference

`cairn backup`, `cairn config check`, help, and version output are available.
Dry-run previews and the scheduler agent are not available yet.

## Commands

```text
cairn backup --job NAME [--config PATH] [--dry-run]
cairn config check [--config PATH]
cairn agent [--config PATH]
```

- `backup` requires `--job NAME` to choose which job to run. It creates one
  archive for that job using the local Borg executable. Cairn checks the Borg
  version first; see [Running a backup](configuration.md#running-a-backup).
- `--config PATH` selects a configuration file. Without it, Cairn discovers the
  configuration using the documented system or user default.
- `config check` checks the configuration and reports problems. It does not run
  backups or contact repositories. See [validation limits](configuration.md#checking-the-configuration).
- `agent` stays running and starts jobs with schedules in the configuration.
- `--help` shows help for Cairn or a command. `--version` prints the version.
  Both work without Borg installed.

## Dry run

`--dry-run` is planned for `backup` to preview backup and prune operations
without changing repository contents. It is not available yet: `backup --dry-run`
exits with code `2` and runs nothing. A future preview will be an estimate, not a
promise of exactly what a later backup will contain.

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
| `4` | Backup or agent execution failed. |
| `130` | Interrupted by the user or system. |

For `backup`, Borg's own warnings and errors appear in the output. Exit code `1`
means Borg created the archive but reported warnings; exit code `4` means the
archive was not created. Specific causes, such as a lock timeout or a rejected
SSH host key, are described in the message rather than a separate exit code.

## Examples

```sh
cairn backup --job service-a-local
cairn backup --job service-a-local --config ./config.yaml
cairn config check --config ./config.yaml
cairn agent --config /etc/cairn/config.yaml
```
