[CmdletBinding()]
param(
    [string]$OutputRoot = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
. (Join-Path $PSScriptRoot 'lib/tooling.ps1')

if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path $repositoryRoot ".cache\p0-08-$([Guid]::NewGuid().ToString('N'))"
}
$OutputRoot = [System.IO.Path]::GetFullPath($OutputRoot)
New-Item -ItemType Directory -Force -Path $OutputRoot | Out-Null

Push-Location $repositoryRoot
try {
    $go = Get-StackPilotGo
    $spike = Join-Path $repositoryRoot 'dist/supervisor-spike.exe'
    Invoke-Checked -Description 'Build Supervisor Spike' -Command {
        & $go build -trimpath -o $spike ./cmd/supervisor-spike
    }

    $fixtures = Join-Path $repositoryRoot 'test/fixtures/supervisor-spike'
    $profiles = @(
        @{ Name = 'generic'; Fixture = '' },
        @{ Name = 'npm'; Fixture = (Join-Path $fixtures 'npm') },
        @{ Name = 'maven'; Fixture = (Join-Path $fixtures 'maven') }
    )

    foreach ($profile in $profiles) {
        $workDir = Join-Path $OutputRoot $profile.Name
        $arguments = @('run', '--work-dir', $workDir, '--profile', $profile.Name)
        if (-not [string]::IsNullOrWhiteSpace($profile.Fixture)) {
            $arguments += @('--fixture-dir', $profile.Fixture)
        }
        $result = & $spike @arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Supervisor Spike profile $($profile.Name) failed with exit code $LASTEXITCODE."
        }
        $report = $result | ConvertFrom-Json
        foreach ($property in @('launcherExited', 'identityRecovered', 'pipeReconnected', 'identityBeforeResume', 'treeTerminated')) {
            if (-not $report.$property) {
                throw "Supervisor Spike profile $($profile.Name) did not prove $property."
            }
        }
        $reportPath = Join-Path $OutputRoot "$($profile.Name).json"
        $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding utf8
        Write-Host "P0-08 $($profile.Name) passed. Report: $reportPath"
    }
}
finally {
    Pop-Location
}

Write-Host 'All P0-08 Windows supervision Spike profiles passed.'
