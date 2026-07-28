$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Invoke-WindowsCi {
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot
try {
$workflow = "go-test.yml"
$branch = (git branch --show-current).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($branch)) {
    throw "Current Git branch could not be determined."
}

$changes = @(git status --porcelain)
if ($LASTEXITCODE -ne 0) {
    throw "Git status failed."
}
if ($changes.Count -ne 0) {
    throw "Working tree is not clean. Commit and push the exact revision before starting Windows CI."
}

git fetch origin --prune --quiet
if ($LASTEXITCODE -ne 0) {
    throw "Fetching origin/$branch failed."
}

$head = (git rev-parse HEAD).Trim()
$remoteHead = (git rev-parse "origin/$branch").Trim()
if ($LASTEXITCODE -ne 0) {
    throw "Remote branch origin/$branch could not be resolved."
}
if ($head -ne $remoteHead) {
    throw "HEAD does not match origin/$branch. Push the exact revision before starting Windows CI."
}

gh auth status | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "GitHub CLI is not authenticated. Run gh auth login first."
}

$startedAt = (Get-Date).ToUniversalTime().AddSeconds(-5)
gh workflow run $workflow --ref $branch
if ($LASTEXITCODE -ne 0) {
    throw "Starting $workflow failed."
}

$run = $null
for ($attempt = 0; $attempt -lt 30 -and $null -eq $run; $attempt++) {
    Start-Sleep -Seconds 2
    $runs = gh run list `
        --workflow $workflow `
        --branch $branch `
        --event workflow_dispatch `
        --limit 20 `
        --json databaseId,headSha,createdAt | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) {
        throw "Listing workflow runs failed."
    }
    $run = $runs |
        Where-Object {
            $_.headSha -eq $head -and
            ([DateTimeOffset]$_.createdAt).UtcDateTime -ge $startedAt
        } |
        Sort-Object createdAt -Descending |
        Select-Object -First 1
}

if ($null -eq $run) {
    throw "The workflow run for commit $head was not found."
}

Write-Host "Watching Windows CI workflow run $($run.databaseId) for commit $head"
gh run watch $run.databaseId --exit-status
if ($LASTEXITCODE -ne 0) {
    throw "Windows CI failed. Inspect it with: gh run view $($run.databaseId) --log-failed"
}

Write-Host "Windows CI passed for commit $head"
} finally {
    Pop-Location
}
}

Invoke-WindowsCi
