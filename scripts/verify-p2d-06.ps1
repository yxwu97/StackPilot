[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Candidate,
    [string]$PMSWorkspace = 'E:\PMSystem',
    [string]$BTCWorkspace = 'E:\BidTravelCloud',
    [string]$DataDir = 'E:\StackPilot\.local\p2d06-data',
    [int]$Port = 32142
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$pmsPath = (Resolve-Path -LiteralPath $PMSWorkspace).Path
$btcPath = (Resolve-Path -LiteralPath $BTCWorkspace).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2d06-data'
$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2d06-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2d06-server.err.log'
$server = $null
$pmsWorkspaceRecord = $null
$btcWorkspaceRecord = $null

function Wait-ControlReady {
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        try {
            if ((Invoke-WebRequest -UseBasicParsing -Uri "$serverURL/health/ready" -TimeoutSec 2).StatusCode -eq 200) { return }
        } catch {}
        Start-Sleep -Milliseconds 200
    }
    throw 'P2D-06 control plane did not become ready'
}

function Invoke-CLIJSON {
    param([string[]]$Arguments, [int[]]$ExpectedExitCodes = @(0))
    $payload = & $candidatePath @Arguments 2>$null | Out-String
    if ($LASTEXITCODE -notin $ExpectedExitCodes) {
        $commandKind = @($Arguments | Select-Object -First 2) -join ' '
        $summary = 'no structured result'
        try {
            $result = $payload | ConvertFrom-Json
            $failedSteps = @($result.steps | Where-Object state -eq 'failed' | ForEach-Object key) -join ','
            $summary = "state=$($result.state), errorCode=$($result.errorCode), failedSteps=$failedSteps"
        } catch {}
        throw "StackPilot $commandKind exited $LASTEXITCODE, expected $($ExpectedExitCodes -join ','): $summary"
    }
    return $payload | ConvertFrom-Json
}

