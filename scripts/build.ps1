$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$version = "1.0.0"
$binary = "overtls.exe"

Write-Host "Building http-proxy v$version ..."

& (Join-Path $PSScriptRoot "build-webui.ps1")
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

Push-Location $root
try {
    go build -tags "with_utls prod" -ldflags "-X main.Version=$version" -o $binary .
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}

Write-Host "Done: $binary"
