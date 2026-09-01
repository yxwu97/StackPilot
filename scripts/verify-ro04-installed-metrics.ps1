[CmdletBinding()]
param(
    [string]$Candidate = 'E:\StackPilot\.local\ro04-production-candidate\stackpilot.exe',
    [string]$Evidence = 'E:\StackPilot\docs\evidence\ro04-installed-metrics.json',
    [int]$MinimumPoints = 2,
    [int]$WarmupSeconds = 35
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedCandidate = Join-Path $repositoryRoot '.local\ro04-production-candidate\stackpilot.exe'
$expectedEvidence = Join-Path $repositoryRoot 'docs\evidence\ro04-installed-metrics.json'
$installMarker = Join-Path $env:LOCALAPPDATA 'Programs\StackPilot\installation.json'
$resolvedCandidate = (Resolve-Path -LiteralPath $Candidate).Path
$resolvedEvidence = [IO.Path]::GetFullPath($Evidence)

if (-not [string]::Equals($resolvedCandidate, $expectedCandidate, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-04 installed Gate only permits the repository candidate: $expectedCandidate"
}
if (-not [string]::Equals($resolvedEvidence, $expectedEvidence, [StringComparison]::OrdinalIgnoreCase)) {
    throw "RO-04 installed Gate only permits its evidence file: $expectedEvidence"
}
if ($MinimumPoints -lt 2 -or $MinimumPoints -gt 10 -or $WarmupSeconds -lt 0 -or $WarmupSeconds -gt 120) {
    throw 'RO-04 installed Gate bounds are invalid'
}

function Invoke-StackPilotJson {
    param([string[]]$Arguments)
    $payload = & $resolvedCandidate @Arguments | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot command failed with exit code $LASTEXITCODE"
    }
    return $payload | ConvertFrom-Json
}

$candidateHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolvedCandidate).Hash.ToLowerInvariant()
$marker = Get-Content -Raw -LiteralPath $installMarker | ConvertFrom-Json
if ($marker.sha256 -ne $candidateHash -or -not [string]::Equals($marker.executablePath, (Join-Path $marker.installDir "versions\$candidateHash\stackpilot.exe"), [StringComparison]::OrdinalIgnoreCase)) {
    throw 'RO-04 installed Gate requires the candidate to be the current immutable installation'
}

$version = Invoke-RestMethod -Method Get -Uri 'http://127.0.0.1:32100/version' -TimeoutSec 5
if ($version.version -ne $marker.version -or $version.capabilities -notcontains 'phase3.resource-monitoring') {
    throw 'RO-04 installed Gate requires the resource monitoring capability from the installed candidate'
}
if ($version.capabilities -contains 'phase3.change-planning' -or $version.capabilities -contains 'phase3.verified-restart') {
    throw 'RO-04 installed Gate found a later Phase 3 capability published early'
}

if ($WarmupSeconds -gt 0) {
    Start-Sleep -Seconds $WarmupSeconds
}

$from = [DateTimeOffset]::UtcNow.AddMinutes(-10).ToString('o')
$systems = @('agenthub', 'aiws', 'btc', 'gnmarket', 'pms')
$serviceResults = [Collections.Generic.List[object]]::new()
$queryDurations = [Collections.Generic.List[double]]::new()
$blockers = [Collections.Generic.List[string]]::new()

foreach ($systemId in $systems) {
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $status = Invoke-StackPilotJson -Arguments @('status', '--output', 'json', $systemId)
    $metrics = Invoke-StackPilotJson -Arguments @('metrics', '--output', 'json', '--from', $from, $systemId)
    $timer.Stop()
    $queryDurations.Add($timer.Elapsed.TotalMilliseconds)
    if ($status.state -notin @('running', 'degraded')) {
        $blockers.Add("$systemId`:RUNTIME_NOT_ACTIVE")
    }
    foreach ($service in $status.services) {
        if ($service.state -ne 'ready') {
            continue
        }
        $series = @($metrics.series | Where-Object { $_.serviceId -eq $service.serviceId })
        $points = @($series | ForEach-Object { $_.points } | Sort-Object observedAt)
        $latest = $points | Select-Object -Last 1
        $metricStatus = if ($null -eq $latest) { 'missing' } else { [string]$latest.status }
        if ($points.Count -lt $MinimumPoints) {
            $blockers.Add("$systemId`:$($service.serviceId):INSUFFICIENT_POINTS")
        } elseif ($metricStatus -ne 'available') {
            $blockers.Add("$systemId`:$($service.serviceId):$metricStatus")
        }
        $serviceResults.Add([ordered]@{
            systemId = $systemId
            serviceId = $service.serviceId
            driver = $service.driver
            runtimeState = $service.state
            pointCount = $points.Count
            latestStatus = $metricStatus
            latestObservedAt = if ($null -eq $latest) { $null } else { $latest.observedAt }
        })
    }
}

$sortedDurations = @($queryDurations | Sort-Object)
$p95Index = [Math]::Max(0, [Math]::Ceiling($sortedDurations.Count * 0.95) - 1)
$report = [ordered]@{
    schemaVersion = 'ro04-installed-metrics/v1'
    generatedAt = [DateTimeOffset]::UtcNow.ToString('o')
    platform = 'windows/amd64'
    candidateVersion = $version.version
    candidateSha256 = $candidateHash
    capability = 'phase3.resource-monitoring'
    gateStatus = if ($blockers.Count -eq 0) { 'passed' } else { 'blocked' }
    activeSystems = $systems.Count
    readyServices = $serviceResults.Count
    minimumPoints = $MinimumPoints
    queryLatencyP95Ms = [Math]::Round($sortedDurations[$p95Index], 3)
    blockers = @($blockers)
    services = @($serviceResults)
}

$json = ($report | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
[IO.File]::WriteAllText($resolvedEvidence, "$json`n", [Text.UTF8Encoding]::new($false))
if ($blockers.Count -gt 0) {
    throw "RO-04 installed metrics Gate is blocked: $($blockers -join ', ')"
}
$report
