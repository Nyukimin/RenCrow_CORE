# Coder 設定ガイド

**作成日**: 2026-03-27
**対象**: RenCrow ユーザー（初回セットアップ〜運用）

---

## 📋 目次

1. [Coder とは](#1-coder-とは)
2. [最小構成での設定](#2-最小構成での設定)
3. [API キーの取得方法](#3-api-キーの取得方法)
4. [環境変数の設定](#4-環境変数の設定)
5. [設定の確認](#5-設定の確認)
6. [高度な設定](#6-高度な設定)
7. [分散実行（SSH）設定](#7-分散実行ssh設定)
8. [トラブルシューティング](#8-トラブルシューティング)

---

## 1. Coder とは

RenCrow の Coder は、コード生成・レビュー・提案を行う 4 つの専門 AI エージェントです。

| Coder | デフォルト名 | LLM プロバイダー | 得意分野 | デフォルト状態 |
|-------|-------------|-----------------|---------|---------------|
| **Coder1** | AO (青) | DeepSeek | 仕様設計、アーキテクチャ検討 | 有効 |
| **Coder2** | Aka (赤) | OpenAI | 実装、コード生成 | 有効 |
| **Coder3** | Kin (金) | Anthropic Claude | 高品質コーディング、推論 | 有効 |
| **Coder4** | Gin (銀) | 設定可能 | 高速プロトタイピング、実験 | **無効** |

### 使い分け

```bash
# 自動振り分け（coder1→2→3→4 の順でフォールバック）
ユーザー: ログイン機能を実装して

# 明示指定
ユーザー: /code1 この機能の設計を検討して       # Coder1 (仕様設計)
ユーザー: /code2 ログイン処理を実装して         # Coder2 (実装)
ユーザー: /code3 このコードをレビューして       # Coder3 (レビュー)
ユーザー: /code4 プロトタイプを作って           # Coder4 (実験)
```

---

## 2. 最小構成での設定

### 2.1 Coder1 のみ有効にする（最小構成）

`config.yaml`:

```yaml
coder1:
  enabled: true
  name: "ao"
  display_name: "青"
  provider: "deepseek"
  model: "deepseek-chat"
  api_key: "${DEEPSEEK_API_KEY}"
  personality: "仕様設計とアーキテクチャ設計を得意とする、論理的で体系的な思考を持つエンジニア"
  tone: "丁寧で論理的"
  light_memory:
    enabled: true
    max_turns: 5

# Coder2-4 は無効化
coder2:
  enabled: false

coder3:
  enabled: false

coder4:
  enabled: false
```

### 2.2 複数 Coder を有効にする（推奨構成）

```yaml
coder1:
  enabled: true
  name: "ao"
  display_name: "青"
  provider: "deepseek"
  model: "deepseek-chat"
  api_key: "${DEEPSEEK_API_KEY}"
  personality: "仕様設計とアーキテクチャ設計を得意とする、論理的で体系的な思考を持つエンジニア"
  tone: "丁寧で論理的"
  light_memory:
    enabled: true
    max_turns: 5

coder2:
  enabled: true
  name: "aka"
  display_name: "赤"
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
  personality: "実装とコーディングを得意とする、実践的で効率重視のエンジニア"
  tone: "簡潔で実用的"
  light_memory:
    enabled: true
    max_turns: 5

coder3:
  enabled: true
  name: "kin"
  display_name: "金"
  provider: "claude"
  model: "claude-sonnet-4-20250514"
  api_key: "${ANTHROPIC_API_KEY}"
  personality: "高品質なコードレビューと推論を得意とする、洞察力のあるエンジニア"
  tone: "丁寧で洞察的"
  light_memory:
    enabled: true
    max_turns: 5

coder4:
  enabled: false  # 必要に応じて有効化
```

---

## 3. API キーの取得方法

### 3.1 DeepSeek (Coder1)

1. [DeepSeek Platform](https://platform.deepseek.com/) にアクセス
2. アカウント登録・ログイン
3. API Keys → Create API Key
4. 生成された API キーをコピー（`sk-...` 形式）

**料金**: 従量課金（低コスト）
**推奨モデル**: `deepseek-chat`

### 3.2 OpenAI (Coder2)

1. [OpenAI Platform](https://platform.openai.com/) にアクセス
2. アカウント登録・ログイン
3. API keys → Create new secret key
4. 生成された API キーをコピー（`sk-...` 形式）

**料金**: 従量課金
**推奨モデル**: `gpt-4o-mini` (低コスト) または `gpt-4o` (高品質)

### 3.3 Anthropic Claude (Coder3)

1. [Anthropic Console](https://console.anthropic.com/) にアクセス
2. アカウント登録・ログイン
3. API Keys → Create Key
4. 生成された API キーをコピー（`sk-ant-...` 形式）

**料金**: 従量課金
**推奨モデル**: `claude-sonnet-4-20250514`

### 3.4 Google Gemini (Coder4 - オプション)

1. [Google AI Studio](https://aistudio.google.com/) にアクセス
2. Get API key をクリック
3. 生成された API キーをコピー

**料金**: 無料枠あり（制限付き）
**推奨モデル**: `gemini-2.0-flash-exp`

---

## 4. 環境変数の設定

### 4.1 環境変数ファイルの作成

```bash
# ~/.rencrow/.env ファイルを作成
mkdir -p ~/.rencrow
touch ~/.rencrow/.env
chmod 600 ~/.rencrow/.env  # セキュリティのため権限を制限
```

### 4.2 API キーを記載

`~/.rencrow/.env`:

```bash
# DeepSeek (Coder1)
export DEEPSEEK_API_KEY="sk-..."

# OpenAI (Coder2)
export OPENAI_API_KEY="sk-..."

# Anthropic Claude (Coder3)
export ANTHROPIC_API_KEY="sk-ant-..."

# Google Gemini (Coder4 - オプション)
export GEMINI_API_KEY="..."
```

### 4.3 環境変数の読み込み

**手動で読み込む場合**:
```bash
source ~/.rencrow/.env
```

**systemd サービスで自動読み込み** (推奨):

`/etc/systemd/user/rencrow.service`:
```ini
[Unit]
Description=RenCrow Multi-LLM Assistant

[Service]
Type=simple
ExecStart=/usr/local/bin/rencrow run
EnvironmentFile=/home/YOUR_USER/.rencrow/.env
Restart=always

[Install]
WantedBy=default.target
```

**起動**:
```bash
systemctl --user daemon-reload
systemctl --user restart rencrow
```

---

## 5. 設定の確認

### 5.1 設定ファイルの検証

```bash
# 設定ファイルの読み込みテスト
rencrow doctor

# 期待される出力（一部）:
# ✓ Config loaded successfully
# ✓ Coder1 (DeepSeek): Enabled
# ✓ Coder2 (OpenAI): Enabled
# ✓ Coder3 (Claude): Enabled
# ✗ Coder4: Disabled
```

### 5.2 API 接続テスト

```bash
# 個別テスト（実装予定）
rencrow test coder1
rencrow test coder2
rencrow test coder3
```

### 5.3 実際に使ってみる

LINE/Slack/Telegram から：

```
/code1 Goでログイン機能の設計を検討して
```

成功すれば、Coder1 (DeepSeek) からの応答が返ってきます。

---

## 6. 高度な設定

### 6.1 Persona（個性）のカスタマイズ

各 Coder に個性を持たせることができます：

```yaml
coder1:
  personality: |
    あなたは経験豊富なアーキテクトです。
    大規模システムの設計経験が豊富で、スケーラビリティとメンテナンス性を重視します。
    提案する際は、トレードオフを明示し、複数の選択肢を示します。
  tone: "professional"
```

**Persona の効果**:
- LLM への system prompt に追加される
- 応答のスタイル・視点が変わる
- VTuber ブリッジ使用時に音声の tone として使用

### 6.2 LightMemory（短期記憶）

会話の文脈を保持します：

```yaml
coder1:
  light_memory:
    enabled: true
    max_turns: 5  # 過去 5 ターンを記憶
```

**効果**:
- 「さっきの設計案を修正して」などの追加指示が可能
- SSH 経由実行時も記憶を保持

**注意**:
- ターン数を増やすとメモリ消費が増加
- 推奨値: 3〜5 ターン

### 6.3 モデルの変更

```yaml
# Coder2 で高品質モデルを使用
coder2:
  provider: "openai"
  model: "gpt-4o"  # gpt-4o-mini から変更
  api_key: "${OPENAI_API_KEY}"
```

```yaml
# Coder4 で Gemini を使用
coder4:
  enabled: true
  provider: "gemini"
  model: "gemini-2.0-flash-exp"
  api_key: "${GEMINI_API_KEY}"
```

---

## 7. 分散実行（SSH）設定

### 7.1 概要

Coder を別のマシン（Windows PC など）で実行し、SSH 経由で通信できます。

**メリット**:
- GPU が必要な処理を別マシンで実行
- API キーを Worker マシンに集中管理（Remote 側に不要）
- 負荷分散

### 7.2 設定例

**Worker PC (Linux) の config.yaml**:

```yaml
distributed:
  enabled: true
  transports:
    coder1:
      type: local  # Worker 上で直接実行
    coder2:
      type: local
    coder3:
      type: ssh    # Windows PC で実行
      remote_host: "192.168.1.25:22"
      remote_user: "nyuki"
      ssh_key_path: "/home/nyukimi/.ssh/rencrow_agent"
      strict_host_key: false
      remote_agent_path: "C:/Users/nyuki/rencrow-agent.exe"
      remote_config_path: "C:/Users/nyuki/.rencrow/config.yaml"
    coder4:
      type: local
```

### 7.3 Remote 側のセットアップ

**Windows PC**:

```powershell
# 1. rencrow-agent をインストール
.\install-agent.ps1 coder3

# 2. SSH サーバーを有効化（OpenSSH Server）
# 設定 → アプリ → オプション機能 → OpenSSH Server

# 3. SSH 公開鍵を登録
# C:\Users\nyuki\.ssh\authorized_keys に Worker の公開鍵を追加

# 4. config.yaml は最小限でOK（API キーは Worker から送信される）
# C:\Users\nyuki\.rencrow\config.yaml
```

**Linux/Mac**:

```bash
# 1. rencrow-agent をインストール
./install-agent.sh coder3

# 2. SSH 公開鍵を登録
cat ~/.ssh/rencrow_agent.pub >> ~/.ssh/authorized_keys
```

### 7.4 接続テスト

```bash
# Worker 側から SSH 接続確認
ssh -i ~/.ssh/rencrow_agent nyuki@192.168.1.25

# rencrow-agent が起動するか確認
rencrow-agent --standalone --agent coder3 --config ~/.rencrow/config.yaml
```

詳細: [分散実行_前提条件とセットアップ.md](./分散実行_前提条件とセットアップ.md)

---

## 8. トラブルシューティング

### 8.1 「API key is required」エラー

**原因**: 環境変数が読み込まれていない

**対処法**:
```bash
# 1. 環境変数ファイルを確認
cat ~/.rencrow/.env

# 2. 手動で読み込み
source ~/.rencrow/.env

# 3. systemd の場合は EnvironmentFile を確認
systemctl --user cat rencrow | grep EnvironmentFile
```

### 8.2 「coder1 is not enabled」エラー

**原因**: config.yaml で `enabled: false` になっている

**対処法**:
```yaml
coder1:
  enabled: true  # false → true に変更
```

### 8.3 「CODE route requested but all coders are unavailable」

**原因**: 有効な Coder が 1 つもない

**対処法**:
- 最低 1 つの Coder を `enabled: true` にする
- API キーが正しく設定されているか確認

### 8.4 SSH 接続エラー

```
Failed to connect SSH transport for agent 'coder3': dial tcp: i/o timeout
```

**対処法**:
```bash
# 1. SSH 接続確認
ssh -i ~/.ssh/rencrow_agent nyuki@192.168.1.25

# 2. ファイアウォール確認
# Remote 側でポート 22 が開いているか

# 3. SSH キーの権限確認
chmod 600 ~/.ssh/rencrow_agent
chmod 644 ~/.ssh/rencrow_agent.pub
```

### 8.5 「proposal generation failed: invalid format」

**原因**: LLM が正しい JSON 形式で Proposal を返していない

**対処法**:
- モデルを変更してみる（例: `gpt-4o-mini` → `gpt-4o`）
- Prompt を調整（prompts_dir のファイルを確認）
- LLM プロバイダーの API 状態を確認

---

## 📚 関連ドキュメント

| ドキュメント | 説明 |
|------------|------|
| [実装仕様_Coder4体化とAgentPersona_v1.md](../04_実装仕様_機能拡張/実装仕様_Coder4体化とAgentPersona_v1.md) | 詳細な実装仕様 |
| [分散実行_前提条件とセットアップ.md](./分散実行_前提条件とセットアップ.md) | SSH 分散実行の詳細 |
| [README.md](../../README.md) | プロジェクト全体の概要 |

---

## 💡 ベストプラクティス

1. **最初は 1 つの Coder から始める**
   - Coder1 のみ有効化 → 動作確認 → 他を追加

2. **API キーはセキュアに管理**
   - `~/.rencrow/.env` のパーミッションは `600`
   - Git にコミットしない（`.gitignore` に追加済み）

3. **コストを意識する**
   - 最初は低コストモデル（`gpt-4o-mini`, `deepseek-chat`）で試す
   - 必要に応じて高品質モデルに切り替え

4. **LightMemory はほどほどに**
   - `max_turns: 3〜5` が推奨
   - 長い会話は `/new` でリセット

5. **分散実行は段階的に**
   - 最初は全 Coder を local で動作確認
   - 安定したら coder3/4 を SSH に移行

---

**最終更新**: 2026-03-27
**問い合わせ**: GitHub Issues
