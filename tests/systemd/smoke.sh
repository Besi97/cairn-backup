#!/usr/bin/env bash
set -euo pipefail

# This harness changes system units and creates accounts' test data. It must
# never be run directly on an operator's workstation or production host.
[[ $(id -u) == 0 && -f /etc/cairn-systemd-test-image ]] || {
  echo "Run tests/systemd/run.sh; smoke.sh requires the disposable test image" >&2
  exit 1
}
[[ $(cat /proc/1/comm) == systemd ]]
setup=/usr/local/share/cairn/setup-systemd.sh
unit=cairn-agent.service
home=/home/cairn-test
base=$home/smoke
binary='/opt/cairn-test/cairn with % and ${HOME} $HOME $$'
config="$base/"'config with % and ${HOME} $HOME $$ \ " @CAIRN_BINARY@.yaml'
if [[ ${1:-} == --boot-check ]]; then
  # Invoked after restarting the entire disposable container, not the unit.
  for ((attempt=0; attempt<300; attempt++)); do
    if ! invocation=$(systemctl show "$unit" --property=InvocationID --value 2>/dev/null); then
      sleep 0.1
      continue
    fi
    if [[ -n $invocation ]] && systemctl is-active --quiet "$unit" &&
      journalctl --no-pager "_SYSTEMD_INVOCATION_ID=$invocation" -o cat |
        grep -F 'agent: started;' >/dev/null; then
      pid=$(systemctl show "$unit" --property=MainPID --value)
      mapfile -d '' -t argv <"/proc/$pid/cmdline"
      [[ ${argv[0]} == "$binary" && ${argv[4]} == "$config" ]]
      echo "Enabled systemd service started at container boot with literal paths"
      exit 0
    fi
    sleep 0.1
  done
  echo "Enabled agent did not start at boot" >&2
  exit 1
fi
install -m 0755 /usr/local/bin/cairn "$binary"
install -d -o cairn-test -g cairn-test -m 0700 "$base"
cat >"$config" <<'YAML'
version: 1
repositories:
  local: {location: ./repo}
workflows:
  scheduled:
    cron: "0 0 30 2 *"
    timezone: UTC
    stages:
      - tasks: [{type: compact, repository: local, lock_wait: 600}]
      - tasks: [{type: backup, repository: local, archive_prefix: smoke, sources: [payload]}]
YAML
chown cairn-test:cairn-test "$config"
chmod 0600 "$config"
cp "$config" "$base/idle-config"

as_account() {
  runuser -u cairn-test -- env HOME="$home" XDG_RUNTIME_DIR= \
    XDG_STATE_HOME="$home/.local/state" BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes "$@"
}
wait_for() {
  local description=$1
  shift
  local deadline=$((SECONDS + 80))
  until "$@"; do
    if ((SECONDS >= deadline)); then
      echo "Timed out: $description" >&2
      return 1
    fi
    sleep 0.1
  done
}
journal_has() {
  journalctl --no-pager -u "$unit" -o cat | grep -F -- "$1" >/dev/null
}
running() {
  local invocation
  invocation=$(systemctl show "$unit" --property=InvocationID --value)
  [[ -n $invocation ]] && systemctl is-active --quiet "$unit" &&
    journalctl --no-pager "_SYSTEMD_INVOCATION_ID=$invocation" -o cat |
      grep -F 'agent: started;' >/dev/null
}
inactive() {
  [[ $(systemctl show "$unit" --property=ActiveState --value) == inactive ]]
}
remove_unit() {
  systemctl disable --now "$unit"
  rm "/etc/systemd/system/$unit"
  systemctl daemon-reload
  systemctl reset-failed
}
install_unit() {
  "$setup" --user cairn-test --config "$config" --binary "$binary" "$@"
}
trap 'systemctl stop "$unit" cairn-repo-holder.service 2>/dev/null || true' EXIT
manager_ready() {
  systemctl list-units --no-pager >/dev/null 2>&1
}
wait_for 'system manager startup' manager_ready

# Exercise the actual root installation path, including the --no-start exit
# status, all four enable/start choices, and refusal to overwrite an old unit.
for enable in --no-enable --enable; do
  for start in --no-start --start; do
    install_unit "$enable" "$start"
    [[ $(stat -c '%U:%G:%a' "/etc/systemd/system/$unit") == root:root:644 ]]
    if [[ $enable == --enable ]]; then
      systemctl is-enabled --quiet "$unit"
    else
      [[ $(systemctl is-enabled "$unit" || true) == disabled ]]
    fi
    if [[ $start == --start ]]; then
      wait_for 'agent startup' running
      # Inspect the real argv, not systemctl's re-escaped rendering.
      pid=$(systemctl show "$unit" --property=MainPID --value)
      mapfile -d '' -t argv <"/proc/$pid/cmdline"
      [[ ${argv[0]} == "$binary" && ${argv[4]} == "$config" ]]
      [[ $(stat -c %u "/proc/$pid") == "$(id -u cairn-test)" ]]
    else
      inactive
    fi
    before=$(sha256sum "/etc/systemd/system/$unit")
    if install_unit --no-enable --no-start >"$base/duplicate.log" 2>&1; then
      echo "Setup overwrote an existing unit" >&2
      exit 1
    fi
    [[ $(sha256sum "/etc/systemd/system/$unit") == "$before" ]]
    remove_unit
  done
done

# Validate with the selected account rather than root's broader file access.
chmod 000 "$config"
if install_unit --no-enable --no-start >"$base/unreadable.log" 2>&1; then
  echo "Setup accepted a config unreadable by the service account" >&2
  exit 1
