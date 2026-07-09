# RenCrow Agent インストーラー (Windows PowerShell)
# エージェントPC用（分散実行対応）
# 使い方: .\install-agent.ps1 -AgentType <type>
#   AgentType: worker, coder1, coder2, coder3

param(
    [Parameter(Mandatory=$true)]
    [ValidateSet("worker", "coder1", "coder2", "coder3")]
    [string]$AgentType
)

$ErrorActionPreference = "Stop"

$RENCROW_HOME = "$env:USERPROFILE\.rencrow"
$RENCROW_BIN = "$RENCROW_HOME\bin"

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "RenCrow Agent インストーラー v1.0" -ForegroundColor Cyan
Write-Host "  Agent Type: $AgentType" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""

# [1/5] 依存確認
Write-Host "[1/5] 環境確認..." -ForegroundColor Yellow

# PowerShell バージョン確認
$psVersion = $PSVersionTable.PSVersion
Write-Host "  ✓ PowerShell $psVersion" -ForegroundColor Green

# Agent-type 別の依存確認
switch ($AgentType) {
    "worker" {
        Write-Host "  ℹ Worker エージェント: Ollama が必要です" -ForegroundColor Cyan
        if (-not (Get-Command ollama -ErrorAction SilentlyContinue)) {
            Write-Host "  ⚠️  Ollama がインストールされていません" -ForegroundColor Yellow
            Write-Host "  https://ollama.com/download からダウンロードしてください" -ForegroundColor Yellow
            $continue = Read-Host "続行しますか？ (y/n)"
            if ($continue -ne "y") { exit 1 }
        } else {
            Write-Host "  ✓ Ollama インストール済み" -ForegroundColor Green
        }
    }
    "coder1" {
        Write-Host "  ℹ Coder1 エージェント: DeepSeek API キーが必要です" -ForegroundColor Cyan
    }
    "coder2" {
        Write-Host "  ℹ Coder2 エージェント: OpenAI API キーが必要です" -ForegroundColor Cyan
    }
    "coder3" {
        Write-Host "  ℹ Coder3 エージェント: Anthropic API キーが必要です" -ForegroundColor Cyan
    }
}

Write-Host ""

# [2/5] ディレクトリ作成
Write-Host "[2/5] ディレクトリの作成..." -ForegroundColor Yellow

New-Item -ItemType Directory -Force -Path "$RENCROW_HOME\logs" | Out-Null
New-Item -ItemType Directory -Force -Path "$RENCROW_HOME\workspace" | Out-Null
New-Item -ItemType Directory -Force -Path "$RENCROW_BIN" | Out-Null

Write-Host "  ✓ $RENCROW_HOME" -ForegroundColor Green
Write-Host "  ✓ $RENCROW_BIN" -ForegroundColor Green

Write-Host ""

# [3/5] バイナリ配置
Write-Host "[3/5] バイナリの配置..." -ForegroundColor Yellow

# バイナリが同じディレクトリにあるか確認
$binaryName = "rencrow-agent.exe"
if (Test-Path ".\rencrow-agent-windows-amd64.exe") {
    Copy-Item ".\rencrow-agent-windows-amd64.exe" "$RENCROW_BIN\$binaryName" -Force
    Write-Host "  ✓ $binaryName → $RENCROW_BIN" -ForegroundColor Green
} elseif (Test-Path ".\rencrow-agent.exe") {
    Copy-Item ".\rencrow-agent.exe" "$RENCROW_BIN\$binaryName" -Force
    Write-Host "  ✓ $binaryName → $RENCROW_BIN" -ForegroundColor Green
} else {
    Write-Host "  ⚠️  rencrow-agent.exe が見つかりません" -ForegroundColor Yellow
    Write-Host "  GitHub Releases からダウンロードしてこのスクリプトと同じフォルダに配置してください" -ForegroundColor Yellow
    Write-Host "  https://github.com/Nyukimin/RenCrow_CORE/releases" -ForegroundColor Yellow
    exit 1
}

Write-Host ""

# [4/5] 設定ファイル生成
Write-Host "[4/5] 設定ファイルの生成..." -ForegroundColor Yellow

# config.yaml
if (-not (Test-Path "$RENCROW_HOME\config.yaml")) {
    if (Test-Path ".\config.yaml.example") {
        Copy-Item ".\config.yaml.example" "$RENCROW_HOME\config.yaml"
    } elseif (Test-Path ".\config.yaml") {
        Copy-Item ".\config.yaml" "$RENCROW_HOME\config.yaml"
    } else {
        Write-Host "  ⚠️  config.yaml テンプレートが見つかりません" -ForegroundColor Yellow
        Write-Host "  手動で作成してください: $RENCROW_HOME\config.yaml" -ForegroundColor Yellow
    }
    Write-Host "  ✓ $RENCROW_HOME\config.yaml" -ForegroundColor Green
} else {
    Write-Host "  ⚠️  config.yaml は既に存在します（スキップ）" -ForegroundColor Yellow
}

Write-Host ""

# API キー設定
Write-Host "API キーの設定:" -ForegroundColor Yellow
Write-Host "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Gray
Write-Host ""

