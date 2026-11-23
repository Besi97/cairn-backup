ARG IMAGE
FROM ${IMAGE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd
COPY tests/container/server.sh /server.sh
ENTRYPOINT ["/bin/sh", "/server.sh"]
