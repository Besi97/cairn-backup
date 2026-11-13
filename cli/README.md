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

`internal/borg` provides the shared process runner. It checks the selected
executable's version before each operation, accepts stable Borg 1.x from 1.2.8,
and streams operation diagnostics to a supplied writer. Callers prepare argument
arrays, environment overrides, and the working directory; the runner neither
loads configuration nor invokes a shell. Results distinguish success, warnings,
failures, and interruption while retaining the process exit code.

The runner is not wired to CLI commands yet. Lock policy and complete
subprocess-tree cancellation are separate work. Its current context cancellation
stops only the direct Borg process.

## Repository access

`Request.Access` supplies an optional passphrase file, optional SSH identity, and
whether the repository uses SSH. Resolve these inputs before running an operation;
file paths are relative to Cairn's original directory, not `Request.Dir`.

An explicit passphrase file overrides `BORG_PASSPHRASE`, `BORG_PASSCOMMAND`, and
`BORG_PASSPHRASE_FD`. Cairn opens the file and forwards a descriptor without copying
the secret into arguments or environment values. Otherwise Borg's precedence is
preserved, including empty values. Selected inherited descriptors are duplicated
and remapped into the child; the original descriptor is not closed, though reading
the duplicate advances their shared file offset. Recognized inherited credential
descriptors are marked close-on-exec to prevent accidental inheritance; only the
selected duplicate is forwarded to the operation, not the version probe.
Cairn closes owned descriptors after each run.
Borg reads descriptor contents and strips trailing newlines in its normal way.

Without credential inputs, Cairn supplies an empty passphrase instead of allowing
Borg to open a terminal prompt. Unencrypted repositories, empty-passphrase keys,
and operations such as compact remain usable without supplying a secret. Missing
or incorrect required credentials fail. `BORG_PASSCOMMAND` remains a trusted
user-provided program: Borg runs it when needed, and users must ensure it is
non-interactive and does not print secrets to stderr.

SSH uses OpenSSH with `BatchMode=yes` and `StrictHostKeyChecking=yes`, overriding
`BORG_RSH`. Standard SSH configuration, agents, known-host files, and URL options
remain available; an identity adds `-i` without restricting other configured keys.
Unknown or changed host keys require explicit setup outside Cairn. Encrypted SSH
keys must already be usable non-interactively, for example through an agent.
Local operations do not require OpenSSH. Cairn does not automatically set Borg
repository-trust approvals; explicit inherited Borg trust settings remain in effect.

CI exercises access against Borg 1.2.8 and 1.4.5 using disposable repositories and
a fake SSH transport (not a live SSH server). To run these checks locally, install
a supported Borg binary named `borg` and run:

```sh
CAIRN_TEST_BORG="$(command -v borg)" go test -race -count=1 -run TestRealBorgAccess -v ./internal/borg
```
