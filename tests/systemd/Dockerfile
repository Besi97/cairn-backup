FROM debian:trixie-slim

ENV container=docker
RUN apt-get update && apt-get install --no-install-recommends -y \
      systemd systemd-sysv dbus borgbackup procps ca-certificates \
    && apt-get clean && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home cairn-test \
    && touch /etc/cairn-systemd-test-image
COPY cairn /usr/local/bin/cairn
COPY setup-systemd.sh cairn-agent.service.in /usr/local/share/cairn/
COPY smoke.sh /opt/cairn-test/smoke.sh
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
