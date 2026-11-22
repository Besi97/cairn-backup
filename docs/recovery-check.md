# Recovery check walkthrough

A recovery check extracts an archive into a temporary directory and checks the
restored files. It does not restore them over the live tree. For an actual
restore, see [manual restore](restore.md); for native Borg recovery, see
[recovery without Cairn](recovery.md).

Suppose `/srv/documents/marker.txt` contains `ready` followed by a newline,
and the local Borg repository `/backups/documents` contains an archive named
`documents-2026-11-20` with the same file archived as `marker.txt`. This
configuration checks the latest archive with the literal prefix `documents-`:

```yaml
version: 1
repositories:
  documents: {location: /backups/documents}
workflows:
  recovery:
    cron: "0 4 * * SUN"
    stages:
      - tasks:
          - type: recovery_check
            repository: documents
            archive_prefix: documents-
            paths: [marker.txt]
            checks:
              - {kind: file_bytes, path: marker.txt, bytes: "ready\n"}
            sample: {live_root: /srv/documents, limit: 10}
```

Save it as `cairn.yaml`, configure repository credentials if needed (see
[configuration](configuration.md)), and run as the account with repository
access:

```sh
cairn config check --config cairn.yaml
cairn workflow run recovery --config cairn.yaml
cairn history
cairn history --json
```

The agent can also run this workflow on the configured schedule while
`cairn agent run` is active. There is no execution queue; overlapping attempts
are governed by workflow and Borg locks. Use the same account and state
environment to inspect scheduled history.

- **Pass:** the report identifies the selected archive, shows extraction,
  identity checks, fixed check and cleanup passed, and a sample with one file
  compared and none different. The workflow exits 0.
- **Failed expectation:** change `archive_prefix` to
  `archive_name: documents-2026-11-20` and change the expected bytes to
  `"approved\n"`. The fixed check reports a mismatch and the workflow fails
  (exit 2). The sample can still pass independently. Investigate the selected
  archive and expectation before deciding what to restore.
- **Live drift:** restore the original configuration, then change the live
  marker to `updated` followed by a newline without creating another archive.
  The fixed check still passes, but the sample reports one difference and the
  workflow warns (exit 1). A changed live file is expected after a backup and
  does not by itself prove corruption.

A warning or failure stops later stages. The report includes selection,
extraction, checks, sampling and cleanup; `cairn history --json` provides a
versioned `runs` array with retained recovery evidence under each `summary`.
The human history view and TUI also show retained evidence. Archive names and
paths there are quoted display values, not restore selectors. History is a
private, bounded store containing identifying archive names and paths; it can
omit details or records. Persistence failure only warns and does not change the
workflow result. See [history limits and permissions](run-history.md).

The sample compares at most the configured limit of eligible regular files,
not the entire archive; it is not a live snapshot. A pass only covers the
selected archive, scope and checks. Identity checks compare the archive ID
before and after extraction; they cannot detect a replace-and-revert or prove
which identity the separate extraction saw. Use a trusted temporary directory
with enough space: the sample limit does not bound extraction or traversal.
Cairn normally removes the workspace, but cleanup can fail or be skipped if
Borg shutdown is unconfirmed, and a crash or SIGKILL can leave files behind.
Inspect cleanup results and handle leftovers securely. A recovery check is not
a repository consistency check or a substitute for practicing a full restore.
