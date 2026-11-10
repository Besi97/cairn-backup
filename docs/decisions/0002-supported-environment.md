# Supported environment for 0.1.0

Status: agreed release contract; implementation and verification pending.

Date: 8 November 2026.

## Platform and distribution

- Linux `amd64` and `arm64`; reference systems: Debian 13 and Ubuntu 24.04 LTS.
  Other distributions are initially unverified.
- Versioned standalone binaries, optional installer, and manual installation
  instructions. Native packages are not required for 0.1.0.
- Operators install Borg and, for SSH repositories, OpenSSH separately. Cairn
  neither bundles nor silently installs them.
- systemd is needed only for the provided service setup. Installation and
  activation require explicit consent.
- Go is a build dependency only: module minimum `1.27.0`, initial release
  toolchain pin `1.27.3`. Update patch pins for security and bug fixes. Cairn
  itself requires no Python; the chosen Borg package may.

## Borg compatibility

Support stable local clients **>=1.4.5 and <1.5.0**, with existing local and SSH
Borg 1 repositories. New patches are verified only after testing. Repository
initialization and upgrades remain the operator's responsibility.

Check dependencies at agent startup and before manual work:

| Local Borg | Behavior |
| --- | --- |
| Stable version in range | Proceed. |
| Other recognizable Borg 1 version, including prereleases | Warn and attempt execution; compatibility is not guaranteed. |
| Major version 2+, including prerelease/development versions | Fail before repository access or workflow commands. |
| Missing, non-executable, or unidentifiable | Fail with an actionable diagnostic. |

Report the executable, detected/required versions, and corrective action without
secrets. Cairn help/version output must work without Borg. Version warnings do
not override operation failures or allow maintenance after a failed backup.

Initially verify matching SSH client/server versions; other pairs are unverified.
Honor standard SSH configuration and host verification. Local preflight does not
establish remote server compatibility.

### Why reject Borg 2?

The [Borg 2 upgrade notes][borg2-upgrade], reviewed at `2.0.0b26.dev184`, document
breaking changes affecting our workflow:

- Separate repository selection replaces `REPO::ARCHIVE` and positional
  repositories for prune/compact; repository formats and SSH locations change.
- Stable archive-series names replace the recommended unique-name pattern.
- Prune filters/options and default retention grouping change.
- Compaction needs keys for encrypted/authenticated repositories and changes
  threshold behavior.

Supporting Borg 2 needs deliberate command generation and integration tests, not
just a warning. Development documentation may change before stable release.

## Paths and permissions

| Purpose | System default | User default |
| --- | --- | --- |
| Binary | `/usr/local/bin/cairn` | Same binary or a custom manual installation location |
| Configuration | `/etc/cairn/config.yaml` | `$XDG_CONFIG_HOME/cairn/config.yaml` (`~/.config/cairn/config.yaml`) |
| State/history | `/var/lib/cairn` | `$XDG_STATE_HOME/cairn` (`~/.local/state/cairn`) |

Parenthesized XDG paths apply when the variable is unset or empty. Define discovery,
overrides, and relative-path resolution with the CLI/configuration contract;
services select paths explicitly.

Service setup explicitly selects root or an existing account with the necessary
source, repository, and credential access. Root is not generally required.
Keep secrets and private state owner-only by default (`0700` directories,
`0600` files); grant additional access deliberately. System configuration may be
root-owned and readable, but not writable, by the service account.

Configuration, secrets, and history do not need system-wide sharing. Repository
coordination is deferred; 0.1.0 relies on Borg's own repository locks. See
[local repository coordination](0001-local-repository-coordination.md).

## Verification before release

- Test the minimum and newest supported Borg patch, pinned explicitly; deduplicate
  jobs while those versions match. Start with matching SSH client/server pairs.
- Exercise local/SSH backups, native-Borg recovery with matching contents,
  retention isolation, compaction, dry-run non-mutation, and version-check policy.
- Execute tests on both architectures; cross-compilation is insufficient. Record
  actual reference OS/architecture combinations rather than implying a full matrix.
- Test cancellation of active and waiting Borg runs, crashes with surviving Borg
  subprocesses, rejected host keys, missing credentials, and secret-free
  diagnostics. Verify that Borg's repository lock serializes concurrent `create`
  and `prune`, and that a run waiting on the lock fails after `lock_wait`.
- Run Go tests, static analysis, security checks, and pinned-toolchain builds.
- Verify installation, explicit identity selection, service lifecycle, upgrade, and
  uninstall in VMs or reproducible manual checks.
  Preserve configuration, state, and backups unless removal is explicitly requested.

Record results, including any checks performed manually. These are requirements,
not claims of completed testing.

[borg2-upgrade]: https://borgbackup.readthedocs.io/en/latest/changes.html#borg-1-2-x-1-4-x-to-borg-2-0
