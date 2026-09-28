param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z0-9][a-z0-9_-]+$')]
    [string]$ProjectName
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$envFile = Join-Path $root '.env.compose'
if (-not (Test-Path -LiteralPath $envFile)) { throw 'Run scripts/init-compose-env.ps1 first.' }
$settings = Get-Content -LiteralPath $envFile -Raw -Encoding UTF8 | ConvertFrom-StringData
$base = 'http://127.0.0.1:8080'
$producer = @{ Authorization = 'Bearer ' + $settings.HOOKRELAY_PRODUCER_TOKEN }
$admin = @{ Authorization = 'Bearer ' + $settings.HOOKRELAY_ADMIN_TOKEN }

function Set-DemoMode([string]$mode) {
    $prior = [Environment]::GetEnvironmentVariable('HOOKRELAY_DEMO_A_MODE', 'Process')
    try {
        $env:HOOKRELAY_DEMO_A_MODE = $mode
        docker compose -p $ProjectName --env-file $envFile up -d --no-deps --force-recreate --wait demo-a | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not start demo-a in $mode mode." }
    } finally {
        [Environment]::SetEnvironmentVariable('HOOKRELAY_DEMO_A_MODE', $prior, 'Process')
    }
}

function New-DemoEvent([string]$key) {
    $headers = $producer.Clone()
    $headers['Idempotency-Key'] = $key
    return Invoke-RestMethod -Method Post -Uri "$base/v1/events" -Headers $headers -ContentType 'application/json' -InFile (Join-Path $root 'examples/order-created-demo-a.json')
}

function Wait-DemoDelivery([string]$id, [string]$expected) {
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    do {
        $delivery = Invoke-RestMethod -Uri "$base/v1/deliveries/$id" -Headers $producer
        if ($delivery.status -eq $expected) { return $delivery }
        if ($delivery.status -in @('succeeded', 'dead')) { throw "Delivery ended as $($delivery.status), expected $expected." }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Delivery did not reach $expected within 60 seconds."
}

function Get-Attempts([string]$id) {
    return Invoke-RestMethod -Uri "$base/v1/deliveries/$id/attempts" -Headers $producer
}

function Get-ExpectedHttpStatus([scriptblock]$action) {
    try {
        & $action | Out-Null
        throw 'Request unexpectedly succeeded.'
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response) { throw }
        return [int]$response.StatusCode
    }
}

Push-Location $root
try {
    $ready = Invoke-WebRequest -Uri "$base/readyz" -UseBasicParsing
    if ($ready.StatusCode -ne 204) { throw 'API is not ready.' }
    $api = docker compose -p $ProjectName --env-file $envFile ps -q api
    if ($LASTEXITCODE -ne 0 -or -not $api) { throw "Compose project $ProjectName has no running API." }

    Set-DemoMode 'ok'
    $key = [guid]::NewGuid().ToString()
    $headers = $producer.Clone()
    $headers['Idempotency-Key'] = $key
    $first = Invoke-WebRequest -Method Post -Uri "$base/v1/events" -Headers $headers -ContentType 'application/json' -InFile (Join-Path $root 'examples/order-created-demo-a.json') -UseBasicParsing
    $replay = Invoke-WebRequest -Method Post -Uri "$base/v1/events" -Headers $headers -ContentType 'application/json' -InFile (Join-Path $root 'examples/order-created-demo-a.json') -UseBasicParsing
    $firstEvent = $first.Content | ConvertFrom-Json
    $secondEvent = $replay.Content | ConvertFrom-Json
    if ($first.StatusCode -ne 202 -or $replay.StatusCode -ne 202 -or $firstEvent.id -ne $secondEvent.id -or $replay.Headers['Idempotency-Replayed'] -ne 'true') {
        throw 'Idempotency replay failed.'
    }
    $changed = '{"type":"order.created","payload":{"order_id":"changed","amount":42},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}'
    $conflict = Get-ExpectedHttpStatus { Invoke-WebRequest -Method Post -Uri "$base/v1/events" -Headers $headers -ContentType 'application/json' -Body $changed -UseBasicParsing }
    if ($conflict -ne 409) { throw "Expected 409 for changed content; got $conflict." }
    $blocked = Get-ExpectedHttpStatus { Invoke-WebRequest -Method Post -Uri "$base/v1/endpoints" -Headers $admin -ContentType 'application/json' -Body '{"name":"blocked-target","url":"http://127.0.0.2:18080/hook"}' -UseBasicParsing }
    if ($blocked -ne 400) { throw "Expected 400 for private target; got $blocked." }
    Wait-DemoDelivery $firstEvent.deliveries[0].id 'succeeded' | Out-Null
    Write-Output "idempotency_pass event_id=$($firstEvent.id) replay=202 conflict=409 private_target=400"

    Set-DemoMode 'flaky'
    $flaky = New-DemoEvent ([guid]::NewGuid().ToString())
    Wait-DemoDelivery $flaky.deliveries[0].id 'succeeded' | Out-Null
    $flakyAttempts = Get-Attempts $flaky.deliveries[0].id
    if ($flakyAttempts.items.Count -ne 3 -or @($flakyAttempts.items | Where-Object http_status -eq 500).Count -ne 2) { throw 'Flaky retry history did not match 500, 500, 204.' }
    Write-Output "retry_pass event_id=$($flaky.id) attempts=3"

    Set-DemoMode 'drop-after-commit'
    $dropped = New-DemoEvent ([guid]::NewGuid().ToString())
    Wait-DemoDelivery $dropped.deliveries[0].id 'succeeded' | Out-Null
    $dropAttempts = Get-Attempts $dropped.deliveries[0].id
    $id = $dropped.deliveries[0].id
    $effectCount = docker compose -p $ProjectName --env-file $envFile exec -T db psql -U hookrelay -d hookrelay -Atc "SELECT count(*) FROM demo_receiver_effects WHERE delivery_id = '$id'"
    if ($LASTEXITCODE -ne 0 -or $dropAttempts.items.Count -ne 2 -or [int]$effectCount -ne 1) { throw 'Response-loss deduplication failed.' }
    Write-Output "response_loss_pass event_id=$($dropped.id) attempts=2 effects=1"

    Set-DemoMode 'error'
    $failed = New-DemoEvent ([guid]::NewGuid().ToString())
    $terminal = Wait-DemoDelivery $failed.deliveries[0].id 'dead'
    $failedAttempts = Get-Attempts $failed.deliveries[0].id
    if ($failedAttempts.items.Count -ne 5 -or $terminal.terminal_reason -ne 'max_attempts') { throw 'Terminal retry limit failed.' }
    Write-Output "terminal_pass event_id=$($failed.id) attempts=5 reason=max_attempts"
} finally {
    Set-DemoMode 'ok'
    Pop-Location
}
