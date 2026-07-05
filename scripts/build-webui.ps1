$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$webui = Join-Path $root "webui"

Push-Location $webui
try {
    if (Test-Path "package-lock.json") {
        npm ci
    } else {
        npm install
    }
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }

    npm run build
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}
