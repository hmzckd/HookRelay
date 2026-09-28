Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$tools = Join-Path $root 'tmp/tools'
[IO.Directory]::CreateDirectory($tools) | Out-Null

function Install-VerifiedTool([string]$Name, [string]$Url, [string]$ChecksumUrl) {
    $destination = Join-Path $tools "$Name.exe"
    $checksumContent = (Invoke-WebRequest -Uri $ChecksumUrl -UseBasicParsing).Content
    $checksumText = if ($checksumContent -is [byte[]]) { [Text.Encoding]::UTF8.GetString($checksumContent) } else { [string]$checksumContent }
    $expected = [regex]::Match($checksumText, '(?i)[a-f0-9]{64}').Value.ToUpperInvariant()
    if (-not $expected) { throw "Missing $Name checksum" }
    if (Test-Path -LiteralPath $destination) {
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ne $expected) {
            throw "$Name exists but its SHA256 is wrong; inspect $destination before retrying"
        }
    } else {
        $download = "$destination.download"
        try {
            Invoke-WebRequest -Uri $Url -OutFile $download
            if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $expected) {
                throw "$Name download SHA256 mismatch"
            }
            Move-Item -LiteralPath $download -Destination $destination
        } finally {
            if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download }
        }
    }
    Write-Output "$Name verified"
}

Install-VerifiedTool 'kind' 'https://kind.sigs.k8s.io/dl/v0.33.0/kind-windows-amd64' 'https://kind.sigs.k8s.io/dl/v0.33.0/kind-windows-amd64.sha256sum'
Install-VerifiedTool 'kubectl' 'https://dl.k8s.io/release/v1.37.0/bin/windows/amd64/kubectl.exe' 'https://dl.k8s.io/release/v1.37.0/bin/windows/amd64/kubectl.exe.sha256'
