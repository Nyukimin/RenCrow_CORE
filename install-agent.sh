#!/bin/bash
set -e

# RenCrow Agent インストールスクリプト
# エージェントPC用（分散実行対応）
# 使い方: ./install-agent.sh <agent-type>
#   agent-type: worker, coder1, coder2, coder3

RENCROW_HOME="$HOME/.rencrow"
RENCROW_BIN="$HOME/.local/bin"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"

if [ $# -lt 1 ]; then
    echo "Usage: $0 <agent-type>"
    echo "  agent-type: worker, coder1, coder2, coder3"
    exit 1
fi

AGENT_TYPE="$1"

case "$AGENT_TYPE" in
    worker|coder1|coder2|coder3)
        ;;
    *)
        echo "Error: Invalid agent type: $AGENT_TYPE"
        echo "Supported: worker, coder1, coder2, coder3"
        exit 1
        ;;
esac

echo "=========================================="
echo "RenCrow Agent インストーラー v1.0"
echo "  Agent Type: $AGENT_TYPE"
echo "=========================================="
echo ""

# 依存パッケージ確認
echo "[1/6] 依存パッケージの確認..."

# Go 1.23+ 確認
if ! command -v go &> /dev/null; then
    echo "  ❌ Go がインストールされていません"
    echo "  以下のコマンドでインストールしてください:"
    echo "  sudo apt update && sudo apt install -y golang-go"
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "  ✓ Go $GO_VERSION"

# agent-type 別の依存確認
case "$AGENT_TYPE" in
    worker)
        echo "  ℹ Worker エージェント: Ollama が必要です"
        if ! command -v ollama &> /dev/null; then
            echo "  ⚠️  Ollama がインストールされていません。インストールしますか？ (y/n)"
            read -r install_ollama
            if [[ "$install_ollama" == "y" ]]; then
                curl -fsSL https://ollama.com/install.sh | sh
                echo "  ✓ Ollama インストール完了"
            else
                echo "  ❌ Worker エージェントには Ollama が必須です"
                exit 1
            fi
        else
            echo "  ✓ Ollama インストール済み"
        fi
        ;;
    coder1)
        echo "  ℹ Coder1 エージェント: DeepSeek API キーが必要です"
        ;;
    coder2)
        echo "  ℹ Coder2 エージェント: OpenAI API キーが必要です"
        ;;
    coder3)
        echo "  ℹ Coder3 エージェント: Anthropic API キーが必要です"
        ;;
esac

echo ""

# ビルド
echo "[2/6] RenCrow Agent のビルド..."
cd "$(dirname "$0")"
go build -o rencrow-agent ./cmd/rencrow-agent
echo "  ✓ ビルド完了（エージェント専用バイナリ）"

# ディレクトリ作成
echo "[3/6] ディレクトリの作成..."
mkdir -p "$RENCROW_HOME"/{logs,workspace}
mkdir -p "$RENCROW_BIN"
mkdir -p "$SYSTEMD_USER_DIR"
echo "  ✓ $RENCROW_HOME"
echo "  ✓ $RENCROW_BIN"
echo "  ✓ $SYSTEMD_USER_DIR"

# バイナリコピー
echo "[4/6] バイナリのインストール..."
cp rencrow-agent "$RENCROW_BIN/rencrow-agent"
chmod +x "$RENCROW_BIN/rencrow-agent"
echo "  ✓ rencrow-agent → $RENCROW_BIN/rencrow-agent"

# 設定ファイル生成
echo "[5/6] 設定ファイルの生成..."
if [ ! -f "$RENCROW_HOME/config.yaml" ]; then
    cp config.yaml.example "$RENCROW_HOME/config.yaml"

    # パスを置換
    sed -i "s|./workspace|$RENCROW_HOME/workspace|g" "$RENCROW_HOME/config.yaml"

    echo "  ✓ $RENCROW_HOME/config.yaml"
else
    echo "  ⚠️  config.yaml は既に存在します（スキップ）"
fi

# .env ファイル生成（agent-type 別）
echo ""
echo "API キーの設定:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

