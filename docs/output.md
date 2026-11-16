# Reading operation output

Standalone backup, prune, compact, and check commands keep status and Borg
diagnostics on stderr and their final human result on stdout. Workflows keep
task results on stdout; manual workflow summaries also use stdout, while agent
lifecycle reports and scheduled workflow summaries use stderr. Exit codes and
cancellation behavior are unchanged. Listing commands and other data stdout are
not routed through the operation display.

## Attribution

```text
cairn [daily 1.2 check/local] whole repository consistency ...
borg [daily 1.2 check/local] Starting repository check
cairn [daily 2.1 compact/local] skipped: previous stage completed with warnings
Workflow "daily" completed with warnings; 1 later stage(s) not started; tasks: 1 succeeded, 1 warning, 1 skipped.
```

The compact task identity is `WORKFLOW STAGE.TASK OPERATION/REPOSITORY`.
Stage and task indexes start at one, matching configuration order. Repository
names are configured aliases, not locations. Every diagnostic line carries its
own identity, including multiline and split writes from concurrent tasks.
`borg` means forwarded Borg text; `cairn` means native task status. Standalone
diagnostics use an operation label such as `borg [backup]`.

Summary counts distinguish succeeded, warning, failed, interrupted, and skipped
tasks, omitting zero counts. Unstarted tasks are skipped, not failed or
interrupted. An independently interrupted Borg child can fail a workflow while
still being counted as an interrupted task. Skip lines explain cancellation,
earlier-stage warnings/failures, or unavailable execution.

## Interactive terminals

When stderr is a capable terminal, Cairn maintains a small transient region
with one row per active task:

```text
cairn [daily 1.1 backup/local] running | borg: 1.23 GB O 800.00 MB C ...
cairn [daily 1.2 check/remote] running | borg: Checking segments 45%
```

Permanent messages appear above the board. Both streams are coordinated so
stdout task results cannot overwrite it. Rows disappear when tasks finish, and
the region is removed when work ends, including cancellation. The display never
reads keyboard input, changes terminal modes, opens a fullscreen interface, or
takes ownership of signals. It is separate from `cairn tui`.

Rows are clipped to the terminal width; excess active tasks are represented by
a count when terminal height is insufficient. Very small terminals and
`TERM=dumb` fall back to plain lines.

Only explicit Borg progress records are transient. Warnings, errors, statistics,
file previews, prune lists, and other non-progress diagnostics remain permanent,
even at informational severity.

## Pipes and journals

Redirected stderr uses plain attributed lines, including progress updates. No
board, cursor movement, or terminal controls are written to that stream. A
redirected stdout remains separate and contains no board effects even if stderr
is still a terminal.

Cairn requests Borg's structured logging internally to distinguish progress from
diagnostics; normal records are rendered as human text, not JSON. Unknown
records with a message retain that message. Unknown or malformed records without
a readable message remain visible in their original form, possibly JSON, rather
than being discarded. Terminal-control characters are escaped. Oversized
diagnostic lines are split into bounded fragments without dropping their text.
Untyped output from custom Borg logging configurations remains permanent rather
than being guessed to be progress.

These are human-readable logs, not a stable machine-readable format or a
persistent history store.
