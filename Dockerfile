# syntax=docker/dockerfile:1
#
# Monstrobook: drop-in Go server implementing the Audiobookshelf v2.37.2
# protocol & SQLite schema, paired with the official v2.37.2 web client.
# Same runtime contract as the stock image: PORT=80, /config, /metadata,
# SOURCE=docker. Existing installs can switch images and keep their volumes.

### STAGE 0: Official Audiobookshelf v2.37.2 web UI (no Node build needed) ###
FROM ghcr.io/advplyr/audiobookshelf:2.37.2 AS upstream-webui

### STAGE 1: Cross-compile the Go server on the builder CPU (no QEMU) ###
# Keep the Go version in step with go.mod.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-server

ARG TARGETARCH
# Git commit shown in the startup log ("Monstrobook v... (build <sha>)").
ARG VCS_REF=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
  go build -trimpath -ldflags "-s -w -X main.buildCommit=${VCS_REF}" -o /out/monstrobook ./cmd/monstrobook

### STAGE 2: Minimal runtime image ###
FROM alpine:3.22

LABEL org.opencontainers.image.title="Monstrobook" \
  org.opencontainers.image.description="Drop-in Go server for Audiobookshelf v2.37.2 web & mobile clients" \
  org.opencontainers.image.source="https://github.com/ppoloskov/audiobookshelf-go" \
  org.opencontainers.image.licenses="GPL-3.0"

# Install only runtime dependencies
RUN apk add --no-cache --update \
  tzdata \
  ffmpeg \
  tini

WORKDIR /app

COPY --from=upstream-webui /app/client/dist /app/client/dist
COPY --from=build-server /out/monstrobook /app/monstrobook

EXPOSE 80

ENV PORT=80
ENV CONFIG_PATH="/config"
ENV METADATA_PATH="/metadata"
ENV SOURCE="docker"
ENV ABS_GO_CLIENT_PATH="/app/client/dist"

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/healthcheck" || exit 1

ENTRYPOINT ["tini", "--"]
CMD ["/app/monstrobook"]
