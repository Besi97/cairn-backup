# Command-line reference

`cairn config check`, help, and version output are available. Backup execution
and the scheduler agent are planned and are not available yet.

## Commands

```text
cairn backup --job NAME [--config PATH] [--dry-run]
cairn config check [--config PATH]
cairn agent [--config PATH]
```

- `backup` requires `--job NAME` to choose which job to run.
- `--config PATH` selects a configuration file. Without it, Cairn discovers the
  configuration using the documented system or user default.
- `config check` checks the configuration and reports problems. It does not run
  backups or contact repositories. See [validation limits](configuration.md#checking-the-configuration).
- `agent` stays running and starts jobs with schedules in the configuration.
- `--help` shows help for Cairn or a command. `--version` prints the version.
  Both work without Borg installed.

## Dry run

Add `--dry-run` to `backup` to preview supported backup and prune operations
without changing repository contents. Compaction is skipped because it has no
non-mutating preview. A preview is an estimate, not a promise of exactly what a
later backup will contain.

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

## Examples

```sh
cairn backup --job service-a-local
cairn backup --job service-a-local --config ./config.yaml --dry-run
cairn config check --config ./config.yaml
cairn agent --config /etc/cairn/config.yaml
```
