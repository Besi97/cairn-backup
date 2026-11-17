# Manual restore

```sh
cairn archive restore --repository /backups/repo --archive-name documents-before-upgrade \
  --destination ./recovered --dry-run Documents

cairn archive restore --repository /backups/repo --archive-name documents-before-upgrade \
  --destination ./recovered Documents

# No paths selects the whole archive.
cairn archive restore --repository ssh://user@host/./repo --archive-name snapshot \
  --destination ./recovered --passphrase-file ./passphrase --identity-file ~/.ssh/backup
```

Restore, mount, and unmount live under `cairn archive` and require
`--destination DIR`. Restore's positional arguments are archive-path selectors.

Require explicit nonempty repository, archive name, and destination. Configuration
is not loaded, `BORG_REPO` is not a fallback, and restores cannot be scheduled or
added to workflows. Repository locations and archive names must be literal:
placeholders are rejected so separate preflight/extraction processes cannot
expand to different archives. Local repositories and destinations must not overlap.

Destination and access-file paths resolve from the invocation directory.
The destination's parent must already exist. A missing destination is created
with mode `0700` only after successful preflight; Borg may subsequently restore
archived directory metadata there. Filesystem root is not a valid destination.
Relative destination paths such as `../recovered` are allowed, but symlinked
destination components, including those hidden by `link/../`, are rejected.

## Selection and replacement

Paths are **byte-exact, literal archive-relative path prefixes**, including their
descendants, not shell globs or Borg patterns. Cairn supplies Borg's `pp:` style;
`sh:literal` selects a path literally named `sh:literal`. Quote shell-sensitive
paths and use `--` before a path starting with a hyphen. Absolute paths, NUL,
empty components, `.`, `..`, and trailing slashes are rejected. Omit paths for the
whole archive. Unmatched selections produce Borg warnings and stop preflight.

**Do not copy inspection/listing display text into a restore selector.** Distinct
non-text names can have identical display strings. Explicit CLI arguments must
contain the original filename bytes. See [archive inspection](archive-inspection.md).

An existing destination requires confirmation by typing `yes` when both stdin
and stdout are terminals. The warning explicitly authorizes replacing conflicting
files and changing directory metadata. Without those terminals, Cairn fails without
reading stdin. `--allow-existing` provides the same authorization without prompting;
there is no separate overwrite flag. A new destination needs no confirmation.

Native Borg replaces conflicting entries where it can, merges directories, and
does not remove unrelated files. Type conflicts and permissions can prevent some
entries from being restored. **Restoration is not atomic and has no rollback.**
Warnings, failures, output errors, and interruptions may leave partial files,
replacements, and metadata changes. Inspect diagnostics and the final exit code.

## Safety boundary and preflight

**The repository, destination, and their parent directories must be trusted and
must not be changed concurrently by other processes. This is not a sandbox.**
Native Borg uses pathname operations; preflight cannot close check/write races
or protect against another process moving or replacing validated directories.
Do not restore over a live application tree. Prefer a separate recovery directory.

Before changing the destination, Cairn:

1. Checks native selected-entry types, rejecting unsupported types and hardlinked
   special files.
2. Streams a native GNU tar export to validate byte-exact paths and hardlink targets.
   No tar is saved or extracted. This pass reads selected file data and retains
   metadata proportional to the entry count.
3. Rejects traversal, duplicate paths, paths below archived symlinks/non-directories,
   and destination symlinks encountered along selected paths. Existing destination
   files with multiple hardlinks are also rejected to protect other names of the
   same inode. Use another destination in these cases.

Symlinks themselves may be restored, including links whose stored targets are
absolute or outside the destination; Cairn refuses extraction **through** them.
Link targets remain byte-exact. Do not follow restored links without inspecting them.
Formatted Borg listing names, including `{bpath}` on tested Borg builds, are not
used as identities: Borg's text output can still replace non-text bytes.

Any preflight failure or warning stops before creating or changing the destination.
Successful preflight is followed by **native `borg extract`**, not tar extraction.
Borg's normal metadata restoration applies, including ownership, modes, ACLs,
xattrs, flags, and supported timestamps, subject to the invoking user's permissions,
filesystem support, and Borg's own limitations. Cairn does not acquire privileges
or silently discard those metadata categories.

## Preview, access, and outcomes

`--dry-run` performs the same preflight followed by `borg extract --dry-run`.
It never creates or changes the destination and needs no replacement confirmation.
Native Borg dry-run reads and verifies selected file data but does not itself
check destination collisions or perform filesystem extraction. Cairn's preflight
adds practical path/link checks, not a promise that later writes will succeed.
Preflight and extraction/preview read selected data separately, so plan for the
extra repository traffic.

The usual credential precedence, noninteractive SSH host verification,
`--lock-wait SECONDS` (default 60), owned-process cancellation, and exit codes
apply. Each Borg phase acquires its native locks independently; Cairn adds no
queue, retries, or lock bypass. Borg can update its repository locks, cache,
security state, and other local state even during a preview.

An inherited `BORG_PASSPHRASE_FD` is consumed once into a sealed anonymous
in-memory descriptor (maximum 1 MiB), reopened separately for each phase, and
closed after all children exit. It is not copied to disk, command arguments, or
environment values. Explicit passphrase files retain precedence.

Status and Borg diagnostics go to stderr; the final human outcome goes to stdout.
Diagnostics and progress share the task-board renderer and terminal-control
escaping used by other operations. The lazy local history observer records one
restore summary, including preview status and normalized outcome, without paths,
archive selectors, credentials, or diagnostic text.
Exit codes are 0 success, 1 warnings, 2 execution/preflight/output failure,
3 invalid input or missing confirmation, and 130/143 interruption.
One manual `restore` run/task lifecycle spans preflight and extraction; events
omit repository locations, destination paths, archive names, and selectors.

## Shared backend

`cli/internal/restore.Prepare` returns an immutable `Plan` from `Options` and the
invocation directory. `Destination` and `Existing` support a caller's confirmation
UI. `Plan.Run(ctx, runner, allowExisting, diagnostics)` enforces replacement
authorization again, performs preflight, and returns `borg.Result`. A true
`allowExisting` must mean the caller has authorized replacement, not merely
selected a directory. Previews do not need that authorization.

The caller supplies a `borg.Runner`, retaining inherited advisory-lock descriptors
and the shared process/access contracts. Backend execution does not emit lifecycle
events itself; CLI adapters own the single run/task envelope. Other interfaces
must not convert `archive.DisplayText` into `Options.Paths`.
`Plan.WithProgress` returns a copy with a structured Borg progress callback;
preflight's binary stdout remains separate from diagnostics and progress.

For recovery without Cairn, see [native Borg recovery](recovery.md).
