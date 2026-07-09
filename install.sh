#!/bin/bash
set -e

# RenCrow インストールスクリプト
# 使い方: curl -fsSL https://raw.githubusercontent.com/Nyukimin/RenCrow_CORE/main/install.sh | bash
# または: ./install.sh

RENCROW_HOME="$HOME/.rencrow"
RENCROW_BIN="$HOME/.local/bin"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"

echo "=========================================="
echo "RenCrow インストーラー v1.0"
echo "=========================================="
echo ""

# 依存パッケージ確認
echo "[1/7] 依存パッケージの確認..."

# Go 1.23+ 確認
if ! command -v go &> /dev/null; then
    echo "  ❌ Go がインストールされていません"
    echo "  以下のコマンドでインストールしてください:"
    echo "  sudo apt update && sudo apt install -y golang-go"
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "  ✓ Go $GO_VERSION"

# Redis 確認
if ! command -v redis-server &> /dev/null; then
    echo "  ⚠️  Redis がインストールされていません。インストールしますか？ (y/n)"
    read -r install_redis
    if [[ "$install_redis" == "y" ]]; then
        sudo apt update && sudo apt install -y redis-server
        sudo systemctl enable redis-server
        sudo systemctl start redis-server
        echo "  ✓ Redis インストール完了"
    else
        echo "  ⚠️  Redis なしで続行します（会話記憶機能が制限されます）"
    fi
else
    echo "  ✓ Redis インストール済み"
fi

# Ollama 確認
if ! command -v ollama &> /dev/null; then
    echo "  ⚠️  Ollama がインストールされていません。インストールしますか？ (y/n)"
    read -r install_ollama
    if [[ "$install_ollama" == "y" ]]; then
        curl -fsSL https://ollama.com/install.sh | sh
        echo "  ✓ Ollama インストール完了"
    else
        echo "  ❌ Ollama は必須です"
        exit 1
    fi
else
    echo "  ✓ Ollama インストール済み"
fi

# Qdrant (Docker) 確認
if ! command -v docker &> /dev/null; then
    echo "  ⚠️  Docker がインストールされていません（Qdrant用）"
    echo "  Docker をインストールしますか？ (y/n)"
    read -r install_docker
    if [[ "$install_docker" == "y" ]]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER"
        echo "  ✓ Docker インストール完了"
        echo "  ⚠️  再ログインが必要です（docker グループ反映のため）"
    else
        echo "  ⚠️  Docker なしで続行します（KB機能が制限されます）"
    fi
else
    echo "  ✓ Docker インストール済み"
    # Qdrant コンテナ起動確認
    if ! docker ps | grep -q qdrant; then
        echo "  ⚠️  Qdrant コンテナを起動しますか？ (y/n)"
        read -r start_qdrant
        if [[ "$start_qdrant" == "y" ]]; then
            docker run -d --name qdrant -p 6334:6334 qdrant/qdrant
            echo "  ✓ Qdrant 起動完了"
        fi
    else
        echo "  ✓ Qdrant 起動済み"
    fi
fi

# Tailscale 確認
if ! command -v tailscale &> /dev/null; then
    echo "  ⚠️  Tailscale がインストールされていません（LINE webhook用）"
    echo "  Tailscale をインストールしますか？ (y/n)"
    read -r install_tailscale
    if [[ "$install_tailscale" == "y" ]]; then
        curl -fsSL https://tailscale.com/install.sh | sh
        echo "  ✓ Tailscale インストール完了"
        echo "  ⚠️  'tailscale up' で認証してください"
    else
        echo "  ⚠️  Tailscale なしで続行します（LINE webhook が使えません）"
    fi
else
    echo "  ✓ Tailscale インストール済み"
fi

echo ""

# ビルド
echo "[2/7] RenCrow のビルド..."
cd "$(dirname "$0")"
go build -o rencrow ./cmd/rencrow
echo "  ✓ ビルド完了（サーバーモード）"

# ディレクトリ作成
echo "[3/7] ディレクトリの作成..."
mkdir -p "$RENCROW_HOME"/{logs,data/sessions}
mkdir -p "$RENCROW_BIN"
mkdir -p "$SYSTEMD_USER_DIR"
echo "  ✓ $RENCROW_HOME"
echo "  ✓ $RENCROW_BIN"
echo "  ✓ $SYSTEMD_USER_DIR"

# バイナリコピー
echo "[4/7] バイナリのインストール..."
cp rencrow "$RENCROW_BIN/rencrow"
chmod +x "$RENCROW_BIN/rencrow"
echo "  ✓ rencrow → $RENCROW_BIN/rencrow"

# 設定ファイル生成
echo "[5/7] 設定ファイルの生成..."
if [ ! -f "$RENCROW_HOME/config.yaml" ]; then
    cp config.yaml.example "$RENCROW_HOME/config.yaml"

    # パスを置換
    sed -i "s|./data/sessions|$RENCROW_HOME/data/sessions|g" "$RENCROW_HOME/config.yaml"
    sed -i "s|./workspace|$RENCROW_HOME/workspace|g" "$RENCROW_HOME/config.yaml"
    sed -i "s|./memory.duckdb|$RENCROW_HOME/memory.duckdb|g" "$RENCROW_HOME/config.yaml"

    echo "  ✓ $RENCROW_HOME/config.yaml"
