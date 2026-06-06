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
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/plex-photos .
COPY --from=builder /app/static ./static

# Generic runtime defaults surfaced in DSM Container Manager's Environment form.
# Everything else (PHOTOS_PATH, DATA_PATH, THUMB_WIDTH, AUTH_PROVIDER) relies on
# the app's built-in defaults; first-run Plex setup values (PLEX_SERVER_URL,
# PLEX_MACHINE_ID, PUBLIC_BASE_URL) and SESSION_SECRET are provided at runtime.
ENV PORT=8099 \
    TZ=America/Toronto

# Default photos mount point; create it so the app starts even before a volume
# is attached. Owned by nobody so the unprivileged runtime user can read it.
RUN mkdir -p /photos /data && chown -R nobody /photos /data
EXPOSE 8099
USER nobody
ENTRYPOINT ["./plex-photos"]
