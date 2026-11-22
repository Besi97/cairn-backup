# Recovery drill executor

`Run` accepts an already prepared repository, absolute invocation directory,
literal archive selector, extraction scope, fixed assertions, and an optional
bounded live-tree sample. It returns `Result` and an error; consumers must
consider both the error and `Warning`, not just the absence of an error.

The drill does not run a whole-repository Borg integrity check. A failed
identity check, extraction warning, assertion
mismatch, sample error or cleanup failure fails the attempt. Different sampled
files and an empty sample produce incomplete warning evidence; omitted sample
files are failures. Sampling compares a bounded subset, not a snapshot.

Selection and identity checks use the same prepared access and lock-wait policy
as extraction. Individual Borg operations acquire their own locks; callers own
any wider cooperative coordination and inherited lock descriptors. The two
identity checks cannot detect replace-and-revert or prove what the separate
preflight and extraction commands saw. A trusted immutable snapshot or a lock
spanning all operations would be needed for stronger guarantees.

Workflow integration supplies prepared options and holds a workflow lock, not
a repository-wide coordination lock. Borg locks individual operations; other
workflows can access the same repository between them. Identity checks do not
exclude concurrent workflows. Configuration, schema, and CLI exposure are not
integrated here.
Diagnostics and full evidence can contain sensitive information; do not put
them in history; history stores a restricted projection. The executor is
Linux-only, like the assertion implementation.
