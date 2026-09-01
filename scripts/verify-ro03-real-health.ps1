[CmdletBinding()]
param(
    [string]$Database = (Join-Path $env:LOCALAPPDATA 'StackPilot\stackpilot.db'),
    [int]$ExpectedVersion = 19,
    [string]$Evidence = 'E:\StackPilot\docs\evidence\ro03-real-health.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDatabase = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA 'StackPilot\stackpilot.db'))
$expectedEvidence = Join-Path $repositoryRoot 'docs\evidence\ro03-real-health.json'
$resolvedDatabase = [IO.Path]::GetFullPath($Database)
$resolvedEvidence = [IO.Path]::GetFullPath($Evidence)
if (-not [string]::Equals($resolvedDatabase, $expectedDatabase, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-03 Gate only permits the default control database: $expectedDatabase"
}
if (-not [string]::Equals($resolvedEvidence, $expectedEvidence, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-03 Gate only permits its evidence file: $expectedEvidence"
}
$payload = & go run ./test/gates/ro03-real-health --database $resolvedDatabase --expected-version $ExpectedVersion | Out-String
if ($LASTEXITCODE -ne 0) { throw "RO-03 real health Gate exited $LASTEXITCODE" }
$report = $payload | ConvertFrom-Json
if ($report.schemaVersion -ne 'ro03-real-health/v1' -or $report.databaseVersion -ne $ExpectedVersion) {
    throw 'RO-03 real health Gate returned an invalid report'
}
$json = ($report | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
[IO.File]::WriteAllText($resolvedEvidence, "$json`n", [Text.UTF8Encoding]::new($false))
$report
