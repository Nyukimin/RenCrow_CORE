[CmdletBinding()]
param(
    [string]$RunnerPath = (Join-Path $PSScriptRoot "test-local.ps1"),
    [string]$FixtureRoot = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$script:repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$script:runnerPath = [IO.Path]::GetFullPath($RunnerPath)
$script:fixtureBase = $null
$script:runRoot = $null
$script:receiptPath = $null
$script:providerCalls = [Collections.Generic.List[object]]::new()
$script:results = [Collections.Generic.List[object]]::new()
$script:symlinkSupported = $false
$script:symlinkError = $null

function Assert-Condition([bool]$Condition, [string]$Message) {
    if (-not $Condition) {
        throw "test-runtime-layout assertion failed: $Message"
    }
}

function New-TestDirectory([string]$Path) {
    [IO.Directory]::CreateDirectory($Path) | Out-Null
}

function New-TestFile([string]$Path, [string]$Contents = "invalid Go fixture") {
    $parent = [IO.Directory]::GetParent($Path)
    if ($null -ne $parent) {
        New-TestDirectory $parent.FullName
    }
    [IO.File]::WriteAllText($Path, $Contents, [Text.UTF8Encoding]::new($false))
}

function Convert-ToRepoRelative([string]$Path) {
    $fullPath = [IO.Path]::GetFullPath($Path)
    $relative = $fullPath.Substring($script:repoRoot.Length).TrimStart(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    return $relative
}

function Get-FunctionFromRunner([string]$Path) {
    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile(
        $Path,
        [ref]$tokens,
        [ref]$parseErrors
    )
    Assert-Condition($parseErrors.Count -eq 0) "runner parse failed: $($parseErrors -join '; ')"
    $matches = @($ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -eq "Assert-TestRuntimeLayout"
    }, $true))
    Assert-Condition($matches.Count -eq 1) "expected one Assert-TestRuntimeLayout function in $Path"
    return $matches[0].Extent.Text
}

# The extracted implementation resolves this provider at invocation time. The
# wrapper records the exact provider contract while delegating to the built-in
# command, so the test does not duplicate the implementation under test.
function Get-ChildItem {
    param(
        [string]$LiteralPath,
        [switch]$Recurse,
        [switch]$File,
        [switch]$Force,
        [System.Management.Automation.ActionPreference]$ErrorAction = [System.Management.Automation.ActionPreference]::Continue
    )
    $fullPath = [IO.Path]::GetFullPath($LiteralPath)
    [void]$script:providerCalls.Add([pscustomobject]@{
        path    = $fullPath
        recurse = [bool]$Recurse
        file    = [bool]$File
    })
    $nativeParameters = @{
        LiteralPath = $LiteralPath
        Force       = $Force
        ErrorAction = $ErrorAction
    }
    if ($Recurse) {
        $nativeParameters.Recurse = $true
    }
    if ($File) {
        $nativeParameters.File = $true
    }
    Microsoft.PowerShell.Management\Get-ChildItem @nativeParameters
}

function Invoke-Layout([string]$Root) {
    $script:runtimeRoot = [IO.Path]::GetFullPath($Root)
    $script:providerCalls.Clear()
    $rejected = $false
    $message = $null
    try {
        Assert-TestRuntimeLayout
    } catch {
        $rejected = $true
        $message = $_.Exception.Message
    }
    $calls = @($script:providerCalls | ForEach-Object {
        [pscustomobject]@{
            path    = $_.path
            recurse = [bool]$_.recurse
            file    = [bool]$_.file
        }
    })
    $result = [pscustomobject]@{
        root     = $script:runtimeRoot
        rejected = $rejected
        message  = $message
        calls    = $calls
    }
    [void]$script:results.Add($result)
    return $result
}

function Assert-ProviderContract($Result, [string[]]$ExcludedDirectories, [string]$SymlinkDirectory) {
    Assert-Condition(@($Result.calls).Count -gt 0) "layout function did not enumerate its root"
    foreach ($call in @($Result.calls)) {
        Assert-Condition(-not $call.recurse) "provider call used -Recurse at $($call.path)"
        $callFull = [IO.Path]::GetFullPath($call.path).TrimEnd(
            [IO.Path]::DirectorySeparatorChar,
            [IO.Path]::AltDirectorySeparatorChar
        )
        foreach ($excluded in $ExcludedDirectories) {
            $excludedFull = [IO.Path]::GetFullPath($excluded).TrimEnd(
                [IO.Path]::DirectorySeparatorChar,
                [IO.Path]::AltDirectorySeparatorChar
            )
            $prefix = $excludedFull + [IO.Path]::DirectorySeparatorChar
            Assert-Condition(
                -not ($callFull.Equals($excludedFull, [StringComparison]::OrdinalIgnoreCase) -or
                    $callFull.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase))
            ) "provider traversed excluded directory $excluded"
        }
        if (-not [string]::IsNullOrWhiteSpace($SymlinkDirectory)) {
            $linkFull = [IO.Path]::GetFullPath($SymlinkDirectory).TrimEnd(
                [IO.Path]::DirectorySeparatorChar,
                [IO.Path]::AltDirectorySeparatorChar
            )
            $linkPrefix = $linkFull + [IO.Path]::DirectorySeparatorChar
            Assert-Condition(
                -not ($callFull.Equals($linkFull, [StringComparison]::OrdinalIgnoreCase) -or
                    $callFull.StartsWith($linkPrefix, [StringComparison]::OrdinalIgnoreCase))
            ) "provider traversed symlink directory $SymlinkDirectory"
        }
    }
}

