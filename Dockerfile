# Build stage
FROM golang:1.26-alpine AS builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION}" -o plex-photos .

# Runtime stage
FROM alpine:3.21
# su-exec lets the root entrypoint drop privileges to PUID:PGID (gosu equivalent).
RUN apk add --no-cache ca-certificates tzdata su-exec
WORKDIR /app
COPY --from=builder /app/plex-photos .
COPY --from=builder /app/static ./static
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Generic runtime defaults surfaced in DSM Container Manager's Environment form.
# Everything else (PHOTOS_PATH, DATA_PATH, THUMB_WIDTH, AUTH_PROVIDER) relies on
# the app's built-in defaults; first-run Plex setup values (PLEX_SERVER_URL,
# PLEX_MACHINE_ID, PUBLIC_BASE_URL) and SESSION_SECRET are provided at runtime.
# PUID/PGID follow the linuxserver.io / arr convention: the container starts as
# root, the entrypoint chowns /config to PUID:PGID, then drops to that user.
ENV TZ=America/Toronto \
    PUID=1000 \
    PGID=1000

RUN mkdir -p /photos /config
EXPOSE 8099
# Runs as root only long enough to fix /config ownership, then su-exec to PUID:PGID.
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["./plex-photos"]
