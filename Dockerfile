# syntax=docker/dockerfile:1

# Multi-stage build: the dashboard (Node), then the Go binary with the
# dashboard embedded, then a distroless runtime image with only that binary.
# The build stages run on the build machine's platform and cross-compile, so
# `docker buildx build --platform linux/amd64,linux/arm64 .` works.

# 1. Dashboard.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Go binary. CGO is off: the SQLite driver is pure Go.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/monitor ./cmd/monitor
# An empty /data owned by the nonroot user, so a fresh volume is writable.
RUN mkdir -p /out/data

# 3. Runtime: no shell, CA certificates (Slack and SMTP over TLS) and tzdata (TZ).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/monitor /monitor
COPY --from=build --chown=65532:65532 /out/data /data
# The repository the image is built from: GitHub's container registry links
# a pushed package to it by this label. CI passes the real repository
# (make docker-multiarch IMAGE_SOURCE=...); OWNER stands in for a build
# without it.
ARG SOURCE_URL=https://github.com/OWNER/monitor
LABEL org.opencontainers.image.source=$SOURCE_URL \
      org.opencontainers.image.description="Self-hosted errors and page statistics that speak the Sentry and PostHog SDKs"
ENV ADDR=:8000 \
    DATA_DIR=/data
VOLUME /data
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/monitor", "healthcheck"]
ENTRYPOINT ["/monitor"]
CMD ["serve"]
