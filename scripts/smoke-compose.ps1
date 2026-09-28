param([ValidateRange(5, 120)][int]$TimeoutSeconds = 30)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$envPath = Join-Path $projectRoot '.env.compose'
if (-not (Test-Path -LiteralPath $envPath)) { throw 'Run scripts/init-compose-env.ps1 first' }
$settings = Get-Content -LiteralPath $envPath -Raw -Encoding UTF8 | ConvertFrom-StringData
if (-not $settings.HOOKRELAY_PRODUCER_TOKEN) { throw 'Compose producer token is missing' }

$baseURL = 'http://127.0.0.1:8080'
$ready = $false
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
while ([DateTime]::UtcNow -lt $deadline) {
    try {
        $response = Invoke-WebRequest -Method Get -Uri "$baseURL/readyz" -TimeoutSec 3
        if ($response.StatusCode -eq 204) { $ready = $true; break }
    } catch {
        Start-Sleep -Milliseconds 500
    }
}
if (-not $ready) { throw 'Compose API did not become ready' }

$headers = @{
    Authorization = 'Bearer ' + $settings.HOOKRELAY_PRODUCER_TOKEN
    'Idempotency-Key' = [guid]::NewGuid().ToString()
}
$event = Invoke-RestMethod -Method Post -Uri "$baseURL/v1/events" -Headers $headers -ContentType 'application/json' -InFile (Join-Path $projectRoot 'examples/order-created.json')
if ($event.deliveries.Count -ne 2) { throw 'The demo event did not create two deliveries' }

$status = $null
while ([DateTime]::UtcNow -lt $deadline) {
    $status = Invoke-RestMethod -Method Get -Uri "$baseURL/v1/events/$($event.id)" -Headers $headers
    if (@($status.deliveries | Where-Object status -ne 'succeeded').Count -eq 0) {
        Write-Output "compose_smoke_pass event_id=$($event.id) deliveries=2 statuses=succeeded,succeeded"
        return
    }
    Start-Sleep -Milliseconds 500
}
$statuses = ($status.deliveries.status -join ',')
throw "Compose deliveries did not both succeed before timeout: $statuses"
