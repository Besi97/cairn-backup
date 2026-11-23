# Container smoke test

Run `tests/container/run.sh` from the repository root with a Docker daemon.
The test builds both targets and uses disposable volumes, a network, and an
OpenSSH server with a generated key and pinned host key. Each image backs up a
mounted source to local and SSH Borg repositories, runs a scheduled backup, and
stops the idle agent with SIGTERM. No privileged container or Docker socket is
used by the test containers. The helper SSH server runs as root; Cairn does not.
The test removes its own containers, volumes, network, and images.

The test can run against a remote daemon and does not require host bind mounts.
Run it with a rootless Docker context to verify that engine; the default CI job
does not. It does not test shutdown during an active backup, bind-mount UID
mapping, arm64, or external SSH servers.
