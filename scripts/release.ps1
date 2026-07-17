#requires -Version 5.1
<#
.SYNOPSIS
  Build the plex-photos Docker image and export portable .tar.gz artifacts.

.DESCRIPTION
  Bakes an internal version into the binary (via -ldflags -X main.version),
  tags the image as BOTH :<version> and :latest, then exports TWO separate
  tarballs — one per tag. Synology Container Manager replaces the running
  image by matching the :latest RepoTag, so the latest tarball MUST contain
  only angedelamort/plex-photos:latest (never a version tag).

  Produces a single-arch Docker image (--provenance=false). This keeps the
  exported image a plain single-manifest tarball, loadable on any Docker host
  (`docker load`) and importable by NAS container UIs such as Synology DSM
  Container Manager, which reject the OCI manifest-list images buildx produces
  by default.

.PARAMETER Version
  Internal version baked into the binary and used for the versioned image tag.
  Defaults to `git describe` or "dev".

.PARAMETER Platform
  Target architecture (e.g. linux/amd64, or linux/arm64 for ARM hosts).

.EXAMPLE
  ./scripts/release.ps1 -Version v0.6.0
#>
param(
    [string]$Version,
    [string]$Platform = "linux/amd64"
)

$ErrorActionPreference = "Stop"

$App = "angedelamort/plex-photos"
$RepoRoot = Split-Path -Parent $PSScriptRoot
$OutDir = Join-Path $RepoRoot "dist"

if (-not $Version) {
    try { $Version = (git describe --tags --always --dirty 2>$null) } catch {}
    if (-not $Version) { $Version = "dev" }
}

$VersionTag = "${App}:${Version}"
$LatestTag  = "${App}:latest"

Write-Host "Building $App (VERSION=$Version, platform=$Platform)" -ForegroundColor Cyan
Write-Host "  tags: $VersionTag  +  $LatestTag"

docker build `
    --provenance=false `
    --platform $Platform `
    --build-arg "VERSION=$Version" `
    -t $VersionTag `
    -t $LatestTag `
    $RepoRoot
if ($LASTEXITCODE -ne 0) { throw "docker build failed" }

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$fileBase = ($App -split "/")[-1]

function Save-GzImage([string]$Tag, [string]$OutName) {
    $tarPath = Join-Path $OutDir "$OutName.tar"
    $gzPath  = Join-Path $OutDir "$OutName.tar.gz"
    Write-Host "Exporting $Tag -> $gzPath" -ForegroundColor Cyan
    docker save $Tag -o $tarPath
    if ($LASTEXITCODE -ne 0) { throw "docker save failed for $Tag" }

    $inStream  = [System.IO.File]::OpenRead($tarPath)
    $outStream = [System.IO.File]::Create($gzPath)
    $gzStream  = New-Object System.IO.Compression.GzipStream($outStream, [System.IO.Compression.CompressionLevel]::Optimal)
    try { $inStream.CopyTo($gzStream) }
    finally { $gzStream.Close(); $outStream.Close(); $inStream.Close() }
    Remove-Item $tarPath

    $sizeMB = [math]::Round((Get-Item $gzPath).Length / 1MB, 1)
    Write-Host "  Ready: $gzPath ($sizeMB MB)" -ForegroundColor Green
    return $gzPath
}

# Separate saves — never `docker save version latest` into one archive.
$versionGz = Save-GzImage $VersionTag "$fileBase-$Version"
$latestGz  = Save-GzImage $LatestTag  "$fileBase-latest"

Write-Host ""
Write-Host "Artifacts ready:" -ForegroundColor Green
Write-Host "  $versionGz  (RepoTag $VersionTag)"
Write-Host "  $latestGz   (RepoTag $LatestTag)"
Write-Host "Load: docker load < file.tar.gz"
Write-Host "Synology: import *-latest.tar.gz to replace the running :latest image"
