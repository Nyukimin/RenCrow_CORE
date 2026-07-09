<#
.SYNOPSIS
    Migrate the legacy picoclaw_multiLLM remote-agent home directory
    ($env:USERPROFILE\.picoclaw) to the RenCrow_CORE remote-agent home
    directory ($env:USERPROFILE\.rencrow), and de-nest a legacy
    .rencrow\rencrow\memory directory into .rencrow\memory if present.

.PARAMETER DryRun
    Print what would happen without changing anything.

.EXAMPLE
    powershell -File scripts\migrate_picoclaw_home.ps1 -DryRun
    powershell -File scripts\migrate_picoclaw_home.ps1
#>

[CmdletBinding()]
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Write-Log {
    param([string]$Message)
    Write-Host "[migrate_picoclaw_home] $Message"
}

$OldHome = Join-Path $env:USERPROFILE ".picoclaw"
$NewHome = Join-Path $env:USERPROFILE ".rencrow"
$NestedMemory = Join-Path $NewHome "rencrow\memory"
$FlatMemory = Join-Path $NewHome "memory"
$NestedDir = Join-Path $NewHome "rencrow"

Write-Log "old runtime home: $OldHome"
Write-Log "new runtime home: $NewHome"

if (-not (Test-Path -LiteralPath $OldHome)) {
    Write-Log "no $OldHome found -- nothing to migrate."
    exit 0
}

if (Test-Path -LiteralPath $NewHome) {
    Write-Log "ABORT: $NewHome already exists. Refusing to overwrite an existing runtime home."
    Write-Log "Inspect $NewHome manually, then re-run once it is removed or merged."
    exit 1
}

# --- Step 1: copy .picoclaw -> .rencrow ---------------------------------
Write-Log "copying $OldHome -> $NewHome"
if ($DryRun) {
    Write-Log "DRY-RUN: Copy-Item -Recurse -Force `"$OldHome`" `"$NewHome`""
} else {
    Copy-Item -LiteralPath $OldHome -Destination $NewHome -Recurse -Force
}

if ($DryRun) {
    Write-Log "DRY-RUN: would de-nest $NestedMemory -> $FlatMemory (if present)"
    Write-Log "DRY-RUN: would remove leftover empty $NestedDir (if empty)"
} else {
    # --- Step 2: de-nest .rencrow\rencrow\memory -> .rencrow\memory ----
    if (Test-Path -LiteralPath $NestedMemory) {
        if (Test-Path -LiteralPath $FlatMemory) {
            Write-Log "WARNING: both $NestedMemory and $FlatMemory exist. Skipping automatic de-nesting."
            Write-Log "WARNING: please merge them manually, e.g.:"
            Write-Log "WARNING:   Copy-Item -Recurse -Force `"$NestedMemory\*`" `"$FlatMemory`" ; Remove-Item -Recurse -Force `"$NestedMemory`""
        } else {
            Write-Log "de-nesting $NestedMemory -> $FlatMemory"
            Move-Item -LiteralPath $NestedMemory -Destination $FlatMemory
        }
    }

    # --- Step 3: remove leftover empty .rencrow\rencrow\ ---------------
    if (Test-Path -LiteralPath $NestedDir) {
        $remaining = Get-ChildItem -LiteralPath $NestedDir -Force
        if ($null -eq $remaining -or $remaining.Count -eq 0) {
            Write-Log "removing empty leftover directory $NestedDir"
            Remove-Item -LiteralPath $NestedDir -Force
        } else {
            Write-Log "WARNING: $NestedDir still has content after de-nesting, left in place for manual review:"
            $remaining | ForEach-Object { Write-Log "WARNING:   $($_.Name)" }
        }
    }
}

Write-Log "migration of runtime home directory complete."
Write-Log ""
Write-Log "Remaining manual steps:"
Write-Log "  1. If a scheduled task or Startup entry launches the legacy picoclaw-agent.exe,"
Write-Log "     review and update it by hand (Task Scheduler / shell:startup) -- this script"
Write-Log "     does not modify scheduled tasks automatically."
Write-Log "  2. Point any scheduled task / shortcut at the new rencrow-agent.exe binary and"
Write-Log "     the new config path under $NewHome\config.yaml."
Write-Log "  3. If you set PICOCLAW_* environment variables (User/System env vars), rename"
Write-Log "     them to RENCROW_*."
Write-Log "  4. Once you have verified $NewHome looks correct, you may delete $OldHome manually:"
Write-Log "       Remove-Item -Recurse -Force `"$OldHome`""
