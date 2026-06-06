# test/

Automated and manual tests for plex-photos.

## Layout

- `unit/` — fast, isolated Go unit tests (session signing, path safety, config validation).
- `integration/` — Go integration tests that spin up the full HTTP server (mock auth)
  against temporary directories and a real SQLite DB, exercising scan, navigation,
  thumbnails, cover setting, and access control.
- `BROWSER_TEST_PLAN.md` — manual/automated browser test plan run against a live dev server.

## Running the Go tests

```
go test ./test/...
```

Verbose:

```
go test ./test/... -v
```

## Running the browser test plan

See `BROWSER_TEST_PLAN.md`. Quick start:

```
AUTH_PROVIDER=mock MOCK_USER=dev MOCK_ADMIN=true \
PHOTOS_PATH=./testdata/photos DATA_PATH=./testdata/data PORT=8099 \
go run .
```

Then drive the steps in a browser at http://localhost:8099.

## One-command run

Type `/test` in Cursor to run the unit + integration tests and execute the
browser test plan automatically (see `.cursor/skills/test/SKILL.md`).