function Assert-RejectionContains($Result, [string[]]$ExpectedPaths) {
    Assert-Condition($Result.rejected) "normal .go fixture was not rejected"
    Assert-Condition($Result.message.StartsWith("TEST_RUNTIME_LAYOUT_INVALID:", [StringComparison]::Ordinal)) "unexpected rejection: $($Result.message)"
    foreach ($expectedPath in $ExpectedPaths) {
        Assert-Condition($Result.message.Contains((Convert-ToRepoRelative $expectedPath))) "rejection omitted $expectedPath"
    }
}

function Save-Receipt([string]$Status, [string]$ErrorMessage = $null) {
    $receipt = [ordered]@{
        schema_version    = 1
        status            = $Status
        runner_path       = $script:runnerPath
        runner_sha256     = (Get-FileHash -LiteralPath $script:runnerPath -Algorithm SHA256).Hash
        fixture_root      = $script:fixtureBase
        symlink_supported = $script:symlinkSupported
        symlink_error     = $script:symlinkError
        results           = @($script:results)
        error             = $ErrorMessage
    }
    New-TestDirectory $script:runRoot
    [IO.File]::WriteAllText(
        $script:receiptPath,
        ($receipt | ConvertTo-Json -Depth 12),
        [Text.UTF8Encoding]::new($false)
    )
}

