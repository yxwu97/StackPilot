[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$CandidateV1,

    [Parameter(Mandatory)]
    [string]$CandidateV2,

    [string]$Workspace = 'E:\BidTravelCloud',
    [string]$InstallDir = 'E:\StackPilot\.local\p1d11-install',
    [string]$DataDir = 'E:\StackPilot\.local\p1d11-data',
    [string]$RegistrationName = 'StackPilot-P1D11',
    [int]$Port = 32107
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-StackPilotJSON {
    param([string]$Executable, [string[]]$Arguments)

    $payload = & $Executable @Arguments | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "$Executable $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
    return $payload | ConvertFrom-Json
}

function Get-ServicePIDs {
    param($Status)

    $result = @{}
    foreach ($service in $Status.services) {
        if ($null -eq $service.pid) {
            throw "service $($service.serviceId) has no PID"
        }
        $result[$service.serviceId] = [int]$service.pid
    }
    return $result
}

function Assert-SameServicePIDs {
    param([hashtable]$Expected, $Status)

    $actual = Get-ServicePIDs $Status
    foreach ($name in $Expected.Keys) {
        if (-not $actual.ContainsKey($name) -or $actual[$name] -ne $Expected[$name]) {
            throw "service PID changed for $name"
        }
    }
}

function Assert-RuntimeReady {
    param($Status)

    if ($Status.state -ne 'running') {
        throw "system state is $($Status.state), want running"
    }
    foreach ($service in $Status.services) {
        if ($service.state -ne 'ready') {
            throw "service $($service.serviceId) state is $($service.state), want ready"
        }
    }
}

function Get-LastSequence {
    param([string]$Executable, [string]$ServerURL, [string]$Target)

    $entries = Invoke-StackPilotJSON $Executable @(
        'logs', '--server', $ServerURL, '--data-dir', $DataDir, '--output', 'json', $Target
    )
    if ($entries.Count -eq 0) {
        throw "no durable logs found for $Target"
    }
    return [int64]$entries[-1].sequence
}

function Assert-BTCReady {
    param($Status)

    $ports = @{}
    foreach ($entry in $Status.ports) {
        $ports[$entry.logicalName] = [int]$entry.port
    }
    if (-not $ports.ContainsKey('backend') -or -not $ports.ContainsKey('web')) {
        throw 'BTC runtime did not publish backend and web ports'
    }
    $backend = Invoke-RestMethod -Uri "http://127.0.0.1:$($ports.backend)/actuator/health" -TimeoutSec 5
    $web = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$($ports.web)" -TimeoutSec 5
    if ($backend.status -ne 'UP' -or $web.StatusCode -ne 200) {
        throw 'BTC endpoints are not ready'
    }
    return $ports
}

function Wait-ControlPortClosed {
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "control port $Port remained open"
}

$v1 = (Resolve-Path -LiteralPath $CandidateV1).Path
$v2 = (Resolve-Path -LiteralPath $CandidateV2).Path
$workspaceRoot = (Resolve-Path -LiteralPath $Workspace).Path
if ((Test-Path -LiteralPath $InstallDir) -or (Test-Path -LiteralPath $DataDir)) {
    throw 'P1D-11 requires absent install and data roots'
}
if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw "control port $Port is already occupied"
}
$serverURL = "http://127.0.0.1:$Port"

$installedV1 = Invoke-StackPilotJSON $v1 @(
    'service', 'install', '--install-dir', $InstallDir, '--data-dir', $DataDir,
    '--task-name', $RegistrationName, '--port', "$Port", '--output', 'json'
)
$workspaceRecord = Invoke-StackPilotJSON $v1 @(
    'workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $workspaceRoot
)
$startOperation = Invoke-StackPilotJSON $v1 @(
    'up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'btc'
)
if ($startOperation.state -ne 'succeeded') {
    throw "BTC start ended $($startOperation.state)"
}
$beforeCrash = Invoke-StackPilotJSON $v1 @(
    'status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc'
)
$null = Assert-RuntimeReady $beforeCrash
$servicePIDs = Get-ServicePIDs $beforeCrash
$ports = Assert-BTCReady $beforeCrash
$sequenceBefore = Get-LastSequence $v1 $serverURL 'btc/backend'

