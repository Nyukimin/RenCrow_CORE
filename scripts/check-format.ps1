$ErrorActionPreference = "Stop"
$files = @(git -c core.quotepath=false ls-files --cached --others --exclude-standard -- '*.go')
if ($LASTEXITCODE -ne 0) { throw "Cannot enumerate Go source." }
foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { continue }
    $unformatted = @(gofmt -l $file)
    if ($LASTEXITCODE -ne 0 -or $unformatted.Count -gt 0) {
        throw "Go formatting check failed: $file"
    }
}
