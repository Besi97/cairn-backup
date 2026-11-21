# Fixed recovery assertions

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

For #127, the descriptor-relative lookup and regular-file open in
`tree_linux.go` are the reuse boundary. Content reads reopen the pinned regular
inode through trusted Linux `/proc/self/fd`; they never reopen a restored
name. The helpers are not a snapshot or a guarantee that a live tree stays
unchanged during comparison.
