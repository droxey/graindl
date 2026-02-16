# ── Build Stage ──────────────────────────────────────────────────────────────

FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./

ARG VERSION=dev
ARG COMMIT=none
RUN go build -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /graindl .

# ── Runtime Stage ────────────────────────────────────────────────────────────
# Chromium is required for browser-based login and video download.
# API-only mode (--token) works without it, but the binary is built
# to optionally launch Chromium via Rod, so we include it.

FROM alpine:3.20

RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    && adduser -D -h /home/exporter exporter

# Rod looks for Chromium here by default on Alpine
ENV ROD_BROWSER=/usr/bin/chromium-browser

USER exporter
WORKDIR /home/exporter

COPY --from=builder /graindl /usr/local/bin/graindl

# Default: API-only, headless, skip video, output to /data
VOLUME ["/data"]
ENTRYPOINT ["graindl"]
CMD ["--output", "/data", "--headless", "--skip-video"]
