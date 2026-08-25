[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Candidate,
    [string]$AIWSWorkspace = 'E:\AIWorkflowStudio',
    [string]$DataDir = 'E:\StackPilot\.local\p2c07-data',
    [int]$Port = 32140,
    [switch]$HoldForUI
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$workspacePath = (Resolve-Path -LiteralPath $AIWSWorkspace).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2c07-data'
$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2c07-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2c07-server.err.log'
$uiHoldPath = Join-Path $repositoryRoot '.local\p2c07-ui.hold'
$uiReadyPath = Join-Path $repositoryRoot '.local\p2c07-ui-ready.json'
$server = $null
$workspace = $null
$status = $null
$start = $null

function Wait-ControlReady {
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "$serverURL/health/ready" -TimeoutSec 2
            if ($response.StatusCode -eq 200) { return }
        } catch {}
        Start-Sleep -Milliseconds 200
    }
    throw 'P2C-07 control plane did not become ready'
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
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Name)
    foreach ($line in [IO.File]::ReadLines($Path, [Text.Encoding]::UTF8)) {
        if ($line.StartsWith("$Name=", [StringComparison]::Ordinal)) {
            $value = $line.Substring($Name.Length + 1)
            if ($value.Length -eq 0) { throw "Required local setting $Name is empty" }
            return $value
        }
    }
    throw "Required local setting $Name is missing"
}

