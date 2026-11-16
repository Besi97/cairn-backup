# Archive listing and inspection

```sh
cairn archive list --repository /backups/repo
cairn archive inspect --repository /backups/repo --archive-name documents-before-upgrade
cairn archive inspect --repository ssh://user@host/./repo \
  --archive-name documents-before-upgrade --identity-file ~/.ssh/backup \
  --passphrase-file ./passphrase --json
```

Both commands require an explicit nonempty repository and never load
configuration or fall back to `BORG_REPO`. Inspection also requires a nonempty
`--archive-name`; Borg expands placeholders normally. There are no positional
arguments, selection patterns, arbitrary Borg options, or dry-run mode.

Listing includes checkpoint archives and uses Borg's timestamp order.
Inspection streams all file metadata in Borg's native order, including directory
entries and link targets. It does not display file contents, extract anything,
or verify data integrity. Size is Borg's metadata size in bytes; notably, hardlink
slave entries can report zero. `healthy` reports Borg's stored replacement-chunk
marker, not a fresh integrity check.

Repository, passphrase-file, and identity-file paths resolve from the invocation
directory. Existing access/secret protections, noninteractive SSH host verification,
native Borg locking, and cancellation apply unchanged. `--lock-wait SECONDS`
defaults to 60; zero requests no waiting. Inspection does not modify archives,
but Borg can acquire locks and update local state.

## Output

Results go to stdout; Borg diagnostics and Cairn errors go to stderr. Human
output quotes names, paths, and link targets to escape newlines and terminal
control characters. No final summary is appended, including for empty results.

`--json` emits Cairn JSON Lines: one object per archive or file, not Borg's
repository JSON wrapper. Archive fields are `name`, `id`, and `start`. File fields
are `path`, `type`, `mode`, `size`, `mtime`, `uid`, `gid`, `linktarget`, and
`healthy`. Timestamps retain Borg's ISO text; type and mode use Borg's native
file-type and permission representations. Link targets are empty for non-links.

**Names, paths, and link targets are display text, not byte-exact identities.**
Borg JSON replaces non-text filename bytes; distinct names can become identical
strings. Do not feed these fields into restoration, extraction, or filesystem
operations as exact selectors. Native Borg's byte-preserving interfaces are
required when exact filename bytes matter.

Exit codes retain Cairn's meanings: 0 success, 1 warnings, 2 execution/output
failure, 3 invalid invocation, and 130/143 for SIGINT/SIGTERM. Output streams
incrementally and can be partial on any failure or interruption. Consumers must
check the final exit code; emitted records are not a success signal.

Cairn does not accumulate listings. Each JSON record, repository metadata value,
or token is bounded to 1 MiB, with bounded parsing buffers and callback
backpressure. Oversized or malformed results fail instead of silently truncating.
There is no cap on the number of records. This bounds Cairn's own handling, not
Borg's internal memory use.

## Shared backend

`cli/internal/archive` exposes `PrepareList`, `PrepareInspect`, `List`, and
`Inspect`. Preparation reuses `borg.PrepareRepository` and `borg.LockWaitArgs`.
Execution accepts a `borg.Runner`, a prepared `borg.Request`, and a serial typed
callback. Set `request.Diagnostics` for stderr; execution owns `request.Stdout`.
Callbacks run on the decoder goroutine and provide backpressure; they must return
promptly, including on cancellation. Returning an error stops the owned Borg
process group and reports failure. Requests retain access settings and runners
retain inherited lock descriptors.

Consumers should reuse `Archive`/`File` records and `borg.Result`, not parse CLI
tables. `DisplayText` deliberately distinguishes lossy display fields from
byte-exact restore identities. Browsers must bound their own retained state;
streaming callbacks do not make an accumulating caller memory-bounded.