try {
    Assert-Condition(Test-Path -LiteralPath $script:runnerPath -PathType Leaf) "runner does not exist: $script:runnerPath"
    $fixtureCandidate = $FixtureRoot
    if ([string]::IsNullOrWhiteSpace($fixtureCandidate)) {
        $fixtureCandidate = if (-not [string]::IsNullOrWhiteSpace($env:TMPDIR)) { $env:TMPDIR } else { $env:TMP }
        $candidateFull = if ([string]::IsNullOrWhiteSpace($fixtureCandidate)) { "" } else { [IO.Path]::GetFullPath($fixtureCandidate) }
        $repoPrefixForCandidate = $script:repoRoot + [IO.Path]::DirectorySeparatorChar
        if ([string]::IsNullOrWhiteSpace($candidateFull) -or -not $candidateFull.StartsWith($repoPrefixForCandidate, [StringComparison]::OrdinalIgnoreCase)) {
            $fixtureCandidate = Join-Path $script:repoRoot "Tmp/test-runtime/_layout-tests"
        }
    }
    $script:fixtureBase = [IO.Path]::GetFullPath($fixtureCandidate)
    $repoPrefix = $script:repoRoot + [IO.Path]::DirectorySeparatorChar
    Assert-Condition(
        $script:fixtureBase.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)
    ) "fixture root escaped repository: $script:fixtureBase"
    New-TestDirectory $script:fixtureBase
    $script:runRoot = Join-Path $script:fixtureBase ("run-" + [Guid]::NewGuid().ToString("N"))
    $script:receiptPath = Join-Path $script:runRoot "receipt.json"
    New-TestDirectory $script:runRoot
    $functionText = Get-FunctionFromRunner $script:runnerPath
    Invoke-Expression -Command $functionText

    $runRoot = $script:runRoot
    $caseRoot = Join-Path $runRoot "case"
    $caseRuntime = Join-Path $caseRoot "runtime"
    New-TestDirectory $caseRuntime
    $caseNormal = Join-Path $caseRuntime "normal"
    $caseNested = Join-Path $caseNormal "nested"
    New-TestFile (Join-Path $caseNormal "root.go")
    New-TestFile (Join-Path $caseNested "deep.go")
    New-TestFile (Join-Path $caseNormal "upper.GO")
    New-TestFile (Join-Path $caseNormal "mixed.Go")
    New-TestFile (Join-Path $caseNormal "lower.gO")
    New-TestFile (Join-Path $caseRuntime "_root.go") "invalid ignored root file"
    New-TestFile (Join-Path $caseRuntime ".root.go") "invalid ignored root file"
    $caseCache = Join-Path $caseRuntime "_cache"
    $caseHidden = Join-Path $caseRuntime ".hidden"
    New-TestFile (Join-Path $caseCache "ignored.go") "this is deliberately invalid Go"
    New-TestFile (Join-Path $caseHidden "ignored.go") "this is deliberately invalid Go"
    $caseResult = Invoke-Layout $caseRuntime
    $caseExpected = @(
        (Join-Path $caseNormal "root.go"),
        (Join-Path $caseNested "deep.go"),
        (Join-Path $caseNormal "upper.GO"),
        (Join-Path $caseNormal "mixed.Go"),
        (Join-Path $caseNormal "lower.gO")
    )
    Assert-ProviderContract $caseResult @($caseCache, $caseHidden) ""
    Assert-RejectionContains $caseResult $caseExpected
    Assert-Condition(-not $caseResult.message.Contains((Convert-ToRepoRelative (Join-Path $caseCache "ignored.go")))) "excluded cache file was reported"
    Assert-Condition(-not $caseResult.message.Contains((Convert-ToRepoRelative (Join-Path $caseHidden "ignored.go")))) "excluded hidden file was reported"

    $linkTarget = Join-Path $caseRuntime "_symlink-target"
    $linkDirectory = Join-Path $caseRuntime "symlink-dir"
    New-TestFile (Join-Path $linkTarget "visible.go")
    try {
        [IO.Directory]::CreateSymbolicLink($linkDirectory, $linkTarget) | Out-Null
        $script:symlinkSupported = $true
    } catch {
        $script:symlinkError = $_.Exception.Message
    }
    if ($script:symlinkSupported) {
        $caseWithLink = Invoke-Layout $caseRuntime
        Assert-ProviderContract $caseWithLink @($caseCache, $caseHidden) $linkDirectory
        Assert-Condition(-not $caseWithLink.message.Contains((Convert-ToRepoRelative (Join-Path $linkDirectory "visible.go")))) "symlink target was reported"
    }

    $cleanRuntime = Join-Path $runRoot "clean-runtime"
    New-TestDirectory $cleanRuntime
    New-TestFile (Join-Path $cleanRuntime "_ignored.go") "invalid ignored Go"
    New-TestFile (Join-Path $cleanRuntime ".ignored.go") "invalid ignored Go"
    New-TestFile (Join-Path (Join-Path $cleanRuntime "_cache") "ignored.go") "invalid ignored Go"
    New-TestFile (Join-Path (Join-Path $cleanRuntime ".hidden") "ignored.go") "invalid ignored Go"
    $cleanResult = Invoke-Layout $cleanRuntime
    Assert-ProviderContract $cleanResult @((Join-Path $cleanRuntime "_cache"), (Join-Path $cleanRuntime ".hidden")) ""
    Assert-Condition(-not $cleanResult.rejected) "excluded-only fixture was rejected: $($cleanResult.message)"

    $capRuntime = Join-Path $runRoot "cap-runtime"
    $capDirectory = Join-Path $capRuntime "normal"
    New-TestDirectory $capDirectory
    $capExpected = [Collections.Generic.List[string]]::new()
    for ($index = 0; $index -lt 25; $index++) {
        $capFile = Join-Path $capDirectory ("generated-{0:D2}.go" -f $index)
        New-TestFile $capFile
        [void]$capExpected.Add((Convert-ToRepoRelative $capFile))
    }
    New-TestFile (Join-Path $capRuntime "_ignored.go") "invalid ignored Go"
    $capResult = Invoke-Layout $capRuntime
    Assert-ProviderContract $capResult @() ""
    Assert-Condition($capResult.rejected) "cap fixture was not rejected"
    Assert-Condition($capResult.message.Contains(" (and 5 more)")) "cap fixture did not preserve report count: $($capResult.message)"
    $capPrefix = "TEST_RUNTIME_LAYOUT_INVALID: generated .go exists below a traversable Tmp/test-runtime directory: "
    Assert-Condition($capResult.message.StartsWith($capPrefix, [StringComparison]::Ordinal)) "cap fixture prefix changed: $($capResult.message)"
    $capDetail = $capResult.message.Substring($capPrefix.Length)
    $capDetail = [Regex]::Replace($capDetail, " \(and 5 more\)$", "")
    $capReported = @($capDetail -split ", " | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    Assert-Condition($capReported.Count -eq 20) "expected exactly 20 reported paths, got $($capReported.Count)"
    foreach ($reported in $capReported) {
        Assert-Condition($capExpected -contains $reported) "report was not an exact repo-relative path: $reported"
    }

    Save-Receipt "passed"
    Write-Output ((Get-Content -LiteralPath $script:receiptPath -Raw).Trim())
    exit 0
} catch {
    $errorMessage = $_.Exception.Message
    try {
        Save-Receipt "failed" $errorMessage
    } catch {
        [Console]::Error.WriteLine("test-runtime-layout receipt write failed: $($_.Exception.Message)")
    }
    [Console]::Error.WriteLine("test-runtime-layout failed: $errorMessage")
    exit 1
}
