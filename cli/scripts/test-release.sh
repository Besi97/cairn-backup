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
prefix="$tmp/prefix with % and dollar \$"
PATH="$tmp/mock-bin:$PATH" MOCK_RELEASE_DIR="$tmp/mock-release" \
  "$cli_dir/scripts/install.sh" --version "$version" --prefix "$prefix"
"$prefix/bin/cairn" --version | grep -F "$version"
[[ -x $prefix/share/cairn/setup-systemd.sh ]]
[[ -r $prefix/share/cairn/cairn-agent.service.in ]]
[[ -r $prefix/share/cairn/systemd.md ]]

# A corrupt release archive must be rejected before the binary is installed.
cp "$artifacts/cairn_${version}_linux_amd64.tar.gz" "$tmp/mock-release/"
printf 'corruption' >>"$tmp/mock-release/cairn_${version}_linux_amd64.tar.gz"
if PATH="$tmp/mock-bin:$PATH" MOCK_RELEASE_DIR="$tmp/mock-release" \
  "$cli_dir/scripts/install.sh" --version "$version" --prefix "$tmp/corrupt-prefix" \
  >"$tmp/corrupt.out" 2>&1; then
  echo "installer accepted an archive with a bad checksum" >&2
  exit 1
fi
[[ ! -e $tmp/corrupt-prefix/bin/cairn ]]

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

config="$tmp/config with % and dollar \$.yaml"
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
grep -F "XDG_STATE_HOME=$HOME/.local/state" "$unit"
grep -F 'RestartPreventExitStatus=3' "$unit"
grep -F 'KillMode=mixed' "$unit"
if "$prefix/share/cairn/setup-systemd.sh" --user "$user" --config "$config" \
  --binary "$prefix/bin/cairn" --print-unit >"$tmp/incomplete.out" 2>&1; then
  echo "systemd setup accepted missing enable/start choices" >&2
  exit 1
fi
echo "Release packaging, checksum installation, and systemd unit validation passed"
