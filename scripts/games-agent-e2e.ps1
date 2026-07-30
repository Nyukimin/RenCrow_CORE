[CmdletBinding()]
param(
    [string]$CoreBaseURL = "http://127.0.0.1:18790",
    [ValidateSet("nethack")]
    [string]$GameID = "nethack",
    [ValidateSet("mio", "shiro", "kuro", "midori")]
    [string]$AgentID = "mio",
    [ValidateRange(1, 100)]
    [int]$Turns = 8,
    [ValidateRange(10, 900)]
    [int]$TimeoutSeconds = 180
)

$ErrorActionPreference = "Stop"
$core = $CoreBaseURL.TrimEnd("/")
$observerAPI = "$core/viewer/games/observer-api"

$status = Invoke-RestMethod -Uri "$core/viewer/games/status" -TimeoutSec 10
if ($status.decision_mode -ne "agent") {
    throw "CORE game decision_mode is '$($status.decision_mode)', expected 'agent'."
}
if (@($status.endpoints) -notcontains "/viewer/games/decision") {
    throw "CORE does not advertise /viewer/games/decision."
}

$launchBody = @{
    game_id = $GameID
    personas = @($AgentID)
    turns = $Turns
    mode = "auto"
    reason = "Agent E2E verification"
} | ConvertTo-Json -Compress

$launch = Invoke-RestMethod `
    -Uri "$core/viewer/games/launch" `
    -Method Post `
    -ContentType "application/json" `
    -Body $launchBody `
    -TimeoutSec 20

if (-not $launch.session_id) {
    throw "CORE launch response did not include session_id."
}
$sessionID = [string]$launch.session_id
$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
$session = $null

do {
    Start-Sleep -Milliseconds 500
    $sessionsResponse = Invoke-RestMethod -Uri "$observerAPI/games/sessions" -TimeoutSec 10
    $session = @($sessionsResponse.sessions) |
        Where-Object { $_.session_id -eq $sessionID } |
        Select-Object -First 1
} until (
    ($null -ne $session -and $session.status -in @("completed", "failed")) -or
    (Get-Date) -ge $deadline
)

if ($null -eq $session) {
    throw "Agent E2E session '$sessionID' did not appear before timeout."
}
if ($session.status -ne "completed") {
    throw "Agent E2E session '$sessionID' ended with status '$($session.status)'."
}

$framesResponse = Invoke-RestMethod `
    -Uri "$observerAPI/games/sessions/$([uri]::EscapeDataString($sessionID))/frames" `
    -TimeoutSec 30
$frames = @($framesResponse.frames)
if ($frames.Count -lt 1) {
    throw "Agent E2E session '$sessionID' has no observer frames."
}

$nonAgentFrames = @($frames | Where-Object {
    $_.decision.agent_id -ne $AgentID -or
    $_.decision.persona -ne $AgentID
})
if ($nonAgentFrames.Count -gt 0) {
    $turnsWithoutAgent = ($nonAgentFrames | ForEach-Object { $_.turn }) -join ","
    throw "Agent ownership is missing or mismatched on turns: $turnsWithoutAgent"
}

[pscustomobject]@{
    OK = $true
    GameID = $GameID
    SessionID = $sessionID
    AgentID = $AgentID
    FrameCount = $frames.Count
    FinalTurn = $frames[-1].turn
    DecisionMode = $status.decision_mode
}
