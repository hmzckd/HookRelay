$taskEnvPath = Join-Path (Split-Path -Parent $PSScriptRoot) '.env'
if (-not (Test-Path -LiteralPath $taskEnvPath)) {
    throw 'Create .env from .env.example in the HookRelay folder first.'
}

$taskAllowedNames = @('DATABASE_URL', 'PRODUCER_TOKEN', 'ADMIN_TOKEN', 'TARGET_PROFILE', 'EXTERNAL_KEY_DIR', 'DEMO_A_SECRET', 'DEMO_B_SECRET', 'LISTEN_ADDR', 'WORKER_CONCURRENCY')
foreach ($taskLine in Get-Content -LiteralPath $taskEnvPath -Encoding UTF8) {
    $taskLine = $taskLine.Trim()
    if ($taskLine -eq '' -or $taskLine.StartsWith('#')) { continue }
    if ($taskLine -notmatch '^([A-Z][A-Z0-9_]*)=(.*)$') {
        throw 'Invalid .env line; expected NAME=value.'
    }
    $taskName = $Matches[1]
    $taskValue = $Matches[2]
    if ($taskName -notin $taskAllowedNames) {
        throw "Unexpected .env variable: $taskName"
    }
    [Environment]::SetEnvironmentVariable($taskName, $taskValue, 'Process')
}
