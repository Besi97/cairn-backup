# Optional systemd service

`cairn agent run` is a foreground process. The optional system-level unit lets
systemd supervise it and send its output to the journal. Installing Cairn alone
does not install, enable, or start a service.

## Prepare the account and configuration

Choose an existing account that can read the configuration, passphrase files,
and SSH identity, and access every source and repository. Root is supported but
must be selected explicitly. Cairn does not create a service account or adjust
your files' ownership and permissions. Keep configuration and credential files
readable only by the accounts that need them; for example, use mode `0600` for
private passphrase files and grant the service account read access explicitly.

Use absolute configuration paths. Check that the selected account can read the
file and any referenced credentials. The setup script runs `cairn config check`
as that account; this validates the configuration without running Borg.

## Install, enable, and start

The setup requires explicit choices for both enablement at boot and immediate
startup. For example:

```sh
sudo /usr/local/share/cairn/setup-systemd.sh \
  --user backup \
  --config /etc/cairn/config.yaml \
  --binary /usr/local/bin/cairn \
  --enable --start
```

Use `--no-enable` to leave the unit disabled or `--no-start` to leave it stopped;
one flag from each pair is required. The setup refuses an existing unit rather
than silently replacing it. `--print-unit` validates the choices and prints the
generated unit without installing it or contacting systemd.
Installation also runs `systemd-analyze verify` before creating the unit.

The unit runs Cairn directly as the selected account, works from `/`, and uses
`/usr/local/bin/cairn` unless `--binary` is provided. It restarts unexpected
runtime failures with a delay and rate limit, but not invalid configuration
(exit 3). SIGINT/SIGTERM-coded exits are treated as orderly shutdown. Stop
requests allow up to five minutes for Cairn and Borg cleanup; after the timeout,
systemd terminates the service's remaining processes.

The `ExecStart` command disables systemd environment substitution and quotes
literal paths, so names containing dollar signs or percent signs are preserved.
Paths containing control characters (including tabs and newlines) are rejected
before rendering or installing anything.

## Shared workflow locks

System services do not inherit a login session's `XDG_RUNTIME_DIR`. To ensure
the service and manual Cairn commands coordinate on the same locks, the unit
uses the selected account's `~/.local/state` for state and clears
`XDG_RUNTIME_DIR`. Run manual workflows as that same account with the same
environment and the exact same configuration path:

```sh
sudo -u backup env \
  HOME=/home/backup \
  XDG_RUNTIME_DIR= \
  XDG_STATE_HOME=/home/backup/.local/state \
  /usr/local/bin/cairn workflow run --config /etc/cairn/config.yaml daily
```

Replace the home, binary, config, and workflow values as appropriate. A manual
invocation from a normal login shell may use a different runtime lock directory;
do not use it to coordinate with this system service. Lock files are advisory
kernel locks and must not be deleted or have their permissions changed while
Cairn or Borg is active.

## Operate and troubleshoot

```sh
systemctl status cairn-agent.service
journalctl -u cairn-agent.service
journalctl -u cairn-agent.service --since today
sudo systemctl restart cairn-agent.service
sudo systemctl stop cairn-agent.service
```

Agent lifecycle and workflow summaries are written to stderr; task outcomes are
written to stdout. systemd sends both to the journal. Cairn does not persist run
history, and journal retention is controlled by the host. An active service only
means the agent process is running; inspect the journal to determine whether
backups are succeeding.

The agent loads configuration once at startup. Restart it to apply changes.
After editing the service account, config path, or unit template, stop and
disable the old unit, remove `/etc/systemd/system/cairn-agent.service`, run
`systemctl daemon-reload`, then run the setup script again with explicit choices.

To uninstall, stop and disable the service, remove the unit, reload systemd, and
then remove Cairn's binary and support files. Do not remove the backup
repositories, source data, credentials, or lock directory as part of uninstall.

```sh
sudo systemctl disable --now cairn-agent.service
sudo rm /etc/systemd/system/cairn-agent.service
sudo systemctl daemon-reload
sudo rm /usr/local/bin/cairn
sudo rm -r /usr/local/share/cairn
```
