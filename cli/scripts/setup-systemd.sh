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
[[ $account =~ ^[a-z_][a-zA-Z0-9_.-]*\$?$ ]] || die "invalid account name"
[[ $config == /* && -f $config && -r $config ]] || die "--config must name a readable absolute file"
id "$account" >/dev/null 2>&1 || die "account does not exist: $account"
if [[ -z $binary ]]; then
  binary=$(command -v cairn || true)
fi
[[ $binary == /* && -x $binary && -f $binary ]] || die "provide --binary with an executable absolute path"
binary=$(readlink -f -- "$binary")
[[ -n $binary ]] || die "cannot resolve the Cairn binary"

passwd_entry=$(getent passwd "$account") || die "cannot look up account: $account"
IFS=: read -r _ _ uid _ _ home _ <<<"$passwd_entry"
[[ $uid =~ ^[0-9]+$ && $home == /* && -d $home ]] || die "account must have an existing absolute home directory"
state_home=$home/.local/state
command -v runuser >/dev/null 2>&1 || die "runuser is required"
command -v systemd-analyze >/dev/null 2>&1 || die "systemd-analyze is required"

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
elif ! runuser -u "$account" -- env HOME="$home" XDG_RUNTIME_DIR= XDG_STATE_HOME="$state_home" \
  "$binary" config check --config "$config" >&2; then
  die "configuration check failed for account $account"
fi

quote_unit() {
  local value=$1
  case $value in
    *[[:cntrl:]]*) die "unit values cannot contain control characters" ;;
  esac
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//%/%%}
  printf '"%s"' "$value"
}

quote_exec() {
  local value=$1
  case $value in
    *[[:cntrl:]]*) die "unit values cannot contain control characters" ;;
  esac
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//%/%%}
  value=${value//\$/\\x24}
  printf '"%s"' "$value"
}

sed_value() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}

unit_text=$(
  sed \
    -e "s|@CAIRN_USER@|$(sed_value "$account")|g" \
    -e "s|@CAIRN_HOME_ENV@|$(sed_value "$(quote_unit "HOME=$home")")|g" \
    -e "s|@CAIRN_STATE_HOME_ENV@|$(sed_value "$(quote_unit "XDG_STATE_HOME=$state_home")")|g" \
    -e "s|@CAIRN_BINARY@|$(sed_value "$(quote_exec "$binary")")|g" \
    -e "s|@CAIRN_CONFIG@|$(sed_value "$(quote_exec "$config")")|g" \
    "$template"
  printf '\001'
)
unit_text=${unit_text%$'\001'}
if [[ $print_unit == true ]]; then
  printf '%s' "$unit_text"
  exit 0
fi

[[ $(id -u) -eq 0 ]] || die "run this script as root to install a system service"
unit_dir=/etc/systemd/system
unit_file=$unit_dir/cairn-agent.service
[[ -d $unit_dir ]] || die "systemd unit directory not found: $unit_dir"
[[ ! -e $unit_file && ! -L $unit_file ]] || die "$unit_file already exists; stop and remove it explicitly before reinstalling"
command -v systemctl >/dev/null 2>&1 || die "systemctl is required"

temporary=$(mktemp "$unit_dir/.cairn-agent.service.XXXXXX")
trap 'rm -f "$temporary"' EXIT
printf '%s' "$unit_text" >"$temporary"
chmod 0644 "$temporary"
chown root:root "$temporary"
ln -- "$temporary" "$unit_file" || die "could not create $unit_file"
rm -f "$temporary"
trap - EXIT
systemctl daemon-reload
if [[ $enable_choice == yes ]]; then
  systemctl enable cairn-agent.service
fi
if [[ $start_choice == yes ]]; then
  systemctl start cairn-agent.service
fi
echo "Installed $unit_file"
[[ $enable_choice == yes ]] && echo "Enabled cairn-agent.service at boot"
[[ $start_choice == yes ]] && echo "Started cairn-agent.service"
