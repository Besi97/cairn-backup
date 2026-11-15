#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: package-release.sh VERSION OUTPUT-DIRECTORY" >&2
  exit 2
fi

version=$1
out_dir=$2
if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]]; then
  echo "invalid version: $version" >&2
  exit 2
fi
mkdir -p "$out_dir"
out_dir=$(cd "$out_dir" && pwd -P)
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
cli_dir=$repo_root/cli
epoch=${SOURCE_DATE_EPOCH:-0}
if [[ ! $epoch =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a non-negative integer" >&2
  exit 2
fi

for arch in amd64 arm64; do
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT
  (
    cd "$cli_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
      -ldflags "-X main.version=$version" -o "$stage/cairn" ./cmd/cairn
  )
  install -m 0755 "$repo_root/cli/scripts/install.sh" "$stage/install.sh"
  install -m 0755 "$repo_root/cli/scripts/setup-systemd.sh" "$stage/setup-systemd.sh"
  install -m 0644 "$repo_root/deploy/systemd/cairn-agent.service.in" "$stage/cairn-agent.service.in"
  mkdir -p "$stage/docs"
  install -m 0644 "$repo_root/docs/installation.md" "$stage/docs/installation.md"
  install -m 0644 "$repo_root/docs/systemd.md" "$stage/docs/systemd.md"
  archive="cairn_${version}_linux_${arch}.tar.gz"
  tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
    -C "$stage" -cf - . | gzip -n >"$out_dir/$archive"
  rm -rf "$stage"
  trap - EXIT
done

(
  cd "$out_dir"
  sha256sum "cairn_${version}_linux_amd64.tar.gz" "cairn_${version}_linux_arm64.tar.gz" >SHA256SUMS
)
echo "Created release artifacts in $out_dir"
