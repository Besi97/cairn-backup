# Recovery without Cairn

Cairn stores ordinary Borg 1.x archives, not a Cairn-specific backup format.
Recovery needs Borg and access to the repository, encryption key/passphrase,
and SSH credentials where applicable. It does not need Cairn, its configuration,
or a running agent. Use a supported Borg 1.x release (at least 1.2.8; 1.4.5 is
the primary target), not Borg 2.

## Prepare repository access

Run recovery as an account that can access the repository and credentials.
Record the repository location and keep recovery credentials separately from
the machine being backed up. For encrypted repositories, keep a separate Borg
key export as well; see `borg key export --help`. A lost encryption key or
passphrase cannot be recovered by Cairn.

The examples below use a subshell so access settings do not remain in your
current shell. Replace all example paths and archive names. For a local
repository:

```sh
(
  REPOSITORY=/backups/repo
  unset BORG_PASSPHRASE BORG_PASSCOMMAND BORG_PASSPHRASE_FD
  # Omit this for an unencrypted repository. Borg prompts if a secret is needed.
  export BORG_PASSCOMMAND='cat /absolute/path/to/passphrase'
  borg list "$REPOSITORY"
)
```

Use an absolute passphrase-file path because extraction below changes directory.
Protect that file with mode `0600`; do not paste real passphrases into shell
commands or history. Native Borg does not read Cairn's `passphrase_file`,
`passphrase`, or `identity_file` settings. Unlike Cairn, native Borg may prompt
interactively when no credential is supplied.

For SSH, use the same native repository location and configure OpenSSH access:

```sh
(
  REPOSITORY=ssh://backup@backup-host/./repo
  unset BORG_PASSPHRASE BORG_PASSCOMMAND BORG_PASSPHRASE_FD
  export BORG_PASSCOMMAND='cat /absolute/path/to/passphrase'
  export BORG_RSH='ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -i /absolute/path/to/identity'
  borg list "$REPOSITORY"
)
```

OpenSSH and compatible Borg 1.x on the remote host are required. Verify the
host key through a trusted channel and populate the recovery account's
`known_hosts` before using strict checking. An encrypted SSH identity must
already be usable non-interactively, for example through `ssh-agent`. Omit
`-i` when using the account's normal SSH configuration or agent.

## Inspect and extract

Use the access setup above inside the same subshell as these commands. Choose
the exact archive name from `borg list`, not a Cairn archive-prefix selector:

```sh
(
  REPOSITORY=/backups/repo
  ARCHIVE=documents-20261115T020000.000000000Z
  unset BORG_PASSPHRASE BORG_PASSCOMMAND BORG_PASSPHRASE_FD
  export BORG_PASSCOMMAND='cat /absolute/path/to/passphrase'
  # For SSH, set REPOSITORY and BORG_RSH as shown above.

  borg list "$REPOSITORY"
  borg info "$REPOSITORY::$ARCHIVE"
  borg list "$REPOSITORY::$ARCHIVE"

  # Create a new empty directory; never extract over the live source tree.
  RESTORE_DIR=$(mktemp -d "$HOME/cairn-restore.XXXXXX") || exit 1
  cd "$RESTORE_DIR" || exit 1
  printf 'Recovery directory: %s\n' "$RESTORE_DIR"
  borg extract --dry-run --list "$REPOSITORY::$ARCHIVE" || exit 1
  borg extract --list "$REPOSITORY::$ARCHIVE"
)
```

Borg extracts relative to the current directory. Paths are the paths shown in
the archive listing, not necessarily the original absolute source paths.
To extract only one subtree, append its listed path, for example
`borg extract "$REPOSITORY::$ARCHIVE" Documents`.

The dry run does not restore files and is not a substitute for a real restore
test. Inspect the extracted files and compare important contents before copying
anything back. Restoring ownership and some metadata may require privileges;
use those deliberately, not by default. Recover only from repositories you trust.

## If recovery fails

- **Authentication/decryption failure:** check the repository, passphrase,
  encryption key, SSH identity, and executing account. Cairn config checking
  does not verify any of these. Do not initialize a new repository over an old one.
- **Unknown/changed SSH host key:** verify the host identity out of band; do not
  disable host-key checking to bypass the error.
- **Repository locked:** stop or wait for active backup/maintenance work, then
  retry explicitly. Native Borg remains responsible for its locks. Do not delete
  lock files or use `borg break-lock` while any client may still be active.
- **Missing files:** inspect the archive listing and try another archive.
  Excluded or unsuccessfully backed-up files cannot be restored from that archive.
- **Damaged repository:** preserve a copy and consult the matching Borg 1.x
  documentation before repair. Do not use destructive repair as a first step.

Practice a restore before an emergency, including recovery from a different
machine/account. A successful backup exit or an active agent service alone does
not prove that the data and credentials needed for recovery are available.
