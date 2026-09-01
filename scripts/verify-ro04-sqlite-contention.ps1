[CmdletBinding()]
param(
    [string]$Evidence = 'E:\StackPilot\docs\evidence\ro04-sqlite-contention.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedEvidence = Join-Path $repositoryRoot 'docs\evidence\ro04-sqlite-contention.json'
$resolvedEvidence = [IO.Path]::GetFullPath($Evidence)

if (-not [string]::Equals($resolvedEvidence, $expectedEvidence, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-04 contention Gate only permits its evidence file: $expectedEvidence"
}

$payload = & go run ./test/gates/ro04-sqlite-contention | Out-String
if ($LASTEXITCODE -ne 0) {
    throw "RO-04 contention Gate exited $LASTEXITCODE"
}
$report = $payload | ConvertFrom-Json
if ($report.schemaVersion -ne 'ro04-sqlite-contention/v1' -or $report.scope -ne 'isolated-sqlite-wal' -or
    $report.serviceCount -ne 19 -or $report.gateStatus -ne 'passed') {
    throw 'RO-04 contention Gate returned an invalid or blocked report'
}
$json = ($report | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
[IO.File]::WriteAllText($resolvedEvidence, "$json`n", [Text.UTF8Encoding]::new($false))
$report
