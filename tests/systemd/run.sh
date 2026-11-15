#!/usr/bin/env bash
set -euo pipefail

# Use Docker's build context/cp transport, not host bind mounts: DOCKER_HOST may
# point at a remote daemon (including the development container's DinD sidecar).
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
make -C "$repo/cli" build VERSION=0.0.0-systemd-test
context=$(mktemp -d)
name="cairn-systemd-test-$$"
image="cairn-systemd-test:$$"
cleanup() {
  local status=$?
  trap - EXIT
  if ((status != 0)); then
    docker logs "$name" >&2 || true
    docker exec "$name" journalctl --no-pager -u cairn-agent.service >&2 || true
  fi
  docker rm --force "$name" >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf "$context"
  exit "$status"
}
trap cleanup EXIT
install -m 0755 "$repo/cli/bin/cairn" "$context/cairn"
install -m 0755 "$repo/cli/scripts/setup-systemd.sh" "$context/setup-systemd.sh"
install -m 0644 "$repo/deploy/systemd/cairn-agent.service.in" "$context/cairn-agent.service.in"
install -m 0755 "$repo/tests/systemd/smoke.sh" "$context/smoke.sh"
install -m 0644 "$repo/tests/systemd/Dockerfile" "$context/Dockerfile"
docker build --tag "$image" "$context"
docker run --detach --name "$name" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /run/lock "$image" >/dev/null
docker exec "$name" timeout 240s /opt/cairn-test/smoke.sh
docker restart --timeout 30 "$name" >/dev/null
docker exec "$name" timeout 45s /opt/cairn-test/smoke.sh --boot-check
