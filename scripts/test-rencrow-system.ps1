param(
    [string[]]$Repository = @(),
    [switch]$KeepRuntime,
    [switch]$SelfTest
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$knownRepositories = @(
    "RenCrow_CMD",
    "RenCrow_CORE",
    "RenCrow_EcoSystem",
    "RenCrow_GAMES",
    "RenCrow_Image",
    "RenCrow_LLM",
    "RenCrow_PORTAL",
    "RenCrow_STT",
    "RenCrow_Tools",
    "RenCrow_TTS",
    "RenCrow_Vision",
    "RenCrow_Workspace"
)

$selectedRepositories = if ($Repository.Count -eq 0) {
    $knownRepositories
} else {
    foreach ($name in $Repository) {
        if ($knownRepositories -notcontains $name) {
            throw "Unknown RenCrow repository: $name"
        }
        $name
    }
}

$passed = @()
$failed = @()
foreach ($name in $selectedRepositories) {
    $runner = Join-Path $workspaceRoot "$name\scripts\test-local.ps1"
    if (-not (Test-Path -LiteralPath $runner -PathType Leaf)) {
        $failed += "$name (runner missing)"
        continue
    }

    Write-Host ""
    Write-Host "=== $name ==="
    try {
        $runnerParameters = @{}
        if ($KeepRuntime) {
            $runnerParameters["KeepRuntime"] = $true
        }
        if ($SelfTest) {
            $runnerParameters["SelfTest"] = $true
        }
        & $runner @runnerParameters
        $passed += $name
    } catch {
        Write-Error -ErrorAction Continue "[$name] $($_.Exception.Message)"
        $failed += $name
    }
}

Write-Host ""
Write-Host "[test-rencrow-system] passed: $($passed.Count)"
Write-Host "[test-rencrow-system] failed: $($failed.Count)"
if ($failed.Count -gt 0) {
    throw "RenCrow system test failed: $($failed -join ', ')"
}