else
    echo "  ⚠️  config.yaml は既に存在します（スキップ）"
fi

# .env ファイル生成（API キー）
echo ""
echo "外部LLM API キーの設定（オプション、スキップ可）:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Anthropic (Coder3)
echo -n "Anthropic API キー (Coder3用、空欄でスキップ): "
read -r anthropic_key

# DeepSeek (Coder1)
echo -n "DeepSeek API キー (Coder1用、空欄でスキップ): "
read -r deepseek_key

# OpenAI (Coder2)
echo -n "OpenAI API キー (Coder2用、空欄でスキップ): "
read -r openai_key

# .env 生成
cat > "$RENCROW_HOME/.env" <<EOF
# RenCrow 環境変数
# 生成日時: $(date)

# Anthropic Claude API (Coder3)
ANTHROPIC_API_KEY="${anthropic_key}"

# DeepSeek API (Coder1)
DEEPSEEK_API_KEY="${deepseek_key}"

# OpenAI API (Coder2)
OPENAI_API_KEY="${openai_key}"
EOF

chmod 600 "$RENCROW_HOME/.env"
echo ""
echo "  ✓ $RENCROW_HOME/.env (chmod 600)"

# API キー設定状況
if [ -n "$anthropic_key" ]; then
    echo "  ✓ Anthropic API キー設定済み"
fi
if [ -n "$deepseek_key" ]; then
    echo "  ✓ DeepSeek API キー設定済み"
fi
if [ -n "$openai_key" ]; then
    echo "  ✓ OpenAI API キー設定済み"
fi
if [ -z "$anthropic_key" ] && [ -z "$deepseek_key" ] && [ -z "$openai_key" ]; then
    echo "  ⚠️  外部LLM API キー未設定（Ollama のみで動作）"
fi

echo ""

# systemd サービスファイル生成
echo "[6/7] systemd サービスの設定..."

# rencrow.service
cat > "$SYSTEMD_USER_DIR/rencrow.service" <<EOF
[Unit]
Description=RenCrow - Ultra-Lightweight AI Assistant
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$HOME/RenCrow_CORE
ExecStart=$RENCROW_BIN/rencrow
EnvironmentFile=$RENCROW_HOME/.env
Environment="RENCROW_CONFIG=$RENCROW_HOME/config.yaml"
Restart=always
RestartSec=5
StandardOutput=append:$RENCROW_HOME/logs/rencrow.log
StandardError=append:$RENCROW_HOME/logs/rencrow.log

[Install]
WantedBy=default.target
EOF

echo "  ✓ $SYSTEMD_USER_DIR/rencrow.service"

# systemd reload & enable
systemctl --user daemon-reload
systemctl --user enable rencrow
echo "  ✓ systemctl --user enable rencrow"

echo ""

# Ollama モデルダウンロード
echo "[7/7] Ollama モデルの準備..."
if command -v ollama &> /dev/null; then
    echo "  必要なモデルをダウンロードしますか？"
    echo "  - Chat (Chat用、必須)"
    echo "  - Worker (Worker用、必須)"
    echo "  - nomic-embed-code (KB埋め込み用、オプション)"
    echo ""
    echo -n "ダウンロードしますか？ (y/n): "
    read -r download_models

    if [[ "$download_models" == "y" ]]; then
        ollama pull Chat || echo "  ⚠️  Chat ダウンロード失敗（後で実行してください）"
        ollama pull Worker || echo "  ⚠️  Worker ダウンロード失敗（後で実行してください）"
        ollama pull nomic-embed-code || echo "  ⚠️  nomic-embed-code ダウンロード失敗（オプション）"
        echo "  ✓ モデルダウンロード完了"
    else
        echo "  ⚠️  モデルは後でダウンロードしてください:"
        echo "     ollama pull Chat"
        echo "     ollama pull Worker"
        echo "     ollama pull nomic-embed-code"
    fi
fi

echo ""
echo "=========================================="
echo "✓ インストール完了！"
echo "=========================================="
echo ""
echo "起動方法:"
echo "  systemctl --user start rencrow"
echo ""
echo "停止方法:"
echo "  systemctl --user stop rencrow"
echo ""
echo "ログ確認:"
echo "  tail -f $RENCROW_HOME/logs/rencrow.log"
echo "  journalctl --user -u rencrow -f"
echo ""
echo "設定ファイル:"
echo "  $RENCROW_HOME/config.yaml"
echo "  $RENCROW_HOME/.env"
echo ""
echo "LINE webhook URL（Tailscale Funnel使用時）:"
echo "  https://\$(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')/webhook"
echo ""
echo "次のステップ:"
echo "  1. Tailscale認証（未実施の場合）: tailscale up"
echo "  2. LINE Messaging API設定（webhook URLを登録）"
echo "  3. RenCrow起動: systemctl --user start rencrow"
echo ""
