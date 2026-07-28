param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("worker", "coder1", "coder2", "coder3", "coder4")]
    [string]$AgentType
)

$ErrorActionPreference = "Stop"

$rencrowHome = Join-Path $env:USERPROFILE ".rencrow"
$rencrowBin = Join-Path $rencrowHome "bin"
$binaryName = "rencrow-agent.exe"

New-Item -ItemType Directory -Force -Path (Join-Path $rencrowHome "logs") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $rencrowHome "workspace") | Out-Null
New-Item -ItemType Directory -Force -Path $rencrowBin | Out-Null

$sourceBinary = if (Test-Path ".\rencrow-agent-windows-amd64.exe") {
    ".\rencrow-agent-windows-amd64.exe"
} elseif (Test-Path ".\rencrow-agent.exe") {
    ".\rencrow-agent.exe"
} else {
    throw "rencrow-agent executable was not found"
}
Copy-Item -LiteralPath $sourceBinary -Destination (Join-Path $rencrowBin $binaryName) -Force

$configPath = Join-Path $rencrowHome "config.yaml"
if (-not (Test-Path -LiteralPath $configPath)) {
    $configSource = if (Test-Path ".\config\config.yaml.example") {
        ".\config\config.yaml.example"
    } elseif (Test-Path ".\config.yaml.example") {
        ".\config.yaml.example"
    } else {
        throw "config.yaml.example was not found"
    }
    Copy-Item -LiteralPath $configSource -Destination $configPath
}

$envPath = Join-Path $rencrowHome ".env"
if (-not (Test-Path -LiteralPath $envPath)) {
    @"
# Optional RenCrow_LLM Gateway credential.
RENCROW_LLM_API_KEY=
"@ | Out-File -FilePath $envPath -Encoding utf8
}

$taskName = "RenCrowAgent-$AgentType"
$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

$action = New-ScheduledTaskAction `
    -Execute (Join-Path $rencrowBin $binaryName) `
    -Argument "-standalone -agent $AgentType -config $configPath" `
    -WorkingDirectory $rencrowHome
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive

Register-ScheduledTask `
    -TaskName $taskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Description "RenCrow Agent ($AgentType)" | Out-Null

Write-Host "Installed $taskName."
Write-Host "Configure llm_gateway in $configPath for RenCrow_LLM."
Write-Host "Start-ScheduledTask -TaskName '$taskName'"
