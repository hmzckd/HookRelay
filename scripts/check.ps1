param(
    [switch]$SkipImage
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    if (-not $env:TEST_DATABASE_URL) {
        throw 'Set TEST_DATABASE_URL to a separate test database before running the full check.'
    }

    $goFiles = @(Get-ChildItem -Path cmd, internal -Filter '*.go' -File -Recurse | ForEach-Object FullName)
    $unformatted = @(gofmt -l $goFiles)
    if ($LASTEXITCODE -ne 0) { throw 'gofmt failed.' }
    if ($unformatted.Count -gt 0) {
        $unformatted | ForEach-Object { Write-Host $_ }
        throw 'Go files need gofmt.'
    }
    Write-Host 'Format: OK'

    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }
    Write-Host 'Vet: OK'

    go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed.' }
    Write-Host 'Tests with PostgreSQL: OK'

    $goOS = go env GOOS
    if ($LASTEXITCODE -ne 0) { throw 'go env GOOS failed.' }
    if ($goOS -eq 'windows' -and -not (Get-Command gcc -ErrorAction SilentlyContinue)) {
        Write-Host 'Race test: skipped (Windows needs a C compiler). Linux CI runs it.'
    } else {
        go test -race -count=1 ./...
        if ($LASTEXITCODE -ne 0) { throw 'go test -race failed.' }
        Write-Host 'Race test with PostgreSQL: OK'
    }

    if ($SkipImage) {
        Write-Host 'Image build: skipped by -SkipImage.'
    } else {
        docker build -t hookrelay:ci .
        if ($LASTEXITCODE -ne 0) { throw 'docker build failed.' }
        Write-Host 'Image build: OK'
    }
} finally {
    Pop-Location
}
