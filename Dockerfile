# Base images are pinned to the multi-arch manifest digest their tag resolves
# to (supply-chain hygiene: a retagged upstream image cannot change what this
# builds from). Dependabot's docker ecosystem refreshes the digests — do not
# replace them with floating tags.

# Stage 1: Build Vite frontend
# Node 24.15 is the project floor (Vitest 5 needs >=22.12, jsdom 30 needs
# >=24.15). Vite 7 itself only needs 20.19: require(esm) must be enabled by
# default, otherwise loading the ESM-only @vitejs/plugin-react fails with
# ERR_REQUIRE_ESM. The pinned builder image below is newer than the floor.
#
# The builder stages run on $BUILDPLATFORM (the machine doing the build), NOT
# on the target platform: the console is architecture-independent, and the Go
# binary is cross-compiled below. That is what makes a linux/arm64 image build
# without QEMU emulation - the alternative was ~34 minutes of emulated npm and
# go build for one extra architecture. It needs BuildKit (the default since
# Docker 23); the classic builder does not understand --platform in FROM.
FROM --platform=$BUILDPLATFORM node:26.13-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS frontend-builder
WORKDIR /build/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 2: Build Go binary with embedded frontend
# The Go version here must equal go.mod's `toolchain` line: the stdlib is
# linked into the shipped binary, so the builder is the version that matters
# for CVEs. scripts/check-docker-go-version.mjs enforces it. GO-2026-6859 is
# fixed in 1.26.11 and 1.27.**4** - a 1.27.0/1/2/3 builder ships a vulnerable
# net/http, which is why Dependabot's blind golang:1.27.0 bump is refused.
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS backend-builder
# TARGETOS/TARGETARCH are the platform the IMAGE is for; the build itself runs
# on the builder platform, so the binary is cross-compiled rather than emulated.
ARG TARGETOS
ARG TARGETARCH
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
# Copy frontend output into embed directory BEFORE copying source
COPY --from=frontend-builder /build/frontend/out /build/internal/webdist/dist
# Copy source code (local webdist/dist not needed in build context)
COPY cmd/ ./cmd/
# api/ is not optional: internal/server/handlers_api_docs.go imports the api package,
# which //go:embed's openapi.yaml so the docs endpoint serves the copy baked into the
# binary (the archives ship no api/ directory, which is why it is embedded at all).
# Omitting it here breaks the IMAGE build only — `go build ./...` passes on any real
# checkout, where api/ exists — so this line is guarded by
# scripts/check-docker-context.mjs, which fails when a top-level package the module
# imports is missing from the COPY list.
COPY api/ ./api/
COPY internal/ ./internal/
COPY pkg/ ./pkg/
ARG VERSION=dev
ARG SOURCE_REPO=r0si/sovereign
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X github.com/r0si/sovereign/internal/server.ServerVersion=${VERSION} -X github.com/r0si/sovereign/internal/buildinfo.Repo=${SOURCE_REPO}" -o sovereign-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o healthcheck ./cmd/healthcheck
# Seed /data with nonroot ownership in the builder (which has a shell).
# NOTE: no RUN in the distroless stage — it has no shell and the build fails.
# The distroless nonroot user is uid/gid 65532 and does not exist in the
# alpine builder image, so chown numerically instead of by name.
RUN mkdir -p /data && chown -R 65532:65532 /data

# Stage 3: PostgreSQL logical-backup tools. The server owns backup/restore and
# invokes pg_dump/pg_restore/createdb/dropdb/psql directly; the distroless runtime
# has no package manager, so copy only the client binaries and their shared-library
# closure from the same Debian release as the runtime base.
FROM --platform=$TARGETPLATFORM postgres:18-bookworm@sha256:e742a2668255221388004f3a42b6150a2103f5054ab3a4a93ae9e4f01e551c35 AS postgres-tools
RUN mkdir -p /tools/usr/local/bin /tools/usr/share/postgresql && \
    for bin in pg_dump pg_restore createdb dropdb psql; do \
      cp "/usr/lib/postgresql/18/bin/$bin" /tools/usr/local/bin/; \
      for lib in $(ldd "/usr/lib/postgresql/18/bin/$bin" | awk '{for (i=1; i<=NF; i++) if ($i ~ /^\//) print $i}'); do \
        cp --parents "$lib" /tools/; \
      done; \
    done && \
    cp -a /usr/share/postgresql/18 /tools/usr/share/postgresql/

# Stage 4: Runtime — distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
LABEL org.opencontainers.image.title="Sovereign"
LABEL org.opencontainers.image.description="Command & Control Framework"
COPY --from=postgres-tools /tools/ /
COPY --from=backend-builder /build/sovereign-server /usr/local/bin/
COPY --from=backend-builder /build/healthcheck /usr/local/bin/healthcheck
# Docker copies image content (including ownership) into a fresh named volume,
# so the server can write its data there despite running as nonroot.
COPY --from=backend-builder --chown=nonroot:nonroot /data /data
EXPOSE 8000 443 8443 53
USER nonroot:nonroot
WORKDIR /home/nonroot
ENTRYPOINT ["sovereign-server"]
CMD ["-config", "/data/config.yaml"]
