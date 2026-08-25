[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Candidate,

    [string]$DataDir = 'E:\StackPilot\.local\p2a05-data',
    [string]$WorkspaceRoot = 'E:\StackPilot\.local\p2a05-workspaces',
    [int]$Port = 32122
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Wait-ControlReady {
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        try {
            if ((Invoke-WebRequest -UseBasicParsing -Uri "$serverURL/health/ready" -TimeoutSec 2).StatusCode -eq 200) { return }
        }
        catch {
        }
        Start-Sleep -Milliseconds 100
    }
    throw 'P2A-05 control plane did not become ready'
}

function Invoke-CLIJSON {
    param([string[]]$Arguments, [int[]]$ExpectedExitCodes = @(0))
    $payload = & $candidatePath @Arguments 2>$null | Out-String
    if ($LASTEXITCODE -notin $ExpectedExitCodes) {
        throw "StackPilot command exited $LASTEXITCODE, expected $($ExpectedExitCodes -join ',')"
    }
    return $payload | ConvertFrom-Json
}

function Invoke-ControlJSON {
    param([string]$Path, [string]$Method = 'GET')
    $payload = & $controlPath --server $serverURL --data-dir $DataDir --method $Method --path $Path | Out-String
    if ($LASTEXITCODE -ne 0) { throw "Gate control request failed for $Path" }
    return $payload | ConvertFrom-Json
}

function Stop-VerifiedProcess {
    param($Process)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2A-05 process'
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

function Write-Manifest {
    param([string]$Name, [string]$Contents)
    $path = Join-Path $WorkspaceRoot $Name
    New-Item -ItemType Directory -Path (Join-Path $path '.stackpilot') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $path '.stackpilot\system.yaml'), $Contents, (New-Object Text.UTF8Encoding($false)))
    return $path
}

function Service-ByID {
    param($Status, [string]$ServiceID)
    return @($Status.services | Where-Object { $_.serviceId -eq $ServiceID })[0]
}

$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2a05-data'
$expectedWorkspaceRoot = Join-Path $repositoryRoot '.local\p2a05-workspaces'
if ($DataDir -ne $expectedDataDir -or $WorkspaceRoot -ne $expectedWorkspaceRoot) { throw 'P2A-05 Gate only permits its isolated roots' }
if ((Test-Path -LiteralPath $DataDir) -or (Test-Path -LiteralPath $WorkspaceRoot) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2A-05 Gate requires absent roots and an unused control port'
}

$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2a05-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2a05-server.err.log'
$controlPath = Join-Path $repositoryRoot '.local\bin\p2a05-control.exe'
$javaHome = Join-Path $WorkspaceRoot '.fixture-java'
$javaPath = Join-Path $javaHome 'bin\java.exe'
$server = $null
$previousJavaHome = $env:JAVA_HOME

New-Item -ItemType Directory -Path $DataDir, $WorkspaceRoot, (Split-Path $javaPath), (Split-Path $controlPath) -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2a05-gate'), 'owned', (New-Object Text.UTF8Encoding($false)))

$successManifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: p2a-completed-chain, name: P2A Completed Chain}
spec:
  ports:
    app: {protocol: tcp, preferred: 32123, fallbackRange: 32124-32130, conflictPolicy: auto, exposure: loopback}
  services:
    setup:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: .
      arguments: ["--mode", "immediate-exit", "--exit-code", "0"]
      stop: {gracefulTimeout: 1s}
    app:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: .
      arguments: ["--mode", "hold-port", "--port", "${ports.app}"]
      dependsOn: {setup: completed}
      stop: {gracefulTimeout: 1s}
      readiness: {type: tcp, host: 127.0.0.1, port: "${ports.app}", timeout: 10s, interval: 100ms}
'@
$failureManifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: p2a-completed-failure, name: P2A Completed Failure}
spec:
  services:
    setup:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: .
      arguments: ["--mode", "immediate-exit", "--exit-code", "23"]
      stop: {gracefulTimeout: 1s}
    app:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: .
      arguments: ["--mode", "ignore-terminate"]
      dependsOn: {setup: completed}
      stop: {gracefulTimeout: 1s}
      readiness: {type: process, timeout: 10s, interval: 100ms}
'@

$successWorkspace = Write-Manifest 'success' $successManifest
$failureWorkspace = Write-Manifest 'failure' $failureManifest

