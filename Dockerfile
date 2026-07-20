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

# Build Go binary (CGo-free — modernc.org/sqlite).
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/groovearr ./cmd/groovearr

# ─── Runtime ───────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/groovearr /usr/local/bin/groovearr

VOLUME ["/config", "/downloads", "/music", "/playlists"]

EXPOSE 8008

ENV GROOVEARR_CONFIG=/config/config.json
ENV GROOVEARR_ADDR=:8008

ENTRYPOINT ["groovearr"]
