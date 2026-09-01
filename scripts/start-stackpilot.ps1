[CmdletBinding()]
param([switch]$NoOpen)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$candidate = Join-Path $repositoryRoot 'dist\stackpilot.exe'

if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
    throw "StackPilot executable was not found: $candidate"
}

function Stop-RunningStackPilotProcesses {
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    $reported = $false

    while ($true) {
        $processes = @(Get-Process -Name 'stackpilot' -ErrorAction SilentlyContinue)
        if ($processes.Count -eq 0) {
            if ($reported) {
                Write-Host 'All previous StackPilot processes have exited.'
            }
            return
        }

        if (-not $reported) {
            $processIds = ($processes | ForEach-Object { $_.Id }) -join ', '
            Write-Host "Stopping all remaining StackPilot processes: $processIds"
            $reported = $true
        }

        foreach ($runningProcess in $processes) {
            try {
                Stop-Process -Id $runningProcess.Id -Force -ErrorAction Stop
            }
            catch {
                if ($null -ne (Get-Process -Id $runningProcess.Id -ErrorAction SilentlyContinue)) {
                    throw "Unable to stop StackPilot process $($runningProcess.Id): $($_.Exception.Message)"
                }
            }
        }

        if ([DateTime]::UtcNow -ge $deadline) {
            $remaining = @(Get-Process -Name 'stackpilot' -ErrorAction SilentlyContinue)
            $remainingIds = ($remaining | ForEach-Object { $_.Id }) -join ', '
            throw "StackPilot processes did not exit within 10 seconds: $remainingIds"
        }
        Start-Sleep -Milliseconds 100
    }
}

$statusPayload = & $candidate service status --output json | Out-String
if ($LASTEXITCODE -ne 0) {
    throw "StackPilot service status failed with exit code $LASTEXITCODE"
}
$status = $statusPayload | ConvertFrom-Json

if (-not $status.installed) {
    Stop-RunningStackPilotProcesses

    Write-Host 'Installing StackPilot for the current user...'
    & $candidate service install
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service install failed with exit code $LASTEXITCODE"
    }
} else {
    Write-Host 'Stopping the installed StackPilot control plane...'
    & $candidate service stop
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service stop failed with exit code $LASTEXITCODE"
    }

    Stop-RunningStackPilotProcesses

    Write-Host 'Updating the installed StackPilot control plane from dist...'
    & $candidate service upgrade
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service upgrade failed with exit code $LASTEXITCODE"
    }

    Write-Host 'Starting a fresh StackPilot control-plane process...'
    & $candidate service start
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot service start failed with exit code $LASTEXITCODE"
    }
}

if (-not $NoOpen) {
    & $candidate open
    if ($LASTEXITCODE -ne 0) {
        throw "StackPilot Web open failed with exit code $LASTEXITCODE"
    }
}
