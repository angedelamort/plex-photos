# Load .env and run the app in mock/dev mode
Get-Content .env | ForEach-Object {
    if ($_ -match '^([^#=]+)=(.*)$') {
        [System.Environment]::SetEnvironmentVariable($matches[1].Trim(), $matches[2].Trim(), 'Process')
    }
}

# Free the dev port if a previous run is still holding it. This avoids the
# "Only one usage of each socket address is normally permitted" bind error when
# an earlier process was left running.
$port = $env:PORT
if (-not $port) { $port = '8099' }

$owners = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue |
    Select-Object -ExpandProperty OwningProcess -Unique
foreach ($processId in $owners) {
    if ($processId -and $processId -ne 0) {
        Write-Host "dev.ps1: freeing port $port (killing PID $processId)"
        Stop-Process -Id $processId -Force -ErrorAction SilentlyContinue
    }
}

go run .
