[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Candidate,

    [string]$Python = 'C:\Python314\python.exe',
    [string]$DataDir = 'E:\StackPilot\.local\p2a06-data',
    [string]$WorkspaceRoot = 'E:\StackPilot\.local\p2a06-workspaces',
    [int]$Port = 32131
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
    throw 'P2A-06 control plane did not become ready'
}

function Invoke-CLIJSON {
    param([string[]]$Arguments, [int[]]$ExpectedExitCodes = @(0))
    $payload = & $candidatePath @Arguments 2>$null | Out-String
    if ($LASTEXITCODE -notin $ExpectedExitCodes) {
        throw "StackPilot command exited $LASTEXITCODE, expected $($ExpectedExitCodes -join ',')"
    }
    return $payload | ConvertFrom-Json
}

function Stop-VerifiedProcess {
    param($Process)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2A-06 process'
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

function Wait-PythonRecord {
    param([string]$Target, [string]$Role)
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        $entries = @(Invoke-CLIJSON @('logs', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $Target))
        foreach ($entry in $entries) {
            try {
                $record = $entry.message | ConvertFrom-Json
                if ($record.role -eq $Role) { return $record }
            }
            catch {
            }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Python $Role identity log did not arrive"
}

$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$pythonPath = (Resolve-Path -LiteralPath $Python).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2a06-data'
$expectedWorkspaceRoot = Join-Path $repositoryRoot '.local\p2a06-workspaces'
if ($DataDir -ne $expectedDataDir -or $WorkspaceRoot -ne $expectedWorkspaceRoot) { throw 'P2A-06 Gate only permits its isolated roots' }
if ((Test-Path -LiteralPath $DataDir) -or (Test-Path -LiteralPath $WorkspaceRoot) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2A-06 Gate requires absent roots and an unused control port'
}

$serverURL = "http://127.0.0.1:$Port"
$appPort = 32132
$stdoutPath = Join-Path $repositoryRoot '.local\p2a06-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2a06-server.err.log'
$workspace = Join-Path $WorkspaceRoot 'python-service'
$venv = Join-Path $workspace '.venv'
$server = $null

New-Item -ItemType Directory -Path $DataDir, (Join-Path $workspace '.stackpilot') -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2a06-gate'), 'owned', (New-Object Text.UTF8Encoding($false)))

$pythonSource = @'
import http.server
import json
import os
import sys

def identity(role):
    print(json.dumps({"role": role, "executable": os.path.realpath(sys.executable), "prefix": os.path.realpath(sys.prefix)}), flush=True)

if sys.argv[1] == "setup":
    identity("setup")
    raise SystemExit(0)

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            self.send_error(404)
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, format, *args):
        return

identity("daemon")
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[2])), Handler).serve_forever()
'@
[IO.File]::WriteAllText((Join-Path $workspace 'gate.py'), $pythonSource, (New-Object Text.UTF8Encoding($false)))

$manifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: p2a-python-venv, name: P2A Python Venv}
spec:
  ports:
    app: {protocol: tcp, preferred: 32132, fallbackRange: 32133-32139, conflictPolicy: auto, exposure: loopback}
  services:
    setup:
      driver: process
      mode: oneshot
      runner: python-venv
      virtualEnvironment: .venv
      workingDirectory: .
      arguments: ["gate.py", "setup"]
      stop: {gracefulTimeout: 1s}
    app:
      driver: process
      mode: daemon
      runner: python-venv
      virtualEnvironment: .venv
      workingDirectory: .
      arguments: ["gate.py", "serve", "${ports.app}"]
      dependsOn: {setup: completed}
      stop: {gracefulTimeout: 1s}
      readiness: {type: http, url: "http://127.0.0.1:${ports.app}/health", timeout: 10s, interval: 100ms}
'@
[IO.File]::WriteAllText((Join-Path $workspace '.stackpilot\system.yaml'), $manifest, (New-Object Text.UTF8Encoding($false)))

try {
    & $pythonPath -m venv $venv
    if ($LASTEXITCODE -ne 0) { throw 'failed to create P2A-06 virtual environment' }
    $venvPython = (Resolve-Path -LiteralPath (Join-Path $venv 'Scripts\python.exe')).Path
    $venvRoot = (Resolve-Path -LiteralPath $venv).Path

    $server = Start-Process -FilePath $candidatePath -ArgumentList @('server', '--port', "$Port", '--data-dir', $DataDir) `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady
    $null = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $workspace)
    $operation = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-python-venv')
    if ($operation.state -ne 'succeeded' -or @($operation.steps | Where-Object { $_.key -eq 'wait-complete:setup' }).Count -ne 1) {
        throw 'Python venv system did not complete its start Operation'
    }
    $status = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'p2a-python-venv')
    $setup = @($status.services | Where-Object { $_.serviceId -eq 'setup' })[0]
    $app = @($status.services | Where-Object { $_.serviceId -eq 'app' })[0]
    if ($setup.state -ne 'completed' -or $app.state -ne 'ready') { throw 'Python services did not reach Completed/Ready' }

    $setupRecord = Wait-PythonRecord 'p2a-python-venv/setup' 'setup'
    $daemonRecord = Wait-PythonRecord 'p2a-python-venv/app' 'daemon'
    foreach ($record in @($setupRecord, $daemonRecord)) {
        if (-not [string]::Equals($record.executable, $venvPython, [StringComparison]::OrdinalIgnoreCase) -or
            -not [string]::Equals($record.prefix, $venvRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "service escaped the selected virtual environment: $($record | ConvertTo-Json -Compress)"
        }
    }
    $version = Invoke-RestMethod -Uri "$serverURL/version" -Method Get -TimeoutSec 5
    if ('phase2.python-venv' -notin $version.capabilities) { throw 'phase2.python-venv capability was not advertised' }

    $stop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-python-venv')
    if ($stop.state -ne 'succeeded') { throw 'Python venv system stop failed' }
    Stop-VerifiedProcess $server
    $server = $null
    $fixtureProcesses = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($WorkspaceRoot, [StringComparison]::OrdinalIgnoreCase) })
    if ($fixtureProcesses.Count -ne 0) { throw 'P2A-06 virtual environment process remained after stop' }

    [ordered]@{
        operationId = $operation.id
        instanceId = $status.instanceId
        pythonVersion = (& $venvPython --version)
        executable = $venvPython
        setupState = $setup.state
        appState = $app.state
        capability = 'phase2.python-venv'
    } | ConvertTo-Json
}
finally {
    Stop-VerifiedProcess $server
    Stop-GateSupervisors
    foreach ($gatePort in @($Port, $appPort, 32133, 32134, 32135, 32136, 32137, 32138, 32139)) {
        if (@(Get-NetTCPConnection -LocalPort $gatePort -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
            throw "P2A-06 port $gatePort remained open"
        }
    }
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2a06-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    if ((Test-Path -LiteralPath $WorkspaceRoot) -and $WorkspaceRoot -eq $expectedWorkspaceRoot) {
        Remove-Item -LiteralPath $WorkspaceRoot -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
}
