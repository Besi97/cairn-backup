# ADR 0001: Supported environment and scope for 0.1.0

- Status: Accepted
- Date: 2026-11-13
- Issue: [#38](https://github.com/Besi97/cairn-backup/issues/38)

## Context

Cairn is starting over with configuration-free manual commands as its foundation.
The first release still needs optional configured workflows and a built-in
scheduler, but it should not inherit the previous configuration-first design.

We need clear platform and release boundaries before rebuilding. Targeting only
one Borg patch release would exclude otherwise compatible installations, while
claiming support for all Borg 1.x versions would overlook differences in commands
and behavior. Borg 2 is still in testing and is not a target for this release.

## Decision

The following defines the intended support for 0.1.0, not capabilities already
implemented.

### Platform and dependencies

Cairn targets Linux on amd64 and arm64. The CLI and built-in scheduler are
implemented in Go and distributed as versioned binaries.

Users install Borg separately; Cairn does not bundle it. OpenSSH is also needed
for SSH repositories. SSH access uses normal SSH configuration and known-host
verification, with an optional explicitly selected identity file. Cairn must not
silently disable host-key verification.

The scheduler can run under systemd. Installing the CLI must not automatically
enable a privileged service; service setup is an explicit choice.

### Borg compatibility

The primary target is the latest stable Borg 1.x release. As of 13 November 2026,
that is Borg 1.4.5, according to the
[Borg release page](https://www.borgbackup.org/releases/).

Older stable Borg 1.x releases should also be supported when they provide the
interfaces and behavior Cairn needs. Compatibility includes the commands and
flags used for backup, prune, and compact, as well as archive selection,
dry-run semantics, exit codes, and locking. Matching flag names alone is not
enough. Cairn must not silently drop options or weaken safety to accommodate an
older version.

The minimum supported Borg version will be established from the requirements of
the [CLI contract](https://github.com/Besi97/cairn-backup/issues/44). CI must
exercise the minimum supported version and the latest stable Borg 1.x release.

Borg 2 and prerelease versions are outside the supported range. Unsupported
versions must produce a clear error before a backup or maintenance operation
starts.

### Included in 0.1.0

- Backups to existing local or SSH-accessible Borg repositories.
- Standalone backup, prune, and compact commands. Each can run without a
  configuration file or a running scheduler when its inputs are supplied
  directly. Backup does not implicitly prune or compact, and prune does not
  implicitly compact.
- Optional configuration for reusable settings and workflows that can compose
  backup, prune, and compact. The schema and workflow behavior will be designed
  separately, rather than inherited from the previous implementation.
- A built-in scheduler for unattended execution of configured work, using the
  same execution behavior as manual commands.
- Backup and prune previews within Borg's supported dry-run semantics. No
  compaction preview is promised.

### Outside 0.1.0

Repositories must already exist; users initialize them separately with Borg.
The release does not include Borg 2 support, restore commands, repository
integrity checks, lifecycle hooks, filesystem snapshots, database dumps, or
container orchestration. Recovery remains available through native Borg and
must be covered in the operating documentation.

Other operating systems and CPU architectures are not initial support targets.
Central management and a monitoring server are also outside this release.

## Consequences

Manual commands cannot depend on configuration loading or a running scheduler.
Configured and scheduled work must reuse the same operation implementations,
rather than introducing a second execution path.

Users remain responsible for installing Borg, setting up repository access, and
initializing repositories. Keeping those responsibilities outside Cairn limits
the initial implementation and testing scope, but makes installation and
native-Borg recovery instructions important parts of the release.

Supporting older Borg releases requires checking behavior, not just version
strings. We may need to exclude older releases when they cannot provide the
required safety guarantees; compatibility must not silently change an operation's
meaning.

## Open question

The exact minimum supported Borg version remains unresolved until the CLI
requirements establish which interfaces Cairn needs. Record that version and
the resulting supported range in a follow-up decision before release.
