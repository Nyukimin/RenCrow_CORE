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
$runsRoot = Join-Path $runtimeRoot "_runs"
$cacheRoot = Join-Path $runtimeRoot "_cache"
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

function Assert-TestRuntimeLayout {
    if (-not (Test-Path -LiteralPath $runtimeRoot -PathType Container)) {
        return
    }

    # Go ignores directory names beginning with '_' or '.', so only a .go
    # file below a normal-name runtime directory can become a package during
    # `go list ./...`. Never remove an unknown stale tree here: fail closed
    # and report the exact paths for an operator-owned cleanup decision.
    $legacyGeneratedFiles = @(Get-ChildItem -LiteralPath $runtimeRoot -Recurse -File -Force -ErrorAction SilentlyContinue | Where-Object {
        if ($_.Extension -ne ".go") {
            return $false
        }
        $relative = $_.FullName.Substring($runtimeRoot.Length).TrimStart([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
        $segments = $relative.Replace("\", "/").Split("/", [StringSplitOptions]::RemoveEmptyEntries)
        -not ($segments | Where-Object { $_.StartsWith("_", [StringComparison]::Ordinal) -or $_.StartsWith(".", [StringComparison]::Ordinal) })
    })
    if ($legacyGeneratedFiles.Count -gt 0) {
        $reported = @($legacyGeneratedFiles | Select-Object -First 20 | ForEach-Object {
            $_.FullName.Substring($repoRoot.Length).TrimStart([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
        })
        $suffix = if ($legacyGeneratedFiles.Count -gt $reported.Count) {
            " (and $($legacyGeneratedFiles.Count - $reported.Count) more)"
        } else {
            ""
        }
        throw "TEST_RUNTIME_LAYOUT_INVALID: generated .go exists below a traversable Tmp/test-runtime directory: $($reported -join ', ')$suffix"
    }
}

Assert-TestRuntimeLayout

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

function Test-IsTrackedTestFile([string]$RelativePath) {
    $normalizedPath = $RelativePath.Replace("\", "/")
    $fileName = [IO.Path]::GetFileName($RelativePath)
    $isTestDirectoryCode = (
        $normalizedPath -match "(^|/)(test|tests|scripts/tests)/" -and
        $fileName -match "\.(go|py|[cm]?js|sh)$"
    )
    return (
        $isTestDirectoryCode -or
        $fileName -match "_test\.go$" -or
        $fileName -match "^test_.*\.py$" -or
        $fileName -match "_test\.py$" -or
        $fileName -match "^test_.*\.[cm]?js$" -or
        $fileName -match "(\.test|_test)\.[cm]?js$" -or
        $fileName -match "_test\.sh$" -or
        $fileName -match "^test[-_].*\.sh$"
    )
}

function Test-MatchesTestFilePattern([string]$RelativePath, [string]$Pattern) {
    $normalizedPattern = $Pattern.Replace("\", "/")
    $wildcardOptions = if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        [Management.Automation.WildcardOptions]::IgnoreCase
    } else {
        [Management.Automation.WildcardOptions]::None
    }
    $wildcard = [Management.Automation.WildcardPattern]::new(
        $normalizedPattern,
        $wildcardOptions
    )
    return $wildcard.IsMatch($RelativePath.Replace("\", "/"))
}

if ($SelfTest -and -not [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "SelfTest cannot run a command. Put -- between FilePath and command arguments."
}
if ($Step.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace($FilePath)) {
    throw "Step cannot be combined with an explicit FilePath."
}

$commands = @()
$testFilePatterns = @()
$trackedTestFiles = @()
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

        if ($planStep.PSObject.Properties.Name -contains "testFiles") {
            foreach ($patternValue in @($planStep.testFiles)) {
                $pattern = ([string]$patternValue).Replace("\", "/")
                if ([string]::IsNullOrWhiteSpace($pattern)) {
                    throw "Canonical test step '$name' contains an empty testFiles pattern."
                }
                if ([IO.Path]::IsPathRooted($pattern) -or $pattern -match "(^|/)\.\.(/|$)") {
                    throw "Canonical test step '$name' contains an unsafe testFiles pattern: $pattern"
                }
                $testFilePatterns += $pattern
            }
        }

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

    $trackedFiles = @(git -C $repoRoot -c core.quotepath=false ls-files --cached --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed while checking canonical test coverage."
    }
    $trackedTestFiles = @($trackedFiles | ForEach-Object { $_.Replace("\", "/") } | Where-Object {
        (Test-Path -LiteralPath (Join-Path $repoRoot $_) -PathType Leaf) -and
        (Test-IsTrackedTestFile $_)
    })
    $uncoveredTestFiles = @($trackedTestFiles | Where-Object {
        $relativePath = $_
        -not ($testFilePatterns | Where-Object {
            Test-MatchesTestFilePattern $relativePath $_
        } | Select-Object -First 1)
    })
    if ($uncoveredTestFiles.Count -gt 0) {
        throw "Canonical test plan does not cover tracked test files: $($uncoveredTestFiles -join ', ')"
    }
    $unusedTestFilePatterns = @($testFilePatterns | Where-Object {
        $pattern = $_
        -not ($trackedTestFiles | Where-Object {
            Test-MatchesTestFilePattern $_ $pattern
        } | Select-Object -First 1)
    })
    if ($unusedTestFilePatterns.Count -gt 0) {
        throw "Canonical test plan contains testFiles patterns that match no tracked test: $($unusedTestFilePatterns -join ', ')"
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
    GOTMPDIR              = (Join-Path $runRoot "_go-build")
    GOCACHE               = (Join-Path $cacheRoot "go-build")
    GOMODCACHE            = (Join-Path $cacheRoot "go-mod")
    PYTHONPYCACHEPREFIX   = (Join-Path $cacheRoot "python-bytecode")
    PYTEST_DEBUG_TEMPROOT = (Join-Path $runRoot "pytest")
    UV_CACHE_DIR          = (Join-Path $cacheRoot "uv")
    PIP_CACHE_DIR         = (Join-Path $cacheRoot "pip")
    npm_config_cache      = (Join-Path $cacheRoot "npm")
    NODE_COMPILE_CACHE    = (Join-Path $cacheRoot "node-compile")
    PLAYWRIGHT_BROWSERS_PATH = (Join-Path $cacheRoot "playwright")
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
$previousTestPython = [Environment]::GetEnvironmentVariable("RENCROW_TEST_PYTHON", "Process")
$sentinelName = "RENCROW_TEST_ENV_MERGE_SENTINEL"
$previousSentinel = [Environment]::GetEnvironmentVariable($sentinelName, "Process")
$sentinelValue = "preserved-{0}" -f ([Guid]::NewGuid().ToString("N"))
[Environment]::SetEnvironmentVariable($sentinelName, $sentinelValue, "Process")
$pythonCommand = Get-Command python -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -ne $pythonCommand) {
    $testPython = $pythonCommand.Source
    if ([IO.Path]::DirectorySeparatorChar -eq '\') {
        $testPython = $testPython.Replace('\', '/')
    }
    [Environment]::SetEnvironmentVariable("RENCROW_TEST_PYTHON", $testPython, "Process")
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
        if (-not (Split-Path -Leaf $runsRoot).StartsWith("_", [StringComparison]::Ordinal) -or
            -not (Split-Path -Leaf $cacheRoot).StartsWith("_", [StringComparison]::Ordinal) -or
            -not (Split-Path -Leaf $paths["GOTMPDIR"]).StartsWith("_", [StringComparison]::Ordinal)) {
            throw "Repository-local test runtime paths must use underscore-prefixed Go-traversal-safe directories."
        }
        $hostExecutable = (Get-Process -Id $PID).Path
        $probeNames = @($protectedEnvironmentNames + $sentinelName)
        $quotedNames = @($probeNames | ForEach-Object {
            '"{0}"' -f $_.Replace('"', '""')
        })
        $probeCode = '$names=@({0}); foreach ($name in $names) {{ [Console]::WriteLine([Environment]::GetEnvironmentVariable($name, "Process")) }}' -f ($quotedNames -join ",")
        $encodedProbe = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($probeCode))
        $childValues = @(& $hostExecutable -NoProfile -NonInteractive -EncodedCommand $encodedProbe)
        if ($LASTEXITCODE -ne 0 -or $childValues.Count -ne $probeNames.Count) {
            throw "Child-process environment probe failed."
        }
        for ($index = 0; $index -lt $protectedEnvironmentNames.Count; $index++) {
            $name = $protectedEnvironmentNames[$index]
            if ([IO.Path]::GetFullPath($childValues[$index]) -ne [IO.Path]::GetFullPath($paths[$name])) {
                throw "Child process did not inherit repository-local $name."
            }
        }
        if ($childValues[$probeNames.Count - 1] -ne $sentinelValue) {
            throw "Child process environment was replaced instead of merged."
        }
        Write-Host "[test-local] plan: $planPath"
        Write-Host "[test-local] steps: $($commands.Name -join ', ')"
        Write-Host "[test-local] tracked tests: $($trackedTestFiles.Count)"
        Write-Host "[OK] Repository-local test runtime and canonical plan contract passed"
        return
    }

    foreach ($command in $commands) {
        $workingPath = Resolve-WorkingPath $command.WorkingDirectory
        $executable = Resolve-TestExecutable $command.FilePath
        $stepPrevious = @{}
        $stepPathWasAdjusted = $false
        $stepPathPrevious = [Environment]::GetEnvironmentVariable("PATH", "Process")
        if ([IO.Path]::GetFileNameWithoutExtension($executable) -eq "bash") {
            $bashDirectory = Split-Path -Parent $executable
            $pathParts = @($stepPathPrevious -split [Regex]::Escape([IO.Path]::PathSeparator))
            if ($pathParts.Count -eq 0 -or -not $pathParts[0].Equals($bashDirectory, $comparison)) {
                [Environment]::SetEnvironmentVariable(
                    "PATH",
                    $bashDirectory + [IO.Path]::PathSeparator + $stepPathPrevious,
                    "Process"
                )
                $stepPathWasAdjusted = $true
            }
        }
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
            if ($stepPathWasAdjusted) {
                [Environment]::SetEnvironmentVariable("PATH", $stepPathPrevious, "Process")
            }
        }
    }
} finally {
    [Environment]::SetEnvironmentVariable($sentinelName, $previousSentinel, "Process")
    [Environment]::SetEnvironmentVariable("RENCROW_TEST_PYTHON", $previousTestPython, "Process")
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
