[CmdletBinding()]
param([switch]$NoOpen)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$candidate = Join-Path $repositoryRoot 'dist\stackpilot.exe'

if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
    throw "StackPilot executable was not found: $candidate"
}

$statusPayload = & $candidate service status --output json | Out-String
if ($LASTEXITCODE -ne 0) {
    throw "StackPilot service status failed with exit code $LASTEXITCODE"
}
$status = $statusPayload | ConvertFrom-Json

if (-not $status.installed) {
    Write-Host 'Installing StackPilot for the current user...'
    & $candidate service install
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service install failed with exit code $LASTEXITCODE"
    }
} else {
    Write-Host 'Stopping the installed StackPilot control plane...'
    & $candidate service stop
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service stop failed with exit code $LASTEXITCODE"
    }

    Write-Host 'Updating the installed StackPilot control plane from dist...'
    & $candidate service upgrade
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service upgrade failed with exit code $LASTEXITCODE"
    }

    Write-Host 'Starting a fresh StackPilot control-plane process...'
    & $candidate service start
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service start failed with exit code $LASTEXITCODE"
    }
}

if (-not $NoOpen) {
    & $candidate open
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot Web open failed with exit code $LASTEXITCODE"
    }
}