$controlProcess = Get-Process -Id $installedV1.pid -ErrorAction Stop
if (-not [string]::Equals($controlProcess.Path, $installedV1.executablePath, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'refusing to terminate an unverified control-plane PID'
}
Stop-Process -Id $installedV1.pid -Force -ErrorAction Stop
Wait-Process -Id $installedV1.pid -ErrorAction SilentlyContinue
Wait-ControlPortClosed
foreach ($pidValue in $servicePIDs.Values) {
    if ($null -eq (Get-Process -Id $pidValue -ErrorAction SilentlyContinue)) {
        throw "managed PID $pidValue exited with the control plane"
    }
}
$null = Assert-BTCReady $beforeCrash

$restartedV1 = Invoke-StackPilotJSON $v1 @('service', 'start', '--install-dir', $InstallDir, '--output', 'json')
$afterRestart = Invoke-StackPilotJSON $v1 @(
    'status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc'
)
$null = Assert-RuntimeReady $afterRestart
Assert-SameServicePIDs $servicePIDs $afterRestart
$sequenceAfter = Get-LastSequence $v1 $serverURL 'btc/backend'
if ($sequenceAfter -lt $sequenceBefore) {
    throw 'durable log sequence regressed after control-plane restart'
}

$upgradedV2 = Invoke-StackPilotJSON $v2 @('service', 'upgrade', '--install-dir', $InstallDir, '--output', 'json')
$afterUpgrade = Invoke-StackPilotJSON $v2 @(
    'status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'btc'
)
$null = Assert-RuntimeReady $afterUpgrade
Assert-SameServicePIDs $servicePIDs $afterUpgrade
$null = Assert-BTCReady $afterUpgrade

$stopOperation = Invoke-StackPilotJSON $v2 @(
    'down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'btc'
)
if ($stopOperation.state -ne 'succeeded') {
    throw "BTC stop ended $($stopOperation.state)"
}
foreach ($pidValue in $servicePIDs.Values) {
    if ($null -ne (Get-Process -Id $pidValue -ErrorAction SilentlyContinue)) {
        throw "managed PID $pidValue survived system stop"
    }
}
foreach ($portValue in $ports.Values) {
    if (@(Get-NetTCPConnection -LocalPort $portValue -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "managed port $portValue survived system stop"
    }
}

$uninstalled = Invoke-StackPilotJSON $v2 @('service', 'uninstall', '--install-dir', $InstallDir, '--output', 'json')
$runKey = Get-ItemProperty -LiteralPath 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runProperty = $runKey.PSObject.Properties[$RegistrationName]
$runValue = if ($null -eq $runProperty) { $null } else { $runProperty.Value }
[ordered]@{
    candidateV1 = $installedV1.version
    candidateV2 = $upgradedV2.version
    workspaceId = $workspaceRecord.id
    instanceId = $beforeCrash.instanceId
    startOperationId = $startOperation.id
    stopOperationId = $stopOperation.id
    controlPIDBeforeCrash = $installedV1.pid
    controlPIDAfterRestart = $restartedV1.pid
    controlPIDAfterUpgrade = $upgradedV2.pid
    servicePIDs = $servicePIDs
    ports = $ports
    logSequenceBeforeRestart = $sequenceBefore
    logSequenceAfterRestart = $sequenceAfter
    uninstallState = $uninstalled.state
    installRemoved = -not (Test-Path -LiteralPath $InstallDir)
    dataPreserved = Test-Path -LiteralPath $DataDir
    startupRegistrationRemoved = $null -eq $runValue
    controlPortClosed = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -eq 0
} | ConvertTo-Json -Depth 6
