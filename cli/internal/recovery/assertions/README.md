# Fixed recovery assertions

The internal recovery drill is wired into the workflow executor and evaluates
these assertions. Configuration and schema exposure remain separate work.

`Evaluate(ctx, root, checks)` runs checks in input order against a stable,
extracted Linux tree. Paths are literal, relative, slash-separated names with
no empty, `.` or `..` components; `.` names the root. The caller owns the
workspace and must trust the ancestors of `root`. The root itself cannot be
a symlink.

- Path type checks inspect the leaf itself, including dangling symlinks.
- Size is the regular file's logical length in bytes. Exact bytes and SHA-256
  read only regular files.
- Count checks start at a directory and count regular paths recursively.
  Hard-linked names each count once; symlinks and special files do not count.
  A readable empty directory counts as zero.
- A missing target, wrong leaf type or different value is a mismatch. A
  non-directory or symlink parent, invalid check or filesystem error is an
  evaluation failure. A walk that cannot finish does not return a partial count.
  Cancellation fails the in-progress check and leaves later checks unevaluated.

Results contain only status and reason codes; callers can match them to inputs
by position. No paths, contents or OS diagnostics are returned.

## Sampled comparison

`CompareSample(ctx, restored, live, limit)` compares regular restored paths
against the same relative paths in an explicitly supplied live tree. Both
roots must have trusted ancestors and cannot themselves be symlinks. Directory
traversal does not follow symlinks; content reads reopen pinned regular inodes
through trusted Linux `/proc/self/fd`. The restored tree must be stable; neither
tree is a snapshot, so concurrent changes may affect the result.

Selection takes the lowest SHA-256 hashes of relative path bytes (ties use
path byte order), up to `limit` regular paths. Hard links count by name;
symlinks and special files are excluded. `limit` must be between 1 and
`MaxSampleFiles`. The method, eligible population, selected and unselected
counts, completed comparisons, differences and omissions are returned without
paths or content. An incomplete walk fails and its eligible count is partial.
The limit bounds comparisons, not extraction, traversal, or bytes read.

Different content returns `SampleDifferent`: it is a warning about live data,
not evidence of archive corruption. Missing, non-regular or unreadable selected
paths are omissions; any omission makes the result incomplete, even if other
comparisons differ. An empty sample is also incomplete. Root, traversal and
cancellation failures and other I/O failures return `SampleFailed`; failures
during comparison count all remaining selected paths as omitted.
