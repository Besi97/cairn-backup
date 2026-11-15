# Live systemd smoke tests

Run from the repository root:

```sh
tests/systemd/run.sh
```

Requires Go and a Linux Docker daemon that permits privileged containers and
private cgroup namespaces (cgroup v2). This is a disposable Debian test image,
not a service installed on the development machine. The script builds the
Cairn binary, transports its files through Docker's build context, boots systemd
as PID 1, runs the tests, restarts the container to test boot enablement, and
removes its own container/image when finished. A remote `DOCKER_HOST` is supported;
no host directory or host systemd socket is mounted.

The image installs systemd and distro Borg 1.x. Coverage includes:

- Root installation as an existing unprivileged service account, all four
  enable/start combinations, and refusing to overwrite an installed unit.
- Exact process arguments containing spaces, quotes, backslashes, percent signs,
  `${HOME}`, `$HOME`, `$$`, and template-like text; rejected control characters.
- Real journal output and service/manual workflow lock contention.
- Scheduled real Borg work waiting on an authoritative repository lock,
  orderly cancellation/reaping, successful manual backup after service shutdown,
  and preserved lock-file inodes.
- Explicit restart, automatic restart after SIGKILL, invalid configuration
  exiting without restart, and enabled service startup on a fresh container boot.

One test waits for a real cron minute; total runtime is normally a few minutes
including the image build. The CLI CI workflow runs this test separately from
the existing Borg 1.2.8/1.4.5 integration matrix. Static packaging, installation,
checksum, architecture-selection, and unit-rendering regressions run through
`make -C cli release-check` without root or a running systemd manager.

Do not invoke `smoke.sh` directly on a real host. It deliberately refuses to run
outside the marked disposable test image.
