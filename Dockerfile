# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on `scratch`.
# -trimpath and the -s -w linker flags keep the image small and reproducible.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/npmplus-docker-sync .

# ---------------------------------------------------------------------------
# Runtime stage: nothing but the binary, CA roots and a passwd entry.
# ---------------------------------------------------------------------------
FROM scratch

LABEL org.opencontainers.image.title="npmplus-docker-sync" \
      org.opencontainers.image.description="Sync Docker container labels to Nginx Proxy Manager / NPMplus proxy hosts, redirections, streams and 404 hosts" \
      org.opencontainers.image.source="https://github.com/VentumPhoenix/npmplus-docker-sync" \
      org.opencontainers.image.licenses="MIT"

# TLS roots for HTTPS access to the NPM API.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Minimal passwd/group so the uid resolves to a name inside the container.
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

COPY --from=builder /out/npmplus-docker-sync /npmplus-docker-sync

# Never run as root: the process only needs outbound network access.
USER 1000:1000

ENV DOCKER_HOST="unix:///var/run/docker.sock" \
    LOG_FORMAT="text"

ENTRYPOINT ["/npmplus-docker-sync"]
