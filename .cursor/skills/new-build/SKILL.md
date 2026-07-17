---
name: new-build
description: Cut a new plex-photos release — read the current version, increment the minor, tag it, and build the Synology/Plex-ready .tar.gz via `make release`. Use when the user asks to make/cut a new build, release, or version bump for plex-photos.
disable-model-invocation: true
---

# New Build (plex-photos)

Cuts a new release of **plex-photos**: bumps the **minor** version, creates the git
tag, and builds the Plex/Synology-ready Docker image exported as
`dist/plex-photos-<version>.tar.gz` **and** `dist/plex-photos-latest.tar.gz`.

The version is derived from git tags (`git describe`) and baked into the binary
via `-ldflags -X main.version=...`. Prefer `make release` (or
`scripts/release.ps1` on Windows).

## Critical: Synology `:latest` override

The Synology host runs `angedelamort/plex-photos:latest`. Importing a tarball
only replaces that running image when the archive's Docker `RepoTags` is
exactly `angedelamort/plex-photos:latest`.

Hard rules — treat a violation as a failed release:

1. Build with **both** tags: `-t IMAGE:VERSION -t IMAGE:latest`.
2. Export **two separate** tarballs with **one `docker save` per tag**:
   - `dist/plex-photos-<version>.tar.gz` → `RepoTags: ["angedelamort/plex-photos:<version>"]`
   - `dist/plex-photos-latest.tar.gz` → `RepoTags: ["angedelamort/plex-photos:latest"]`
3. **Never** `docker save IMAGE:VERSION IMAGE:latest` into one archive.
4. **Never** put a version RepoTag inside the `*-latest.tar.gz` file (filename
   alone does not matter — Synology keys off the tag inside the archive).
5. After export, verify `manifest.json` `RepoTags` in each tarball (Step 4).
   If the latest file is not exactly `:latest`, rebuild/export before reporting
   success.

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
   - That the output will be `dist/plex-photos-<next>.tar.gz` **and**
     `dist/plex-photos-latest.tar.gz` (Plex/Synology)

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

Create an annotated tag for the chosen version (default `<next>` from the wizard),
then push it to the remote so the release is published:

```bash
git tag -a v1.3.0 -m "Release v1.3.0"
git push origin v1.3.0
```

Pushing the tag is a standard part of the release. Only skip the push if the
user explicitly asks you to keep the tag local.

## Step 3: Build the release artifact

Run the existing release target. It builds the Docker image with the version
baked in and exports **two** gzipped tarballs (one tag each):

```bash
make release
```

On Windows without `make`/`gzip`, use:

```powershell
./scripts/release.ps1 -Version v1.3.0
```

The Docker image is named `angedelamort/plex-photos`. **You MUST always build
AND export BOTH tags at once — the versioned tag (`:v1.3.0`) and `:latest` —**
because they serve different purposes: the versioned tag pins this exact release,
while `:latest` is what Synology runs and must be overwritten on update.
Tag the image with both in a single `docker build`
(`-t IMAGE:VERSION -t IMAGE:latest`).

**You MUST produce TWO separate tarball files, each containing only its own tag:**

- `dist/plex-photos-<version>.tar.gz` — saved with **only** the `:v1.3.0` tag.
- `dist/plex-photos-latest.tar.gz` — saved with **only** the `:latest` tag.

Both are exported from the same freshly built image in the same run, so they
always match. Do NOT bundle both tags into one tarball, and do NOT skip the
`latest` file — without a tarball whose RepoTag is exactly `:latest`, Synology
never replaces the running image. Both files are overwritten fresh on every
release.

Equivalent manual steps if `make` / `release.ps1` are unavailable:

```bash
docker build --build-arg VERSION=v1.3.0 -t angedelamort/plex-photos:v1.3.0 -t angedelamort/plex-photos:latest .
mkdir -p dist
docker save angedelamort/plex-photos:v1.3.0 | gzip > dist/plex-photos-v1.3.0.tar.gz
docker save angedelamort/plex-photos:latest | gzip > dist/plex-photos-latest.tar.gz
```

On Windows PowerShell without the script, build with both tags, then save and
gzip **each tag into its own tarball** via .NET:

```powershell
docker build --build-arg VERSION=v1.3.0 -t angedelamort/plex-photos:v1.3.0 -t angedelamort/plex-photos:latest .
New-Item -ItemType Directory -Force -Path dist | Out-Null

function Save-GzImage($tag, $outName) {
  $tar = "dist/$outName.tar"
  docker save $tag -o $tar
  $src = [System.IO.File]::OpenRead((Resolve-Path $tar))
  $dst = [System.IO.File]::Create((Join-Path (Get-Location) "dist/$outName.tar.gz"))
  $gz  = New-Object System.IO.Compression.GzipStream($dst, [System.IO.Compression.CompressionLevel]::Optimal)
  $src.CopyTo($gz); $gz.Close(); $dst.Close(); $src.Close()
  Remove-Item $tar
}

# One save per tag — never pass both tags to a single docker save.
Save-GzImage "angedelamort/plex-photos:v1.3.0" "plex-photos-v1.3.0"
Save-GzImage "angedelamort/plex-photos:latest" "plex-photos-latest"
```

## Step 4: Verify and report

1. Confirm BOTH artifacts exist and share the same fresh timestamp:
   `dist/plex-photos-<version>.tar.gz` and `dist/plex-photos-latest.tar.gz`.
2. **Verify RepoTags inside each archive** (required). Extract `manifest.json`
   from each `.tar.gz` and confirm:
   - version file → `["angedelamort/plex-photos:<version>"]` only
   - latest file → `["angedelamort/plex-photos:latest"]` only  
   If the latest file has a version tag (or both tags), the release is broken
   for Synology — re-export with separate `docker save` calls.
3. Report to the user: the new version, the tag created, both artifact paths,
   and the verified RepoTags.
4. Remind them: import `plex-photos-latest.tar.gz` in Synology Container
   Manager → Image → Add from file to replace the running `:latest` image.
   The versioned tarball is optional (pin/archive). The app boots into the
   **first-run setup wizard** at `/setup` only on a fresh data dir (no Plex
   env vars baked in — server URL / machine ID are entered there on first run).

## Notes

- Tags use the `vMAJOR.MINOR.PATCH` (semver) format with a leading `v`.
- This skill defaults to a **minor** bump; the wizard lets the user override.
- The release tag is pushed to the remote by default (Step 2). The `main`
  branch is also pushed when committing a dirty tree (Step 1b). Never
  force-push or rewrite shared history as part of this skill.
