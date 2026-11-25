# Rootless container deployment

The [Compose example](../deploy/compose/) runs the foreground scheduler as
UID and GID `10001`, the default in both images. It needs neither privileged
mode nor a Docker socket. The images include Borg 1.x and OpenSSH. The
distroless image has no shell; invoke Cairn commands directly. Shell workflow
tasks require an explicitly selected installed shell and will not run in the
distroless image unless one is supplied separately.

## Choose an image

The release workflow publishes `ghcr.io/besi97/cairn-backup:vVERSION` (regular)
and `ghcr.io/besi97/cairn-backup:distroless-vVERSION` (distroless), with
linux/amd64 and linux/arm64 manifests. Select a version that has actually been
published; do not assume a future release is available. For example, after a
`v0.4.0` release publishes its images, select either:

```sh
export CAIRN_IMAGE=ghcr.io/besi97/cairn-backup:v0.4.0
```

or:

```sh
export CAIRN_IMAGE=ghcr.io/besi97/cairn-backup:distroless-v0.4.0
```

For a local build before publication, build the `regular` or `distroless`
Dockerfile target from the repository root, then select the local tag:

```sh
docker build --target regular -t cairn-local:regular .
export CAIRN_IMAGE=cairn-local:regular
```

Use `--target distroless -t cairn-local:distroless` and select that tag to
build the other variant.

## Prepare paths and permissions

Copy `deploy/compose/compose.yaml` and `deploy/compose/config.yaml` into a
private deployment directory. Run the following Compose commands from there,
using a rootless Docker context. Create `source`, `repository`, `home`, and
`secrets` directories there first. Replace `source` with a bind mount of the
actual data, or change its source path in Compose. The mount is read-only; Cairn
still needs permission to traverse directories and read every file to back up.
The config file must also be readable by the container user. Unreadable source
files can make a backup incomplete. Review Borg warnings.

Bind-mount permissions are checked using the daemon's user namespace mapping,
not the host's numeric UID 10001 by default. In rootless Docker, container UID 0
maps to the daemon user's host UID; container UID 10001 maps into its subordinate
UID range. GIDs are mapped similarly. Inspect the actual `uid_map` and `gid_map`
of a running container or the daemon's subordinate ID configuration, and grant
the mapped identity access using ownership or carefully scoped ACLs. Mapping
and filesystem policies vary; do not make source data or secrets world-readable
to work around access errors. A rootful daemon with user namespace remapping
also needs its own mappings considered.

The `home` directory must be writable and owned by the mapped UID, with private
permissions. It persists Cairn's state and locks, and Borg's cache, security
state and any local keys. Back it up securely as appropriate. The `repository`
directory must be writable by the mapped identity. Keep repository storage
separate from the source and protect it from loss; the example uses an existing
encrypted Borg repository at `repository/borg`. Cairn does not initialize it.
Initialize it separately with compatible Borg 1.x and retain its encryption key
and passphrase safely. Put the passphrase in `secrets/borg-passphrase`, readable
only by the mapped identity (normally mode `0600`), and protect the directory.
Do not commit credentials or put them in Compose environment variables.

Set `BORG_HOST_ID` to a stable, unique value for this installation so Borg can
recognize its locks across container replacements. Keep the same home, state,
config path and runtime settings for invocations that must coordinate. Do not
run multiple agents with separate state against the same configuration.

## SSH repository

For an existing remote repository, replace the `local` repository in config
with the following, adjusting the host, user and repository path:

```yaml
  local:
    location: ssh://backup@backup.example.com/./cairn
    passphrase_file: /run/cairn-secrets/borg-passphrase
    identity_file: /run/cairn-secrets/id_ed25519
```

Install the private identity in `secrets/id_ed25519`, owned by the mapped UID
and mode `0600`. The SSH key must work non-interactively; an encrypted key
requires an accessible SSH agent. Authorize its public key on the server and
ensure a compatible Borg 1.x is available there. The local repository mount
can be removed for an SSH-only configuration.

Place the server's verified host key in `home/.ssh/known_hosts`, with a private
`home/.ssh` directory. Verify the fingerprint through an independent trusted
channel before trusting a scanned key. Cairn uses strict host-key checking and
batch mode: unknown or changed keys fail rather than prompting. Test access to
the real server and credentials before relying on scheduled backups.

## Run and operate

```sh
docker compose run --rm cairn config check --config /etc/cairn/config.yaml
docker compose run --rm cairn workflow run nightly --config /etc/cairn/config.yaml
docker compose up -d
docker compose logs -f cairn
```

Configuration checking does not test access to files, credentials or the
repository. The manual run does; inspect its outcome and verify a restore.
The agent runs at 02:00 UTC while it is running, not immediately on startup;
missed schedules are not replayed. Restart after config edits. Logs and
[history](run-history.md) help inspect outcomes; a running container alone
does not mean a backup succeeded.

Use `docker compose stop` for shutdown. Docker sends SIGTERM; Cairn stops
dispatching, cancels active work and waits for subprocess cleanup. The five
minute grace period gives cleanup time before Docker forces termination; adjust
it for the environment. Cancellation is not a rollback and may leave Borg
checkpoints. See [scheduling and locks](configuration.md#schedules) for
overlap behavior and the full configuration contract.
