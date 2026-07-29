param(
    [string]$WorkspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repositories = Get-ChildItem -LiteralPath $WorkspaceRoot -Directory |
    Where-Object {
        $_.Name -like "RenCrow_*" -and
        (Test-Path -LiteralPath (Join-Path $_.FullName ".git") -PathType Container)
    } |
    Sort-Object Name

$violations = @()
foreach ($repository in $repositories) {
    $trackedFiles = @(git -C $repository.FullName -c core.quotepath=false ls-files)
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed: $($repository.Name)"
    }

    foreach ($relativePath in $trackedFiles) {
        $normalized = $relativePath.Replace("\", "/")
        $fileName = [IO.Path]::GetFileName($normalized)
        $isTestFile = (
            $normalized -match "(^|/)(tests?|scripts/tests)/" -or
            $fileName -match "^test_.*\.py$" -or
            $fileName -match "_test\.go$" -or
            $fileName -match "\.(test|spec)\.[cm]?js$" -or
            $fileName -match "_test\.sh$"
        )
        if (-not $isTestFile) {
            continue
        }

        $fullPath = Join-Path $repository.FullName $relativePath
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
            continue
        }
        $content = Get-Content -LiteralPath $fullPath -Raw

        if ($normalized.EndsWith(".py", [StringComparison]::OrdinalIgnoreCase)) {
            foreach ($match in [regex]::Matches($content, "(?s)\benv\s*=\s*\{(?<body>[^{}]*)\}")) {
                if ($match.Groups["body"].Value -notmatch "\*\*\s*os\.environ") {
                    $line = 1 + ($content.Substring(0, $match.Index) -split "`n").Count - 1
                    $violations += "$($repository.Name)/${normalized}:${line}: Python child env must include **os.environ"
                }
            }
        }

        if ($normalized.EndsWith(".go", [StringComparison]::OrdinalIgnoreCase)) {
            foreach ($match in [regex]::Matches($content, "\.Env\s*=\s*\[\]string\s*\{")) {
                $line = 1 + ($content.Substring(0, $match.Index) -split "`n").Count - 1
                $violations += "$($repository.Name)/${normalized}:${line}: Go child env must append to os.Environ()"
            }
        }

        if ($normalized -match "\.[cm]?js$") {
            foreach ($match in [regex]::Matches($content, "(?s)\benv\s*:\s*\{(?<body>[^{}]*)\}")) {
                if ($match.Groups["body"].Value -notmatch "\.\.\.\s*process\.env") {
                    $line = 1 + ($content.Substring(0, $match.Index) -split "`n").Count - 1
                    $violations += "$($repository.Name)/${normalized}:${line}: Node child env must include ...process.env"
                }
            }
        }

        if ($normalized.EndsWith(".sh", [StringComparison]::OrdinalIgnoreCase)) {
            foreach ($match in [regex]::Matches($content, "(?m)(^|[;&|]\s*)env\s+-i\b")) {
                $line = 1 + ($content.Substring(0, $match.Index) -split "`n").Count - 1
                $violations += "$($repository.Name)/${normalized}:${line}: shell test must not clear the inherited environment with env -i"
            }
        }
    }
}

if ($violations.Count -gt 0) {
    $violations | ForEach-Object { Write-Host "[NG] $_" }
    throw "Test child-process environment contract failed with $($violations.Count) violation(s)."
}

Write-Host "[OK] Test child-process environment inheritance contract passed"
