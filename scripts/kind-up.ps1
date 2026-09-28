param([string]$MigrationManifest)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kind = Join-Path $root 'tmp/tools/kind.exe'
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$envPath = Join-Path $root '.env.compose'
if (-not $MigrationManifest) { $MigrationManifest = Join-Path $root 'k8s/30-migrate.yaml' }
if (-not (Test-Path -LiteralPath $MigrationManifest)) { throw "Missing migration manifest: $MigrationManifest" }
foreach ($path in @($kind, $kubectl, $envPath)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing prerequisite: $path" }
}
$settings = Get-Content -LiteralPath $envPath -Raw -Encoding UTF8 | ConvertFrom-StringData
$fields = @('HOOKRELAY_DB_PASSWORD', 'HOOKRELAY_PRODUCER_TOKEN', 'HOOKRELAY_ADMIN_TOKEN', 'HOOKRELAY_DEMO_A_SECRET', 'HOOKRELAY_DEMO_B_SECRET')
foreach ($field in $fields) {
    if ($settings[$field] -notmatch '^[a-fA-F0-9]{64}$') { throw "Missing or invalid $field in .env.compose" }
}
if (($fields | ForEach-Object { $settings[$_] } | Select-Object -Unique).Count -ne $fields.Count) { throw 'Secrets must be distinct' }

function Run([string]$Program, [string[]]$Arguments) {
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Command failed: $([IO.Path]::GetFileName($Program)) $($Arguments[0])" }
}
$previousKubeconfig = $env:KUBECONFIG
$env:KUBECONFIG = $kubeconfig
$tempSecrets = Join-Path $root 'tmp/kind-secrets.env'
try {
    $clusters = @(& $kind get clusters)
    if ($LASTEXITCODE -ne 0) { throw 'Could not query kind clusters; start Docker Desktop' }
    if ($clusters -notcontains 'hookrelay') {
        Run $kind @('create', 'cluster', '--name', 'hookrelay', '--config', (Join-Path $root 'k8s/kind.yaml'), '--kubeconfig', $kubeconfig, '--wait', '5m')
    } elseif (-not (Test-Path -LiteralPath $kubeconfig)) {
        Run $kind @('export', 'kubeconfig', '--name', 'hookrelay', '--kubeconfig', $kubeconfig)
    }
    $context = & $kubectl --kubeconfig $kubeconfig config current-context
    if ($LASTEXITCODE -ne 0 -or $context -ne 'kind-hookrelay') { throw 'Unexpected Kubernetes context' }
    & docker image inspect 'hookrelay:local' *> $null
    if ($LASTEXITCODE -ne 0) { throw 'Build the app image first: docker compose --env-file .env.compose build api' }
    Run $kind @('load', 'docker-image', 'hookrelay:local', '--name', 'hookrelay')
    Run $kubectl @('--kubeconfig', $kubeconfig, 'apply', '-f', (Join-Path $root 'k8s/00-namespace.yaml'))

    $lines = @(
        "POSTGRES_PASSWORD=$($settings.HOOKRELAY_DB_PASSWORD)"
        "DATABASE_URL=postgres://hookrelay:$($settings.HOOKRELAY_DB_PASSWORD)@db:5432/hookrelay?sslmode=disable"
        "PRODUCER_TOKEN=$($settings.HOOKRELAY_PRODUCER_TOKEN)"
        "ADMIN_TOKEN=$($settings.HOOKRELAY_ADMIN_TOKEN)"
        "DEMO_A_SECRET=$($settings.HOOKRELAY_DEMO_A_SECRET)"
        "DEMO_B_SECRET=$($settings.HOOKRELAY_DEMO_B_SECRET)"
    )
    [IO.File]::WriteAllLines($tempSecrets, [string[]]$lines, [Text.UTF8Encoding]::new($false))
    $secretYaml = & $kubectl --kubeconfig $kubeconfig -n hookrelay create secret generic hookrelay-secrets "--from-env-file=$tempSecrets" --dry-run=client -o yaml
    if ($LASTEXITCODE -ne 0) { throw 'Could not construct Kubernetes Secret' }
    $secretYaml | & $kubectl --kubeconfig $kubeconfig apply -f - | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not apply Kubernetes Secret' }
    Remove-Item -LiteralPath $tempSecrets

    foreach ($file in @('10-config.yaml', '20-postgres.yaml')) {
        Run $kubectl @('--kubeconfig', $kubeconfig, 'apply', '-f', (Join-Path $root "k8s/$file"))
    }
    Run $kubectl @('--kubeconfig', $kubeconfig, '-n', 'hookrelay', 'rollout', 'status', 'deployment/db', '--timeout=180s')
    Run $kubectl @('--kubeconfig', $kubeconfig, '-n', 'hookrelay', 'delete', 'job/hookrelay-migrate', '--ignore-not-found=true', '--wait=true')
    Run $kubectl @('--kubeconfig', $kubeconfig, 'apply', '-f', $MigrationManifest)
    $migrationDone = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(180)
    while ([DateTime]::UtcNow -lt $deadline) {
        $job = & $kubectl --kubeconfig $kubeconfig -n hookrelay get job/hookrelay-migrate -o json | ConvertFrom-Json
        if ($LASTEXITCODE -ne 0) { throw 'Could not inspect migration Job' }
        $conditions = @()
        if ($job.PSObject.Properties.Name -contains 'status' -and $job.status.PSObject.Properties.Name -contains 'conditions') {
            $conditions = @($job.status.conditions)
        }
        foreach ($condition in $conditions) {
            if ($null -eq $condition) { continue }
            if ($condition.type -eq 'Complete' -and $condition.status -eq 'True') { $migrationDone = $true; break }
            if ($condition.type -eq 'Failed' -and $condition.status -eq 'True') { throw 'Migration Job failed; application manifests were not applied' }
        }
        if ($migrationDone) { break }
        Start-Sleep -Seconds 1
    }
    if (-not $migrationDone) { throw 'Migration Job did not complete within 180 seconds; application manifests were not applied' }
    foreach ($file in @('40-api.yaml', '50-demo-a.yaml', '51-demo-b.yaml', '60-worker.yaml')) {
        Run $kubectl @('--kubeconfig', $kubeconfig, 'apply', '-f', (Join-Path $root "k8s/$file"))
    }
    foreach ($deployment in @('api', 'demo-a', 'demo-b', 'worker')) {
        Run $kubectl @('--kubeconfig', $kubeconfig, '-n', 'hookrelay', 'rollout', 'status', "deployment/$deployment", '--timeout=180s')
    }
    Write-Output 'kind_up_pass namespace=hookrelay migrations=complete deployments=ready'
} finally {
    if (Test-Path -LiteralPath $tempSecrets) { Remove-Item -LiteralPath $tempSecrets }
    $env:KUBECONFIG = $previousKubeconfig
}
