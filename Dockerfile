FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY cli/go.mod cli/go.sum ./
RUN go mod download
COPY cli/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=${VERSION}" -o /out/cairn ./cmd/cairn

FROM debian:trixie-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends borgbackup openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && borg --version | grep -Eq '^borg 1\.(2\.(8|9|[1-9][0-9]+)|[3-9]\.[0-9]+)([^0-9]|$)' \
    && useradd --uid 10001 --create-home cairn
ENV HOME=/home/cairn
COPY --from=build /out/cairn /usr/local/bin/cairn
USER 10001:10001
WORKDIR /home/cairn
ENTRYPOINT ["/usr/local/bin/cairn"]
CMD ["agent", "run"]

FROM gcr.io/distroless/base-debian13:nonroot AS distroless
COPY --from=runtime /usr/lib/ /usr/lib/
COPY --from=runtime /usr/bin/python3 /usr/bin/python3
COPY --from=runtime /usr/bin/python3.13 /usr/bin/python3.13
COPY --from=runtime /usr/bin/borg /usr/bin/borg
COPY --from=runtime /usr/bin/ssh /usr/bin/ssh
COPY --from=runtime /etc/ssh/ssh_config /etc/ssh/ssh_config
COPY --from=runtime /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime /etc/passwd /etc/passwd
COPY --from=runtime /etc/group /etc/group
COPY --from=runtime --chown=10001:10001 /home/cairn /home/cairn
COPY --from=build /out/cairn /usr/local/bin/cairn
ENV HOME=/home/cairn
USER 10001:10001
WORKDIR /home/cairn
ENTRYPOINT ["/usr/local/bin/cairn"]
CMD ["agent", "run"]

FROM runtime AS regular
