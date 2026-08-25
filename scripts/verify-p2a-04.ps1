[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Candidate,

    [string]$DataDir = 'E:\StackPilot\.local\p2a04-data',
    [string]$WorkspaceRoot = 'E:\StackPilot\.local\p2a04-workspaces',
    [int]$Port = 32121
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Wait-ControlReady {
    param([string]$URL)
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        try {
            if ((Invoke-WebRequest -UseBasicParsing -Uri "$URL/health/ready" -TimeoutSec 2).StatusCode -eq 200) { return }
        }
        catch {
        }
        Start-Sleep -Milliseconds 100
    }
    throw 'P2A-04 control plane did not become ready'
}

function Invoke-CLIJSON {
    param([string[]]$Arguments, [int[]]$ExpectedExitCodes = @(0))
    $payload = & $candidatePath @Arguments 2>$null | Out-String
    $exitCode = $LASTEXITCODE
    if ($exitCode -notin $ExpectedExitCodes) {
        throw "StackPilot command exited $exitCode, expected $($ExpectedExitCodes -join ',')"
    }
    return $payload | ConvertFrom-Json
}

function Invoke-ControlJSON {
    param([string]$Method, [string]$Path)
    $payload = & $controlPath --server $serverURL --data-dir $DataDir --method $Method --path $Path | Out-String
    if ($LASTEXITCODE -ne 0) { throw "Gate control request failed for $Path" }
    return $payload | ConvertFrom-Json
}

function Stop-VerifiedProcess {
    param($Process, [string]$ExpectedPath)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $ExpectedPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2A-04 process'
    }
    Stop-Process -Id $Process.Id -Force -ErrorAction Stop
    Wait-Process -Id $Process.Id -Timeout 10 -ErrorAction SilentlyContinue
}

function Stop-GateSupervisors {
    if (-not (Test-Path -LiteralPath (Join-Path $DataDir 'instances'))) { return }
    foreach ($identityFile in Get-ChildItem -LiteralPath (Join-Path $DataDir 'instances') -Filter supervisor.json -File -Recurse) {
        $identity = Get-Content -Raw -LiteralPath $identityFile.FullName | ConvertFrom-Json
        $process = Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
        if ($null -eq $process) { continue }
        if (-not [string]::Equals($process.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'refusing to stop a Supervisor with mismatched identity'
        }
        Stop-Process -Id $identity.pid -Force -ErrorAction Stop
        Wait-Process -Id $identity.pid -Timeout 10 -ErrorAction SilentlyContinue
    }
}

function Write-WorkspaceManifest {
    param([string]$Name, [string]$Contents)
    $path = Join-Path $WorkspaceRoot $Name
    New-Item -ItemType Directory -Path (Join-Path $path '.stackpilot') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $path '.stackpilot\system.yaml'), $Contents, (New-Object Text.UTF8Encoding($false)))
    return $path
}

function Assert-OneshotStatus {
    param([string]$SystemID, [string]$State, [uint32]$ExitCode)
    $status = Invoke-ControlJSON 'GET' "/api/v1/systems/$SystemID/status"
	$pidProperty = $status.services[0].PSObject.Properties['pid']
    if ($status.services.Count -ne 1 -or $status.services[0].state -ne $State -or
        [uint32]$status.services[0].exitCode -ne $ExitCode -or ($null -ne $pidProperty -and $null -ne $pidProperty.Value)) {
        throw "unexpected $SystemID status: $($status | ConvertTo-Json -Depth 8 -Compress)"
    }
    return $status
}

function Wait-ForActivePID {
    param([string]$SystemID)
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        $status = Invoke-ControlJSON 'GET' "/api/v1/systems/$SystemID/status"
        $pidProperty = $status.services[0].PSObject.Properties['pid']
        if ($status.services.Count -eq 1 -and $null -ne $pidProperty -and $null -ne $pidProperty.Value) { return }
        Start-Sleep -Milliseconds 50
    }
    throw "$SystemID did not reach an active process state"
}

$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2a04-data'
$expectedWorkspaceRoot = Join-Path $repositoryRoot '.local\p2a04-workspaces'
if ($DataDir -ne $expectedDataDir -or $WorkspaceRoot -ne $expectedWorkspaceRoot) {
    throw 'P2A-04 Gate only permits its isolated roots'
}
if ((Test-Path -LiteralPath $DataDir) -or (Test-Path -LiteralPath $WorkspaceRoot) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2A-04 Gate requires absent roots and an unused control port'
}

$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2a04-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2a04-server.err.log'
$controlPath = Join-Path $repositoryRoot '.local\bin\p2a04-control.exe'
$javaHome = Join-Path $WorkspaceRoot '.fixture-java'
$javaPath = Join-Path $javaHome 'bin\java.exe'
$server = $null
$previousJavaHome = $env:JAVA_HOME

New-Item -ItemType Directory -Path $DataDir, $WorkspaceRoot, (Split-Path $javaPath), (Split-Path $controlPath) -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2a04-gate'), 'owned', (New-Object Text.UTF8Encoding($false)))

$successManifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: p2a-oneshot-success, name: P2A Oneshot Success}
spec:
  policies: {startTimeout: 5s}
  services:
    task:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: .
      arguments: ["--mode", "immediate-exit", "--exit-code", "0"]
      stop: {gracefulTimeout: 1s}
