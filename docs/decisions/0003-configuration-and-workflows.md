# ADR 0003: Versioned configuration and staged workflows

- Status: Accepted
- Date: 2026-11-14
- Issue: [#52](https://github.com/Besi97/cairn-backup/issues/52)

## Context

Standalone operations are already usable without configuration. Reusable
repository access settings and scheduled procedures should compose those same
operations without bringing back implicit backup-with-maintenance behavior.
Configuration should support editor validation and autocomplete before runtime
loading and workflow execution are implemented.

## Decision

Use YAML with three required top-level fields: `version`, `repositories`, and
`workflows`. Version 1 is explicit; omission never means "latest". Repositories
and workflows are named maps. Workflows contain ordered, unnamed stages; each
stage contains unnamed tasks that run in parallel.

Use flat, operation-specific task properties, discriminated by `type: backup`,
`type: prune`, or `type: compact`. Publish a versioned JSON Schema for YAML
validation and completion. A `oneOf` union and closed objects reject unknown and
operation-inappropriate fields without extra nesting.

Advance only when all tasks in a stage finish successfully without warnings.
Let already-running sibling tasks finish after a warning or failure, then stop
the workflow. Backup, prune, and compact are explicit tasks; never infer
maintenance. Workflows may omit cron and run manually without the scheduler.

Repository credentials may be an inline passphrase or a passphrase-file
reference, never both. Configured credentials take precedence over inherited
Borg credentials; absent configured credentials retain standalone precedence.
Keep secrets out of output, diagnostics, and process arguments. Keep the
standalone CLI's no-inline-passphrase-flag policy unchanged.

Resolve configured local paths relative to the selected configuration's
directory, with limited current-user `~` expansion. Discover one configuration
using an explicit path, the user's XDG config location, then `/etc/cairn`.
Never merge files or fall back after a selected file cannot be read or validated.
Standalone operations must not discover configuration.

The complete field and behavior contract is in
[Configuration](../configuration.md), with a
[version 1 schema](../../cli/schema/config-v1.schema.json) and
[example](../../cli/examples/config.yaml).

## Consequences

Explicit schema versions make incompatible upgrades deliberate. Named
repositories avoid duplicated access settings, while tasks remain explicit about
archive naming, sources, retention, and repository-wide compaction.

Schema validation catches structural errors without Borg or access to secret
files. Runtime validation must also check references and operation semantics;
standard JSON Schema cannot derive repository references from another map's keys.

Loading and validation belong to #53, execution and the manual workflow command
to #54, and scheduling and overlap enforcement to #55. Those features are not
implemented by this decision. Cron dialect, timezone, missed-run, and retry
policies remain open for #55. Workflow overlap coordination is separate from
Borg's authoritative repository locking.
