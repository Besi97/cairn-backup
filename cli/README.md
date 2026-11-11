# Cairn Backup CLI

Go source for the `cairn` command, which provides manual CLI execution and the
scheduler agent.

Run the following commands from `cli/`.

See the [command-line reference](../docs/cli.md) for commands and their behavior.
Configuration checking, manual backups, dry-run previews, help, and version output
are available; the agent is not yet implemented.

## Build

```sh
go build -o bin/cairn ./cmd/cairn
```

## Run

```sh
./bin/cairn --version
./bin/cairn config check --config ../docs/config.example.yaml
```

## Checks

Requires Go 1.27.3, GNU Make, and a C compiler.

```sh
make check
```

This runs formatting, vet, Staticcheck, govulncheck, race-enabled tests, and
Linux amd64/arm64 builds. GitHub Actions runs the same checks on pull requests
and pushes to `main`.

Most backup tests use a fake `borg` script. The real-Borg tests in
`internal/backup` run only when `borg` is on `PATH`; otherwise they are skipped.
Set `CAIRN_REQUIRE_BORG=1` to make a missing `borg` a failure instead, as CI does
with Borg 1.4.5. SSH host-key and credential behavior is checked manually against
a real SSH server.

## Configuration preflight

Execution preflight and agent startup must call `config.LoadValidated` (or its
environment-aware equivalent) and reject errors before starting work. It uses
the shared loader, validates semantics before path resolution, and does not read
referenced files or contact repositories. `Config.Validate` is also available for
in-memory configuration; validate before normalizing paths. Do not substitute the
loading-only `config.Load` for validated preflight.

Use `config.ParseSchedule` for the agent's cron interpretation so scheduling and
validation accept the same six-field syntax. Agent configuration changes require
an explicit restart; no hot reload is supported in 0.1.0.
