#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: build-release.sh VERSION IMAGE" >&2
  exit 2
fi
version=$1
image=$2
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]] || (( ${#version} > 116 )); then
  echo "Invalid release version: $version" >&2
  exit 1
fi

for variant in regular distroless; do
  tag="v$version"
  [[ $variant == regular ]] || tag+="-distroless"
  docker build --platform linux/amd64 --target "$variant" \
    --build-arg "VERSION=$version" -t "$image:$tag" .
done

for tag in "v$version" "v$version-distroless"; do
  actual=$(docker run --rm --network none "$image:$tag" --version)
  if [[ $actual != "cairn $version" ]]; then
    echo "Unexpected version in $image:$tag: $actual" >&2
    exit 1
  fi
done
