#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Install a versioned Cairn release for Linux amd64 or arm64.

Usage: install.sh --version VERSION [--prefix ABSOLUTE-PATH]

The matching release archive and SHA256SUMS are downloaded from GitHub Releases.
The default prefix is /usr/local. This installs the CLI and optional systemd
setup files; it does not install Borg, configure backups, or enable a service.
EOF
}

version=
prefix=/usr/local
while (($#)); do
  case $1 in
    --version)
      (($# >= 2)) || { usage >&2; exit 2; }
      version=$2
      shift 2
      ;;
    --prefix)
      (($# >= 2)) || { usage >&2; exit 2; }
      prefix=$2
      shift 2
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

if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]]; then
  echo "--version must be a release version such as 0.1.0" >&2
  exit 2
fi
[[ $(uname -s) == Linux ]] || {
  echo "Cairn release binaries are currently available for Linux only" >&2
  exit 1
}
if [[ $prefix != /* ]]; then
  echo "--prefix must be an absolute path" >&2
  exit 2
fi

case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *)
    echo "unsupported Linux architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

for tool in curl sha256sum tar install; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "required command not found: $tool" >&2
    exit 1
  }
done

asset="cairn_${version}_linux_${arch}.tar.gz"
base="https://github.com/Besi97/cairn-backup/releases/download/v${version}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl --fail --location --silent --show-error --output "$tmp/$asset" "$base/$asset"
curl --fail --location --silent --show-error --output "$tmp/SHA256SUMS" "$base/SHA256SUMS"

hash=$(awk -v expected="$asset" '
  $2 == expected { if (found++) exit 1; print $1 }
  END { if (found != 1) exit 1 }
' "$tmp/SHA256SUMS") || {
  echo "release checksum file has no unique entry for $asset" >&2
  exit 1
}
if [[ ! $hash =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "release checksum is malformed" >&2
  exit 1
fi
printf '%s  %s\n' "$hash" "$tmp/$asset" | sha256sum --check --status || {
  echo "checksum verification failed for $asset" >&2
  exit 1
}

tar -xzf "$tmp/$asset" -C "$tmp" \
  ./cairn ./setup-systemd.sh ./cairn-agent.service.in \
  ./docs/installation.md ./docs/systemd.md
[[ -f $tmp/cairn && ! -L $tmp/cairn && -x $tmp/cairn ]] || {
  echo "release archive does not contain an executable cairn binary" >&2
  exit 1
}

bin_dir=$prefix/bin
share_dir=$prefix/share/cairn
mkdir -p "$bin_dir" "$share_dir"
[[ -d $bin_dir && ! -L $bin_dir && -d $share_dir && ! -L $share_dir ]] || {
  echo "refusing symlink or non-directory install destination" >&2
  exit 1
}
for target in "$bin_dir/cairn" "$share_dir/setup-systemd.sh" \
  "$share_dir/cairn-agent.service.in" "$share_dir/installation.md" "$share_dir/systemd.md"; do
  if [[ -e $target || -L $target ]]; then
    [[ -f $target && ! -L $target ]] || {
      echo "refusing to replace non-regular or symlink target: $target" >&2
      exit 1
    }
  fi
done
install -m 0755 "$tmp/cairn" "$bin_dir/cairn"
install -m 0755 "$tmp/setup-systemd.sh" "$share_dir/setup-systemd.sh"
install -m 0644 "$tmp/cairn-agent.service.in" "$share_dir/cairn-agent.service.in"
install -m 0644 "$tmp/docs/installation.md" "$share_dir/installation.md"
install -m 0644 "$tmp/docs/systemd.md" "$share_dir/systemd.md"
echo "Installed Cairn $version to $bin_dir/cairn"
echo "Optional systemd setup: $share_dir/setup-systemd.sh"
