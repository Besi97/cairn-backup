# Archive mounting (optional)

Mounting is optional on Linux. Listing, inspection, and native Borg extraction
do not need FUSE. Do not delay recovery to configure FUSE: use
[extraction](recovery.md#inspect-and-extract) when mounting is unavailable.

```sh
mkdir -m 700 "$HOME/archive-view"
cairn archive mount --repository /backups/repo \
  --archive-name documents-before-upgrade "$HOME/archive-view"

# In another terminal, after closing readers and leaving the mounted directory:
cairn archive unmount "$HOME/archive-view"
```

## Dependencies and permissions

Install supported Borg separately (stable 1.2.8 or compatible later 1.x;
1.4.5 is the primary target). Borg must have a working FUSE implementation
(`llfuse`/FUSE 2 or `pyfuse3`/FUSE 3, depending on its packaging), the matching
system libraries, and access to `/dev/fuse`. Containers may lack the device,
mount permissions, or a suitable mount namespace. Cairn reports failures; it
does not install packages, load kernel modules, enable services, change system
configuration, or request privileges.

Native `borg umount`, used by Cairn, invokes `fusermount -u`, falling back to
`umount` if `fusermount` is absent. A distribution providing only `fusermount3`
may need administrator-provided compatible packaging or explicit native
cleanup. Cairn does not create helper symlinks or escalate privileges.
The existence of `/dev/fuse` alone does not prove that mounting works.

The mountpoint must already be an empty directory owned by the invoking user,
not group/world writable or a final-component symlink. Use a private directory
outside live source trees.
Borg enforces read-only access and its normal file permissions. Cairn exposes
no arbitrary mount options, `allow_other`, permission overrides, or damaged-file
override. Archived ownership can prevent reading files as an ordinary user;
do not run as root just to bypass this restriction.

**A read-only archive mount is not a sandbox.** Archived symlinks can resolve to
live files outside the mount. Inspect only trusted archives, do not follow
untrusted links, and do not use mount contents as an unattended mirror source.
Mounting/copying also does not preserve every restore attribute: Borg's FUSE
interface does not support special filesystem flags or ACLs.

## Lifetime, access, and locks

`cairn archive mount` requires explicit `--repository`, `--archive-name`, and
one positional mountpoint. It mounts exactly one archive, not a whole repository.
There is no background mode, dry run, configuration loading, configured-name
lookup, or `BORG_REPO` fallback. Borg expands archive placeholders normally.
Use `--` before mountpoints beginning with a hyphen.

Local repository and access-file paths resolve from the invocation directory.
SSH locations use the existing noninteractive access policy with host-key
verification. `--passphrase-file` and `--identity-file` work as for inspection;
secrets are not accepted inline. `--lock-wait SECONDS` defaults to 60, with zero
requesting an immediate attempt. Borg owns its repository lock for the mount's
lifetime; long-lived mounts can block backup/maintenance. There is no new queue,
retry, bypass-lock option, or Cairn mount registry.

Cairn stays running until Borg exits. Startup text is not a mount-ready
notification. Diagnostics/status go to stderr, and a final outcome goes to
stdout only when the command ends. Normal Cairn exit codes apply, including
130/143 for SIGINT/SIGTERM. Cancellation stops and waits for the owned process
group using the existing bounded shutdown grace period.

## Unmount and interrupted cleanup

Close open files, leave the mounted directory in all shells, and explicitly
run `cairn archive unmount MOUNTPOINT` as the mounting user. Foreground Ctrl+C
(SIGINT) normally lets Borg unmount cleanly. SIGTERM, crashes, SIGKILL, or
escalation after Cairn's shutdown grace period can leave a disconnected/stale
mount. There is **no automatic crash cleanup**, including after mount failure.
Inspect the mount state and request explicit unmount; never delete Borg locks
while another client might still be active.

Unmount checks `/proc/self/mountinfo` in the current mount namespace: exactly
one entry at the exact mountpoint, filesystem `fuse` or `fuse.borgfs`, source
`borgfs`, root `/`, and `user_id` equal to the invoking effective UID. It accepts
native Borg mounts too, including disconnected ones whose metadata remains
available. Final-component symlink aliases, missing/stacked mounts, missing UID,
custom filesystem identities, bind subtrees, and unrelated mounts are refused.
Parents are resolved without reading inside a disconnected mount.
No repository location or credentials are required by the unmount command.

These checks identify a same-user Borg mount, **not its creator or repository**.
Another process running as that user can imitate the identity or replace a mount
between checking and invoking Borg. This same-user TOCTOU trust boundary is not
perfect containment. Do not concurrently replace mountpoints or operate on
directories controlled by untrusted users.

There is no force/lazy unmount or automatic retry. Busy mounts fail; close their
readers before trying explicitly again. If metadata is ambiguous, permissions
prevent cleanup, or the helper is unavailable, inspect the matching Borg/FUSE
documentation with an administrator. Do not blindly unmount arbitrary paths.
Cairn verifies that no mount remains at the target after mount/unmount completes;
an unverifiable result is a failure, not reported success. It never removes a
remaining mount automatically.

## Verification limits and backend

Automated tests cover request construction, strict mount metadata checks,
refusal before invoking Borg, dependency failures, unmount postconditions, and
owned-process cancellation using inert fixtures. Supported Borg integration
tests explicitly disable FUSE and replace the unmount helper with an inert
failure fixture; they never mount or unmount a real filesystem.

A successful FUSE mount/read/unmount, native mounted-lock contention, and real
signal-driven filesystem cleanup still require testing on a separately prepared
Linux FUSE host. They were not verified in the development container, which has
no `/dev/fuse` or FUSE helpers. Mounting remains optional for the release.

`cli/internal/mount` prepares foreground requests through the existing Borg
repository/access and lock-wait helpers, runs them with `borg.Runner`, and
verifies unmount metadata. It does not change archive listing records, restore
selectors, shared process cleanup, configuration, workflows, or TUI APIs.
