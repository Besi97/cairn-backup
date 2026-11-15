#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: test-release.sh ARTIFACT-DIRECTORY VERSION" >&2
  exit 2
fi
artifacts=$(cd "$1" && pwd -P)
version=$2
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
cli_dir=$(cd "$script_dir/.." && pwd -P)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

(
  cd "$artifacts"
  sha256sum --check SHA256SUMS
)
for arch in amd64 arm64; do
  archive=$artifacts/cairn_${version}_linux_${arch}.tar.gz
  entries=$(tar -tzf "$archive")
  grep -Fqx './cairn' <<<"$entries"
  grep -Fqx './setup-systemd.sh' <<<"$entries"
  grep -Fqx './cairn-agent.service.in' <<<"$entries"
  grep -Fqx './docs/installation.md' <<<"$entries"
  grep -Fqx './docs/systemd.md' <<<"$entries"
done

mkdir "$tmp/mock-bin" "$tmp/mock-release"
cp "$artifacts/"* "$tmp/mock-release/"
cat >"$tmp/mock-bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
output=
for ((i=1; i<=$#; i++)); do
  if [[ ${!i} == --output ]]; then
    next=$((i + 1))
    output=${!next}
  fi
done
url=${!#}
[[ -n $output ]]
cp "$MOCK_RELEASE_DIR/${url##*/}" "$output"
MOCK
chmod 0755 "$tmp/mock-bin/curl"
prefix="$tmp/"'prefix with % and ${HOME} $HOME $$'
PATH="$tmp/mock-bin:$PATH" MOCK_RELEASE_DIR="$tmp/mock-release" \
  "$cli_dir/scripts/install.sh" --version "$version" --prefix "$prefix"
"$prefix/bin/cairn" --version | grep -F "$version"
[[ -x $prefix/share/cairn/setup-systemd.sh ]]
[[ -r $prefix/share/cairn/cairn-agent.service.in ]]
[[ -r $prefix/share/cairn/systemd.md ]]

# Exercise architecture selection independently of the test host. Checksum
# rejection happens before executing either binary, so no emulation is needed.
cat >"$tmp/mock-bin/uname" <<'MOCK'
#!/usr/bin/env bash
case $1 in
  -s) echo Linux ;;
  -m) echo "$MOCK_MACHINE" ;;
  *) exit 1 ;;
esac
MOCK
chmod 0755 "$tmp/mock-bin/uname"
for pair in 'amd64 x86_64' 'arm64 aarch64'; do
  read -r arch machine <<<"$pair"
  cp "$artifacts/cairn_${version}_linux_${arch}.tar.gz" "$tmp/mock-release/"
  printf 'corruption' >>"$tmp/mock-release/cairn_${version}_linux_${arch}.tar.gz"
  if PATH="$tmp/mock-bin:$PATH" MOCK_MACHINE=$machine MOCK_RELEASE_DIR="$tmp/mock-release" \
    "$cli_dir/scripts/install.sh" --version "$version" --prefix "$tmp/corrupt-prefix-$arch" \
    >"$tmp/corrupt-$arch.out" 2>&1; then
    echo "installer accepted a $arch archive with a bad checksum" >&2
    exit 1
  fi
  grep -Fq "checksum verification failed for cairn_${version}_linux_${arch}.tar.gz" "$tmp/corrupt-$arch.out"
  [[ ! -e $tmp/corrupt-prefix-$arch/bin/cairn ]]
done
rm "$tmp/mock-bin/uname"

mkdir -p "$tmp/symlink-prefix/bin"
printf 'keep this target\n' >"$tmp/keep"
ln -s "$tmp/keep" "$tmp/symlink-prefix/bin/cairn"
if PATH="$tmp/mock-bin:$PATH" MOCK_RELEASE_DIR="$artifacts" \
  "$cli_dir/scripts/install.sh" --version "$version" --prefix "$tmp/symlink-prefix" \
  >"$tmp/symlink.out" 2>&1; then
  echo "installer replaced a symlink target" >&2
  exit 1
fi
grep -Fxq 'keep this target' "$tmp/keep"

config="$tmp/"'config with % and ${HOME} $HOME $$.yaml'
cat >"$config" <<'YAML'
version: 1
repositories:
  local: {location: ./repo}
workflows:
  idle:
    cron: "0 0 30 2 *"
    stages: [{tasks: [{type: compact, repository: local}]}]
YAML
unit=$tmp/cairn-agent.service
user=$(id -un)
"$prefix/share/cairn/setup-systemd.sh" --user "$user" --config "$config" \
  --binary "$prefix/bin/cairn" --no-enable --no-start --print-unit >"$unit"
systemd-analyze verify "$unit"
grep -F "User=$user" "$unit"
grep -F "XDG_RUNTIME_DIR=" "$unit"
account_home=$(getent passwd "$user" | cut -d: -f6)
grep -F "XDG_STATE_HOME=$account_home/.local/state" "$unit"
grep -F 'ExecStart=:' "$unit"
grep -F '${HOME} $HOME $$' "$unit"
grep -F 'RestartPreventExitStatus=3' "$unit"
grep -F 'KillMode=mixed' "$unit"
if "$prefix/share/cairn/setup-systemd.sh" --user "$user" --config "$config" \
  --binary "$prefix/bin/cairn" --print-unit >"$tmp/incomplete.out" 2>&1; then
  echo "systemd setup accepted missing enable/start choices" >&2
  exit 1
fi

# Control characters must be rejected before rendering, even if the path names
# a real file. Check both a config and an executable path, and empty stdout.
for control in $'\n' $'\r' $'\t' $'\001'; do
  bad_config="$tmp/config${control}.yaml"
  cp "$config" "$bad_config"
  if "$prefix/share/cairn/setup-systemd.sh" --user "$user" --config "$bad_config" \
    --binary "$prefix/bin/cairn" --no-enable --no-start --print-unit \
    >"$tmp/bad-unit" 2>"$tmp/bad-error"; then
    echo "systemd setup accepted a control character in a config path" >&2
    exit 1
  fi
  [[ ! -s $tmp/bad-unit ]]
  grep -Fq 'unit values cannot contain control characters' "$tmp/bad-error"
  bad_binary="$tmp/cairn${control}"
  ln -s "$prefix/bin/cairn" "$bad_binary"
  if "$prefix/share/cairn/setup-systemd.sh" --user "$user" --config "$config" \
    --binary "$bad_binary" --no-enable --no-start --print-unit \
    >"$tmp/bad-unit" 2>"$tmp/bad-error"; then
    echo "systemd setup accepted a control character in a binary path" >&2
    exit 1
  fi
  [[ ! -s $tmp/bad-unit ]]
  grep -Fq 'unit values cannot contain control characters' "$tmp/bad-error"
done
echo "Release packaging, checksum installation, and systemd unit validation passed"
