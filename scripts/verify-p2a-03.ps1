[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Candidate,

    [string]$DataDir = 'E:\StackPilot\.local\p2a03-data',
    [string]$Workspace = 'E:\StackPilot\.local\p2a03-workspace',
    [int]$Port = 32110
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
    throw 'P2A-03 control plane did not become ready'
}

function Invoke-StackPilotJSON {
    param([string[]]$Arguments)
    $payload = & $candidatePath @Arguments | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot command failed with exit code $LASTEXITCODE"
    }
    return $payload | ConvertFrom-Json
}

function Stop-VerifiedProcess {
    param($Process, [string]$ExpectedPath)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $ExpectedPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2A-03 process'
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

function Wait-FileNonEmpty {
    param([string]$Path)
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        if ((Test-Path -LiteralPath $Path) -and (Get-Item -LiteralPath $Path).Length -gt 0) { return }
        Start-Sleep -Milliseconds 100
    }
    throw "no follower output appeared in $Path"
}

function Assert-NoPlaintext {
    param([string]$Value)
    foreach ($file in Get-ChildItem -LiteralPath $DataDir -File -Recurse) {
        $stream = New-Object IO.FileStream(
            $file.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read,
            ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete)
        )
        $memory = New-Object IO.MemoryStream
        $payload = $null
        try {
            $stream.CopyTo($memory)
            $payload = $memory.ToArray()
            if ([Text.Encoding]::UTF8.GetString($payload).Contains($Value)) {
                throw "plaintext test Secret found in $($file.FullName)"
            }
        }
        finally {
            if ($null -ne $payload) { [Array]::Clear($payload, 0, $payload.Length) }
            $memory.Dispose()
            $stream.Dispose()
        }
    }
}

$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2a03-data'
$expectedWorkspace = Join-Path $repositoryRoot '.local\p2a03-workspace'
if ($DataDir -ne $expectedDataDir -or $Workspace -ne $expectedWorkspace) {
    throw 'P2A-03 Gate only permits its isolated data and workspace roots'
}
if ((Test-Path -LiteralPath $DataDir) -or (Test-Path -LiteralPath $Workspace) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2A-03 Gate requires absent roots and an unused control port'
}

$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2a03-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2a03-server.err.log'
$followOut = Join-Path $repositoryRoot '.local\p2a03-follow.out.log'
$followErr = Join-Path $repositoryRoot '.local\p2a03-follow.err.log'
$javaHome = Join-Path $Workspace '.fixture-java'
$javaPath = Join-Path $javaHome 'bin\java.exe'
$server = $null
$follower = $null
$secretValue = $null
$previousJavaHome = $env:JAVA_HOME

New-Item -ItemType Directory -Path $DataDir, (Join-Path $Workspace '.stackpilot'), (Split-Path $javaPath) | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2a03-gate'), 'owned', (New-Object Text.UTF8Encoding($false)))
$manifest = @'
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: p2a-secret
  name: P2A Secret Gate
spec:
  ports:
    gate:
      protocol: tcp
      preferred: 32111
      fallbackRange: 32112-32120
      conflictPolicy: auto
      exposure: loopback
  services:
    keeper:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: .
      arguments: ["--mode", "hold-port", "--port", "${ports.gate}"]
      readiness:
        type: tcp
        host: 127.0.0.1
        port: "${ports.gate}"
        timeout: 10s
        interval: 100ms
      stop:
        gracefulTimeout: 1s
    echo:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: .
      arguments: ["--mode", "secret-log", "--environment", "STACKPILOT_GATE_SECRET"]
      environment:
        STACKPILOT_GATE_SECRET: "${secret.gate-key}"
      dependsOn:
        keeper: ready
      readiness:
        type: process
        timeout: 10s
        interval: 100ms
      stop:
        gracefulTimeout: 1s
'@
[IO.File]::WriteAllText((Join-Path $Workspace '.stackpilot\system.yaml'), $manifest, (New-Object Text.UTF8Encoding($false)))

