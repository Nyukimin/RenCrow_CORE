param(
    [string]$WorkingDirectory = ".",
    [string[]]$Step = @(),
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
$runsRoot = Join-Path $runtimeRoot "r"
$cacheRoot = Join-Path $runtimeRoot "cache"
$runName = "{0}-{1}" -f $PID, ([Guid]::NewGuid().ToString("N").Substring(0, 8))
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
$planPath = Join-Path $PSScriptRoot "test-local.plan.json"

function Resolve-WorkingPath([string]$Candidate) {
    $resolved = if ([IO.Path]::IsPathRooted($Candidate)) {
        [IO.Path]::GetFullPath($Candidate)
    } else {
        [IO.Path]::GetFullPath((Join-Path $repoRoot $Candidate))
    }
    if ($resolved -ne $repoRoot -and -not $resolved.StartsWith($repoPrefix, $comparison)) {
        throw "WorkingDirectory must stay inside the repository: $resolved"
    }
    if (-not (Test-Path -LiteralPath $resolved -PathType Container)) {
        throw "WorkingDirectory does not exist: $resolved"
    }
    return $resolved
}

function Resolve-TestExecutable([string]$Candidate) {
    if ($Candidate -eq "bash" -and [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $bashCommand = Get-Command bash -ErrorAction SilentlyContinue
        if ($null -ne $bashCommand) {
            return $bashCommand.Source
        }
        $gitBash = Join-Path $env:ProgramFiles "Git\bin\bash.exe"
        if (Test-Path -LiteralPath $gitBash -PathType Leaf) {
            return $gitBash
        }
    }
    return $Candidate
}

if ($SelfTest -and -not [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "SelfTest cannot run a command. Put -- between FilePath and command arguments."
}
if ($Step.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "Step cannot be combined with an explicit FilePath."
}

$commands = @()
if ([string]::IsNullOrWhiteSpace($FilePath)) {
    if (-not (Test-Path -LiteralPath $planPath -PathType Leaf)) {
        throw "Canonical test plan does not exist: $planPath"
    }
    $plan = Get-Content -LiteralPath $planPath -Raw | ConvertFrom-Json
    if ($plan.version -ne 1) {
        throw "Unsupported canonical test plan version: $($plan.version)"
    }
    $allStepNames = @()
    foreach ($planStep in @($plan.steps)) {
        $name = [string]$planStep.name
        if ([string]::IsNullOrWhiteSpace($name)) {
            throw "Every canonical test step must have a name."
        }
        if ($allStepNames -contains $name) {
            throw "Canonical test step names must be unique: $name"
        }
        $allStepNames += $name
        if ($Step.Count -gt 0 -and $Step -notcontains $name) {
            continue
        }

        $environment = @{}
        if ($planStep.PSObject.Properties.Name -contains "environment") {
            foreach ($property in $planStep.environment.PSObject.Properties) {
                $environment[$property.Name] = [string]$property.Value
            }
        }
        $arguments = @()
        if ($planStep.PSObject.Properties.Name -contains "arguments") {
            $arguments = @($planStep.arguments | ForEach-Object { [string]$_ })
        }
        $stepWorkingDirectory = "."
        if ($planStep.PSObject.Properties.Name -contains "workingDirectory") {
            $stepWorkingDirectory = [string]$planStep.workingDirectory
        }
        $commands += [pscustomobject]@{
            Name             = $name
            WorkingDirectory = $stepWorkingDirectory
            FilePath         = [string]$planStep.filePath
            Arguments        = $arguments
            Environment      = $environment
        }
    }
    foreach ($requestedStep in $Step) {
        if ($allStepNames -notcontains $requestedStep) {
            throw "Unknown canonical test step: $requestedStep"
        }
    }
    if ($commands.Count -eq 0) {
        throw "Canonical test plan contains no selected steps: $planPath"
    }
} else {
    $commands += [pscustomobject]@{
        Name             = "explicit"
        WorkingDirectory = $WorkingDirectory
        FilePath         = $FilePath
        Arguments        = @($ArgumentList)
        Environment      = @{}
    }
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

$protectedEnvironmentNames = @($paths.Keys)
foreach ($command in $commands) {
    if ([string]::IsNullOrWhiteSpace($command.FilePath)) {
        throw "Canonical test step '$($command.Name)' must have a filePath."
    }
    [void](Resolve-WorkingPath $command.WorkingDirectory)
    foreach ($name in $command.Environment.Keys) {
        if ($protectedEnvironmentNames -contains $name) {
            throw "Canonical test step '$($command.Name)' cannot override protected environment variable $name."
        }
    }
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
        $hostExecutable = (Get-Process -Id $PID).Path
        $quotedNames = @($protectedEnvironmentNames | ForEach-Object {
            '"{0}"' -f $_.Replace('"', '""')
        })
        $probeCode = '$names=@({0}); foreach ($name in $names) {{ [Console]::WriteLine([Environment]::GetEnvironmentVariable($name, "Process")) }}' -f ($quotedNames -join ",")
        $encodedProbe = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($probeCode))
        $childValues = @(& $hostExecutable -NoProfile -NonInteractive -EncodedCommand $encodedProbe)
        if ($LASTEXITCODE -ne 0 -or $childValues.Count -ne $protectedEnvironmentNames.Count) {
            throw "Child-process environment probe failed."
        }
        for ($index = 0; $index -lt $protectedEnvironmentNames.Count; $index++) {
            $name = $protectedEnvironmentNames[$index]
            if ([IO.Path]::GetFullPath($childValues[$index]) -ne [IO.Path]::GetFullPath($paths[$name])) {
                throw "Child process did not inherit repository-local $name."
            }
        }
        Write-Host "[test-local] plan: $planPath"
        Write-Host "[test-local] steps: $($commands.Name -join ', ')"
        Write-Host "[OK] Repository-local test runtime and canonical plan contract passed"
        return
    }

    foreach ($command in $commands) {
        $workingPath = Resolve-WorkingPath $command.WorkingDirectory
        $executable = Resolve-TestExecutable $command.FilePath
        $stepPrevious = @{}
        foreach ($name in $command.Environment.Keys) {
            $stepPrevious[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
            [Environment]::SetEnvironmentVariable($name, $command.Environment[$name], "Process")
        }

        Write-Host "[test-local] step: $($command.Name)"
        Write-Host "[test-local] command: $executable $($command.Arguments -join ' ')"
        Push-Location $workingPath
        try {
            $global:LASTEXITCODE = 0
            & $executable @($command.Arguments)
            if ($LASTEXITCODE -ne 0) {
                throw "Test step '$($command.Name)' failed with exit code ${LASTEXITCODE}: $executable $($command.Arguments -join ' ')"
            }
        } finally {
            Pop-Location
            foreach ($name in $command.Environment.Keys) {
                [Environment]::SetEnvironmentVariable($name, $stepPrevious[$name], "Process")
            }
        }
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
