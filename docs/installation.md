# Installation

Cairn releases provide Linux `amd64` and `arm64` archives. Borg 1.x (at least
1.2.8) and OpenSSH for SSH repositories are separate prerequisites.

## Install a version

Choose a published version and inspect the versioned installer before running it:

```sh
VERSION=0.1.0
curl --fail --location \
  "https://raw.githubusercontent.com/Besi97/cairn-backup/v${VERSION}/cli/scripts/install.sh" \
  --output install.sh
less install.sh
```

The installer downloads the matching Linux architecture archive and its
`SHA256SUMS` file from the same GitHub Release, verifies the archive, and installs
the binary and optional systemd setup files. The checksum detects transfer
corruption; it is not a signature or an independent authenticity check.

```sh
sudo bash install.sh --version "$VERSION"
```

The default prefix is `/usr/local`. For a per-user install, use a writable
absolute prefix, for example:

```sh
bash install.sh --version "$VERSION" --prefix "$HOME/.local"
```

Make sure `$HOME/.local/bin` is on `PATH`. Installing Cairn
does not install Borg, change configuration, create repositories, or enable a
systemd service. Existing regular Cairn files may be upgraded; symlinks and
non-regular targets are refused.

## Manual installation

Download the archive and checksum file for `linux_amd64` or `linux_arm64` from
the version's GitHub Release. Verify the archive before extracting:

```sh
grep '  cairn_0.1.0_linux_amd64.tar.gz$' SHA256SUMS | sha256sum --check
tar -xzf cairn_0.1.0_linux_amd64.tar.gz
sudo install -m 0755 cairn /usr/local/bin/cairn
```

The archive also contains `setup-systemd.sh`, its service template, and these
instructions. You can keep the optional setup material beside the binary or use
the installer to place it under `/usr/local/share/cairn`.

Maintainers can build the two archives and `SHA256SUMS` with
`make -C cli release VERSION=0.1.0`. The manually triggered Release artifacts
workflow builds the same files as a downloadable workflow artifact; it does not
publish a GitHub Release or create a tag.

## Upgrade and remove

Run the installer for the new version. It replaces only Cairn's binary and
optional setup/documentation files; it does not change configuration, data,
permissions of backup sources, or service enablement. If the agent service is
running, restart it after upgrading so it uses the new binary.

To remove a per-user install, remove that prefix's `bin/cairn` and
`share/cairn`. For a system install, stop and disable the service first as
described in [systemd setup](systemd.md), then remove `/usr/local/bin/cairn` and
`/usr/local/share/cairn`. Never remove repositories or Cairn lock files while
Cairn or Borg work is active. Removing the program does not remove backups.
