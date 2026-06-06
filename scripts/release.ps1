#requires -Version 5.1
<#
.SYNOPSIS
  Build the plex-photos Docker image and export a DSM-importable .tar.gz.

.DESCRIPTION
  Bakes an internal version into the binary (via -ldflags -X main.version),
  but tags the image only as :latest for now. The real version is still
  observable in the container startup logs ("plex-photos <version> listening").

  Produces a single-arch Docker image (--provenance=false) so Synology DSM
  Container Manager can import it. The OCI manifest-list images that buildx
  produces by default fail to import in DSM.

.PARAMETER Version
  Internal version baked into the binary. Defaults to `git describe` or "dev".

.PARAMETER Platform
  Target architecture. Use linux/arm64 for ARM-based Synology models.

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
Write-Host "Ready for Synology: $gzPath ($sizeMB MB)" -ForegroundColor Green
Write-Host "Container Manager -> Image -> Add -> Add from file -> select the .tar.gz" -ForegroundColor Green
