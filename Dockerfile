# syntax=docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89
# =============================================================================
# noxa - voice/video server (Phase 1 base image)
# =============================================================================
# Multi-stage build:
#   1. builder  - cross-compiles a static Go binary from Go 1.27.3/Alpine 3.24
#   2. runtime  - minimal Alpine 3.24 image running as a non-root user
#
# NOTE on CGO: Phase 1 keeps CGO_ENABLED=0 to produce a fully static binary
# that runs on scratch/alpine without libc dependencies. Later phases that
# require FFmpeg/cgo bindings will need to switch to CGO_ENABLED=1 with an
# appropriate toolchain (e.g. gcc/musl-gcc) and a matching runtime base.
# =============================================================================

# -----------------------------------------------------------------------------
# Mobile web app stage: builds mobile/ into /out/web/app (served under /app/)
# -----------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:24.12.0-alpine3.22@sha256:4f4a059445c5a6ef2b9d169d9afde176301263178141fc05ba657dab1c84f9a7 AS mobile

WORKDIR /src/mobile
COPY mobile/package.json mobile/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY mobile/ ./
RUN npx vite build --outDir /out/web/app

# -----------------------------------------------------------------------------
# Builder stage
# -----------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS builder

ARG TARGETOS
ARG TARGETARCH

# Version metadata is normally injected via --build-arg by Make/CI. A direct
# Docker build has no .git directory, so the build falls back to a deterministic
# hash of the copied source tree.
ARG NOXA_VERSION
ARG NOXA_COMMIT
ARG NOXA_DIRTY=false
ARG NOXA_BUILD_DATE
ARG NOXA_UPDATE_REPO=arumes31/noxa

# git is required by `go mod download` for modules that reference VCS sources.
RUN apk add --no-cache git

WORKDIR /src

# Cache module downloads: copy only the module manifests first so that
# subsequent source changes do not invalidate the mod-download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

# Copy the rest of the source tree.
COPY . .

# Build a stripped, static binary with embedded version metadata.
#   -s : strip symbol table
#   -w : strip DWARF debug info
# CGO_ENABLED=0 ensures a static binary with no libc dependency.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -n "${NOXA_VERSION}" ]; then \
      NOXA_LDFLAGS="-X=noxa/internal/version.Version=${NOXA_VERSION} \
        -X=noxa/internal/version.Commit=${NOXA_COMMIT} \
        -X=noxa/internal/version.BuildDate=${NOXA_BUILD_DATE} \
        -X=noxa/internal/version.Dirty=${NOXA_DIRTY}"; \
    else \
      NOXA_LDFLAGS="$(go run ./cmd/version -format ldflags)"; \
    fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -mod=readonly -trimpath \
    -ldflags="-s -w -buildid= ${NOXA_LDFLAGS} \
      -X=noxa/internal/version.UpdateRepo=${NOXA_UPDATE_REPO}" \
    -o /out/noxa ./cmd/server && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/adduser ./cmd/adduser && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/role-setup ./cmd/role-setup && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/e2e ./cmd/e2e

# -----------------------------------------------------------------------------
# Runtime stage
# -----------------------------------------------------------------------------
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS runtime

# ca-certificates: required for outbound TLS (e.g. HTTPS clients, webhooks).
# tzdata:         required for proper timezone handling in logs/scheduling.
# wget:           used by the HEALTHCHECK below.
# Require the patched OpenSSL libraries even when the pinned base contains older
# packages. Keep upgrades scoped to these libraries and their package dependencies.
RUN apk add --no-cache --upgrade ca-certificates tzdata wget ffmpeg \
    'libcrypto3>=3.5.8-r0' 'libssl3>=3.5.8-r0'

# Create a non-root user/group with a fixed UID for predictable permissions.
# uid 10001 avoids clashes with common alpine system users.
RUN addgroup -S -g 10001 noxa \
    && adduser  -S -G noxa -u 10001 -h /home/noxa noxa

# Data directories for uploaded files, avatars/icons, and recordings. The
# compose stack mounts a named volume at /data; creating it here (owned by
# the runtime user) makes fresh volumes initialize with the right ownership
# even with read_only rootfs + cap_drop.
RUN mkdir -p /data/files /data/recordings \
    && chown -R 10001:10001 /data

# Copy the compiled binary from the builder stage.
COPY --from=builder --chown=10001:10001 /out/noxa /out/noxa
COPY --from=builder --chown=10001:10001 /out/adduser /out/role-setup /out/migrate /out/e2e /out/
# The mobile web app; web.app_dir defaults to ./web/app relative to /.
COPY --from=mobile --chown=10001:10001 /out/web/app /web/app
COPY --chmod=0555 --chown=10001:10001 scripts/secret-env.sh /usr/local/lib/noxa/secret-env.sh
COPY --chmod=0555 --chown=10001:10001 scripts/noxa-entrypoint.sh /usr/local/bin/noxa-entrypoint.sh

# Drop privileges: run as the non-root noxa user.
USER 10001:10001

# Expose the service ports:
#   12333/tcp  - TLS control channel
#   12334/udp  - UDP keepalive/media support
#   12335/tcp  - ServerQuery admin/bot text protocol
#   12336/tcp  - file transfer (token-authorized)
#   12337/tcp  - health/readiness HTTP endpoint (/healthz, /readyz)
#   12338/tcp  - gRPC signaling/control (loopback-only by default)
#   12339/tcp  - ServerQuery over SSH (disabled by default)
#   12342/tcp  - mobile web app and WebSocket control (web.enabled; behind HTTPS)
EXPOSE 12333/tcp 12334/udp 12335/tcp 12336/tcp 12337/tcp 12338/tcp 12339/tcp 12342/tcp

# Liveness probe against the health endpoint. The HTTPS retry covers
# health_tls_enabled: the self-signed control certificate cannot be verified
# here, and a loopback probe needs no peer authentication.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/bin/sh", "-c", "wget -qO- http://127.0.0.1:12337/healthz || wget -qO- --no-check-certificate https://127.0.0.1:12337/healthz"]

STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/noxa-entrypoint.sh"]
CMD ["/out/noxa"]
