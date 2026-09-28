param([ValidateSet('ok', 'slow')][string]$Mode)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$kubectl = Join-Path $root 'tmp/tools/kubectl.exe'
$kubeconfig = Join-Path $root 'tmp/kind-kubeconfig'
$patchFile = Join-Path $root 'tmp/kind-demo-a-mode-patch.json'
$arguments = @('-listen', '0.0.0.0:18080', '-key-id', 'demo/a-v1')
if ($Mode -ne 'ok') { $arguments += @('-mode', $Mode) }
$patch = ConvertTo-Json -InputObject @(@{ op = 'replace'; path = '/spec/template/spec/containers/0/args'; value = $arguments }) -Depth 5
try {
    [IO.File]::WriteAllText($patchFile, $patch, [Text.UTF8Encoding]::new($false))
    & $kubectl --kubeconfig $kubeconfig -n hookrelay patch deployment/demo-a --type=json "--patch-file=$patchFile"
    if ($LASTEXITCODE -ne 0) { throw 'Could not change demo-a mode' }
    & $kubectl --kubeconfig $kubeconfig -n hookrelay rollout status deployment/demo-a --timeout=120s
    if ($LASTEXITCODE -ne 0) { throw 'demo-a rollout failed' }
    Write-Output "demo_a_mode=$Mode"
} finally {
    if (Test-Path -LiteralPath $patchFile) { Remove-Item -LiteralPath $patchFile }
}
