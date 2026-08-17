[CmdletBinding()]
param(
    [string]$Output = 'dist/stackpilot.exe',
    [string]$Version = '',
    [string]$Commit = '',
    [string]$BuildTime = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
. (Join-Path $PSScriptRoot 'lib/tooling.ps1')

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = if ($env:STACKPILOT_VERSION) { $env:STACKPILOT_VERSION } else { 'dev' }
}
if ([string]::IsNullOrWhiteSpace($Commit)) {
    $Commit = if ($env:STACKPILOT_COMMIT) { $env:STACKPILOT_COMMIT } else { 'unknown' }
}
if ([string]::IsNullOrWhiteSpace($BuildTime)) {
    $BuildTime = if ($env:STACKPILOT_BUILD_TIME) { $env:STACKPILOT_BUILD_TIME } else { [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ') }
}

$safeValuePattern = '^[0-9A-Za-z][0-9A-Za-z._:+-]*$'
foreach ($value in @($Version, $Commit, $BuildTime)) {
    if ($value -notmatch $safeValuePattern) {
        throw "Build metadata contains unsupported characters: $value"
    }
}

$resolvedOutput = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $Output))
$outputDirectory = Split-Path -Parent $resolvedOutput
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null

Push-Location $repositoryRoot
try {
    Invoke-Checked -Description 'Web production build' -Command { npm run build:web }

    $go = Get-StackPilotGo
    $linkerFlags = "-X stackpilot/internal/buildinfo.Version=$Version -X stackpilot/internal/buildinfo.Commit=$Commit -X stackpilot/internal/buildinfo.BuildTime=$BuildTime"
    Invoke-Checked -Description 'Go binary build' -Command {
        & $go build -trimpath -ldflags $linkerFlags -o $resolvedOutput ./cmd/stackpilot
    }
}
finally {
    Pop-Location
}

Write-Host "Built $resolvedOutput"
