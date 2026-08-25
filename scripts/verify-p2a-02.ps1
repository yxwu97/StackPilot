[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Candidate,

    [string]$DataDir = 'E:\StackPilot\.local\p2a02-data',
    [int]$Port = 32108
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Wait-ControlReady {
    param([string]$URL)

    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "$URL/health/ready" -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                return
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 100
    }
    throw 'P2A-02 control plane did not become ready'
}

function New-TestSecret {
    $bytes = New-Object byte[] 32
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $random.GetBytes($bytes)
        return [Convert]::ToBase64String($bytes)
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
        $random.Dispose()
    }
}

function Invoke-SecretSet {
    param([string]$Executable, [string]$URL, [string]$Value)

    $output = $Value | & $Executable secret set --server $URL --data-dir $DataDir --output json aiws gate-key | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "secret set failed with exit code $LASTEXITCODE"
    }
    return $output
}

function Assert-NoPlaintext {
    param([string[]]$Values)

    foreach ($file in Get-ChildItem -LiteralPath $DataDir -File -Recurse) {
        $payload = [IO.File]::ReadAllBytes($file.FullName)
        try {
            $text = [Text.Encoding]::UTF8.GetString($payload)
            foreach ($value in $Values) {
                if ($text.Contains($value)) {
                    throw "plaintext test Secret found in $($file.Name)"
                }
            }
        }
        finally {
            [Array]::Clear($payload, 0, $payload.Length)
        }
    }
}

function Stop-GateServer {
    param($Process, [string]$ExpectedPath)

    if ($null -eq $Process -or $null -eq (Get-Process -Id $Process.Id -ErrorAction SilentlyContinue)) {
        return
    }
    $actual = Get-Process -Id $Process.Id -ErrorAction Stop
    if (-not [string]::Equals($actual.Path, $ExpectedPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'refusing to stop an unverified P2A-02 control-plane PID'
    }
    Stop-Process -Id $Process.Id -Force -ErrorAction Stop
    Wait-Process -Id $Process.Id -Timeout 10 -ErrorAction SilentlyContinue
}

$candidatePath = (Resolve-Path -LiteralPath $Candidate).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$expectedDataDir = Join-Path $repositoryRoot '.local\p2a02-data'
if ($DataDir -ne $expectedDataDir) {
    throw "P2A-02 Gate only permits its isolated data root: $expectedDataDir"
}
if ((Test-Path -LiteralPath $DataDir) -or @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'P2A-02 Gate requires an absent data root and unused control port'
}

$serverURL = "http://127.0.0.1:$Port"
$stdoutPath = Join-Path $repositoryRoot '.local\p2a02-server.out.log'
$stderrPath = Join-Path $repositoryRoot '.local\p2a02-server.err.log'
$server = $null
$firstValue = $null
$secondValue = $null
New-Item -ItemType Directory -Path $DataDir | Out-Null
Set-Content -LiteralPath (Join-Path $DataDir '.p2a02-gate') -Value 'owned' -NoNewline

try {
    $server = Start-Process -FilePath $candidatePath -ArgumentList @('server', '--port', "$Port", '--data-dir', $DataDir) `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -WindowStyle Hidden -PassThru
    Wait-ControlReady $serverURL

    $firstValue = New-TestSecret
    $firstOutput = Invoke-SecretSet $candidatePath $serverURL $firstValue
    $first = $firstOutput | ConvertFrom-Json
    if ($first.version -ne 1 -or $firstOutput.Contains($firstValue)) {
        throw 'first Secret response was invalid or leaked its value'
    }

    $metadataOutput = & $candidatePath secret get-metadata --server $serverURL --data-dir $DataDir --output json aiws gate-key | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw 'secret get-metadata failed'
    }
    $metadata = $metadataOutput | ConvertFrom-Json
    if ($metadata.version -ne 1 -or $metadataOutput.Contains($firstValue)) {
        throw 'Secret metadata response was invalid or leaked its value'
    }

    $secondValue = New-TestSecret
    $secondOutput = Invoke-SecretSet $candidatePath $serverURL $secondValue
    $second = $secondOutput | ConvertFrom-Json
    if ($second.version -ne 2 -or $secondOutput.Contains($secondValue)) {
        throw 'second Secret response was invalid or leaked its value'
    }

    $deleteOutput = & $candidatePath secret delete --server $serverURL --data-dir $DataDir --output json aiws gate-key | Out-String
    if ($LASTEXITCODE -ne 0 -or $deleteOutput.Contains($firstValue) -or $deleteOutput.Contains($secondValue)) {
        throw 'Secret delete failed or leaked a value'
    }
    $missingOutput = & $candidatePath secret get-metadata --server $serverURL --data-dir $DataDir --output json aiws gate-key 2>&1 | Out-String
    if ($LASTEXITCODE -eq 0 -or -not $missingOutput.Contains('SECRET_NOT_FOUND')) {
        throw 'deleted Secret did not return SECRET_NOT_FOUND'
    }
    Stop-GateServer $server $candidatePath
    $server = $null
    Assert-NoPlaintext @($firstValue, $secondValue)

    [ordered]@{
        firstVersion = $first.version
        secondVersion = $second.version
        provider = $second.provider
        deleted = $true
        missingCode = 'SECRET_NOT_FOUND'
        plaintextScan = 'passed'
    } | ConvertTo-Json
}
finally {
    $firstValue = $null
    $secondValue = $null
    Stop-GateServer $server $candidatePath
    if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "P2A-02 control port $Port remained open"
    }
    if ((Test-Path -LiteralPath (Join-Path $DataDir '.p2a02-gate')) -and $DataDir -eq $expectedDataDir) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
}
