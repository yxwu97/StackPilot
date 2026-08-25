Set-StrictMode -Version Latest

$script:StackPilotVersionPattern = '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
$script:StackPilotExcludedVersionPaths = @(
    '^(prompt|plan|docs/evidence)(/|$)',
    '^(dist|output|web/dist|\.cache|\.local)(/|$)',
    '^VERSION$'
)

function ConvertFrom-StackPilotVersionText {
    param([Parameter(Mandatory)][string]$Value)

    if ($Value -notmatch $script:StackPilotVersionPattern) {
        throw "Product version must use canonical MAJOR.MINOR.PATCH form: $Value"
    }

    $parts = $Value.Split('.')
    $numbers = foreach ($part in $parts) {
        $parsed = 0L
        if (-not [Int64]::TryParse($part, [Globalization.NumberStyles]::None, [Globalization.CultureInfo]::InvariantCulture, [ref]$parsed)) {
            throw "Product version component is outside the supported Int64 range: $part"
        }
        $parsed
    }

    [pscustomobject]@{
        Major = $numbers[0]
        Minor = $numbers[1]
        Patch = $numbers[2]
        Value = $Value
    }
}

function Get-StackPilotProductVersion {
    param([Parameter(Mandatory)][string]$RepositoryRoot)

    $path = Join-Path $RepositoryRoot 'VERSION'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Product version file is missing: $path"
    }

    $bytes = [IO.File]::ReadAllBytes($path)
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
        throw 'VERSION must be UTF-8 without BOM.'
    }

    try {
        $text = (New-Object Text.UTF8Encoding($false, $true)).GetString($bytes)
    }
    catch {
        throw "VERSION is not valid UTF-8: $($_.Exception.Message)"
    }
    if (-not $text.EndsWith("`n") -or $text.Contains("`r") -or $text.Substring(0, $text.Length - 1).Contains("`n")) {
        throw 'VERSION must contain exactly one LF-terminated line.'
    }

    (ConvertFrom-StackPilotVersionText -Value $text.Substring(0, $text.Length - 1)).Value
}

function Compare-StackPilotVersion {
    param(
        [Parameter(Mandatory)][string]$Left,
        [Parameter(Mandatory)][string]$Right
    )

    $leftVersion = ConvertFrom-StackPilotVersionText -Value $Left
    $rightVersion = ConvertFrom-StackPilotVersionText -Value $Right
    foreach ($name in @('Major', 'Minor', 'Patch')) {
        if ($leftVersion.$name -lt $rightVersion.$name) { return -1 }
        if ($leftVersion.$name -gt $rightVersion.$name) { return 1 }
    }
    return 0
}

function Get-NextStackPilotVersion {
    param(
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][ValidateSet('patch', 'minor', 'major')][string]$Part
    )

    $parsed = ConvertFrom-StackPilotVersionText -Value $Version
    switch ($Part) {
        'patch' {
            if ($parsed.Patch -eq [Int64]::MaxValue) { throw 'Product version patch component cannot be incremented.' }
            $parsed.Patch++
        }
        'minor' {
            if ($parsed.Minor -eq [Int64]::MaxValue) { throw 'Product version minor component cannot be incremented.' }
            $parsed.Minor++
            $parsed.Patch = 0
        }
        'major' {
            if ($parsed.Major -eq [Int64]::MaxValue) { throw 'Product version major component cannot be incremented.' }
            $parsed.Major++
            $parsed.Minor = 0
            $parsed.Patch = 0
        }
    }
    return "$($parsed.Major).$($parsed.Minor).$($parsed.Patch)"
}

function Test-StackPilotProductPath {
    param([Parameter(Mandatory)][string]$Path)

    $normalized = $Path.Replace('\', '/').TrimStart('./')
    foreach ($pattern in $script:StackPilotExcludedVersionPaths) {
        if ($normalized -match $pattern) { return $false }
    }
    return $true
}

function Get-StackPilotVersionAtRef {
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$Reference
    )

    $spec = "${Reference}:VERSION"
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $value = & git -C $RepositoryRoot show $spec 2>$null
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousPreference
    }
    if ($exitCode -ne 0) { return $null }
    return (ConvertFrom-StackPilotVersionText -Value (($value | Out-String).Trim())).Value
}

function Get-StackPilotChangedPaths {
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [string]$BaseRef = ''
    )

    $paths = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    if (-not [string]::IsNullOrWhiteSpace($BaseRef)) {
        $committed = & git -C $RepositoryRoot diff --name-only "$BaseRef...HEAD" --
        if ($LASTEXITCODE -ne 0) { throw "Unable to compare version baseline $BaseRef." }
        foreach ($path in $committed) { if ($path) { [void]$paths.Add($path) } }
    }

    $status = & git -C $RepositoryRoot status --porcelain=v1 --untracked-files=all
    if ($LASTEXITCODE -ne 0) { throw 'Unable to inspect repository changes for version validation.' }
    foreach ($entry in $status) {
        if ($entry.Length -lt 4) { continue }
        $path = $entry.Substring(3)
        if ($path.Contains(' -> ')) { $path = $path.Split(' -> ')[-1] }
        [void]$paths.Add($path.Trim('"'))
    }
    return @($paths)
}

function Enter-StackPilotVersionLock {
    param([Parameter(Mandatory)][string]$RepositoryRoot)

    $cache = Join-Path $RepositoryRoot '.cache'
    New-Item -ItemType Directory -Force -Path $cache | Out-Null
    $path = Join-Path $cache 'version.lock'
    try {
        return [IO.File]::Open($path, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    }
    catch [IO.IOException] {
        throw 'Another StackPilot version update is already running.'
    }
}

function Set-StackPilotProductVersion {
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$Version
    )

    $canonical = (ConvertFrom-StackPilotVersionText -Value $Version).Value
    $path = Join-Path $RepositoryRoot 'VERSION'
    $temporary = "$path.$([Guid]::NewGuid().ToString('N')).tmp"
    $backup = "$path.$([Guid]::NewGuid().ToString('N')).bak"
    try {
        [IO.File]::WriteAllText($temporary, "$canonical`n", (New-Object Text.UTF8Encoding($false)))
        [IO.File]::Replace($temporary, $path, $backup, $true)
    }
    finally {
        Remove-Item -LiteralPath $temporary, $backup -Force -ErrorAction SilentlyContinue
    }
}
