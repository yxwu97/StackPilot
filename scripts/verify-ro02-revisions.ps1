[CmdletBinding()]
param(
    [string[]]$Workspaces = @(
        'E:\AgentHub',
        'E:\AIWorkflowStudio',
        'E:\BidTravelCloud',
        'E:\GNMarket',
        'E:\PMSystem'
    ),
    [string]$DataDir = 'E:\StackPilot\.local\ro02-revision-data',
    [string]$Evidence = 'E:\StackPilot\docs\evidence\ro02-real-workspace-revisions.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\ro02-revision-data'
$expectedEvidence = Join-Path $repositoryRoot 'docs\evidence\ro02-real-workspace-revisions.json'
$resolvedDataDir = [IO.Path]::GetFullPath($DataDir)
$resolvedEvidence = [IO.Path]::GetFullPath($Evidence)

if (-not [string]::Equals($resolvedDataDir, $expectedDataDir, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-02 Gate only permits its isolated data root: $expectedDataDir"
}
if (-not [string]::Equals($resolvedEvidence, $expectedEvidence, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-02 Gate only permits its evidence file: $expectedEvidence"
}
if (Test-Path -LiteralPath $resolvedDataDir) {
    throw 'RO-02 Gate data directory already exists'
}

$resolvedWorkspaces = foreach ($workspace in $Workspaces) {
    $root = (Resolve-Path -LiteralPath $workspace).Path
    $manifest = Join-Path $root '.stackpilot\system.yaml'
    if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) {
        throw "Registered workspace manifest is missing: $workspace"
    }
    $root
}

New-Item -ItemType Directory -Path $resolvedDataDir | Out-Null
$marker = Join-Path $resolvedDataDir '.ro02-gate'
[IO.File]::WriteAllText($marker, 'owned', [Text.UTF8Encoding]::new($false))

try {
    $arguments = @('run', './test/gates/ro02-revisions', '--data-dir', $resolvedDataDir)
    foreach ($workspace in $resolvedWorkspaces) {
        $arguments += @('--workspace', $workspace)
    }
    $payload = & go @arguments | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "RO-02 revision Gate exited $LASTEXITCODE"
    }
    $report = $payload | ConvertFrom-Json
    if ($report.schemaVersion -ne 'ro02-real-workspace/v1' -or @($report.systems).Count -ne $resolvedWorkspaces.Count) {
        throw 'RO-02 revision Gate returned an invalid report'
    }
    foreach ($system in @($report.systems)) {
        if (-not $system.deterministic -or -not $system.revisionDigest -or -not $system.manifestDigest) {
            throw "RO-02 revision Gate returned incomplete evidence for $($system.systemId)"
        }
    }
    $json = ($report | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
    [IO.File]::WriteAllText($resolvedEvidence, "$json`n", [Text.UTF8Encoding]::new($false))
    $report
} finally {
    $actualDataDir = [IO.Path]::GetFullPath($resolvedDataDir)
    if ([string]::Equals($actualDataDir, $expectedDataDir, [StringComparison]::OrdinalIgnoreCase) -and
        (Test-Path -LiteralPath $marker -PathType Leaf)) {
        Remove-Item -LiteralPath $actualDataDir -Recurse -Force
    }
}
