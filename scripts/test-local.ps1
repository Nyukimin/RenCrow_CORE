param(
    [string]$WorkingDirectory = ".",
    [switch]$KeepRuntime,
    [switch]$SelfTest,
    [Parameter(Position = 0)]
    [string]$FilePath,
    [Parameter(Position = 1, ValueFromRemainingArguments = $true)]
    [string[]]$ArgumentList = @()
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtimeRoot = Join-Path $repoRoot "Tmp\test-runtime"
$runsRoot = Join-Path $runtimeRoot "runs"
$cacheRoot = Join-Path $runtimeRoot "cache"
$runName = "{0:yyyyMMdd-HHmmss}-{1}-{2}" -f (Get-Date), $PID, ([Guid]::NewGuid().ToString("N"))
$runRoot = Join-Path $runsRoot $runName

$comparison = if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
    [StringComparison]::OrdinalIgnoreCase
} else {
    [StringComparison]::Ordinal
}

function Get-DirectoryPrefix([string]$Path) {
    return [IO.Path]::GetFullPath($Path).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    ) + [IO.Path]::DirectorySeparatorChar
}

$repoPrefix = Get-DirectoryPrefix $repoRoot
$localTempPrefix = Get-DirectoryPrefix (Join-Path $repoRoot "Tmp")
$workingPath = if ([IO.Path]::IsPathRooted($WorkingDirectory)) {
    [IO.Path]::GetFullPath($WorkingDirectory)
} else {
    [IO.Path]::GetFullPath((Join-Path $repoRoot $WorkingDirectory))
}
if ($workingPath -ne $repoRoot -and -not $workingPath.StartsWith($repoPrefix, $comparison)) {
    throw "WorkingDirectory must stay inside the repository: $workingPath"
}
if (-not (Test-Path -LiteralPath $workingPath -PathType Container)) {
    throw "WorkingDirectory does not exist: $workingPath"
}
if ($SelfTest -and -not [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "SelfTest cannot run a command. Put -- between FilePath and command arguments."
}
if (-not $SelfTest -and [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "FilePath is required unless -SelfTest is used."
}

$paths = @{
    TEMP                  = $runRoot
    TMP                   = $runRoot
    TMPDIR                = $runRoot
    GOTMPDIR              = (Join-Path $runRoot "go-build")
    GOCACHE               = (Join-Path $cacheRoot "go-build")
    PYTHONPYCACHEPREFIX   = (Join-Path $cacheRoot "python-bytecode")
    PYTEST_DEBUG_TEMPROOT = (Join-Path $runRoot "pytest")
    UV_CACHE_DIR          = (Join-Path $cacheRoot "uv")
    PIP_CACHE_DIR         = (Join-Path $cacheRoot "pip")
    npm_config_cache      = (Join-Path $cacheRoot "npm")
    XDG_CACHE_HOME        = (Join-Path $cacheRoot "xdg")
}

$previous = @{}
foreach ($name in $paths.Keys) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    New-Item -ItemType Directory -Force -Path $paths[$name] | Out-Null
    [Environment]::SetEnvironmentVariable($name, $paths[$name], "Process")
}

try {
    foreach ($name in $paths.Keys) {
        $resolved = [IO.Path]::GetFullPath($paths[$name])
        if (-not $resolved.StartsWith($localTempPrefix, $comparison)) {
            throw "$name escaped the repository-local Tmp directory: $resolved"
        }
    }

    Write-Host "[test-local] repository: $repoRoot"
    Write-Host "[test-local] runtime: $runRoot"
    Write-Host "[test-local] cache: $cacheRoot"

    if ($SelfTest) {
        Write-Host "[OK] Repository-local test runtime contract passed"
        return
    }

    Push-Location $workingPath
    try {
        $global:LASTEXITCODE = 0
        & $FilePath @ArgumentList
        if ($LASTEXITCODE -ne 0) {
            throw "Test command failed with exit code ${LASTEXITCODE}: $FilePath $($ArgumentList -join ' ')"
        }
    } finally {
        Pop-Location
    }
} finally {
    foreach ($name in $paths.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], "Process")
    }

    if (-not $KeepRuntime -and (Test-Path -LiteralPath $runRoot)) {
        $resolvedRunRoot = [IO.Path]::GetFullPath($runRoot)
        $runsPrefix = Get-DirectoryPrefix $runsRoot
        if (-not $resolvedRunRoot.StartsWith($runsPrefix, $comparison)) {
            throw "Refusing to clean a path outside the repository test runtime: $resolvedRunRoot"
        }
        Remove-Item -LiteralPath $resolvedRunRoot -Recurse -Force
    }
}
