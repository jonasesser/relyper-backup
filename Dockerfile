# syntax=docker/dockerfile:1.7
#
# Relyper Backup – single production image.
#
# Builds the web UI and the Go server from source and starts everything the
# product needs in one container: the Relyper Backup server (web UI, API,
# scheduler) with restic, rclone and the OpenSSH client for SFTP
# repositories.
#
#   docker build -t relyper-backup .
#   docker compose up -d          # see docker-compose.yml
#
# See DEPLOYMENT.md for mounts, environment variables and security notes.
# The image runs with a read-only root filesystem (only /config, /data,
# /cache, /restore and a tmpfs /tmp are written) and the capabilities listed
# in docker-compose.yml.

ARG GO_VERSION=1.26
ARG NODE_VERSION=22
ARG ALPINE_VERSION=3.24

# --- Stage 1: web UI ---------------------------------------------------------
FROM node:${NODE_VERSION}-alpine AS webui
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
WORKDIR /src/webui
COPY webui/package.json webui/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY webui/ ./
ARG RELYPER_VERSION=dev
ENV BACKREST_BUILD_VERSION=${RELYPER_VERSION}
RUN pnpm run build

# --- Stage 2: Go server and restic ------------------------------------------
FROM golang:${GO_VERSION}-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui /src/webui/dist ./webui/dist
ARG RELYPER_VERSION=dev
ARG RELYPER_COMMIT=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w -X main.version=${RELYPER_VERSION} -X main.commit=${RELYPER_COMMIT}" \
      -o /out/backrest ./cmd/backrest && \
    go build -trimpath -ldflags "-s -w" -o /out/docker-entrypoint ./cmd/docker-entrypoint && \
    go build -trimpath -ldflags "-s -w" -o /out/relyper-healthcheck ./cmd/relyper-healthcheck && \
    go build -trimpath -ldflags "-s -w" -o /out/relyper-dbdump ./cmd/relyper-dbdump
# Download the restic version this release is tested with. The installer
# verifies the SHA-256 sums and their signature before using the binary.
RUN XDG_DATA_HOME=/tmp/deps /out/backrest --install-deps-only && \
    install -m 0755 /tmp/deps/backrest/restic /out/restic

# --- Stage 3: runtime --------------------------------------------------------
FROM alpine:${ALPINE_VERSION}
ARG RELYPER_VERSION=dev
LABEL org.opencontainers.image.title="Relyper Backup" \
      org.opencontainers.image.description="Self-hosted backup management based on Backrest and restic" \
      org.opencontainers.image.source="https://github.com/jonasesser/relyper-backup" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${RELYPER_VERSION}"

# tini: init process (signal forwarding, zombie reaping)
# openssh-client: SFTP repositories; rclone: rclone-based repositories
# No docker CLI: Relyper Backup reaches Docker only through the Relyper
# Docker guard (Dockerfile.docker-guard), which refuses generic commands.
# apk upgrade pulls security fixes released after the base image was built.
RUN apk upgrade --no-cache && \
    apk add --no-cache tini ca-certificates tzdata bash curl openssh-client rclone && \
    mkdir -p /config /data /cache /tmp /restore && chmod 1777 /tmp

COPY --from=server /out/restic /usr/local/bin/restic
COPY --from=server /out/backrest /backrest
COPY --from=server /out/docker-entrypoint /docker-entrypoint
COPY --from=server /out/relyper-healthcheck /usr/local/bin/relyper-healthcheck
COPY --from=server /out/relyper-dbdump /usr/local/bin/relyper-dbdump
COPY scripts/relyper-recover.sh /usr/local/bin/relyper-recover

# Defaults also applied by /docker-entrypoint when unset.
ENV BACKREST_PORT=0.0.0.0:9898 \
    BACKREST_DATA=/data \
    BACKREST_CONFIG=/config/config.json \
    BACKREST_RESTIC_COMMAND=/usr/local/bin/restic \
    XDG_CACHE_HOME=/cache \
    TMPDIR=/tmp

VOLUME ["/config", "/data", "/cache"]
EXPOSE 9898

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --start-interval=5s --retries=3 \
  CMD ["/usr/local/bin/relyper-healthcheck"]

# tini -g forwards SIGTERM to the whole process group, so the server (a child
# of /docker-entrypoint) shuts down gracefully. Give running restic
# operations a chance to finish (set stop_grace_period in compose).
STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "-g", "--", "/docker-entrypoint"]
CMD ["/backrest"]
