Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$destination = Join-Path $projectRoot '.env.compose'
if (Test-Path -LiteralPath $destination) {
    throw '.env.compose already exists; it was not changed'
}
$names = @('HOOKRELAY_DB_PASSWORD', 'HOOKRELAY_PRODUCER_TOKEN', 'HOOKRELAY_ADMIN_TOKEN', 'HOOKRELAY_DEMO_A_SECRET', 'HOOKRELAY_DEMO_B_SECRET')
$lines = foreach ($name in $names) {
    $value = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
    "$name=$value"
}
[IO.File]::WriteAllLines($destination, [string[]]$lines, [Text.UTF8Encoding]::new($false))
Write-Output 'Created private .env.compose with distinct random values'