api_key=""
case "$AGENT_TYPE" in
    worker)
        echo "Worker エージェントは Ollama を使用します（API キー不要）"
        ;;
    coder1)
        echo -n "DeepSeek API キー: "
        read -r api_key
        ;;
    coder2)
        echo -n "OpenAI API キー: "
        read -r api_key
        ;;
    coder3)
        echo -n "Anthropic API キー: "
        read -r api_key
        ;;
esac

# .env 生成
cat > "$RENCROW_HOME/.env" <<EOF
# RenCrow Agent 環境変数
# Agent Type: $AGENT_TYPE
# 生成日時: $(date)

# API Keys
$(if [ "$AGENT_TYPE" = "coder1" ]; then echo "DEEPSEEK_API_KEY=\"$api_key\""; fi)
$(if [ "$AGENT_TYPE" = "coder2" ]; then echo "OPENAI_API_KEY=\"$api_key\""; fi)
$(if [ "$AGENT_TYPE" = "coder3" ]; then echo "ANTHROPIC_API_KEY=\"$api_key\""; fi)
EOF

chmod 600 "$RENCROW_HOME/.env"
echo ""
echo "  ✓ $RENCROW_HOME/.env (chmod 600)"

if [ -n "$api_key" ]; then
    echo "  ✓ API キー設定済み"
fi

echo ""

# systemd サービスファイル生成
echo "[6/6] systemd サービスの設定..."

# rencrow-agent-<type>.service
cat > "$SYSTEMD_USER_DIR/rencrow-agent-$AGENT_TYPE.service" <<EOF
[Unit]
Description=RenCrow Agent ($AGENT_TYPE)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$RENCROW_HOME
ExecStart=$RENCROW_BIN/rencrow-agent -standalone -agent $AGENT_TYPE -config $RENCROW_HOME/config.yaml
EnvironmentFile=$RENCROW_HOME/.env
Restart=always
RestartSec=5
StandardInput=null
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF

echo "  ✓ $SYSTEMD_USER_DIR/rencrow-agent-$AGENT_TYPE.service"

# systemd reload & enable
systemctl --user daemon-reload
systemctl --user enable rencrow-agent-$AGENT_TYPE
echo "  ✓ systemctl --user enable rencrow-agent-$AGENT_TYPE"

echo ""

# Ollama モデルダウンロード（worker のみ）
if [ "$AGENT_TYPE" = "worker" ]; then
    echo "[追加] Ollama モデルの準備..."
    echo "  必要なモデルをダウンロードしますか？"
    echo "  - Worker (Worker用、必須)"
    echo ""
    echo -n "ダウンロードしますか？ (y/n): "
    read -r download_models

    if [[ "$download_models" == "y" ]]; then
        ollama pull Worker || echo "  ⚠️  Worker ダウンロード失敗（後で実行してください）"
        echo "  ✓ モデルダウンロード完了"
    else
        echo "  ⚠️  モデルは後でダウンロードしてください:"
        echo "     ollama pull Worker"
    fi
    echo ""
fi

echo "=========================================="
echo "✓ Agent インストール完了！"
echo "=========================================="
echo ""
echo "Agent Type: $AGENT_TYPE"
echo ""
echo "起動方法:"
echo "  systemctl --user start rencrow-agent-$AGENT_TYPE"
echo ""
echo "停止方法:"
echo "  systemctl --user stop rencrow-agent-$AGENT_TYPE"
echo ""
echo "ログ確認:"
echo "  journalctl --user -u rencrow-agent-$AGENT_TYPE -f"
echo ""
echo "設定ファイル:"
echo "  $RENCROW_HOME/config.yaml"
echo "  $RENCROW_HOME/.env"
echo ""
echo "注意:"
echo "  このエージェントは stdin/stdout で JSON 通信します。"
echo "  メインPCから SSH 経由で起動してください。"
echo ""
echo "次のステップ:"
echo "  1. メインPCから SSH 接続テスト"
echo "  2. メインPCの config.yaml で distributed.enabled=true"
echo "  3. メインPCから rencrow 起動"
echo ""
