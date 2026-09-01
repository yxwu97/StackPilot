[CmdletBinding()]
param(
    [string]$Database = (Join-Path $env:LOCALAPPDATA 'StackPilot\stackpilot.db'),
    [int]$ExpectedVersion = 16,
    [string]$Evidence = 'E:\StackPilot\docs\evidence\ro04-runtime-observation.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDatabase = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA 'StackPilot\stackpilot.db'))
$expectedEvidence = Join-Path $repositoryRoot 'docs\evidence\ro04-runtime-observation.json'
$resolvedDatabase = [IO.Path]::GetFullPath($Database)
$resolvedEvidence = [IO.Path]::GetFullPath($Evidence)

if (-not [string]::Equals($resolvedDatabase, $expectedDatabase, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-04 Gate only permits the default control database: $expectedDatabase"
}
if (-not [string]::Equals($resolvedEvidence, $expectedEvidence, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-04 Gate only permits its evidence file: $expectedEvidence"
}
if (-not (Test-Path -LiteralPath $resolvedDatabase -PathType Leaf)) {
    throw 'RO-04 Gate control database is missing'
}

$arguments = @(
    'run', './test/gates/ro04-runtime-observation',
    '--database', $resolvedDatabase,
    '--expected-version', $ExpectedVersion
)
$payload = & go @arguments | Out-String
if ($LASTEXITCODE -ne 0) {
    throw "RO-04 runtime observation Gate exited $LASTEXITCODE"
}
$report = $payload | ConvertFrom-Json
if ($report.schemaVersion -ne 'ro04-runtime-observation/v1' -or $report.databaseVersion -ne $ExpectedVersion) {
    throw 'RO-04 runtime observation Gate returned an invalid report'
}
$json = ($report | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
[IO.File]::WriteAllText($resolvedEvidence, "$json`n", [Text.UTF8Encoding]::new($false))
$report
