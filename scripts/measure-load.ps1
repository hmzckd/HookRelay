param(
    [ValidateRange(4, 200)][int]$Events = 40,
    [string]$PgBin = 'C:\Program Files\PostgreSQL\17\bin'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not $env:DATABASE_URL -or -not $env:DEMO_A_SECRET -or -not $env:DEMO_B_SECRET) {
    throw 'Load .env first; DATABASE_URL and both demo secrets are required'
}
if (-not (Test-Path -LiteralPath (Join-Path $PgBin 'psql.exe'))) { throw 'psql.exe is required' }

$sourceURI = [Uri]$env:DATABASE_URL
if ($sourceURI.Scheme -notin @('postgres', 'postgresql') -or $sourceURI.Host -notin @('127.0.0.1', 'localhost', '::1')) {
    throw 'The load experiment requires a loopback PostgreSQL URL'
}
$userInfo = $sourceURI.UserInfo.Split(':', 2)
if ($userInfo.Count -ne 2) { throw 'DATABASE_URL must include user and password' }
$dbUser = [Uri]::UnescapeDataString($userInfo[0])
$dbPassword = [Uri]::UnescapeDataString($userInfo[1])
$testURIBuilder = [UriBuilder]$sourceURI
$testURIBuilder.Path = '/hookrelay_test'
$testURIBuilder.Query = $testURIBuilder.Query.TrimStart('?') -replace '(^|&)search_path=[^&]*', ''
$testURI = $testURIBuilder.Uri
$dbPort = if ($sourceURI.Port -gt 0) { $sourceURI.Port } else { 5432 }

