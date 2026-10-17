# ─── Builder ───────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache nodejs npm

WORKDIR /src

# Go deps first (layer caching).
COPY go.mod go.sum ./
RUN go mod download

# UI deps + build.
COPY ui/package.json ui/package-lock.json* ui/
RUN cd ui && npm ci

# Source.
COPY . .

# Build embedded SPA.
RUN cd ui && npm run build

# Build-time version stamped into the binary (GET /api/version). CI passes the
# git tag (v0.9.0) or the commit sha; local builds default to "dev".
ARG VERSION=dev

# Build Go binary (CGo-free — modernc.org/sqlite).
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/ramonskie/groovearr/internal/version.Version=${VERSION}" -o /out/groovearr ./cmd/groovearr

# ─── Runtime ───────────────────────────────────────────────
FROM alpine:3.21

# ffmpeg supplies ffprobe, used by the scanner/backfill to populate track
# duration. The Go binary itself remains CGo-free and does not link ffmpeg.
RUN apk add --no-cache ca-certificates tzdata ffmpeg

COPY --from=builder /out/groovearr /usr/local/bin/groovearr

VOLUME ["/config", "/downloads", "/music", "/playlists"]

EXPOSE 8008

ENV GROOVEARR_CONFIG=/config/config.json
ENV GROOVEARR_ADDR=:8008

ENTRYPOINT ["groovearr"]
