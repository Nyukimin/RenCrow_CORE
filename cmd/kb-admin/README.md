# kb-admin - Knowledge Base 管理CLI

RenCrow の Knowledge Base (KB) を管理するための CLI ツールです。

## 機能

### 1. search - KB検索テスト

指定ドメインで KB 検索を実行し、結果を表示します。

```bash
kb-admin search <domain> <query>
```

**例:**
```bash
kb-admin search programming "Go言語 並行処理"
kb-admin search movie "おすすめのSF映画"
```

**出力:**
- 検索結果のドキュメント一覧
- ID、Source、Score、Created、Content preview

### 2. stats - 統計情報表示

KB の統計情報を表示します。

```bash
kb-admin stats
```

**出力:**
- 既知ドメインの存在チェック
- 各ドメインの状態（empty / has documents）

### 3. list - ドキュメント一覧（将来実装）

指定ドメインの全ドキュメントを一覧表示します。

```bash
kb-admin list <domain>
```

**現状:** search コマンドで代用してください。

### 4. cleanup - 古いドキュメント削除（将来実装）

指定日数より古いドキュメントを削除します。

```bash
kb-admin cleanup <domain> <days>
```

**現状:** Qdrant Web UI で手動削除してください。

## 設定

`--config` オプションで設定ファイルを指定できます（デフォルト: `./config.yaml`）。

設定ファイルは `conversation.enabled: true` が必要です。

## 環境変数

`.env` ファイルから以下の環境変数を読み込みます:

- `REDIS_URL` - Redis 接続先
- `DUCKDB_PATH` - DuckDB ファイルパス
- `VECTORDB_URL` - Qdrant 接続先 (例: `localhost:6334`)

## 使用例

### KB検索テスト

```bash
# 映画ドメインで「感動する映画」を検索
kb-admin search movie "感動する映画"

# プログラミングドメインで「Goのエラーハンドリング」を検索
kb-admin search programming "Goのエラーハンドリング"
```

### 統計情報確認

```bash
# 全ドメインの状態を確認
kb-admin stats
```

### 設定ファイル指定

```bash
# カスタム設定ファイルを使用
kb-admin --config /path/to/config.yaml stats
```

## トラブルシューティング

### "conversation system is not enabled"

設定ファイルで `conversation.enabled: true` を設定してください。

### "failed to init manager: connection refused"

Redis / Qdrant が起動しているか確認してください。

```bash
# Qdrant起動確認
curl http://localhost:6333/

# Redis起動確認
redis-cli ping
```

### "SearchKB failed: embedder not configured"

Embedder が設定されていません。ConversationManager 初期化時に WithEmbedder() でセットする必要があります。

**Note:** 現在の kb-admin は Embedder 未設定のため、search コマンドが動作しない可能性があります。Phase 4.2 で対応予定です。

## 開発メモ

### 実装状況

- ✅ 基本CLI構造
- ✅ search コマンド（Embedder設定が必要）
- ✅ stats コマンド（簡易実装）
- ⏸️ list コマンド（VectorDB API 追加が必要）
- ⏸️ cleanup コマンド（VectorDB API 追加が必要）

### Phase 4.2 で実装予定

1. **Embedder 初期化**
   - config から Embedding provider を読み込み
   - WithEmbedder() で注入

2. **VectorDB API 公開**
   - RealConversationManager に以下を追加:
     - `ListKBDocuments(domain, limit) ([]Document, error)`
     - `GetKBCollections() ([]string, error)`
     - `GetKBStats(domain) (*KBStats, error)`
     - `DeleteOldKBDocuments(domain, before) (int, error)`

3. **完全実装**
   - list コマンド実装
   - cleanup コマンド実装（削除確認プロンプト付き）
   - バッチ処理対応（複数ドメイン一括処理）