$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..')).TrimEnd('\')
$labParent = Join-Path $projectRoot 'tmp'
$labRoot = Join-Path $labParent ('load-verify-' + [guid]::NewGuid().ToString('N'))
$resolvedParent = [IO.Path]::GetFullPath($labParent).TrimEnd('\')
$resolvedLab = [IO.Path]::GetFullPath($labRoot)
if (-not $resolvedLab.StartsWith($resolvedParent + '\load-verify-', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Load lab path escaped the project tmp directory'
}

$savedEnv = @{}
foreach ($name in @('DATABASE_URL', 'PGPASSWORD', 'PGOPTIONS', 'TARGET_PROFILE', 'WORKER_CONCURRENCY', 'WORKER_HEALTH_ADDR', 'DEMO_RECEIVER_SECRET', 'EXTERNAL_KEY_DIR', 'GOCACHE')) {
    $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$env:PGPASSWORD = $dbPassword
$env:PGOPTIONS = ''

function Invoke-TestSql([string]$sql, [string]$schema = '') {
    $env:PGOPTIONS = if ($schema) { "-c search_path=$schema" } else { '' }
    $answer = & (Join-Path $PgBin 'psql.exe') -X -qAt -v ON_ERROR_STOP=1 -h $sourceURI.Host -p $dbPort -U $dbUser -d hookrelay_test -c $sql
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL load experiment query failed' }
    return ($answer | Out-String).Trim()
}

function Stop-LabProcess($process) {
    if ($null -eq $process) { return }
    $process.Refresh()
    if (-not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit(5000) | Out-Null
    }
}

$activeSchemas = [Collections.Generic.List[string]]::new()
try {
    if ((Invoke-TestSql 'SELECT current_database()') -ne 'hookrelay_test') {
        throw 'The dedicated hookrelay_test database is required'
    }
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 18080)
    try { $listener.Start() } catch { throw 'Demo receiver port 18080 is in use; stop that receiver before the experiment' }
    finally { $listener.Stop() }

    New-Item -ItemType Directory -Path $labRoot -Force | Out-Null
    $env:GOCACHE = Join-Path $labParent 'go-build'
    $workerExe = Join-Path $labRoot 'worker.exe'
    $receiverExe = Join-Path $labRoot 'receiver.exe'
    $migrateExe = Join-Path $labRoot 'migrate.exe'
    foreach ($target in @(@($workerExe, './cmd/worker'), @($receiverExe, './cmd/demo-receiver'), @($migrateExe, './cmd/migrate'))) {
        & go build -o $target[0] $target[1]
        if ($LASTEXITCODE -ne 0) { throw "Go build failed for $($target[1])" }
    }

    $env:TARGET_PROFILE = 'demo'
    $env:WORKER_CONCURRENCY = '1'
    $env:WORKER_HEALTH_ADDR = ''
    $env:EXTERNAL_KEY_DIR = ''
    $env:DEMO_RECEIVER_SECRET = $env:DEMO_A_SECRET
    $cpuName = (Get-ItemProperty 'HKLM:\HARDWARE\DESCRIPTION\System\CentralProcessor\0' -ErrorAction SilentlyContinue).ProcessorNameString
    Write-Output "load_environment cpu=$cpuName logical_processors=$([Environment]::ProcessorCount) go=$(& go version) events_per_case=$Events per_process_concurrency=1 receiver_mode=ok"

    foreach ($workerCount in @(1, 4)) {
        $schema = 'hrload_' + [guid]::NewGuid().ToString('N')
        $receiverProcess = $null
        $workerProcesses = [Collections.Generic.List[object]]::new()
        try {
            Invoke-TestSql "CREATE SCHEMA $schema" | Out-Null
            $activeSchemas.Add($schema)
            $scopedURIBuilder = [UriBuilder]$testURI
            $query = $scopedURIBuilder.Query.TrimStart('?')
            $scopedURIBuilder.Query = if ($query) { "$query&search_path=$schema" } else { "search_path=$schema" }
            $env:DATABASE_URL = $scopedURIBuilder.Uri.AbsoluteUri
            & $migrateExe | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Test schema migration failed' }

            $seedSql = @"
WITH new_events AS (
    INSERT INTO events (id, event_type, payload)
    SELECT gen_random_uuid(), 'load.test', convert_to('{"load":true}', 'UTF8')
    FROM generate_series(1, $Events) RETURNING id
)
INSERT INTO deliveries (id, event_id, endpoint_id, endpoint_url, endpoint_version, secret_ref)
SELECT gen_random_uuid(), id, '11111111-1111-4111-8111-111111111111',
       'http://127.0.0.1:18080/hook', 1, 'demo/a-v1' FROM new_events
"@
            Invoke-TestSql $seedSql $schema | Out-Null
            if ((Invoke-TestSql 'SELECT count(*) FROM deliveries' $schema) -ne "$Events") {
                throw 'Seeded delivery count does not match requested event count'
            }

            $receiverProcess = Start-Process -FilePath $receiverExe -ArgumentList '-listen 127.0.0.1:18080 -mode ok' -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $labRoot "receiver-$workerCount.out") -RedirectStandardError (Join-Path $labRoot "receiver-$workerCount.err")
            $receiverReady = $false
            for ($attempt = 0; $attempt -lt 50; $attempt++) {
                try {
                    $probe = [Net.Sockets.TcpClient]::new('127.0.0.1', 18080)
                    $probe.Close()
                    $receiverReady = $true
                    break
                } catch {
                    $receiverProcess.Refresh()
                    if ($receiverProcess.HasExited) { break }
                    Start-Sleep -Milliseconds 100
                }
            }
            if (-not $receiverReady) { throw 'Demo receiver did not start' }

            $timer = [Diagnostics.Stopwatch]::StartNew()
            for ($index = 0; $index -lt $workerCount; $index++) {
                $workerProcesses.Add((Start-Process -FilePath $workerExe -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $labRoot "worker-$workerCount-$index.out") -RedirectStandardError (Join-Path $labRoot "worker-$workerCount-$index.err")))
            }
            $succeeded = 0
            while ($timer.Elapsed -lt [TimeSpan]::FromMinutes(3)) {
                $state = Invoke-TestSql "SELECT count(*) FILTER (WHERE status = 'succeeded'), count(*) FILTER (WHERE status IN ('failed','dead')) FROM deliveries" $schema
                $parts = $state.Split('|')
                $succeeded = [int]$parts[0]
                if ([int]$parts[1] -gt 0) { throw 'A delivery reached a terminal failure during the load experiment' }
                if ($succeeded -eq $Events) { break }
                foreach ($process in $workerProcesses) {
                    $process.Refresh()
                    if ($process.HasExited) { throw 'A worker exited before all deliveries completed' }
                }
                Start-Sleep -Milliseconds 250
            }
            $timer.Stop()
            if ($succeeded -ne $Events) { throw 'Load experiment timed out before all deliveries completed' }
            $integrity = Invoke-TestSql 'SELECT (SELECT count(*) FROM delivery_attempts), (SELECT count(*) FROM demo_receiver_effects)' $schema
            if ($integrity -ne "$Events|$Events") { throw "Attempt/effect counts did not match delivery count: $integrity" }
            $latency = Invoke-TestSql "SELECT round(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM a.finished_at - d.created_at))::numeric, 3), round(percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM a.finished_at - d.created_at))::numeric, 3) FROM deliveries d JOIN delivery_attempts a ON a.delivery_id = d.id" $schema
            $elapsed = [math]::Round($timer.Elapsed.TotalSeconds, 3)
            $throughput = [math]::Round($Events / $timer.Elapsed.TotalSeconds, 2)
            Write-Output "load_result worker_processes=$workerCount deliveries=$succeeded attempts=$Events receiver_effects=$Events elapsed_seconds=$elapsed deliveries_per_second=$throughput latency_p50_p95_seconds=$latency"
        }
        finally {
            foreach ($process in $workerProcesses) { Stop-LabProcess $process }
            Stop-LabProcess $receiverProcess
            if ($activeSchemas.Contains($schema)) {
                Invoke-TestSql "SET client_min_messages TO warning; DROP SCHEMA $schema CASCADE" | Out-Null
                $activeSchemas.Remove($schema) | Out-Null
            }
        }
    }
}
finally {
    try {
        foreach ($schema in $activeSchemas) { Invoke-TestSql "SET client_min_messages TO warning; DROP SCHEMA $schema CASCADE" | Out-Null }
    }
    finally {
        foreach ($name in $savedEnv.Keys) {
            [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], 'Process')
        }
        if (Test-Path -LiteralPath $labRoot) {
            $safeLab = [IO.Path]::GetFullPath($labRoot)
            if (-not $safeLab.StartsWith($resolvedParent + '\load-verify-', [StringComparison]::OrdinalIgnoreCase)) {
                throw 'Refusing to remove load lab outside project tmp directory'
            }
            Remove-Item -LiteralPath $safeLab -Recurse -Force
        }
    }
}
