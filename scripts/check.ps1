[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
. (Join-Path $PSScriptRoot 'lib/tooling.ps1')

Push-Location $repositoryRoot
try {
    $go = Get-StackPilotGo
    & (Join-Path $PSScriptRoot 'test-version.ps1')
    & (Join-Path $PSScriptRoot 'version.ps1') -Action Check
    $goFiles = & $go fmt ./...
    if ($LASTEXITCODE -ne 0) {
        throw "gofmt failed with exit code $LASTEXITCODE."
    }
    if ($goFiles) {
        Write-Host "gofmt updated:`n$($goFiles -join "`n")"
    }

    Invoke-Checked -Description 'Web unit tests' -Command { npm run test:web }
    Invoke-Checked -Description 'Web type check' -Command { npm run type-check }
    Invoke-Checked -Description 'Web production build' -Command { npm run build:web }
    Invoke-Checked -Description 'Go tests' -Command { & $go test ./... }
    Invoke-Checked -Description 'Go vet' -Command { & $go vet ./... }
}
finally {
    Pop-Location
}

Write-Host 'All repository checks passed.'
