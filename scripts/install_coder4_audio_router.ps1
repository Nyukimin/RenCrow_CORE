$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$ExeSource = Join-Path $RepoRoot "dist\rencrow-agent-coder4.exe"
$ConfigSource = Join-Path $RepoRoot "scripts\coder4_audio_router_config.yaml"
$TargetDir = Join-Path $env:USERPROFILE ".rencrow"
$TargetExe = Join-Path $env:USERPROFILE "rencrow-agent.exe"
$TargetConfig = Join-Path $TargetDir "config.yaml"

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

if (-not (Test-Path $ExeSource)) {
    throw "Missing binary: $ExeSource"
}
if (-not (Test-Path $ConfigSource)) {
    throw "Missing config template: $ConfigSource"
}

Copy-Item -Force $ExeSource $TargetExe

if (-not (Test-Path $TargetConfig)) {
    Copy-Item $ConfigSource $TargetConfig
    Write-Host "Created config: $TargetConfig"
} else {
    Write-Host "Config already exists, left unchanged: $TargetConfig"
}

Write-Host ""
Write-Host "1. Enumerate devices:"
Write-Host "   $TargetExe --standalone --agent audio_router devices --config $TargetConfig"
Write-Host ""
Write-Host "2. Edit device_id values in:"
Write-Host "   $TargetConfig"
Write-Host ""
Write-Host "3. Start AudioRouter:"
Write-Host "   $TargetExe --standalone --agent audio_router --config $TargetConfig"
