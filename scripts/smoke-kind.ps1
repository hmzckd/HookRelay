param([ValidateRange(10, 180)][int]$TimeoutSeconds = 60)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$envPath = Join-Path $root '.env.compose'
foreach ($path in @($kubectl, $kubeconfig, $envPath)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing prerequisite: $path" }
}
$settings = Get-Content -LiteralPath $envPath -Raw -Encoding UTF8 | ConvertFrom-StringData
if (-not $settings.HOOKRELAY_PRODUCER_TOKEN) { throw 'Missing producer token' }
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 8080)
try { $listener.Start() } catch { throw 'Port 8080 is in use; stop the other local server before the smoke test' } finally { $listener.Stop() }

$out = Join-Path $root 'tmp/kind-port-forward.out'
$err = Join-Path $root 'tmp/kind-port-forward.err'
$args = @('--kubeconfig', $kubeconfig, '-n', 'hookrelay', 'port-forward', 'service/api', '8080:8080', '--address', '127.0.0.1')
$forward = Start-Process -FilePath $kubectl -ArgumentList $args -WindowStyle Hidden -PassThru -RedirectStandardOutput $out -RedirectStandardError $err
$baseURL = 'http://127.0.0.1:8080'
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
try {
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($forward.HasExited) { throw 'kubectl port-forward exited before API became ready' }
        try {
            $response = Invoke-WebRequest -Method Get -Uri "$baseURL/readyz" -TimeoutSec 2
            if ($response.StatusCode -eq 204) { $ready = $true; break }
        } catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $ready) { throw 'Kubernetes API did not become ready' }
    $headers = @{
        Authorization = 'Bearer ' + $settings.HOOKRELAY_PRODUCER_TOKEN
        'Idempotency-Key' = [guid]::NewGuid().ToString()
    }
    $event = Invoke-RestMethod -Method Post -Uri "$baseURL/v1/events" -Headers $headers -ContentType 'application/json' -InFile (Join-Path $root 'examples/order-created.json')
    if ($event.deliveries.Count -ne 2) { throw 'Expected two deliveries' }
    $status = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $status = Invoke-RestMethod -Method Get -Uri "$baseURL/v1/events/$($event.id)" -Headers $headers
        if (@($status.deliveries | Where-Object status -ne 'succeeded').Count -eq 0) {
            Write-Output "kind_smoke_pass event_id=$($event.id) deliveries=2 statuses=succeeded,succeeded"
            return
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Deliveries did not succeed: $($status.deliveries.status -join ',')"
} finally {
    if (-not $forward.HasExited) { Stop-Process -Id $forward.Id -Force }
    $forward.WaitForExit()
    foreach ($path in @($out, $err)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path } }
}
