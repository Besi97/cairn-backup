# Cairn CLI

The CLI currently provides help and version output only. Backup, maintenance,
configuration, and scheduling will be added separately.

Use Go 1.27.4 or later and Make. Run from this directory:

```sh
make build
./bin/cairn --help
./bin/cairn --version
make check
make cross-build
```

`make check` checks formatting, runs `go vet`, tests with the race detector,
and runs a pinned version of `govulncheck` against the current vulnerability
database. Go downloads dependencies and the scanner as needed; no global tool
installation is required. Race tests require a C compiler.

Builds default to version `dev`. Set `VERSION` for a versioned binary:

```sh
make build VERSION=0.1.0
```

`cmd/cairn` contains the process entry point. `internal/cli` handles arguments
and output; future Borg execution code should remain separate from command
parsing. Help and version output do not require Borg or configuration.
