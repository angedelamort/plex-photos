# plex-photos

A lightweight self-hosted photo gallery, authenticated via Plex SSO, organized
into libraries / collections / albums mapped onto your existing folder structure.
Think "Plex, but for photos".

- **Auth via Plex** — log in with your plex.tv account; access is validated against your Plex server.
- **Folder-based** — point a library at a folder; collections and albums are detected by scanning the filesystem.
- **Per-library access** — each user only sees the libraries they are whitelisted for.
- **Read-only** — the app never modifies your photos.
- **Auto-scan** — a filesystem watcher detects new folders/photos and rescans automatically; an optional periodic rescan (every N hours) can be set in Admin as a safety net.

See [plex-photos-architecture.md](plex-photos-architecture.md) for the full design.

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
PLEX_MACHINE_ID=your-server-machine-id \
DATA_PATH=./data \
go run .
```

> Find your server's machine ID with:
> `curl -s http://<plex-host>:32400/identity` (look for `machineIdentifier`).
> `SESSION_SECRET` is optional — if unset, a random key is generated and stored
> at `<DATA_PATH>/session.key` on first run (set the env var only to pin it).
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

In plex mode you no longer have to supply the Plex settings up front. If
`PLEX_SERVER_URL` / `PLEX_MACHINE_ID` are not provided via environment variables,
the app boots into a **first-run setup wizard** served at `/setup` (the root URL
redirects there). Enter your Plex server URL and click **Check connection**: the
app contacts `<serverURL>/identity` to verify the server is reachable and fetches
its machine ID automatically. **Save and continue** stays disabled until the
check succeeds. Settings are persisted in the data dir and applied immediately —
no container restart.

Precedence is **environment variable > saved setting**: any value provided via
env is authoritative and is not editable in the wizard. The setup page is
unauthenticated by necessity (no Plex login exists yet) and becomes inert once
configured, so complete first-run setup on your local network.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `AUTH_PROVIDER` | | `plex` | Auth backend: `plex` or `mock` (dev) |
| `PLEX_SERVER_URL` | | first-run wizard | Local Plex server URL. If unset, collected via the `/setup` wizard |
| `PLEX_MACHINE_ID` | | first-run wizard | Plex server machine ID (validates server access). If unset, auto-detected/collected via the `/setup` wizard |
| `SESSION_SECRET` | | auto-generated | Cookie signing key. If unset, a random key is generated and persisted to `<DATA_PATH>/session.key` on first run |
| `DATA_PATH` | yes | `/config` | Single mountable data dir (arr-style `/config`): holds the SQLite DB plus a `cache/` subfolder with `cache/thumbs` and `cache/art` (uploaded custom posters/backgrounds) |
| `PORT` | | `8099` | HTTP listen port |
| `THUMB_WIDTH` | | `400` | Thumbnail width in pixels |
| `TZ` | | `UTC` | Timezone for logs |
| `MOCK_USER` | | `dev` | Username when `AUTH_PROVIDER=mock` |
| `MOCK_ADMIN` | | `true` | Whether the mock user is an admin |

> **Photos are not configured via an env var.** Mount your photo folders into
> the container (conventionally at `/photos`) and add a library from the admin
> UI, choosing its root folder with the directory browser. Each library's root
> is the anchor for everything beneath it.

## Roadmap (V2)

### Search

A search box to find photos across every library the user can access, querying
the indexed metadata rather than scanning the filesystem on each request. The
`photo_meta` / `photo_people` index already populated at scan time (capture
date, dimensions, GPS, geocoded city/country, person tags) makes this feasible
without re-reading EXIF per query.

Sketch:

- A search endpoint that filters indexed photos by free text (filename, place,
  person) and structured facets (date range, camera/lens, has-GPS, orientation),
  scoped to the caller's accessible libraries.
- Tokenize person/place names for partial matches; consider SQLite FTS5 for the
  text columns if simple `LIKE` proves too limited.
- A search affordance in the header that opens a results grid reusing the
  existing photo tiles and viewer, plus an "open as slideshow" action.
- Naturally complements smart collections below (a saved search ≈ a rule-based
  collection).

### Photo playlists

A user-curated, ordered set of individual **photos** (as opposed to album-level
favorites, which already exist). Unlike albums — which mirror folders on disk —
a playlist is a virtual, cross-album/cross-library collection of photos.

Sketch:

- New tables, e.g. `playlists(id, plex_username, name, created_at)` and
  `playlist_items(playlist_id, photo_path, position)` (ordered).
- Endpoints to create/rename/delete playlists, add/remove/reorder items, and
  list a playlist's photos.
- A "playlists" section in the sidebar and a swimlane on Accueil; an "add to
  playlist" action in the photo viewer.
- Slideshow support so a playlist can be played end-to-end like an album.

### Smart collections

Rule-based collections that populate automatically instead of being manually
curated (analogous to Plex smart collections). Selected by filters such as date
range, EXIF camera/lens, GPS area, filename pattern, or favorited status — and
optionally combined with manual playlists.

Sketch:

- Persist a rule definition (e.g. JSON criteria) per smart collection.
- Evaluate rules at query time over the indexed photo metadata (`photo_meta` /
  `photo_people`: capture date, GPS, geocoded place, dimensions, person tags),
  which is already populated during scans.
- Surface them alongside playlists in the UI.

## License

MIT.