try {
    . (Join-Path $repositoryRoot 'scripts\lib\tooling.ps1')
    $go = Get-StackPilotGo
    & $go build -o $javaPath (Join-Path $repositoryRoot 'test\fixtures\process-fixture')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build P2A-03 Java Runner fixture' }
    $env:JAVA_HOME = $javaHome
    $server = Start-Process -FilePath $candidatePath -ArgumentList @('server', '--port', "$Port", '--data-dir', $DataDir) `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady $serverURL

    $secretBytes = New-Object byte[] 32
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $random.GetBytes($secretBytes)
        $secretValue = [Convert]::ToBase64String($secretBytes)
    }
    finally {
        [Array]::Clear($secretBytes, 0, $secretBytes.Length)
        $random.Dispose()
    }
    $secretResult = $secretValue | & $candidatePath secret set --server $serverURL --data-dir $DataDir --output json p2a-secret gate-key | Out-String
    if ($LASTEXITCODE -ne 0 -or $secretResult.Contains($secretValue)) { throw 'Secret set failed or leaked plaintext' }

    $workspaceResult = Invoke-StackPilotJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', $Workspace)
    $start = Invoke-StackPilotJSON @('up', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-secret')
    if ($start.state -ne 'succeeded') { throw "Secret process start ended $($start.state)" }
    $statusText = & $candidatePath status --server $serverURL --data-dir $DataDir --output json p2a-secret | Out-String
    if ($LASTEXITCODE -ne 0 -or $statusText.Contains($secretValue)) {
        throw 'runtime status leaked plaintext Secret'
    }

    $follower = Start-Process -FilePath $candidatePath -ArgumentList @(
        'logs', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--follow', 'p2a-secret/echo'
    ) -RedirectStandardOutput $followOut -RedirectStandardError $followErr -WindowStyle Hidden -PassThru
    Wait-FileNonEmpty $followOut
    Stop-VerifiedProcess $follower $candidatePath
    $follower = $null
    $followText = [IO.File]::ReadAllText($followOut)
    if ($followText.Contains($secretValue) -or -not $followText.Contains('[REDACTED:secret]')) {
        throw 'log SSE/CLI output leaked plaintext or omitted the redaction marker'
    }

    Assert-NoPlaintext $secretValue
    $projectionText = & $go run ./test/gates/p2a03-dbcheck --db (Join-Path $DataDir 'stackpilot.db') | Out-String
    if ($LASTEXITCODE -ne 0) { throw 'Secret launch projection verification failed' }
    $projection = $projectionText | ConvertFrom-Json

    $stop = Invoke-StackPilotJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'p2a-secret')
    if ($stop.state -ne 'succeeded') { throw "Secret process stop ended $($stop.state)" }
    Stop-VerifiedProcess $server $candidatePath
    $server = $null
    Assert-NoPlaintext $secretValue

    [ordered]@{
        workspaceId = $workspaceResult.id
        startOperationId = $start.id
        stopOperationId = $stop.id
        environmentName = $projection.environmentName
        secretVersion = $projection.version
        provider = $projection.provider
        rawSpoolScan = 'passed'
        durableLogAndSSE = '[REDACTED:secret]'
        plaintextScan = 'passed'
    } | ConvertTo-Json
}
finally {
    $env:JAVA_HOME = $previousJavaHome
    $secretValue = $null
    if ($null -ne $server -and $null -ne (Get-Process -Id $server.Id -ErrorAction SilentlyContinue)) {
        try {
            $null = & $candidatePath down --server $serverURL --data-dir $DataDir --output json --wait p2a-secret 2>$null
        }
        catch {
        }
    }
    Stop-VerifiedProcess $follower $candidatePath
    Stop-VerifiedProcess $server $candidatePath
    Stop-GateSupervisors
    if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "P2A-03 control port $Port remained open"
    }
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2a03-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    if ((Test-Path -LiteralPath $Workspace) -and $Workspace -eq $expectedWorkspace) {
        Remove-Item -LiteralPath $Workspace -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath, $followOut, $followErr -Force -ErrorAction SilentlyContinue
}
