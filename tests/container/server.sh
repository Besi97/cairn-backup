set -eu
mkdir -p /data/ssh /home/cairn/.ssh
echo container-smoke > /source/marker
cat > /data/config.yaml <<'EOF'
version: 1
repositories:
  local:
    location: /data/agent-repo
workflows:
  scheduled:
    cron: "* * * * *"
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_prefix: agent
            source_root: /source
            sources: ["."]
EOF
ssh-keygen -q -t ed25519 -N '' -f /data/ssh/id_ed25519
ssh-keygen -q -t ed25519 -N '' -f /data/ssh/host_key
cp /data/ssh/id_ed25519.pub /home/cairn/.ssh/authorized_keys
printf 'server %s\n' "$(cat /data/ssh/host_key.pub)" > /data/ssh/known_hosts
chown -R 10001:10001 /data /source /home/cairn/.ssh
chmod 700 /home/cairn/.ssh /data/ssh
chmod 600 /home/cairn/.ssh/authorized_keys
passwd -d cairn >/dev/null
cat > /tmp/sshd_config <<'EOF'
Port 22
HostKey /data/ssh/host_key
PidFile /run/sshd.pid
AuthorizedKeysFile .ssh/authorized_keys
PasswordAuthentication no
PermitRootLogin no
UsePAM no
StrictModes yes
EOF
exec /usr/sbin/sshd -D -e -f /tmp/sshd_config
