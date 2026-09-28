param(
    [string]$PgBin = 'C:\Program Files\PostgreSQL\17\bin'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not $env:DATABASE_URL) { throw 'DATABASE_URL is required' }
foreach ($name in @('initdb.exe', 'pg_ctl.exe', 'pg_dump.exe', 'pg_restore.exe', 'createdb.exe', 'psql.exe', 'postgres.exe', 'pg_isready.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $PgBin $name))) { throw "Missing PostgreSQL tool: $name" }
}

$sourceURI = [Uri]$env:DATABASE_URL
$sourceUserInfo = $sourceURI.UserInfo.Split(':', 2)
if ($sourceUserInfo.Count -ne 2) { throw 'DATABASE_URL must include user and password' }
$sourceUser = [Uri]::UnescapeDataString($sourceUserInfo[0])
$sourcePassword = [Uri]::UnescapeDataString($sourceUserInfo[1])
$sourceDB = [Uri]::UnescapeDataString($sourceURI.AbsolutePath.TrimStart('/'))

$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..')).TrimEnd('\')
$labParent = Join-Path $projectRoot 'tmp'
New-Item -ItemType Directory -Path $labParent -Force | Out-Null
$labRoot = Join-Path $labParent ('restore-verify-' + [guid]::NewGuid().ToString('N'))
$resolvedLab = [IO.Path]::GetFullPath($labRoot)
$resolvedParent = [IO.Path]::GetFullPath($labParent).TrimEnd('\')
if (-not $resolvedLab.StartsWith($resolvedParent + '\restore-verify-', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Restore lab path escaped the project tmp directory'
}
New-Item -ItemType Directory -Path $labRoot | Out-Null

$dataDir = Join-Path $labRoot 'cluster'
$archive = Join-Path $labRoot 'hookrelay.dump'
$passwordFile = Join-Path $labRoot 'restore-password.txt'
$logFile = Join-Path $labRoot 'postgres.log'
$errorLog = Join-Path $labRoot 'postgres-error.log'
$started = $false
$serverProcess = $null
$restorePassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))

$portListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$portListener.Start()
$restorePort = $portListener.LocalEndpoint.Port
$portListener.Stop()

$countSQL = 'SELECT (SELECT count(*) FROM events), (SELECT count(*) FROM deliveries), (SELECT count(*) FROM delivery_attempts), (SELECT count(*) FROM idempotency_keys), (SELECT count(*) FROM schema_migrations)'
$historySQL = 'SELECT count(*) FROM events e JOIN deliveries d ON d.event_id = e.id JOIN delivery_attempts a ON a.delivery_id = d.id'
$backupTimer = [Diagnostics.Stopwatch]::new()
$restoreTimer = [Diagnostics.Stopwatch]::new()

try {
    [IO.File]::WriteAllText($passwordFile, $restorePassword, [Text.Encoding]::ASCII)
    & (Join-Path $PgBin 'initdb.exe') --pgdata=$dataDir --username=restore_lab --pwfile=$passwordFile --auth-host=scram-sha-256 --auth-local=scram-sha-256 --encoding=UTF8 --no-locale --no-instructions | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'initdb failed' }
    Remove-Item -LiteralPath $passwordFile

    $serverProcess = Start-Process -FilePath (Join-Path $PgBin 'postgres.exe') -ArgumentList "-D `"$dataDir`" -h 127.0.0.1 -p $restorePort" -WindowStyle Hidden -PassThru -RedirectStandardOutput $logFile -RedirectStandardError $errorLog
    $ready = $false
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        & (Join-Path $PgBin 'pg_isready.exe') -h 127.0.0.1 -p $restorePort -q
        if ($LASTEXITCODE -eq 0) { $ready = $true; break }
        if ($serverProcess.HasExited) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not $ready) { throw 'temporary PostgreSQL startup failed' }
    $started = $true

    $env:PGPASSWORD = $restorePassword
    & (Join-Path $PgBin 'createdb.exe') -h 127.0.0.1 -p $restorePort -U restore_lab -T template0 hookrelay_restore
    if ($LASTEXITCODE -ne 0) { throw 'create clean restore database failed' }

    $env:PGPASSWORD = $sourcePassword
    $sourceCounts = (& (Join-Path $PgBin 'psql.exe') -h $sourceURI.Host -p $sourceURI.Port -U $sourceUser -d $sourceDB -tAc $countSQL).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'read source counts failed' }
    $sourceHistory = (& (Join-Path $PgBin 'psql.exe') -h $sourceURI.Host -p $sourceURI.Port -U $sourceUser -d $sourceDB -tAc $historySQL).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'read source history failed' }
    $backupTimer.Start()
    & (Join-Path $PgBin 'pg_dump.exe') -h $sourceURI.Host -p $sourceURI.Port -U $sourceUser -d $sourceDB -Fc --no-owner --no-privileges -f $archive
    $backupTimer.Stop()
    if ($LASTEXITCODE -ne 0) { throw 'pg_dump failed' }
    $archiveHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash

    $env:PGPASSWORD = $restorePassword
    $restoreTimer.Start()
    & (Join-Path $PgBin 'pg_restore.exe') --exit-on-error --no-owner --no-privileges -h 127.0.0.1 -p $restorePort -U restore_lab -d hookrelay_restore $archive
    $restoreTimer.Stop()
    if ($LASTEXITCODE -ne 0) { throw 'pg_restore failed' }
    $restoredCounts = (& (Join-Path $PgBin 'psql.exe') -h 127.0.0.1 -p $restorePort -U restore_lab -d hookrelay_restore -tAc $countSQL).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'read restored counts failed' }
    $restoredHistory = (& (Join-Path $PgBin 'psql.exe') -h 127.0.0.1 -p $restorePort -U restore_lab -d hookrelay_restore -tAc $historySQL).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'read restored history failed' }
    if ($sourceCounts -ne $restoredCounts -or $sourceHistory -ne $restoredHistory -or [int]$restoredHistory -lt 1) {
        throw 'restored event/delivery/attempt history did not match source'
    }
    Write-Output "backup_restore_verified counts=$restoredCounts history_rows=$restoredHistory archive_sha256=$archiveHash backup_ms=$($backupTimer.ElapsedMilliseconds) restore_ms=$($restoreTimer.ElapsedMilliseconds)"
}
finally {
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    if ($started) {
        & (Join-Path $PgBin 'pg_ctl.exe') -D $dataDir -m fast -w stop | Out-Null
        if ($LASTEXITCODE -ne 0 -and $serverProcess -and -not $serverProcess.HasExited) {
            Stop-Process -Id $serverProcess.Id -Force
            $serverProcess.WaitForExit(5000) | Out-Null
        }
    }
    if ($serverProcess -and -not $serverProcess.HasExited) {
        Stop-Process -Id $serverProcess.Id -Force
        $serverProcess.WaitForExit(5000) | Out-Null
    }
    if ($serverProcess -and -not $serverProcess.HasExited) {
        throw "Temporary PostgreSQL did not stop; inspect $labRoot"
    }
    if (Test-Path -LiteralPath $labRoot) {
        $safeLab = [IO.Path]::GetFullPath($labRoot)
        if (-not $safeLab.StartsWith($resolvedParent + '\restore-verify-', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Refusing to remove restore lab outside project tmp directory'
        }
        Remove-Item -LiteralPath $safeLab -Recurse -Force
    }
}
