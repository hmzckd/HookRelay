param([ValidateRange(1, 20)][int]$Count = 12, [switch]$DemoAOnly)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$settings = Get-Content -LiteralPath (Join-Path $root '.env.compose') -Raw -Encoding UTF8 | ConvertFrom-StringData
if (-not $settings.HOOKRELAY_PRODUCER_TOKEN) { throw 'Missing producer token' }
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 8080)
try { $listener.Start() } catch { throw 'Port 8080 is already in use' } finally { $listener.Stop() }
$out = Join-Path $root 'tmp/kind-enqueue-forward.out'
$err = Join-Path $root 'tmp/kind-enqueue-forward.err'
$forward = Start-Process -FilePath $kubectl -ArgumentList @('--kubeconfig', $kubeconfig, '-n', 'hookrelay', 'port-forward', 'service/api', '8080:8080', '--address', '127.0.0.1') -WindowStyle Hidden -PassThru -RedirectStandardOutput $out -RedirectStandardError $err
try {
    $ready = $false
    for ($i = 0; $i -lt 40; $i++) {
        if ($forward.HasExited) { throw 'port-forward stopped' }
        try {
            if ((Invoke-WebRequest -Uri 'http://127.0.0.1:8080/readyz' -TimeoutSec 2).StatusCode -eq 204) { $ready = $true; break }
        } catch { Start-Sleep -Milliseconds 250 }
    }
    if (-not $ready) { throw 'API did not become ready' }
    $example = if ($DemoAOnly) { 'order-created-demo-a.json' } else { 'order-created.json' }
    $eventIds = @()
    for ($i = 0; $i -lt $Count; $i++) {
        $headers = @{
            Authorization = 'Bearer ' + $settings.HOOKRELAY_PRODUCER_TOKEN
            'Idempotency-Key' = [guid]::NewGuid().ToString()
        }
        $event = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8080/v1/events' -Headers $headers -ContentType 'application/json' -InFile (Join-Path $root "examples/$example")
        $eventIds += $event.id
    }
    Write-Output "kind_enqueue_pass count=$Count targets_per_event=$(if ($DemoAOnly) { 1 } else { 2 }) event_ids=$($eventIds -join ',')"
} finally {
    if (-not $forward.HasExited) { Stop-Process -Id $forward.Id -Force }
    $forward.WaitForExit()
    foreach ($path in @($out, $err)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path } }
}
