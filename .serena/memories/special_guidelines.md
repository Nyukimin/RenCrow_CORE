# RenCrow 特殊ガイドラインと設計パターン

## アーキテクチャの重要原則

### 1. ルーティング拡張アーキテクチャ
RenCrow は Chat/Worker/Coder の3役割モデルを採用：
- **Chat (Mio)**: ユーザー対話窓口（必ず Ollama chat-v1 モデル）
- **Worker**: データ分析・検証などの非コード作業（Ollama worker-v1）
- **Coder**: コード生成・修正（クラウド API のみ）

### 2. ルーティングカテゴリ
- CHAT: 説明、相談、雑談、要約
- PLAN: 段取り、仕様化、タスク分解
- ANALYZE: データ抽出、構造化、集計、分析
- OPS: 運用手順、障害対応、設定確認
- RESEARCH: 調査、出典確認、最新情報
- CODE/CODE1/CODE2: コード生成・修正

### 3. Ollama モデル管理
- **永続化**: `keep_alive: -1` を使用してモデルをメモリに常駐
- **モデル名**: `chat-v1:latest` と `worker-v1:latest`
- **ヘルスチェック**: LLM 呼び出し前に常時チェック
- **自動復旧**: Ollama ダウン時は `ollama_restart_command` で再起動

### 4. LINE 固有動作
- LINE 入口は `CHAT(Mio)` 固定（`line_forced_chat`）
- 委譲判断は Mio が会話中に実施
- 画像は OpenAI 互換の `image_url` 形式で送信
- 一時メディアは即座に削除せず保持時間を設ける

### 5. セキュリティと権限境界
- **サンドボックス**: `restrict_to_workspace: true` でワークスペース外へのアクセスを制限
- **最終回答権限**: 常に Chat (Mio) が生成
- **ループ制御権限**: LoopController がシステム側で制御
- **危険コマンド**: `exec` ツールは `rm -rf`, `format`, `shutdown` などをブロック

## デザインパターン

### MessageBus パターン
コンポーネント間の疎結合通信に `pkg/bus` を使用：
```go
bus.Subscribe(constants.ChannelTelegram, handler)
bus.Publish(constants.ChannelTelegram, message)
```

### Provider 抽象化
すべての LLM プロバイダーは共通インターフェースを実装：
```go
type LLMProvider interface {
    SendMessage(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
```

### ツールレジストリパターン
動的ツール登録と実行：
```go
registry.Register("read_file", readFileHandler)
result := registry.Execute(ctx, toolName, params)
```

## 開発時の注意点

### 1. メモリ効率
- 文字列の連結は `strings.Builder` を使用
- 大きなデータは streaming で処理
- 不要な変数のコピーを避ける

### 2. エラーハンドリング
- エラーは wrap して情報を追加（`fmt.Errorf("...: %w", err)`）
- ログにエラーコンテキストを含める
- リトライロジックは慎重に実装（無限ループ防止）

### 3. 並行処理
- goroutine は必ず終了させる（context でキャンセル）
- shared state は mutex で保護
- channel は必ず close する

### 4. テスト
- モックは interface ベースで作成
- table-driven tests を使用
- 外部依存（LLM API）はモックする

### 5. ログ
- 構造化ログ: `{key=value}` 形式
- 機密情報（API キー、トークン）はログに出さない
- ログレベルを適切に使い分け（ERROR/INFO/DEBUG）

## 特殊機能

### /work モード
Mio の出力を簡潔にする「仕事モード」：
- `/work`: 8 ターン有効化
- `/work N`: N ターン有効化
- `/normal`: 無効化
- 実装: `WorkOverlayDirectiveText` を system prompt に追加

### セッション管理
- セッションキーで履歴を管理
- 日次カットオーバー対応
- 要約機能で長いセッションを圧縮

### ハートビート
- 定期タスクを `HEARTBEAT.md` で定義
- 30分間隔で実行（設定可能）
- 長時間タスクは `spawn` でサブエージェント化

## 監視と運用

### ヘルスチェック
- `/health` エンドポイントで状態確認
- Ollama 接続チェック
- セッションマネージャー状態

### Watchdog
- systemd timer で定期監視
- ゲートウェイ再起動、Ollama 復旧
- `ops_watchdog.sh` スクリプト
