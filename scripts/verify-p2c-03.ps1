[CmdletBinding()]
param(
    [string]$AIWSWorkspace = 'E:\AIWorkflowStudio'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$project = 'stackpilot-p2c03-gate'
$baseFile = Join-Path $AIWSWorkspace 'deploy/stackpilot-compose.yml'
$python = Join-Path $AIWSWorkspace 'agent-runtime/.venv/Scripts/python.exe'
$configure = Join-Path $AIWSWorkspace 'agent-runtime/configure_keycloak.py'
$runtimeDir = Join-Path $PSScriptRoot '../test/result/p2c-03'
$overrideFile = Join-Path $runtimeDir 'compose.override.yml'

function New-GatePassword {
    $bytes = New-Object byte[] 32
    $random = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $random.GetBytes($bytes)
        return [Convert]::ToBase64String($bytes)
    }
    finally {
        $random.Dispose()
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
}

$gatePassword = New-GatePassword

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new(
        [System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port }
    finally { $listener.Stop() }
}

function Write-Override {
    param([Parameter(Mandatory = $true)][string]$Image,
          [Parameter(Mandatory = $true)][int]$Port)
    $contents = @"
services:
  keycloak:
    image: $Image
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: $gatePassword
    ports:
      - target: 18180
        published: "$Port"
        host_ip: 127.0.0.1
        protocol: tcp
"@
    [System.IO.File]::WriteAllText($overrideFile, $contents,
        [System.Text.UTF8Encoding]::new($false))
}

function Invoke-Compose {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    & docker compose --project-name $project --file $baseFile --file $overrideFile @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker Compose failed with exit code $LASTEXITCODE." }
}

function Invoke-Configure {
    param([Parameter(Mandatory = $true)][int]$Port)
    $env:AIWS_KEYCLOAK_URL = "http://127.0.0.1:$Port"
    $env:AIWS_KEYCLOAK_ADMIN = 'admin'
    $env:AIWS_KEYCLOAK_ADMIN_PASSWORD = $gatePassword
    $env:AIWS_KEYCLOAK_WEB_ORIGIN = 'http://127.0.0.1:26173'
    & $python $configure
    if ($LASTEXITCODE -ne 0) { throw "Keycloak Configure failed with exit code $LASTEXITCODE." }
}

function Set-PartialProfileState {
    param([Parameter(Mandatory = $true)][int]$Port)
    $tokenUrl = "http://127.0.0.1:$Port/realms/master/protocol/openid-connect/token"
    $tokenResponse = Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
        grant_type = 'password'; client_id = 'admin-cli'; username = 'admin'; password = $gatePassword
    } -ContentType 'application/x-www-form-urlencoded'
    $headers = @{ Authorization = "Bearer $($tokenResponse.access_token)" }
    $profileUrl = "http://127.0.0.1:$Port/admin/realms/aiws/users/profile"
    $profile = Invoke-RestMethod -Method Get -Uri $profileUrl -Headers $headers
    $profile | Add-Member -NotePropertyName unmanagedAttributePolicy -NotePropertyValue ADMIN_EDIT -Force
    $payload = $profile | ConvertTo-Json -Depth 20 -Compress
    Invoke-RestMethod -Method Put -Uri $profileUrl -Headers $headers -Body $payload `
        -ContentType 'application/json; charset=utf-8' | Out-Null
}

function Assert-KeycloakVersion {
    param([Parameter(Mandatory = $true)][string]$Expected)
    $container = docker compose --project-name $project --file $baseFile --file $overrideFile `
        ps --quiet keycloak
    $image = docker inspect --format '{{.Config.Image}}' $container
    if ($image.Trim() -ne $Expected) { throw "Unexpected Keycloak image: $image" }
}

New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
$port = Get-FreePort
try {
    Write-Override 'quay.io/keycloak/keycloak:26.2.5' $port
    Invoke-Compose up -d --wait --wait-timeout 180 --no-deps keycloak
    Assert-KeycloakVersion 'quay.io/keycloak/keycloak:26.2.5'
    Set-PartialProfileState $port
    Invoke-Configure $port
    Invoke-Configure $port

    Invoke-Compose stop keycloak
    Write-Override 'quay.io/keycloak/keycloak:26.3.3' $port
    Invoke-Compose up -d --wait --wait-timeout 180 --no-deps --force-recreate keycloak
    Assert-KeycloakVersion 'quay.io/keycloak/keycloak:26.3.3'
    Invoke-Configure $port
    Write-Output 'P2C-03 Gate passed: partial recovery, repeat execution, and 26.2.5 -> 26.3.3 upgrade.'
}
finally {
    docker compose --project-name $project --file $baseFile --file $overrideFile `
        down --volumes --remove-orphans | Out-Null
}
