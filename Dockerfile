# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage
#
# --platform=$BUILDPLATFORM pins the builder to the *host* architecture of the
# CI runner. The Go toolchain then cross-compiles for TARGETOS/TARGETARCH
# natively instead of running the whole build under QEMU emulation, which is
# 10-20x faster for arm64/armv7 and far less flaky.
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
# Populated automatically by BuildKit for every requested platform.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on `scratch`.
# -trimpath and the -s -w linker flags keep the image small and reproducible.
# GOARM is derived from TARGETVARIANT (linux/arm/v7 -> GOARM=7); it is empty
# and therefore ignored for every other platform.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/npmplus-docker-sync .

# ---------------------------------------------------------------------------
# Runtime stage: nothing but the binary and the TLS roots.
# ---------------------------------------------------------------------------
FROM scratch

LABEL org.opencontainers.image.title="npmplus-docker-sync" \
      org.opencontainers.image.description="Sync Docker container labels to Nginx Proxy Manager / NPMplus proxy hosts, redirections, streams and 404 hosts" \
      org.opencontainers.image.source="https://github.com/VentumPhoenix/npmplus-docker-sync" \
      org.opencontainers.image.licenses="MIT"

# TLS roots for HTTPS access to the NPM API.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Minimal passwd/group so tools that look up the uid do not fail. Alpine has no
# entry for uid 1000, so the process shows up as a bare numeric id - that is fine.
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

COPY --from=builder /out/npmplus-docker-sync /npmplus-docker-sync

# Never run as root: the process only needs outbound network access.
USER 1000:1000

ENV DOCKER_HOST="unix:///var/run/docker.sock" \
    LOG_FORMAT="text"

# The binary has a built-in probe (there is no shell or curl in scratch). It
# only reports healthy when HEALTH_ADDR is set and /readyz answers 200, so
# the check is a no-op success when the health endpoint is disabled.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/npmplus-docker-sync", "healthcheck"]

ENTRYPOINT ["/npmplus-docker-sync"]
