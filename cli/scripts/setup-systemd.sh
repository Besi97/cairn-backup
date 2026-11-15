#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Install the optional system-level Cairn agent service.

Usage:
  setup-systemd.sh --user ACCOUNT --config ABSOLUTE-PATH
    [--binary ABSOLUTE-PATH] [--enable|--no-enable] [--start|--no-start]
    [--print-unit]

ACCOUNT must already exist. Both enablement and immediate startup require an
explicit choice. --print-unit validates the inputs and prints the unit without
installing it or contacting systemd.
EOF
}

die() {
  echo "cairn systemd setup: $*" >&2
  exit 1
}

account=
config=
binary=
enable_choice=
start_choice=
print_unit=false
while (($#)); do
  case $1 in
    --user)
      (($# >= 2)) || { usage >&2; exit 2; }
      account=$2
      shift 2
      ;;
    --config)
      (($# >= 2)) || { usage >&2; exit 2; }
      config=$2
      shift 2
      ;;
    --binary)
      (($# >= 2)) || { usage >&2; exit 2; }
      binary=$2
      shift 2
      ;;
    --enable)
      [[ -z $enable_choice ]] || die "choose only one of --enable and --no-enable"
      enable_choice=yes
      shift
      ;;
    --no-enable)
      [[ -z $enable_choice ]] || die "choose only one of --enable and --no-enable"
      enable_choice=no
      shift
      ;;
    --start)
      [[ -z $start_choice ]] || die "choose only one of --start and --no-start"
      start_choice=yes
      shift
      ;;
    --no-start)
      [[ -z $start_choice ]] || die "choose only one of --start and --no-start"
      start_choice=no
      shift
      ;;
    --print-unit)
      print_unit=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

[[ -n $account && -n $config && -n $enable_choice && -n $start_choice ]] || {
  usage >&2
  exit 2
}
# Validate in the main shell, before command substitutions or any side effects.
# A failure buried in a rendering substitution need not propagate to its caller.
validate_value() {
  case $1 in
    *[[:cntrl:]]*) die "unit values cannot contain control characters" ;;
  esac
}
validate_value "$config"
validate_value "$binary"
[[ $account =~ ^[a-z_][a-zA-Z0-9_.-]*\$?$ ]] || die "invalid account name"
[[ $config == /* && -f $config && -r $config ]] || die "--config must name a readable absolute file"
id "$account" >/dev/null 2>&1 || die "account does not exist: $account"
if [[ -z $binary ]]; then
  binary=$(command -v cairn || true)
fi
[[ $binary == /* && -x $binary && -f $binary ]] || die "provide --binary with an executable absolute path"
binary=$(readlink -f -- "$binary")
[[ -n $binary ]] || die "cannot resolve the Cairn binary"
validate_value "$binary"

passwd_entry=$(getent passwd "$account") || die "cannot look up account: $account"
IFS=: read -r _ _ uid _ _ home _ <<<"$passwd_entry"
[[ $uid =~ ^[0-9]+$ && $home == /* && -d $home ]] || die "account must have an existing absolute home directory"
state_home=$home/.local/state
validate_value "$home"

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
template=$script_dir/cairn-agent.service.in
if [[ ! -f $template ]]; then
  template=$(cd "$script_dir/../../deploy/systemd" && pwd -P)/cairn-agent.service.in
fi
[[ -r $template ]] || die "cannot find cairn-agent.service.in"

# Validate with the same stable state-lock environment the service will use.
if [[ $(id -u) == "$uid" ]]; then
  if ! env HOME="$home" XDG_RUNTIME_DIR= XDG_STATE_HOME="$state_home" \
    "$binary" config check --config "$config" >&2; then
    die "configuration check failed for account $account"
  fi
else
  command -v runuser >/dev/null 2>&1 || die "runuser is required"
  if ! runuser -u "$account" -- env HOME="$home" XDG_RUNTIME_DIR= XDG_STATE_HOME="$state_home" \
    "$binary" config check --config "$config" >&2; then
    die "configuration check failed for account $account"
  fi
fi

quote_unit() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//%/%%}
  printf '"%s"' "$value"
}

# Each assignment propagates failure. Render exact template lines instead of
# sed replacement expressions, so inserted values cannot become placeholders.
home_env=$(quote_unit "HOME=$home") || die "cannot quote home"
state_env=$(quote_unit "XDG_STATE_HOME=$state_home") || die "cannot quote state directory"
binary_arg=$(quote_unit "$binary") || die "cannot quote binary"
config_arg=$(quote_unit "$config") || die "cannot quote config"
render_unit() {
  local line
  while IFS= read -r line || [[ -n $line ]]; do
    case $line in
      'User=@CAIRN_USER@') printf 'User=%s\n' "$account" || return ;;
      'Environment=@CAIRN_HOME_ENV@') printf 'Environment=%s\n' "$home_env" || return ;;
      'Environment=@CAIRN_STATE_HOME_ENV@') printf 'Environment=%s\n' "$state_env" || return ;;
      'ExecStart=:@CAIRN_BINARY@ agent run --config @CAIRN_CONFIG@')
        # ":" disables systemd's environment expansion, including ${HOME}.
        printf 'ExecStart=:%s agent run --config %s\n' "$binary_arg" "$config_arg" || return
        ;;
      *) printf '%s\n' "$line" || return ;;
    esac
  done <"$template"
}
unit_text=$(render_unit) || die "cannot render service unit"
if [[ $print_unit == true ]]; then
  printf '%s\n' "$unit_text"
  exit 0
fi

[[ $(id -u) -eq 0 ]] || die "run this script as root to install a system service"
unit_dir=/etc/systemd/system
unit_file=$unit_dir/cairn-agent.service
[[ -d $unit_dir ]] || die "systemd unit directory not found: $unit_dir"
[[ ! -e $unit_file && ! -L $unit_file ]] || die "$unit_file already exists; stop and remove it explicitly before reinstalling"
command -v systemctl >/dev/null 2>&1 || die "systemctl is required"
command -v systemd-analyze >/dev/null 2>&1 || die "systemd-analyze is required"

staging=$(mktemp -d "$unit_dir/.cairn-setup.XXXXXX")
trap 'rm -rf "$staging"' EXIT
temporary=$staging/cairn-agent.service
printf '%s\n' "$unit_text" >"$temporary"
chmod 0644 "$temporary"
chown root:root "$temporary"
systemd-analyze verify "$temporary" || die "generated service unit failed verification"
ln -- "$temporary" "$unit_file" || die "could not create $unit_file"
rm -rf "$staging"
trap - EXIT
systemctl daemon-reload
if [[ $enable_choice == yes ]]; then
  systemctl enable cairn-agent.service
fi
if [[ $start_choice == yes ]]; then
  systemctl start cairn-agent.service
fi
echo "Installed $unit_file"
if [[ $enable_choice == yes ]]; then
  echo "Enabled cairn-agent.service at boot"
fi
if [[ $start_choice == yes ]]; then
  echo "Started cairn-agent.service"
fi
exit 0