fi
[[ ! -e /etc/systemd/system/$unit ]]
chmod 0600 "$config"

# Even a failed template verification must leave neither a unit nor staging
# files. Exercise this before normal scheduling starts.
template=/usr/local/share/cairn/cairn-agent.service.in
cp "$template" "$base/original-template"
printf '\n[Service]\nExecStart=/usr/bin/true\n' >>"$template"
if install_unit --no-enable --no-start >"$base/bad-template.log" 2>&1; then
  echo "Setup installed a malformed service template" >&2
  exit 1
fi
[[ ! -e /etc/systemd/system/$unit ]]
[[ -z $(find /etc/systemd/system -maxdepth 1 -name '.cairn-setup.*' -print) ]]
cp "$base/original-template" "$template"

# Rejected config and binary values must never leave a partially rendered unit.
for control in $'\n' $'\r' $'\t'; do
  bad="$base/config${control}.yaml"
  cp "$config" "$bad"
  chown cairn-test:cairn-test "$bad"
  if "$setup" --user cairn-test --config "$bad" --binary "$binary" \
    --no-enable --no-start >"$base/bad.log" 2>&1; then
    echo "Setup accepted a control-character config" >&2
    exit 1
  fi
  [[ ! -e /etc/systemd/system/$unit ]]
done

# Hold Borg's native repository lock outside the agent's cgroup. A scheduled
# compact will wait there, making overlap and cancellation deterministic.
as_account borg init --encryption=none "$base/repo"
printf 'systemd backup payload\n' >"$base/payload"
chown cairn-test:cairn-test "$base/payload"
systemd-run --unit=cairn-repo-holder --property=User=cairn-test \
  --setenv="HOME=$home" \
  borg with-lock "$base/repo" /bin/sh -c 'touch "$1"; exec sleep infinity' sh "$base/holder-ready"
wait_for 'native repository lock' test -f "$base/holder-ready"
sed -i 's/cron: "0 0 30 2 \*"/cron: "* * * * *"/' "$config"
install_unit --enable --start
wait_for 'scheduled Borg lock wait' journal_has 'reclaiming'
# The task label may precede exec: wait for Borg's lock waiter marker too.
borg_waiting() {
  compgen -G "$base/repo/lock.exclusive.*.tmp" >/dev/null
}
wait_for 'Borg waiting on the native lock' borg_waiting
pid=$(systemctl show "$unit" --property=MainPID --value)
mapfile -t borg_pids < <(pgrep -P "$pid")
((${#borg_pids[@]} > 0))
set +e
as_account "$binary" workflow run --config "$config" scheduled >"$base/manual-busy.log" 2>&1
code=$?
set -e
[[ $code == 2 ]]
grep -Fq 'already running' "$base/manual-busy.log"
lock_snapshot=$(find "$home/.local/state/cairn/locks" -type f -printf '%i %f\n' | sort)
systemctl stop "$unit"
inactive
[[ $(systemctl show "$unit" --property=Result --value) == success ]]
[[ $(systemctl show "$unit" --property=ExecMainStatus --value) == 143 ]]
journal_has 'interrupted'
journal_has 'agent: stopped'
for borg_pid in "${borg_pids[@]}"; do
  [[ ! -e /proc/$borg_pid ]]
done
systemctl is-active --quiet cairn-repo-holder.service
[[ $(find "$home/.local/state/cairn/locks" -type f -printf '%i %f\n' | sort) == "$lock_snapshot" ]]
systemctl stop cairn-repo-holder.service

# The released service lock permits an immediate manual run. No need to wait
# for another scheduled minute; the real backup verifies useful work as well.
as_account "$binary" workflow run --config "$config" scheduled
archive=$(as_account borg list --short "$base/repo")
[[ $archive == smoke-* ]]
as_account borg extract --stdout "$base/repo::$archive" payload >"$base/extracted"
cmp "$base/payload" "$base/extracted"

# Stop/restart uses a fresh snapshot, keeps the unit enabled, and releases its
# agent lock. SIGKILL triggers a supervised restart, not a permanent outage.
sed -i 's/cron: "\* \* \* \* \*"/cron: "0 0 30 2 *"/' "$config"
systemctl start "$unit"
wait_for 'restarted agent' running
old_pid=$(systemctl show "$unit" --property=MainPID --value)
systemctl restart "$unit"
wait_for 'explicit restart' running
[[ $(systemctl show "$unit" --property=MainPID --value) != "$old_pid" ]]
old_pid=$(systemctl show "$unit" --property=MainPID --value)
systemctl reset-failed "$unit"
systemctl kill --kill-whom=main --signal=SIGKILL "$unit"
new_process() {
  local new_pid
  new_pid=$(systemctl show "$unit" --property=MainPID --value)
  [[ $new_pid != 0 && $new_pid != "$old_pid" ]] && systemctl is-active --quiet "$unit"
}
wait_for 'automatic restart after SIGKILL' new_process
systemctl stop "$unit"

# Invalid configuration must fail once, without a restart loop.
printf 'invalid configuration\n' >"$config"
systemctl reset-failed "$unit"
systemctl start "$unit" || true
failed_config() {
  [[ $(systemctl show "$unit" --property=ExecMainStatus --value) == 3 ]] &&
    [[ $(systemctl show "$unit" --property=ActiveState --value) == failed ]]
}
wait_for 'invalid configuration exit' failed_config
[[ $(systemctl show "$unit" --property=NRestarts --value) == 0 ]]
journal_has 'Error:'
cp "$base/idle-config" "$config"
systemctl reset-failed "$unit"
echo "Live systemd installation, argv, journal, overlap, Borg cancellation, and restart tests passed"
