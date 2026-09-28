Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$badManifest = Join-Path $root 'tmp/kind-failing-migration.yaml'
$before = & $kubectl --kubeconfig $kubeconfig -n hookrelay get deployment/api -o jsonpath='{.metadata.generation}'
if ($LASTEXITCODE -ne 0) { throw 'Could not read API Deployment generation' }
$source = Get-Content -LiteralPath (Join-Path $root 'k8s/30-migrate.yaml') -Raw -Encoding UTF8
foreach ($expected in @('backoffLimit: 2', 'activeDeadlineSeconds: 180', 'command: ["/app/migrate"]')) {
    if (-not $source.Contains($expected)) { throw 'Migration manifest changed; fault template must be reviewed' }
}
$fault = $source.Replace('backoffLimit: 2', 'backoffLimit: 0').Replace('activeDeadlineSeconds: 180', 'activeDeadlineSeconds: 30').Replace('command: ["/app/migrate"]', 'command: ["/app/probe", "http://127.0.0.1:9/readyz"]')
$gateObserved = $false
try {
    [IO.File]::WriteAllText($badManifest, $fault, [Text.UTF8Encoding]::new($false))
    try {
        & (Join-Path $PSScriptRoot 'kind-up.ps1') -MigrationManifest $badManifest | Out-Null
    } catch {
        if ($_.Exception.Message -match 'Migration Job failed; application manifests were not applied') { $gateObserved = $true }
        else { throw }
    }
    if (-not $gateObserved) { throw 'Failing migration did not stop kind-up' }
    $after = & $kubectl --kubeconfig $kubeconfig -n hookrelay get deployment/api -o jsonpath='{.metadata.generation}'
    if ($LASTEXITCODE -ne 0 -or $before -ne $after) { throw 'API Deployment changed despite migration failure' }
} finally {
    if (Test-Path -LiteralPath $badManifest) { Remove-Item -LiteralPath $badManifest }
    & (Join-Path $PSScriptRoot 'kind-up.ps1') | Out-Null
}
Write-Output 'kind_migration_gate_pass failed_job_blocked_application_apply=true normal_job_restored=true'