'@
$failureManifest = $successManifest.Replace('p2a-oneshot-success', 'p2a-oneshot-failure').Replace('P2A Oneshot Success', 'P2A Oneshot Failure').Replace('"0"', '"23"')
$timeoutManifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: p2a-oneshot-timeout, name: P2A Oneshot Timeout}
spec:
  policies: {startTimeout: 1s}
  services:
    task:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: .
      arguments: ["--mode", "ignore-terminate"]
      stop: {gracefulTimeout: 1s}
'@
$cancelManifest = $timeoutManifest.Replace('p2a-oneshot-timeout', 'p2a-oneshot-cancel').Replace('P2A Oneshot Timeout', 'P2A Oneshot Cancel').Replace('startTimeout: 1s', 'startTimeout: 10s')

$workspaces = [ordered]@{
    success = Write-WorkspaceManifest 'success' $successManifest
    failure = Write-WorkspaceManifest 'failure' $failureManifest
    timeout = Write-WorkspaceManifest 'timeout' $timeoutManifest
    cancel = Write-WorkspaceManifest 'cancel' $cancelManifest
}

try {
    . (Join-Path $repositoryRoot 'scripts\lib\tooling.ps1')
    $go = Get-StackPilotGo
    & $go build -o $javaPath (Join-Path $repositoryRoot 'test\fixtures\process-fixture')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build P2A-04 Java Runner fixture' }
    & $go build -o $controlPath (Join-Path $repositoryRoot 'test\gates\p2a04-control')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build P2A-04 control helper' }
    $env:JAVA_HOME = $javaHome
    $server = Start-Process -FilePath $candidatePath -ArgumentList @('server', '--port', "$Port", '--data-dir', $DataDir) `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady $serverURL

    foreach ($path in $workspaces.Values) {
        $null = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $path)
    }

    $success = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-success')
    if ($success.state -ne 'succeeded' -or $success.steps[4].key -ne 'wait-complete:task') { throw 'successful oneshot Operation was invalid' }
    $null = Assert-OneshotStatus 'p2a-oneshot-success' 'completed' 0
    $logs = & $candidatePath logs --server $serverURL --data-dir $DataDir --output json p2a-oneshot-success/task | Out-String
    if ($LASTEXITCODE -ne 0 -or -not $logs.Contains('oneshot stdout before exit') -or -not $logs.Contains('oneshot stderr before exit')) {
        throw 'successful oneshot logs were not durably preserved'
    }
    $successStop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-success')
    if ($successStop.state -ne 'succeeded') { throw 'stopping Completed oneshot failed' }

    $failure = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-failure') @(4)
    if ($failure.state -ne 'failed' -or $failure.errorCode -ne 'PROCESS_EXITED') { throw 'nonzero oneshot failure was invalid' }
    $null = Assert-OneshotStatus 'p2a-oneshot-failure' 'failed' 23
    $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-failure')

    $timeout = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-timeout') @(4)
    if ($timeout.state -ne 'failed' -or $timeout.errorCode -ne 'HEALTH_READINESS_TIMEOUT') { throw 'oneshot timeout was invalid' }
    $null = Assert-OneshotStatus 'p2a-oneshot-timeout' 'failed' 137
    $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-oneshot-timeout')

    $cancel = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'p2a-oneshot-cancel')
    Wait-ForActivePID 'p2a-oneshot-cancel'
    $null = Invoke-ControlJSON 'POST' "/api/v1/operations/$($cancel.operationId)/cancel"
    $cancelled = Invoke-CLIJSON @('wait', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $cancel.operationId) @(4)
    if ($cancelled.state -ne 'cancelled') { throw 'oneshot cancellation did not complete' }
    $cancelStatus = Invoke-ControlJSON 'GET' '/api/v1/systems/p2a-oneshot-cancel/status'
    if ($cancelStatus.state -ne 'stopped' -or $cancelStatus.services.Count -ne 0) { throw 'cancelled oneshot remained active' }

    $version = Invoke-RestMethod -Uri "$serverURL/version" -Method Get -TimeoutSec 5
    if ('phase2.oneshot' -notin $version.capabilities) { throw 'phase2.oneshot capability was not advertised' }
    Stop-VerifiedProcess $server $candidatePath
    $server = $null

    $fixtureProcesses = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($WorkspaceRoot, [StringComparison]::OrdinalIgnoreCase) })
    if ($fixtureProcesses.Count -ne 0) { throw 'P2A-04 fixture process remained after Gate cleanup' }

    [ordered]@{
        successOperationId = $success.id
        nonzeroOperationId = $failure.id
        timeoutOperationId = $timeout.id
        cancelledOperationId = $cancel.operationId
        successExitCode = 0
        nonzeroExitCode = 23
        timeoutExitCode = 137
        logs = 'stdout+stderr preserved'
        capability = 'phase2.oneshot'
    } | ConvertTo-Json
}
finally {
    $env:JAVA_HOME = $previousJavaHome
    Stop-VerifiedProcess $server $candidatePath
    Stop-GateSupervisors
    if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "P2A-04 control port $Port remained open"
    }
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2a04-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    if ((Test-Path -LiteralPath $WorkspaceRoot) -and $WorkspaceRoot -eq $expectedWorkspaceRoot) {
        Remove-Item -LiteralPath $WorkspaceRoot -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
}
