#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
id="cairn-container-$$"
server="$id-server"
network="$id-net"
data="$id-data"
source="$id-source"
sshvol="$id-ssh"
cleanup() {
    docker rm -f "$server" "$id-agent" >/dev/null 2>&1 || :
    docker network rm "$network" >/dev/null 2>&1 || :
    docker volume rm "$data" "$source" "$sshvol" >/dev/null 2>&1 || :
    docker image rm "$id-server" "$id-regular" "$id-distroless" >/dev/null 2>&1 || :
}
trap cleanup EXIT HUP INT TERM
arch="${1:-}"
version=0.0.0-ci
if [ -n "$arch" ]; then
    set -- --platform "linux/$arch"
else
    set --
fi
docker build "$@" --build-arg "VERSION=$version" --target regular -t "$id-regular" .
docker build "$@" --build-arg "VERSION=$version" --target distroless -t "$id-distroless" .
docker build --build-arg "IMAGE=$id-regular" -f tests/container/server.Dockerfile -t "$id-server" .
docker network create "$network" >/dev/null
docker volume create "$data" >/dev/null
docker volume create "$source" >/dev/null
docker volume create "$sshvol" >/dev/null
docker run -d --name "$server" --network "$network" --network-alias server \
    -v "$data:/data" -v "$source:/source" -v "$sshvol:/data/ssh" "$id-server" >/dev/null
for i in $(seq 1 30); do
    if docker logs "$server" 2>&1 | grep -q 'Server listening'; then break; fi
    sleep 1
done
docker logs "$server" 2>&1 | grep -q 'Server listening'

for variant in regular distroless; do
    image="$id-$variant"
    run() {
        docker run --rm --network "$network" -e BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes -v "$data:/data" \
            -v "$source:/source:ro" -v "$sshvol:/home/cairn/.ssh:ro" "$@"
    }
    test "$(docker image inspect --format '{{.Config.User}}' "$image")" = 10001:10001
    if [ -n "$arch" ]; then
        test "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")" = "linux/$arch"
    fi
    test "$(docker run --rm --network none "$image" --version)" = "cairn $version"
    run --entrypoint borg "$image" --version
    run --entrypoint ssh "$image" -V
    run --entrypoint borg "$image" init --encryption=none "/data/$variant-local"
    run --entrypoint borg "$image" init --encryption=none "/data/$variant-remote"
    run "$image" backup --repository "/data/$variant-local" --archive-name local \
        --source-root /source .
    run "$image" backup --repository "ssh://cairn@server/data/$variant-remote" \
        --identity-file /home/cairn/.ssh/id_ed25519 --archive-name remote \
        --source-root /source .
    run --entrypoint borg "$image" list --short "/data/$variant-local" | grep -qx local
    run --entrypoint borg "$image" list --short "/data/$variant-remote" | grep -qx remote
    run --entrypoint borg "$image" init --encryption=none /data/agent-repo
    docker run -d --name "$id-agent" --network "$network" \
        -e BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes -v "$data:/data" -v "$source:/source:ro" \
        "$image" agent run --config /data/config.yaml >/dev/null
    found=
    for i in $(seq 1 90); do
        if run --entrypoint borg "$image" list --short /data/agent-repo | grep -q '^agent'; then
            found=1
            break
        fi
        sleep 1
    done
    if [ -z "$found" ]; then docker logs "$id-agent"; exit 1; fi
    docker stop -t 15 "$id-agent" >/dev/null
    test "$(docker inspect --format '{{.State.ExitCode}}' "$id-agent")" = 143
    docker rm "$id-agent" >/dev/null
    docker exec "$server" rm -rf /data/agent-repo
done
