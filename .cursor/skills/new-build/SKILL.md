---
name: new-build
description: Cut a new plex-photos release — read the current version, increment the minor, tag it, and build the Synology/Plex-ready .tar.gz via `make release`. Use when the user asks to make/cut a new build, release, or version bump for plex-photos.
disable-model-invocation: true
---

# New Build (plex-photos)

Cuts a new release of **plex-photos**: bumps the **minor** version, creates the git
tag, and builds the Plex/Synology-ready Docker image exported as
`dist/plex-photos-<version>.tar.gz`.

The version is derived from git tags (`git describe`) and baked into the binary
via `-ldflags -X main.version=...`. The release artifact is produced by the
existing `make release` target (`docker save | gzip`).

## Step 1: Wizard (confirm before doing anything)

Always start here. Do NOT tag or build until the user confirms.

1. Read the current version:

```bash
git describe --tags --abbrev=0
```

   - If a tag exists (e.g. `v1.2.3`), the current version is that tag.
   - If the command fails / no tags exist, treat the current version as `v0.0.0`.

2. Compute the **next** version by incrementing the **minor** and resetting patch:
   - `vMAJOR.MINOR.PATCH` → `vMAJOR.(MINOR+1).0`
   - Examples: `v1.2.3` → `v1.3.0`; `v0.0.0` (no tags) → `v0.1.0`.

3. Confirm the build configuration is for **Plex** (production), not mock:
   - The release image is built from the `Dockerfile` and run via
     `docker-compose.yml`, which sets `AUTH_PROVIDER: plex`. This is correct —
     do not use the mock override (`docker-compose.override.yml`) or
     `make dev` for a release.

4. Present a confirmation wizard to the user using `AskQuestion`, showing:
   - Current version → next version (the minor bump)
   - That it will create git tag `<next>` and run `make release`
   - That the output will be `dist/plex-photos-<next>.tar.gz` (Plex/Synology)

   Ask whether to proceed. Offer options: **Proceed**, **Bump patch instead**,
   **Bump major instead**, **Cancel**. Only continue once the user picks a bump.

5. Before tagging, verify the working tree is clean (`git status --porcelain`).
   If dirty, warn the user — a dirty tree makes `git describe` emit `-dirty`
   and bakes an unclean version. Let them decide to commit/stash or continue.

## Step 1b: Commit and push (if the tree is dirty)

If the working tree has uncommitted changes and the user wants them in the
release, commit them to `main` and push to the remote **before** tagging:

1. Review the changes (`git diff`) and draft a clear, concise English commit
   message describing the "why" of the change.
2. Stage, commit, and push to `main`:

```bash
git add -A
git commit -m "<subject>" -m "<body>"
git push origin main
```

Notes for Windows/PowerShell: `&&` and heredocs are not supported — run each
command separately and pass multi-line commit messages with repeated `-m`
flags. Re-verify the tree is clean (`git status --porcelain`) after pushing.

## Step 2: Tag

Create an annotated tag for the chosen version (default `<next>` from the wizard):

```bash
git tag -a v1.3.0 -m "Release v1.3.0"
```

Do NOT push the tag unless the user explicitly asks.

## Step 3: Build the release artifact

Run the existing release target. It builds the Docker image with the version
baked in and exports the gzipped tarball:

```bash
make release
```

The Docker image is named `angedelamort/plex-photos`. **You MUST always build
AND export BOTH tags at once — the versioned tag (`:v1.3.0`) and `:latest` —**
so both are updated together. Tag the image with both in a single `docker build`
(`-t IMAGE:VERSION -t IMAGE:latest`) and save both into the same tarball
(`docker save IMAGE:VERSION IMAGE:latest`). If you skip `:latest`, the imported
image never updates `latest` on the target host. The exported artifact keeps the
short `plex-photos-<version>.tar.gz` name.

Equivalent manual steps if `make` is unavailable (e.g. plain Windows shell):

```bash
docker build --build-arg VERSION=v1.3.0 -t angedelamort/plex-photos:v1.3.0 -t angedelamort/plex-photos:latest .
mkdir -p dist
docker save angedelamort/plex-photos:v1.3.0 angedelamort/plex-photos:latest | gzip > dist/plex-photos-v1.3.0.tar.gz
```

On Windows PowerShell, `make` and `gzip` are typically unavailable. Build with
both tags, save an uncompressed tar (both tags), then gzip it via .NET:

```powershell
docker build --build-arg VERSION=v1.3.0 -t angedelamort/plex-photos:v1.3.0 -t angedelamort/plex-photos:latest .
New-Item -ItemType Directory -Force -Path dist | Out-Null
docker save angedelamort/plex-photos:v1.3.0 angedelamort/plex-photos:latest -o dist/plex-photos-v1.3.0.tar
$src = [System.IO.File]::OpenRead((Resolve-Path "dist/plex-photos-v1.3.0.tar"))
$dst = [System.IO.File]::Create((Join-Path (Get-Location) "dist/plex-photos-v1.3.0.tar.gz"))
$gz  = New-Object System.IO.Compression.GzipStream($dst, [System.IO.Compression.CompressionLevel]::Optimal)
$src.CopyTo($gz); $gz.Close(); $dst.Close(); $src.Close()
Remove-Item "dist/plex-photos-v1.3.0.tar"
```

## Step 4: Verify and report

1. Confirm the artifact exists: `dist/plex-photos-<version>.tar.gz`.
2. Report to the user: the new version, the tag created, and the artifact path.
3. Remind them: import it in Synology Container Manager → Registry → Add, and
   that the app boots into the **first-run setup wizard** at `/setup` (no Plex
   env vars baked in — server URL / machine ID are entered there on first run).

## Notes

- Tags use the `vMAJOR.MINOR.PATCH` (semver) format with a leading `v`.
- This skill defaults to a **minor** bump; the wizard lets the user override.
- Never push to remote or force-push as part of this skill unless asked.
