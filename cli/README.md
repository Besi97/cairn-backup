# Cairn Backup CLI

Go source for the `cairn` command, which provides manual CLI execution and the
scheduler agent.

Run the following commands from `cli/`.

See the [command-line reference](../docs/cli.md) for the planned commands and
their behavior. Only `--version` is currently implemented.

## Build

```sh
go build -o bin/cairn ./cmd/cairn
```

## Run

```sh
./bin/cairn --version
```

## Checks

Requires Go 1.27.3, GNU Make, and a C compiler.

```sh
make check
```

This runs formatting, vet, Staticcheck, govulncheck, race-enabled tests, and
Linux amd64/arm64 builds. GitHub Actions runs the same checks on pull requests
and pushes to `main`.
