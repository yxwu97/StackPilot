Set-StrictMode -Version Latest

function Initialize-StackPilotGoEnvironment {
    $repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
    $env:GOCACHE = Join-Path $repositoryRoot '.cache\go-build'
    $env:GOMODCACHE = Join-Path $repositoryRoot '.cache\go-mod'
    New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOMODCACHE | Out-Null
}

function Get-StackPilotGo {
    $repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
    Initialize-StackPilotGoEnvironment
    $bundledGo = Join-Path $repositoryRoot '.tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $bundledGo -PathType Leaf) {
        return $bundledGo
    }

    $goCommand = Get-Command go -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $goCommand) {
        throw 'Go was not found. Install Go 1.26.6 or place it at .tools/go.'
    }

    return $goCommand.Source
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory)]
        [scriptblock]$Command,

        [Parameter(Mandatory)]
        [string]$Description
    )

    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
}
