Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'lib/versioning.ps1')

function Assert-Equal($Actual, $Expected, [string]$Message) {
    if ($Actual -ne $Expected) { throw "$Message`: got $Actual, want $Expected" }
}

function Assert-Throws([scriptblock]$Action, [string]$Message) {
    try { & $Action } catch { return }
    throw "$Message`: expected an exception"
}

Assert-Equal (Get-NextStackPilotVersion '0.1.0' patch) '0.1.1' 'patch bump'
Assert-Equal (Get-NextStackPilotVersion '0.1.9' minor) '0.2.0' 'minor bump'
Assert-Equal (Get-NextStackPilotVersion '0.9.9' major) '1.0.0' 'major bump'
Assert-Equal (Compare-StackPilotVersion '0.9.9' '0.10.0') -1 'numeric comparison'
Assert-Equal (Test-StackPilotProductPath 'internal/api/router.go') $true 'production path classification'
Assert-Equal (Test-StackPilotProductPath 'prompt/example.md') $false 'prompt path classification'
Assert-Throws { ConvertFrom-StackPilotVersionText '01.2.3' } 'leading zero validation'
Assert-Throws { ConvertFrom-StackPilotVersionText '1.2.3-alpha' } 'prerelease validation'

$fixture = Join-Path ([IO.Path]::GetTempPath()) "stackpilot-version-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $fixture | Out-Null
try {
    [IO.File]::WriteAllText((Join-Path $fixture 'VERSION'), "1.2.3`n", (New-Object Text.UTF8Encoding($false)))
    Assert-Equal (Get-StackPilotProductVersion $fixture) '1.2.3' 'strict version read'
    Set-StackPilotProductVersion $fixture '1.2.4'
    Assert-Equal (Get-StackPilotProductVersion $fixture) '1.2.4' 'atomic version write'

    $firstLock = Enter-StackPilotVersionLock $fixture
    try { Assert-Throws { Enter-StackPilotVersionLock $fixture } 'exclusive version lock' } finally { $firstLock.Dispose() }

    [IO.File]::WriteAllBytes((Join-Path $fixture 'VERSION'), [byte[]](0xEF, 0xBB, 0xBF, 0x31, 0x2E, 0x32, 0x2E, 0x33, 0x0A))
    Assert-Throws { Get-StackPilotProductVersion $fixture } 'BOM validation'
    [IO.File]::WriteAllText((Join-Path $fixture 'VERSION'), "1.2.3`r`n", (New-Object Text.UTF8Encoding($false)))
    Assert-Throws { Get-StackPilotProductVersion $fixture } 'CRLF validation'
}
finally {
    Remove-Item -LiteralPath $fixture -Recurse -Force -ErrorAction SilentlyContinue
}

Assert-Equal (Get-StackPilotProductVersion $repositoryRoot) '0.1.0' 'repository version'
Write-Host 'Version tests passed.'
