$ErrorActionPreference = "Stop"
function Get-UntrackedSourceSnapshot {
    $paths = @(git -c core.quotepath=false ls-files --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw "Cannot enumerate untracked source." }
    return @($paths | Sort-Object | ForEach-Object {
        if (Test-Path -LiteralPath $_ -PathType Leaf) {
            "$_ $((Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash)"
        }
    }) -join "`n"
}
# Compare against the caller's existing diff; generation must not change it.
$before = @(git diff --binary HEAD --)
if ($LASTEXITCODE -ne 0) { throw "Cannot capture pre-generation diff." }
$untrackedBefore = Get-UntrackedSourceSnapshot
go generate ./...
if ($LASTEXITCODE -ne 0) { throw "go generate failed." }
$after = @(git diff --binary HEAD --)
if ($LASTEXITCODE -ne 0) { throw "Cannot capture post-generation diff." }
if (($before -join "`n") -cne ($after -join "`n") -or $untrackedBefore -cne (Get-UntrackedSourceSnapshot)) {
    throw "Generated files changed; inspect and commit the generated source before retrying."
}