$apiKey = ""
switch ($AgentType) {
    "worker" {
        Write-Host "Worker エージェントは Ollama を使用します（API キー不要）" -ForegroundColor Cyan
    }
    "coder1" {
        $apiKey = Read-Host "DeepSeek API キー"
    }
    "coder2" {
        $apiKey = Read-Host "OpenAI API キー"
    }
    "coder3" {
        $apiKey = Read-Host "Anthropic API キー"
    }
}

# .env 生成
$envContent = @"
# RenCrow Agent 環境変数
# Agent Type: $AgentType
# 生成日時: $(Get-Date -Format "yyyy-MM-dd HH:mm:ss")

# API Keys
"@

switch ($AgentType) {
    "coder1" { $envContent += "`nDEEPSEEK_API_KEY=$apiKey" }
    "coder2" { $envContent += "`nOPENAI_API_KEY=$apiKey" }
    "coder3" { $envContent += "`nANTHROPIC_API_KEY=$apiKey" }
}

$envContent | Out-File -FilePath "$RENCROW_HOME\.env" -Encoding UTF8

Write-Host ""
Write-Host "  ✓ $RENCROW_HOME\.env" -ForegroundColor Green

if ($apiKey -ne "") {
    Write-Host "  ✓ API キー設定済み" -ForegroundColor Green
}

Write-Host ""

# [5/5] タスクスケジューラ登録
Write-Host "[5/5] タスクスケジューラの設定..." -ForegroundColor Yellow

$taskName = "RenCrowAgent-$AgentType"
$taskExists = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue

if ($taskExists) {
    Write-Host "  ⚠️  タスク '$taskName' は既に存在します" -ForegroundColor Yellow
    $overwrite = Read-Host "上書きしますか？ (y/n)"
    if ($overwrite -eq "y") {
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    } else {
        Write-Host "  タスク登録をスキップしました" -ForegroundColor Yellow
        Write-Host ""
        Write-Host "手動起動コマンド:" -ForegroundColor Cyan
        Write-Host "  cd $RENCROW_BIN" -ForegroundColor White
        Write-Host "  .\rencrow-agent.exe -standalone -agent $AgentType -config $RENCROW_HOME\config.yaml" -ForegroundColor White
        exit 0
    }
}

# タスク定義（ログイン時に自動起動、常駐）
$action = New-ScheduledTaskAction -Execute "$RENCROW_BIN\$binaryName" `
    -Argument "-standalone -agent $AgentType -config $RENCROW_HOME\config.yaml" `
    -WorkingDirectory $RENCROW_HOME

$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME

$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive

Register-ScheduledTask -TaskName $taskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Description "RenCrow Agent ($AgentType) - 分散実行エージェント" | Out-Null

Write-Host "  ✓ タスク '$taskName' 登録完了" -ForegroundColor Green

Write-Host ""

# Ollama モデルダウンロード（worker のみ）
if ($AgentType -eq "worker") {
    Write-Host "[追加] Ollama モデルの準備..." -ForegroundColor Yellow
    Write-Host "  必要なモデルをダウンロードしますか？" -ForegroundColor Cyan
    Write-Host "  - Worker (Worker用、必須)" -ForegroundColor Cyan
    Write-Host ""
    $downloadModels = Read-Host "ダウンロードしますか？ (y/n)"

    if ($downloadModels -eq "y") {
        ollama pull Worker
        Write-Host "  ✓ モデルダウンロード完了" -ForegroundColor Green
    } else {
        Write-Host "  ⚠️  モデルは後でダウンロードしてください:" -ForegroundColor Yellow
        Write-Host "     ollama pull Worker" -ForegroundColor White
    }
    Write-Host ""
}

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "✓ インストール完了！" -ForegroundColor Green
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Agent Type: $AgentType" -ForegroundColor White
Write-Host ""
Write-Host "タスク管理:" -ForegroundColor Yellow
Write-Host "  起動: " -NoNewline -ForegroundColor White
Write-Host "Start-ScheduledTask -TaskName '$taskName'" -ForegroundColor Cyan
Write-Host "  停止: " -NoNewline -ForegroundColor White
Write-Host "Stop-ScheduledTask -TaskName '$taskName'" -ForegroundColor Cyan
Write-Host "  状態: " -NoNewline -ForegroundColor White
Write-Host "Get-ScheduledTask -TaskName '$taskName'" -ForegroundColor Cyan
Write-Host ""
Write-Host "ログ確認:" -ForegroundColor Yellow
Write-Host "  $RENCROW_HOME\logs\" -ForegroundColor White
Write-Host ""
Write-Host "設定ファイル:" -ForegroundColor Yellow
Write-Host "  $RENCROW_HOME\config.yaml" -ForegroundColor White
Write-Host "  $RENCROW_HOME\.env" -ForegroundColor White
Write-Host ""
Write-Host "注意:" -ForegroundColor Yellow
Write-Host "  このエージェントは stdin/stdout で JSON 通信します。" -ForegroundColor White
Write-Host "  メインPCから SSH 経由で起動してください。" -ForegroundColor White
Write-Host ""
Write-Host "次のステップ:" -ForegroundColor Yellow
Write-Host "  1. メインPCから SSH 接続テスト" -ForegroundColor White
Write-Host "  2. メインPCの config.yaml で distributed.enabled=true" -ForegroundColor White
Write-Host "  3. メインPCから rencrow 起動" -ForegroundColor White
Write-Host ""
