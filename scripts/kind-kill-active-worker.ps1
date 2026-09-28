param([Parameter(Mandatory)][guid]$EventId)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
& $kubectl --kubeconfig $kubeconfig -n hookrelay scale deployment/worker --replicas=2 | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not start workers' }
$deadline = [DateTime]::UtcNow.AddSeconds(15)
$chosen = $null
while ([DateTime]::UtcNow -lt $deadline -and -not $chosen) {
    $pods = & $kubectl --kubeconfig $kubeconfig -n hookrelay get pods -l app.kubernetes.io/component=worker -o json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'Could not list worker Pods' }
    foreach ($pod in $pods.items) {
        if ($pod.status.phase -ne 'Running') { continue }
        $log = & $kubectl --kubeconfig $kubeconfig -n hookrelay logs $pod.metadata.name --since=1m
        if ($LASTEXITCODE -ne 0) { continue }
        if (@($log | Where-Object { $_ -match 'delivery attempt started' -and $_ -match [string]$EventId }).Count -gt 0) {
            $chosen = $pod.metadata.name
            break
        }
    }
    if (-not $chosen) { Start-Sleep -Milliseconds 100 }
}
if (-not $chosen) { throw 'No Pod logged a claim for the selected event in time' }
$status = & $kubectl --kubeconfig $kubeconfig -n hookrelay exec deployment/db -- psql -U hookrelay -d hookrelay -tAc "SELECT status FROM deliveries WHERE event_id = '$EventId'"
if ($LASTEXITCODE -ne 0 -or $status -ne 'processing') { throw 'Selected event is no longer processing; queue a new slow event before deleting a Pod' }
& $kubectl --kubeconfig $kubeconfig -n hookrelay delete "pod/$chosen" --force --grace-period=0 --wait=false
if ($LASTEXITCODE -ne 0) { throw 'Could not delete worker Pod' }
Write-Output "worker_pod_deleted=$chosen event_id=$EventId"
