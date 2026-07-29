Write-Warning "test-windows-ci.ps1 is retained for compatibility. Use test-github-ci.ps1."
& (Join-Path $PSScriptRoot "test-github-ci.ps1")
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
