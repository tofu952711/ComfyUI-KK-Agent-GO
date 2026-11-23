param([switch]$Test)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $projectRoot
try {
    if ($Test) {
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    }
    New-Item -ItemType Directory -Path runtime/bin -Force | Out-Null
    & go build -trimpath -ldflags '-s -w' -o runtime/bin/kkagent.exe ./cmd/agentd
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
} finally { Pop-Location }
