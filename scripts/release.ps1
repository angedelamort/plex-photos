#requires -Version 5.1
<#
.SYNOPSIS
  Build the plex-photos Docker image and export a portable .tar.gz.

.DESCRIPTION
  Bakes an internal version into the binary (via -ldflags -X main.version),
  but tags the image only as :latest for now. The real version is still
  observable in the container startup logs ("plex-photos <version> listening").

  Produces a single-arch Docker image (--provenance=false). This keeps the
  exported image a plain single-manifest tarball, loadable on any Docker host
  (`docker load`) and importable by NAS container UIs such as Synology DSM
  Container Manager, which reject the OCI manifest-list images buildx produces
  by default.

.PARAMETER Version
  Internal version baked into the binary. Defaults to `git describe` or "dev".

.PARAMETER Platform
  Target architecture (e.g. linux/amd64, or linux/arm64 for ARM hosts).

.EXAMPLE
  ./scripts/release.ps1 -Version 0.1.0
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

Write-Host "Building $App (internal version: $Version, platform: $Platform, tag: latest)" -ForegroundColor Cyan

docker build `
    --provenance=false `
    --platform $Platform `
    --build-arg "VERSION=$Version" `
    -t "${App}:latest" `
    $RepoRoot
if ($LASTEXITCODE -ne 0) { throw "docker build failed" }

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$fileBase = ($App -split "/")[-1]
$tarPath = Join-Path $OutDir "$fileBase-$Version.tar"
$gzPath  = Join-Path $OutDir "$fileBase-$Version.tar.gz"

Write-Host "Exporting image to $gzPath" -ForegroundColor Cyan
docker save "${App}:latest" -o $tarPath
if ($LASTEXITCODE -ne 0) { throw "docker save failed" }

$inStream  = [System.IO.File]::OpenRead($tarPath)
$outStream = [System.IO.File]::Create($gzPath)
$gzStream  = New-Object System.IO.Compression.GZipStream($outStream, [System.IO.Compression.CompressionLevel]::Optimal)
try { $inStream.CopyTo($gzStream) }
finally { $gzStream.Close(); $outStream.Close(); $inStream.Close() }
Remove-Item $tarPath

$sizeMB = [math]::Round((Get-Item $gzPath).Length / 1MB, 1)
Write-Host ""
Write-Host "Artifact ready: $gzPath ($sizeMB MB)" -ForegroundColor Green
Write-Host "Load on any Docker host: docker load < $gzPath" -ForegroundColor Green
Write-Host "On a NAS (e.g. Synology): Container Manager -> Image -> Add -> Add from file" -ForegroundColor Green
