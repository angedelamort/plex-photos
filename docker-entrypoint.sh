#!/bin/sh
# linuxserver.io / arr-style entrypoint.
# Starts as root, makes /config writable by the requested PUID:PGID, then drops
# privileges to that user before exec'ing the app. This is why no manual `chown`
# on the host is ever required.
set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# Ensure the persistent data dir exists and is owned by the runtime user.
mkdir -p /config
chown -R "${PUID}:${PGID}" /config

echo "Starting plex-photos as ${PUID}:${PGID}"

# Drop root and exec the app (passed via CMD).
exec su-exec "${PUID}:${PGID}" "$@"
