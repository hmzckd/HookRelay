param([ValidateRange(5, 60)][int]$MonitorSeconds = 15)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$monitor = Start-Job -ArgumentList $kubectl, $kubeconfig, $MonitorSeconds -ScriptBlock {
    param($kubectlPath, $configPath, $seconds)
    $deadline = [DateTime]::UtcNow.AddSeconds($seconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $details = & $kubectlPath --kubeconfig $configPath -n hookrelay exec deployment/demo-b -- /app/probe http://api:8080/readyz 2>&1
        if ($LASTEXITCODE -eq 0) { 'pass' } else { "fail $([DateTime]::UtcNow.ToString('HH:mm:ss.fff')) $($details -join ' ')" }
        Start-Sleep -Milliseconds 100
    }
}
try {
    Start-Sleep -Seconds 1
    & $kubectl --kubeconfig $kubeconfig -n hookrelay rollout restart deployment/api
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart API Deployment' }
    & $kubectl --kubeconfig $kubeconfig -n hookrelay rollout status deployment/api --timeout=120s
    if ($LASTEXITCODE -ne 0) { throw 'API rollout failed' }
    Wait-Job -Job $monitor -Timeout ($MonitorSeconds + 15) | Out-Null
    if ($monitor.State -ne 'Completed') { throw 'Rollout monitor did not complete' }
    $samples = @(Receive-Job -Job $monitor)
    $passes = @($samples | Where-Object { $_ -eq 'pass' }).Count
    $failedSamples = @($samples | Where-Object { $_ -like 'fail *' })
    $fails = $failedSamples.Count
    if ($passes -eq 0 -or $fails -gt 0) { throw "In-cluster API probe failed: passes=$passes failures=$fails first_failure=$($failedSamples | Select-Object -First 1)" }
    Write-Output "kind_api_rollout_pass in_cluster_probes=$passes failures=$fails"
} finally {
    if ($monitor.State -eq 'Running') { Stop-Job -Job $monitor }
    Remove-Job -Job $monitor -Force
}