try {
    . (Join-Path $repositoryRoot 'scripts\lib\tooling.ps1')
    $go = Get-StackPilotGo
    & $go build -o $javaPath (Join-Path $repositoryRoot 'test\fixtures\process-fixture')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build P2A-05 Java Runner fixture' }
    & $go build -o $controlPath (Join-Path $repositoryRoot 'test\gates\p2a04-control')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build P2A-05 control helper' }
    $env:JAVA_HOME = $javaHome
    $server = Start-Process -FilePath $candidatePath -ArgumentList @('server', '--port', "$Port", '--data-dir', $DataDir) `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady
    $null = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $successWorkspace)
    $null = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $failureWorkspace)

    $first = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-completed-chain')
    if ($first.state -ne 'succeeded' -or @($first.steps | Where-Object { $_.key -eq 'wait-complete:setup' }).Count -ne 1) {
        throw 'completed chain did not succeed through wait-complete'
    }
    $firstStatus = Invoke-ControlJSON '/api/v1/systems/p2a-completed-chain/status'
    $firstSetup = Service-ByID $firstStatus 'setup'
    $firstApp = Service-ByID $firstStatus 'app'
    if ($firstSetup.state -ne 'completed' -or [uint32]$firstSetup.exitCode -ne 0 -or
        $firstApp.state -ne 'ready' -or $null -eq $firstApp.PSObject.Properties['pid']) {
        throw 'completed dependency did not release the daemon'
    }

    $repeat = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-completed-chain')
    if ($repeat.state -ne 'succeeded' -or @($repeat.steps | Where-Object { $_.state -ne 'skipped' }).Count -ne 0) {
        throw 'repeated start was not an idempotent skipped success'
    }
    $repeatStatus = Invoke-ControlJSON '/api/v1/systems/p2a-completed-chain/status'
    $repeatSetup = Service-ByID $repeatStatus 'setup'
    if ($repeatStatus.instanceId -ne $firstStatus.instanceId -or $repeatSetup.serviceInstanceId -ne $firstSetup.serviceInstanceId) {
        throw 'repeated start created a new instance or reran oneshot'
    }

    $restartRef = Invoke-ControlJSON '/api/v1/systems/p2a-completed-chain/restart' 'POST'
    $restart = Invoke-CLIJSON @('wait', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $restartRef.operationId)
    if ($restart.state -ne 'succeeded') { throw 'explicit completed-chain restart failed' }
    $restartStatus = Invoke-ControlJSON '/api/v1/systems/p2a-completed-chain/status'
    $restartSetup = Service-ByID $restartStatus 'setup'
    if ($restartStatus.instanceId -eq $firstStatus.instanceId -or $restartSetup.serviceInstanceId -eq $firstSetup.serviceInstanceId -or $restartSetup.state -ne 'completed') {
        throw 'explicit restart did not rerun oneshot in a fresh instance'
    }
    $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-completed-chain')

    $failure = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-completed-failure') @(4)
    if ($failure.state -ne 'failed' -or $failure.errorCode -ne 'PROCESS_EXITED') { throw 'failed setup Operation was invalid' }
    $failureStatus = Invoke-ControlJSON '/api/v1/systems/p2a-completed-failure/status'
    $failedSetup = Service-ByID $failureStatus 'setup'
    $blockedApp = Service-ByID $failureStatus 'app'
    if ($failedSetup.state -ne 'failed' -or [uint32]$failedSetup.exitCode -ne 23 -or $blockedApp.state -ne 'waiting_dependency') {
        throw 'failed setup did not freeze completed downstream'
    }
    $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-completed-failure')

    Stop-VerifiedProcess $server
    $server = $null
    $fixtureProcesses = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($WorkspaceRoot, [StringComparison]::OrdinalIgnoreCase) })
    if ($fixtureProcesses.Count -ne 0) { throw 'P2A-05 fixture process remained after Gate cleanup' }

    [ordered]@{
        firstOperationId = $first.id
        repeatedOperationId = $repeat.id
        restartOperationId = $restart.id
        failedOperationId = $failure.id
        firstInstanceId = $firstStatus.instanceId
        restartedInstanceId = $restartStatus.instanceId
        repeatedStart = 'all steps skipped; same service instance'
        failedDownstream = 'waiting_dependency'
    } | ConvertTo-Json
}
finally {
    $env:JAVA_HOME = $previousJavaHome
    Stop-VerifiedProcess $server
    Stop-GateSupervisors
    if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "P2A-05 control port $Port remained open"
    }
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2a05-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    if ((Test-Path -LiteralPath $WorkspaceRoot) -and $WorkspaceRoot -eq $expectedWorkspaceRoot) {
        Remove-Item -LiteralPath $WorkspaceRoot -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
}
