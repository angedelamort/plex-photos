# plex-photos

A fast, lightweight, self-hosted photo gallery for your own folders — think
**"Plex, but for photos."** It points at the directory tree you already have,
authenticates against your Plex account, and gives you a Plex-style home page
with swimlanes, posters, favorites, and slideshows — without ever touching or
reorganizing a single file on disk.

## Why I built this

I have ~300,000 photos sitting in a folder tree on a Synology NAS, carefully
organized over the years. I wanted to actually *browse* them — nicely, quickly,
from any device — without uploading them to a cloud, importing them into a
database-driven app, or letting some tool rearrange my carefully named folders.

Nothing I tried fit, so I built this with a few firm goals:

- **Made for myself first.** It started as a way to enjoy my own library on my
  Synology NAS, and it stays opinionated around that real-world use.
- **Fast and light.** A small memory footprint and snappy navigation even at
  scale: it comfortably handles **hundreds of thousands of photos** (~300k in my
  own library) and everything still loads instantly.
- **Your folders, untouched.** Your directory tree *is* the catalog. The app is
  strictly **read-only** and never moves, renames, or modifies your files.
- **My own Frame TV "art store."** I have a Samsung Frame TV and didn't want to
  pay for the art subscription, so plex-photos doubles as a server for it: point
  it at a playlist and it drives an endless slideshow of *my* photos — no
  subscription, no per-image limit.
- **A real test of agent-assisted coding.** This project was also an experiment
  in how far AI-driven development can go on a non-trivial, real application.

## Features

- **Plex-style home page** — random swimlanes per library, plus **favorites** and
  **recently viewed** rows, so there's always something to rediscover.
- **Folders are albums, recursively** — every folder is an album, and an album
  can contain sub-albums to any depth, exactly mirroring your directory tree.
  Folders with photos render as albums; folders of folders render as collections.
- **Auth via Plex** — log in with your plex.tv account; access is validated
  against your own Plex server. A **mock mode** is available for local dev.
- **Per-library access** — each user only sees the libraries they're whitelisted
  for.
- **Playlists & slideshows** — per-user, hand-curated ordered sets of photos that
  span albums and libraries, with covers and end-to-end slideshow playback.
- **Rich metadata** — EXIF capture date, dimensions, GPS with geocoded
  city/country, and person tags are indexed at scan time and shown in the viewer.
- **Editable, Plex-like details** — give any album/collection a custom poster,
  background, sort title, summary, and more — stored separately so they survive
  rescans.
- **Auto-scan** — a filesystem watcher detects new folders/photos and rescans
  automatically; an optional periodic rescan (every N hours) is available in
  Admin as a safety net.
- **Samsung Frame TV** — cast a playlist to a Samsung Frame TV's art mode and let
  it cycle through your photos.
- **Localized UI** — available in English and French.

See the [Roadmap](ROADMAP.md) for what's planned next.

## Stack

Go (`net/http`) · SQLite (`modernc.org/sqlite`, no CGO) · `imaging` for thumbnails · vanilla HTML/CSS/JS frontend · Alpine Docker image.

## Requirements

- Go 1.23+ (for local dev / building)
- Docker + Docker Compose (for containerized run)
- A Plex server + account (for production auth; not needed in mock mode)

## Run locally (no Docker)

For development you can run with a **mock auth provider** that logs you in
automatically — no Plex server required.

```bash
AUTH_PROVIDER=mock \
MOCK_USER=dev MOCK_ADMIN=true \
DATA_PATH=./testdata/data \
PORT=8099 \
go run .
```

> `MOCK_USER` (default `dev`) and `MOCK_ADMIN` (default `true`) only apply when
> `AUTH_PROVIDER=mock`; they set the auto-logged-in user's name and admin rights.
> `PLEX_SERVER_URL` Local Plex server URL. If unset, collected via the `/setup` wizard |

> Photo folders are not configured via an env var. After the app starts, add a
> library from the admin UI and pick its root folder with the directory browser
> (it starts at `/photos` and you can navigate anywhere on the server).

Then open http://localhost:8099.

There is also a Make shortcut for the above:

```bash
make dev
```

To generate sample photos for testing:

```bash
go run testdata/gen/gen.go
```

### Run locally against a real Plex server

```bash
AUTH_PROVIDER=plex \
PLEX_SERVER_URL=http://192.168.1.10:32400 \
DATA_PATH=./data \
go run .
```

