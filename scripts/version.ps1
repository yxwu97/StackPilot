[CmdletBinding()]
param(
    [ValidateSet('Show', 'Bump', 'Check')]
    [string]$Action = 'Show',
    [ValidateSet('patch', 'minor', 'major')]
    [string]$Part = 'patch',
    [string]$BaseRef = '',
    [string]$ExpectedTag = '',
    [string]$RepositoryRoot = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
}
else {
    $RepositoryRoot = (Resolve-Path $RepositoryRoot).Path
}
. (Join-Path $PSScriptRoot 'lib/versioning.ps1')

function Get-VersionBaseline {
    $reference = if ($BaseRef) { $BaseRef } else { 'HEAD' }
    [pscustomobject]@{
        Reference = $reference
        Version = Get-StackPilotVersionAtRef -RepositoryRoot $RepositoryRoot -Reference $reference
    }
}

function Assert-VersionTag {
    param([Parameter(Mandatory)][string]$Version)

    if ($ExpectedTag -and $ExpectedTag -ne "v$Version") {
        throw "Release tag $ExpectedTag does not match VERSION v$Version."
    }
}

function Invoke-VersionCheck {
    $current = Get-StackPilotProductVersion -RepositoryRoot $RepositoryRoot
    Assert-VersionTag -Version $current
    if (-not $BaseRef) {
        Write-Output "version=$current status=valid baseline=none"
        return
    }

    $baseline = Get-VersionBaseline
    if ($null -eq $baseline.Version) {
        Write-Output "version=$current status=initial baseline=$($baseline.Reference)"
        return
    }
    if ((Compare-StackPilotVersion -Left $current -Right $baseline.Version) -lt 0) {
        throw "VERSION $current is lower than baseline $($baseline.Version)."
    }

    $productChanges = @(Get-StackPilotChangedPaths -RepositoryRoot $RepositoryRoot -BaseRef $BaseRef | Where-Object { Test-StackPilotProductPath $_ })
    if ($productChanges.Count -gt 0 -and (Compare-StackPilotVersion -Left $current -Right $baseline.Version) -le 0) {
        throw "Product changes require VERSION to advance beyond $($baseline.Version)."
    }
    Write-Output "version=$current status=valid baseline=$($baseline.Version) productChanges=$($productChanges.Count)"
}

function Invoke-VersionBump {
    $lock = Enter-StackPilotVersionLock -RepositoryRoot $RepositoryRoot
    try {
        $current = Get-StackPilotProductVersion -RepositoryRoot $RepositoryRoot
        $baseline = Get-VersionBaseline
        if ($null -eq $baseline.Version) {
            Write-Output "oldVersion=$current newVersion=$current part=$Part status=already-bumped baseline=initial"
            return
        }

        $comparison = Compare-StackPilotVersion -Left $current -Right $baseline.Version
        if ($comparison -lt 0) { throw "VERSION $current is lower than baseline $($baseline.Version)." }
        if ($comparison -gt 0) {
            Write-Output "oldVersion=$current newVersion=$current part=$Part status=already-bumped baseline=$($baseline.Version)"
            return
        }

        $productChanges = @(Get-StackPilotChangedPaths -RepositoryRoot $RepositoryRoot -BaseRef $BaseRef | Where-Object { Test-StackPilotProductPath $_ })
        if ($productChanges.Count -eq 0) { throw 'No product changes require a version bump.' }
        $next = Get-NextStackPilotVersion -Version $current -Part $Part
        Set-StackPilotProductVersion -RepositoryRoot $RepositoryRoot -Version $next
        Write-Output "oldVersion=$current newVersion=$next part=$Part status=bumped baseline=$($baseline.Version)"
    }
    finally {
        if ($null -ne $lock) { $lock.Dispose() }
    }
}

switch ($Action) {
    'Show' { Get-StackPilotProductVersion -RepositoryRoot $RepositoryRoot }
    'Check' { Invoke-VersionCheck }
    'Bump' { Invoke-VersionBump }
}