function Set-SecretFromEnvironmentFile {
    param([string]$SecretName, [string]$Path, [string]$EnvironmentName)
    $value = Read-DotEnvValue $Path $EnvironmentName
    try {
        $result = $value | & $candidatePath secret set --server $serverURL --data-dir $DataDir `
            --output json aiws $SecretName | Out-String
        if ($LASTEXITCODE -ne 0 -or $result.Contains($value)) {
            throw "Secret metadata write failed for $SecretName"
        }
    } finally { $value = $null }
}

function Stop-VerifiedServer {
    param($Process)
    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) { return }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2C-07 Server process'
    }
    Stop-Process -Id $Process.Id -Force
    Wait-Process -Id $Process.Id -Timeout 15 -ErrorAction SilentlyContinue
}

function Start-GateServer {
    $process = Start-Process -FilePath $candidatePath -ArgumentList @(
        'server', '--port', "$Port", '--data-dir', $DataDir,
        '--reconcile-interval', '10s', '--lease-reconcile-interval', '30s'
    ) -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady
    return $process
}

function Get-PortValue {
    param($RuntimeStatus, [string]$LogicalName)
    $match = @($RuntimeStatus.ports | Where-Object logicalName -eq $LogicalName)
    if ($match.Count -ne 1) { throw "Missing planned port $LogicalName" }
    return [int]$match[0].port
}

function Assert-ServiceStates {
    param($RuntimeStatus)
    $expected = @{ infrastructure = 'ready'; 'keycloak-configure' = 'completed'; server = 'ready';
        'agent-runtime' = 'ready'; web = 'ready' }
    foreach ($entry in $expected.GetEnumerator()) {
        $service = @($RuntimeStatus.services | Where-Object serviceId -eq $entry.Key)
        if ($service.Count -ne 1 -or $service[0].state -ne $entry.Value) {
            throw "Unexpected service state for $($entry.Key)"
        }
    }
}

function Assert-Endpoints {
    param($RuntimeStatus)
    $checks = @(
        "http://127.0.0.1:$(Get-PortValue $RuntimeStatus 'server')/actuator/health/readiness",
        "http://127.0.0.1:$(Get-PortValue $RuntimeStatus 'agent-runtime')/health/ready",
        "http://127.0.0.1:$(Get-PortValue $RuntimeStatus 'web')",
        "http://127.0.0.1:$(Get-PortValue $RuntimeStatus 'keycloak')/realms/aiws/.well-known/openid-configuration"
    )
    foreach ($url in $checks) {
        $response = Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 10
        if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 400) { throw "Endpoint failed: $url" }
    }
}

function Wait-ForUIVerification {
    param($RuntimeStatus)
    if (-not $HoldForUI) { return }
    [IO.File]::WriteAllText($uiHoldPath, 'owned', [Text.UTF8Encoding]::new($false))
    $readyPayload = [ordered]@{
        serverUrl = $serverURL
        workspaceId = $workspace.id
        instanceId = $RuntimeStatus.instanceId
        systemId = $RuntimeStatus.systemId
    } | ConvertTo-Json
    [IO.File]::WriteAllText($uiReadyPath, $readyPayload, [Text.UTF8Encoding]::new($false))
    $deadline = (Get-Date).AddMinutes(15)
    while ((Test-Path -LiteralPath $uiHoldPath) -and (Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 250
    }
    if (Test-Path -LiteralPath $uiHoldPath) { throw 'P2C-07 UI verification timed out' }
}

function Stop-GateSupervisors {
    $root = Join-Path $DataDir 'instances'
    if (-not (Test-Path -LiteralPath $root)) { return }
    foreach ($file in Get-ChildItem -LiteralPath $root -Filter supervisor.json -File -Recurse) {
        $identity = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
        $process = Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
        if ($null -eq $process) { continue }
        if (-not [string]::Equals($process.Path, $candidatePath, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'refusing to stop a P2C-07 Supervisor with mismatched identity'
        }
        Stop-Process -Id $identity.pid -Force
        Wait-Process -Id $identity.pid -Timeout 15 -ErrorAction SilentlyContinue
    }
}

function Remove-GateComposeProject {
    if ($null -eq $workspace -or $null -eq $status -or $null -eq $start) { return }
    $workspaceShort = $workspace.id.Substring(3, 8).ToLowerInvariant()
    $instanceShort = $status.instanceId.Substring(3, 8).ToLowerInvariant()
    $project = "sp-aiws-$workspaceShort-$instanceShort"
    if ($project -notmatch '^sp-aiws-[0-9a-z]{8}-[0-9a-z]{8}$') { throw 'invalid Gate project identity' }
    $override = Join-Path $DataDir "runtime/operations/$($start.id)/compose.override.yml"
    if (-not (Test-Path -LiteralPath $override)) { return }
    & docker compose --project-name $project --file (Join-Path $workspacePath 'deploy/stackpilot-compose.yml') `
        --file $override down --volumes --remove-orphans | Out-Null
}

if ($DataDir -ne $expectedDataDir -or (Test-Path -LiteralPath $DataDir) -or
    @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2C-07 Gate requires its absent isolated data root and unused control port'
}

New-Item -ItemType Directory -Path $DataDir | Out-Null
[IO.File]::WriteAllText((Join-Path $DataDir '.p2c07-gate'), 'owned', [Text.UTF8Encoding]::new($false))
try {
    $server = Start-GateServer
    $deployEnvironment = Join-Path $workspacePath 'deploy/.env'
    $runtimeEnvironment = Join-Path $workspacePath 'agent-runtime/.env'
    Set-SecretFromEnvironmentFile 'database-password' $deployEnvironment 'AIWS_DB_PASSWORD'
    Set-SecretFromEnvironmentFile 'keycloak-admin-password' $deployEnvironment 'AIWS_KEYCLOAK_ADMIN_PASSWORD'
    Set-SecretFromEnvironmentFile 'minio-root-password' $deployEnvironment 'AIWS_MINIO_ROOT_PASSWORD'
    Set-SecretFromEnvironmentFile 'agent-runtime-token' $deployEnvironment 'AIWS_AGENT_RUNTIME_INTERNAL_TOKEN'
    Set-SecretFromEnvironmentFile 'runtime-event-signing-secret' $deployEnvironment 'AIWS_RUNTIME_EVENT_SIGNING_SECRET'
    Set-SecretFromEnvironmentFile 'runtime-credential-refs' $runtimeEnvironment 'AIWS_RUNTIME_CREDENTIAL_REFS'

    $workspace = Invoke-CLIJSON @('workspace', 'add', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', $workspacePath)
    $start = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', '--wait', 'aiws')
    if ($start.state -ne 'succeeded') { throw "AIWS start ended $($start.state): $($start.errorCode)" }
    $status = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'aiws')
    Assert-ServiceStates $status
    Assert-Endpoints $status

    $repeat = Invoke-CLIJSON @('up', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', '--wait', 'aiws')
    if ($repeat.state -ne 'succeeded' -or @($repeat.steps | Where-Object state -ne 'skipped').Count -ne 0) {
        throw 'AIWS repeated start was not an idempotent skipped success'
    }

    Stop-VerifiedServer $server
    $server = $null
    $server = Start-GateServer
    Start-Sleep -Seconds 12
    $recovered = Invoke-CLIJSON @('status', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', 'aiws')
    Assert-ServiceStates $recovered
    if ($recovered.instanceId -ne $status.instanceId) { throw 'AIWS control-plane recovery changed instance identity' }
    Wait-ForUIVerification $recovered

    $stop = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir,
        '--output', 'json', '--wait', 'aiws')
    if ($stop.state -ne 'succeeded') { throw "AIWS stop ended $($stop.state)" }
    [ordered]@{
        workspaceId = $workspace.id; instanceId = $status.instanceId; startOperationId = $start.id
        repeatOperationId = $repeat.id; stopOperationId = $stop.id; services = $status.services.Count
        ports = $status.ports.Count; recovery = 'same instance'; endpoints = 'server/runtime/web/oidc ready'
    } | ConvertTo-Json
}
finally {
    if ($null -ne $server -and $null -ne $workspace) {
        try { $null = Invoke-CLIJSON @('down', '--server', $serverURL, '--data-dir', $DataDir, '--output', 'json', '--wait', 'aiws') @(0, 4) } catch {}
    }
    Stop-VerifiedServer $server
    Stop-GateSupervisors
    Remove-GateComposeProject
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2c07-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $uiHoldPath, $uiReadyPath -Force -ErrorAction SilentlyContinue
}