> The server's machine ID is auto-detected from `PLEX_SERVER_URL` (via its
> `/identity` endpoint), so you don't need to set it manually.
> Plex sign-in uses a client-side popup PIN flow (the browser talks to plex.tv
> directly), so no public callback URL is needed and `PUBLIC_BASE_URL` is not
> required — login works the same via `localhost`, a LAN IP, or a reverse proxy.

## Run with Docker

The default [docker-compose.yml](docker-compose.yml) is the **simple local
stack**: mock auth and the sample photos, no Plex server required.

```bash
docker compose up        # → http://localhost:8099, logged in as a mock admin
# or:
make run                 # builds the image first, then `docker compose up`
```

To test the real Plex integration, an opt-in override (not auto-merged) layers
on top, using the `PLEX_*` values from `.env`:

```bash
docker compose -f docker-compose.yml -f docker-compose.plex.yml up
```

For production on any Linux/Docker host, the app is deployed as a release
artifact, not via compose — see
[Build a release artifact](#build-a-release-artifact). Configure the env vars and
the `/photos` mount however your host does it (e.g. a NAS UI like Synology DSM
Container Manager, or a plain `docker run`). The app listens on port `8099` —
put it behind a reverse proxy (nginx / Traefik) for HTTPS.

## Build

### Build the Docker image

```bash
make build                 # tags plex-photos:<version> and plex-photos:latest
make build VERSION=1.0.0    # explicit version
```

The version is auto-derived from the git tag (`git describe`) and baked into the
binary via `-ldflags` (also passed to Docker as `--build-arg VERSION`).

### Build a release artifact

Produces a self-contained `.tar.gz` of the Docker image (`docker save`), loadable
on any Docker host with `docker load < plex-photos-<version>.tar.gz`.

```bash
git tag v1.0.0
make release
# → dist/plex-photos-v1.0.0.tar.gz
```

On a NAS, import the `.tar.gz` through its container UI instead — e.g. Synology
Container Manager → Image → Add → Add from file.

### Build the binary directly

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o plex-photos .
```

## Tests

```bash
go test ./test/...        # Go unit + integration tests
```

A browser test plan and a `/test` Cursor skill are available — see [test/README.md](test/README.md).

## First-run setup

In plex mode, if `PLEX_SERVER_URL` isn't set via an environment variable, the app
boots into a **first-run setup wizard** at `/setup` (the root URL redirects there)
that walks you through connecting your Plex server. Settings are persisted in the
data dir and applied immediately — no restart.

Precedence is **environment variable > saved setting**: any value provided via
env is authoritative and is not editable in the wizard. The setup page is
unauthenticated by necessity (no Plex login exists yet) and becomes inert once
configured, so complete first-run setup on your local network.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `AUTH_PROVIDER` | | `plex` | Auth backend: `plex` or `mock` (dev) |
| `DATA_PATH` | yes | `/config` | Single mountable data dir (arr-style `/config`): holds the SQLite DB plus a `cache/` subfolder with `cache/thumbs` and `cache/art` (uploaded custom posters/backgrounds) |
| `PORT` | | `8099` | HTTP listen port |
| `TZ` | | `UTC` | Timezone for logs |

> **Photos are not configured via an env var.** Mount your photo folders into
> the container (conventionally at `/photos`) and add a library from the admin
> UI, choosing its root folder with the directory browser. Each library's root
> is the anchor for everything beneath it.

### Example `docker-compose.yml`

A minimal production stack: Plex auth, your photos mounted read-only, and a
volume for the app's data (DB + thumbnail cache).

```yaml
services:
  plex-photos:
    image: plex-photos:latest
    container_name: plex-photos
    ports:
      - "8099:8099"
    environment:
      AUTH_PROVIDER: plex
      TZ: America/New_York
    volumes:
      - /path/to/your/photos:/photos:ro   # your library, read-only
      - plex-photos-data:/config          # SQLite DB + thumbnail cache
    restart: unless-stopped

volumes:
  plex-photos-data:
```

> Put it behind a reverse proxy (nginx / Traefik) for HTTPS. You can omit
> `PLEX_SERVER_URL` and complete it through the `/setup` wizard on first run.

## Roadmap

Planned features (search, smart collections, scan-pipeline improvements) live in
[ROADMAP.md](ROADMAP.md).

## License

MIT.
