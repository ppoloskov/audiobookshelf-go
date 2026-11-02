# syntax=docker/dockerfile:1
#
# Audiobookshelf with the Go server (go-backend/) in place of the TypeScript
# server. Same runtime contract as the stock Dockerfile: PORT=80, /config,
# /metadata, SOURCE=docker. Existing installs can switch images and keep their
# volumes.

### STAGE 0: Build the stock web client once, on the builder CPU ###
# The output is static files, identical for every target platform, so this
# never runs under QEMU (an emulated arm64 npm ci + nuxt generate is very slow).
FROM --platform=$BUILDPLATFORM node:24-alpine AS build-client

WORKDIR /client
COPY /client /client
RUN npm ci && npm cache clean --force
RUN npm run generate

### STAGE 1: Cross-compile the Go server on the builder CPU (no QEMU) ###
# Keep the version in step with go-backend/go.mod.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build-server

ARG TARGETARCH

WORKDIR /src/go-backend
COPY go-backend/go.mod go-backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY go-backend /src/go-backend
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
  go build -trimpath -ldflags "-s -w" -o /out/audiobookshelf-go ./cmd/audiobookshelf-go

### STAGE 2: Minimal runtime image ###
FROM alpine:3.22

LABEL org.opencontainers.image.title="Audiobookshelf (Go server)" \
  org.opencontainers.image.description="Audiobookshelf v2.37.2 web client served by the experimental Go server rewrite (go-backend/), a drop-in replacement for the TypeScript server" \
  org.opencontainers.image.source="https://github.com/ppoloskov/audiobookshelf-go" \
  org.opencontainers.image.licenses="GPL-3.0"

# Install only runtime dependencies
RUN apk add --no-cache --update \
  tzdata \
  ffmpeg \
  tini

WORKDIR /app

COPY --from=build-client /client/dist /app/client/dist
COPY --from=build-server /out/audiobookshelf-go /app/audiobookshelf-go

EXPOSE 80

ENV PORT=80
ENV CONFIG_PATH="/config"
ENV METADATA_PATH="/metadata"
ENV SOURCE="docker"
ENV ABS_GO_CLIENT_PATH="/app/client/dist"

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/healthcheck" || exit 1

ENTRYPOINT ["tini", "--"]
CMD ["/app/audiobookshelf-go"]