function Read-DotEnvValue {
    param([string]$Path, [string]$Name)
    foreach ($line in [IO.File]::ReadLines($Path, [Text.Encoding]::UTF8)) {
        if (-not $line.StartsWith("$Name=", [StringComparison]::Ordinal)) { continue }
        $value = $line.Substring($Name.Length + 1).Trim()
        if ($value.Length -ge 2 -and (($value[0] -eq '"' -and $value[-1] -eq '"') -or
            ($value[0] -eq "'" -and $value[-1] -eq "'"))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        if ($value.Length -eq 0) { throw "Required local setting $Name is empty" }
        return $value
    }
    throw "Required local setting $Name is missing"
}

function Set-PMSSecret {
    param([string]$SecretName, [string]$EnvironmentName)
    $environmentFile = Join-Path $pmsPath 'pmsystem-rag\.env'
    $value = Read-DotEnvValue $environmentFile $EnvironmentName
    try {
        $result = $value | & $candidatePath secret set --server $serverURL --data-dir $DataDir `
            --output json pms $SecretName | Out-String
        if ($LASTEXITCODE -ne 0 -or $result.Contains($value)) {
            throw "Secret metadata write failed for $SecretName"
        }
    } finally { $value = $null }
}

function Start-GateServer {
    $process = Start-Process -FilePath $candidatePath -ArgumentList @(
        'server', '--port', "$Port", '--data-dir', $DataDir,
        '--reconcile-interval', '10s', '--lease-reconcile-interval', '30s'
    ) -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady
    return $process
}

function Stop-VerifiedServer {
    param($Process)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2D-06 Server process'
    }
    Stop-Process -Id $Process.Id -Force
    Wait-Process -Id $Process.Id -Timeout 15 -ErrorAction SilentlyContinue
}

function Stop-GateSupervisors {
    $root = Join-Path $DataDir 'instances'
    if (-not (Test-Path -LiteralPath $root)) { return }
    foreach ($file in Get-ChildItem -LiteralPath $root -Filter supervisor.json -File -Recurse) {
        $identity = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
        $process = Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
        if ($null -eq $process) { continue }
        if (-not [string]::Equals($process.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'refusing to stop a P2D-06 Supervisor with mismatched identity'
        }
        Stop-Process -Id $identity.pid -Force
        Wait-Process -Id $identity.pid -Timeout 15 -ErrorAction SilentlyContinue
    }
}

function Get-PortValue {
    param($RuntimeStatus, [string]$LogicalName)
    $match = @($RuntimeStatus.ports | Where-Object logicalName -eq $LogicalName)
    if ($match.Count -ne 1) { throw "Missing planned port $LogicalName" }
    return [int]$match[0].port
}

function Assert-ServiceStates {
    param($RuntimeStatus, [string[]]$ServiceIDs)
    foreach ($serviceID in $ServiceIDs) {
        $service = @($RuntimeStatus.services | Where-Object serviceId -eq $serviceID)
        if ($service.Count -ne 1 -or $service[0].state -ne 'ready') {
            throw "Unexpected service state for $serviceID"
        }
    }
}

function Assert-PMSReadiness {
    param($RuntimeStatus)
    Assert-ServiceStates $RuntimeStatus @('backend', 'rag', 'web')
    $backendPort = Get-PortValue $RuntimeStatus 'backend'
    $ragPort = Get-PortValue $RuntimeStatus 'rag'
    $webPort = Get-PortValue $RuntimeStatus 'web'
    $backend = Invoke-RestMethod -Uri "http://127.0.0.1:$backendPort/actuator/health" -TimeoutSec 10
    $rag = Invoke-RestMethod -Uri "http://127.0.0.1:$ragPort/health" -TimeoutSec 10
    $web = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$webPort" -TimeoutSec 10
    if ($backend.status -ne 'UP' -or $rag.status -ne 'ok' -or -not $rag.qdrant -or $web.StatusCode -ne 200) {
        throw 'PMS endpoint readiness did not match the real service state'
    }
    if ($webPort -eq 5173 -or $backendPort -eq 5173 -or $ragPort -eq 5173) {
        throw 'PMS selected the prohibited port 5173'
    }
}

function Assert-BTCReadiness {
    param($RuntimeStatus)
    Assert-ServiceStates $RuntimeStatus @('backend', 'web')
    $backendPort = Get-PortValue $RuntimeStatus 'backend'
    $webPort = Get-PortValue $RuntimeStatus 'web'
    if ((Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$backendPort/actuator/health" -TimeoutSec 10).StatusCode -ne 200 -or
        (Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$webPort" -TimeoutSec 10).StatusCode -ne 200) {
        throw 'BTC endpoint readiness failed during the dual-system Gate'
    }
}

function Assert-PMSLogs {
    foreach ($target in @('pms/backend', 'pms/rag', 'pms/web')) {
        $entries = @(Invoke-CLIJSON @('logs', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $target))
        if ($entries.Count -eq 0) { throw "No managed logs captured for $target" }
    }
}

function Read-SharedFileBytes {
    param([string]$Path)
    $share = [IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, $share)
    $memory = [IO.MemoryStream]::new()
    try {
        $stream.CopyTo($memory)
        return $memory.ToArray()
    } finally {
        $memory.Dispose()
        $stream.Dispose()
    }
}

function Assert-NoSecretCopies {
    $environmentFile = Join-Path $pmsPath 'pmsystem-rag\.env'
    foreach ($name in @('RAG_SERVICE_TOKEN', 'DASHSCOPE_API_KEY')) {
        $value = Read-DotEnvValue $environmentFile $name
        try {
            foreach ($file in Get-ChildItem -LiteralPath $DataDir -File -Recurse) {
                if ($file.Length -eq 0 -or $file.Length -gt 10MB) { continue }
                $text = [Text.Encoding]::UTF8.GetString((Read-SharedFileBytes $file.FullName))
                if ($text.Contains($value)) { throw "Secret value was copied into $($file.Name)" }
            }
        } finally { $value = $null }
    }
}

function Assert-ExternalDependencies {
    foreach ($externalPort in @(3306, 6379, 6333)) {
        if (@(Get-NetTCPConnection -LocalPort $externalPort -State Listen -ErrorAction SilentlyContinue).Count -eq 0) {
            throw "Required PMS external dependency is not listening on port $externalPort"
        }
    }
}

if ($DataDir -ne $expectedDataDir -or (Test-Path -LiteralPath $DataDir) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0 -or
    @(Get-NetTCPConnection -LocalPort 32102 -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2D-06 Gate requires its absent isolated data root and unused control/preferred Web ports'
}

Assert-ExternalDependencies
New-Item -ItemType Directory -Path $DataDir | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2d06-gate'), 'owned', [Text.UTF8Encoding]::new($false))
try {
    $server = Start-GateServer
    Set-PMSSecret 'pms-rag-service-token' 'RAG_SERVICE_TOKEN'
    Set-PMSSecret 'pms-dashscope-api-key' 'DASHSCOPE_API_KEY'
    $btcWorkspaceRecord = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', $btcPath)
    $pmsWorkspaceRecord = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', $pmsPath)

    $btcStart = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'btc')
    if ($btcStart.state -ne 'succeeded') { throw "BTC start ended $($btcStart.state): $($btcStart.errorCode)" }
    $btcStatus = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc')
    Assert-BTCReadiness $btcStatus
    if ((Get-PortValue $btcStatus 'web') -ne 32102) { throw 'BTC did not acquire the shared preferred Web port' }

    $pmsStart = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'pms')
    if ($pmsStart.state -ne 'succeeded') { throw "PMS start ended $($pmsStart.state): $($pmsStart.errorCode)" }
    $pmsStatus = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'pms')
    Assert-PMSReadiness $pmsStatus
    $fallbackWebPort = Get-PortValue $pmsStatus 'web'
    if ($fallbackWebPort -lt 32400 -or $fallbackWebPort -gt 32599) { throw 'PMS did not resolve the preferred Web port conflict' }
    Assert-BTCReadiness (Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc'))
    Assert-PMSLogs

    $repeat = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'pms')
    if ($repeat.state -ne 'succeeded' -or @($repeat.steps | Where-Object state -ne 'skipped').Count -ne 0) {
        throw 'PMS repeated start was not an idempotent skipped success'
    }

    $firstStop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'pms')
    if ($firstStop.state -ne 'succeeded') { throw 'PMS first stop failed' }
    Assert-BTCReadiness (Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc'))

    $stickyStart = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'pms')
    if ($stickyStart.state -ne 'succeeded') { throw 'PMS sticky restart failed' }
    $stickyStatus = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'pms')
    Assert-PMSReadiness $stickyStatus
    if ((Get-PortValue $stickyStatus 'web') -ne $fallbackWebPort) { throw 'PMS sticky Web port was not reused' }
    Assert-NoSecretCopies

    $pmsStop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'pms')
    $btcStop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'btc')
    if ($pmsStop.state -ne 'succeeded' -or $btcStop.state -ne 'succeeded') { throw 'Dual-system stop failed' }
    [ordered]@{
        btcWorkspaceId = $btcWorkspaceRecord.id; pmsWorkspaceId = $pmsWorkspaceRecord.id
        btcInstanceId = $btcStatus.instanceId; pmsFirstInstanceId = $pmsStatus.instanceId
        pmsStickyInstanceId = $stickyStatus.instanceId; btcWebPort = 32102; pmsWebPort = $fallbackWebPort
        pmsStartOperationId = $pmsStart.id; pmsRepeatOperationId = $repeat.id
        pmsStickyOperationId = $stickyStart.id; services = 3; readiness = 'backend/rag/web ready'
    } | ConvertTo-Json
} finally {
    if ($null -ne $server) {
        foreach ($systemID in @('pms', 'btc')) {
            try { $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', $systemID) @(0, 4) } catch {}
        }
    }
    Stop-VerifiedServer $server
    Stop-GateSupervisors
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2d06-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
}
