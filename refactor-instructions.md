# RenCrow リファクタリング指示書

**作成日**: 2026-06-12  
**対象プロジェクト**: RenCrow_CORE  
**目的**: 既存仕様を壊さず、技術的負債を減らし、今後変更しやすい状態にする

> [WARN] L番号定義の正本は `docs/refs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md` である。L0〜L4 は保存媒体ではなく lifecycle 上の位置で定義する。本書は 2026-06-12 時点の履歴文書であり、本文中の Redis / SQLite / DuckDB / Qdrant を L0〜L3 に割り当てる記述は旧定義として扱う。

---

## Objective

このリファクタリングの目的は以下の通り：

1. **日常会話の記憶システムの完成**（最優先）：L0/L1/L2/L3 の4層記憶システムを完成させ、E2E テストを通す
2. **アーカイブコードの整理**：`internal/application/archive/` および `internal/application/idlechat/archive/` に存在する古い実装を archive として保持または削除する
3. **Legacy ツールランナーの即時移行**：利用中のツールを新 Manifest 形式に即時移行し、legacy runner を削除する
4. **責務の明確化**：Clean Architecture 4層構造に準拠しない箇所を修正し、依存方向の逆転を防ぐ
5. **テストカバレッジの向上**：現在83.6%のカバレッジを向上させ、新規コード 90%以上、Domain層 95%以上を目指す

**重要**: このリファクタリングは「見た目の綺麗さ」ではなく、**日常会話の記憶システムの完成と保守性の改善**を目的とする。

**その他の部分実装項目**（Knowledge DB 高度化、News Recall trace、6エージェントのキャラ会話ランタイム等）は別途実装仕様を作成し、別タスクとする。

---

## Project Understanding

### プロジェクト概要

RenCrow は Go 言語で実装された超軽量パーソナル AI アシスタント。

**主要特徴**:
- メモリ使用量 <10MB で動作
- マルチ LLM ルーティング（Ollama、Claude、DeepSeek、OpenAI）
- Chat（Mio）/ Worker（Shiro）/ Coder（Aka/Ao/Gin/Kin）の役割分離
- Clean Architecture 4層構造（Domain → Application → Infrastructure → Adapter）
- テストカバレッジ 83.6%（Domain層 93.5%）
- 「記憶OS」構想：L0（短期）/ L1（hot store）/ L2（中期）/ L3（長期）の4層記憶システム

### 主要なユーザー体験・ワークフロー

1. **会話フロー**:
   ```
   ユーザー → LINE/Slack → Adapter → MessageOrchestrator
     → Mio（ルーティング判断） → Chat/Worker/Coder 実行
     → レスポンス生成 → ユーザーへ返信
   ```

2. **コーディング支援フロー**:
   ```
   ユーザー「実装してほしい」 → Mio → CODE2 ルーティング
     → Coder2（Ao） Proposal 生成 → Shiro（Worker）patch 実行
     → Git auto-commit → 結果返却
   ```

3. **IdleChat フロー**:
   ```
   定期実行 → トピック取得（RSS/News DB）
     → Story Mode / Forecast Mode で本文生成
     → TTS 同期 → 音声出力 → Viewer 表示
   ```

4. **記憶システムフロー**:
   ```
   会話イベント → L0（Redis active thread）
     → L1（SQLite hot store）→ staging → validator → promoter
     → L2（DuckDB thread summary）→ L3（Qdrant vector DB）
   ```

### 主要エントリーポイント

- **`cmd/rencrow/main.go`** (209行): メインエントリーポイント、サブコマンド振り分け
  - `run`: HTTP サーバー起動（デフォルト）
  - `chat`, `evidence`, `source-registry`, `web-gather` 等の管理コマンド

### 主要モジュールと責務

#### Domain層（`internal/domain/`）

| モジュール | 責務 | 主要ファイル |
|---|---|---|
| `agent` | エージェントエンティティ（Mio/Shiro/Coder/Wild/Heavy） | `mio.go`, `shiro.go`, `coder.go`, `wild.go`, `heavy.go` |
| `routing` | ルーティング決定ロジック | `decision.go`, `route.go` |
| `task` | タスク値オブジェクト | `task.go`, `job_id.go` |
| `proposal` | Coder 提案の値オブジェクト | `proposal.go` |
| `patch` | パッチコマンドの値オブジェクト | `command.go`, `result.go` |
| `conversation` | 会話エンジン・RecallPack | `engine.go`, `recall_pack.go` |
| `session` | セッション管理 | `session.go`, `repository.go` |
| `tool` | ツールマニフェスト・実行 | `manifest.go`, `legacy.go`（※legacy あり） |

#### Application層（`internal/application/`）

| モジュール | 責務 | 主要ファイル |
|---|---|---|
| `orchestrator` | メッセージ処理オーケストレーション | `message_orchestrator.go` (546行) |
| `service` | ルーティング・Worker実行等のサービス | `routing_service.go`, `worker_execution_service.go` |
| `idlechat` | IdleChat オーケストレーション | `orchestrator.go`, `story_mode_simple.go`, `forecast_session.go` |
| `idlechat/archive` | **アーカイブ**：複雑な Story Mode | `complex_story_mode/story_mode.go` (1670行) |
| `heartbeat` | ヘルスチェック・Ollama常駐監視 | `service.go` (569行) |
| `dci` | 直接コーパス探索 | `explorer.go` (713行) |
| `sandbox` | Sandbox プロモーション | `promotion_diff_apply.go` (573行) |
| `sourcefetcher` | Source Registry 自動巡回 | `registry_sweeper.go` (517行) |

#### Infrastructure層（`internal/infrastructure/`）

| モジュール | 責務 | 主要ファイル |
|---|---|---|
| `llm/ollama` | Ollama プロバイダー | `provider.go` |
| `llm/claude` | Anthropic Claude プロバイダー | `provider.go` |
| `llm/openai` | OpenAI プロバイダー | `provider.go` |
| `llm/deepseek` | DeepSeek プロバイダー | `provider.go` |
| `persistence/conversation` | 会話永続化（Redis/DuckDB/Qdrant） | `redis_store.go`, `duckdb_store.go`, `vectordb_kb.go` |
| `persistence/workstream` | Workstream JSONL 保存 | `jsonl_store.go` |
| `webgather` | Web取得（SearXNG/YaCy） | `webwright_fetcher.go` (472行) |

#### Adapter層（`internal/adapter/`）

| モジュール | 責務 | 主要ファイル |
|---|---|---|
| `config` | 設定ロード・デフォルト | `config_types.go` (934行), `config_defaults.go` (992行), `config_validation.go` (651行) |
| `viewer` | Live Viewer UI・API ハンドラー | `complexity_hotspot_handler.go` (1034行), `movie_catalog_handler.go` (939行) など多数 |
| `inbound/line` | LINE Webhook ハンドラー | `handler.go` |
| `inbound/slack` | Slack ハンドラー | `handler.go` |

### データフロー

```
1. 外部入力（LINE/Slack Webhook）
   ↓
2. Adapter層（Handler）→ Request 変換
   ↓
3. Application層（MessageOrchestrator）→ ルーティング判断
   ↓
4. Domain層（Mio/Shiro/Coder Agent）→ ビジネスロジック実行
   ↓
5. Infrastructure層（LLM Provider / Persistence）→ 外部サービス呼び出し
   ↓
6. Application層 → レスポンス組み立て
   ↓
7. Adapter層 → 外部へ返信
```

**記憶データフロー**:
```
会話 → L0（Redis）
  → flush（12メッセージ） → L1（SQLite）
    → staging → validator → promoter → confirmed
      → L2（DuckDB summarized thread）
        → L3（Qdrant vector embedding）
```

### 外部依存

- **LLM プロバイダー**: Ollama (常駐), Anthropic Claude API, DeepSeek API, OpenAI API
- **データストア**: Redis (L0), SQLite (L1 hot store), DuckDB (L2), Qdrant (L3)
- **Web検索**: SearXNG, YaCy
- **外部ツール**: Webwright Fetch (Python), Browser Actor (Node.js)
- **音声**: TTS/STT エンジン（別リポジトリ）
- **通知**: LINE Messaging API, Slack API

### 現在の検証コマンド

```bash
# ビルド
make build

# テスト
make test
go test ./...

# カバレッジ
go test -cover ./internal/domain/...  # 93.5%
go test -cover ./internal/...          # 83.6%

# Lint
make vet
go vet ./...

# フォーマット
make fmt
go fmt ./...

# 依存関係検証
make deps
go mod verify

# すべて実行
make check  # deps + fmt + vet + test

# 実行
make run
./build/rencrow run

# ヘルスチェック
./build/rencrow health
./build/rencrow doctor
```

### 絶対に壊してはいけない既存挙動

1. **責務の三分割**（設計原則 §2.1）:
   - Chat（Mio）: 対話・ルーティング判断
   - Worker（Shiro）: 実行・自己拡張判断
   - Coder（Aka/Ao/Gin/Kin）: plan/patch 生成（直接実行しない）

2. **指揮命令系統**（設計原則 §2.2）:
   - Mio は Coder に直接指示しない
   - Shiro が Coder 選択を自律判断
   - すべての会話は日本語

3. **Worker 即時実行のセーフガード**（実装仕様 §1.2）:
   - Git auto-commit（ロールバック可能）
   - 保護ファイルパターン（`.env*`, `*credentials*`, `*.key`, `*.pem`）
   - Workspace 制限（workspace 外への書き込み禁止）
   - 詳細ログ記録（実行前・実行中・実行後）

4. **Clean Architecture 依存方向**（設計原則 §2.5）:
   - 外層（Adapter）→ 内層（Domain）への一方向依存
   - Domain層は何にも依存しない

5. **L0/L1/L2/L3 記憶システムの階層性**:
   - L0 → L1 → L2 → L3 の一方向フロー
   - staging → validator → promoter の承認フロー

6. **既存 API 契約**:
   - LINE/Slack Webhook エンドポイント
   - Viewer API エンドポイント（`/viewer/*`）
   - LLM Provider インターフェース

---

## Non-Negotiables

以下は**絶対に変更してはならない**制約：

1. **メモリ使用量 <10MB**: 低スペックデバイスでの動作が前提
2. **Git auto-commit の有効化時は必ず commit 成功を確認**: 失敗時は実行中断
3. **保護ファイルパターンへの操作は即座にエラー**: セキュリティ上の必須制約
4. **仕様と実装の同期**: 実装変更時は必ず `docs/01_正本仕様/実装仕様.md` を更新
5. **テストカバレッジの目標**: 
   - **Phase 3 完了時点**: Domain層 95%以上（✅達成済み）、全体 70%以上（✅達成済み: 71.7%）
   - **Phase 4 完了時点**: 全体 85%以上、新規コード 90%以上
   - **注**: カバレッジ85%達成は Phase 3 完了後に実施
6. **データ変換方針**（人間の承認: 方法1）:
   - **後方互換性は不要**: 古い形式を永続的にサポートするコードは書かない
   - **一度きりの変換スクリプト提供**: ユーザーが一度だけ実行する変換スクリプトを提供
   - **対象**: 設定ファイル（config.yaml）、データベーススキーマ（SQLite/DuckDB）
   - **バックアップ必須**: 変換前に必ず `.bak` ファイルを作成
   - **ドキュメント必須**: 変換手順を `docs/<モジュール名>/*_migration.md` に記載

---

## Stop And Ask Conditions

以下の状況では**実装を止めて質問する**こと：

1. **正しい仕様がコードから判断できない**:
   - 部分実装の「完成形」が不明
   - テストと実装が矛盾している

2. **削除候補のコードが本当に不要かわからない**:
   - `archive/` ディレクトリのコードが現在も参照されているか不明
   - legacy ツールランナーを使っているツールが残っているか不明

3. **公開 API・DB スキーマ・保存済みデータに影響する可能性**:
   - Viewer API エンドポイントの変更
   - SQLite/DuckDB スキーマの変更
   - 既存データのマイグレーションが必要

4. **認証・課金・通知・外部連携に影響する可能性**:
   - LINE/Slack Webhook の挙動変更
   - LLM Provider の切り替えロジック変更

5. **互換性を壊す可能性**:
   - 設定ファイルフォーマットの変更
   - 既存ユーザーの workspace/session データに影響

6. **複数の設計案があり、プロダクト判断が必要**:
   - 「記憶OS 構想」の未実装部分（LangGraph 移行など）
   - Story Mode の archive 版と simple 版の統合方針

---

## Baseline Commands

**必須**: 実装前に以下を実行し、baseline の検証結果を記録する。

```bash
# 1. Git状態確認
git status
git diff

# 2. ビルド確認
make clean
make build

# 3. テスト実行（全体）
go test ./... -v 2>&1 | tee baseline_test.log

# 4. カバレッジ確認
go test -cover ./internal/... 2>&1 | tee baseline_coverage.log

# 5. Lint 確認
go vet ./... 2>&1 | tee baseline_vet.log

# 6. 依存関係確認
go mod verify
go mod tidy
git diff go.mod go.sum

# 7. 起動確認
./build/rencrow health
./build/rencrow doctor
```

**実装後も同じコマンドを実行**し、差分を確認すること。

---

## Debt Map

技術的負債を以下の観点で整理する。

### 1. Archive ディレクトリの未整理

**場所**:
- `internal/application/archive/parquet_export_job.go` (2344行)
- `internal/application/idlechat/archive/complex_story_mode/` (story_mode.go 1670行)

**根拠**:
- `archive/` ディレクトリに古い実装が残っている
- `complex_story_mode` は現在 `story_mode_simple.go` に置き換えられている可能性が高い
- しかし、テストファイル `orchestrator_story_test.go` (65897行) が残っており、参照されているか不明

**なぜ負債か**:
- archive と active code の境界が不明確
- 削除して良いか判断できない

**影響範囲**:
- IdleChat の Story Mode 生成ロジック
- テストスイート

**変更リスク**: 中（テストが依存している可能性）

**改善案**（人間の回答に基づく）:
1. `complex_story_mode` を参照しているコードを検索
2. 参照がなければ、**archive ディレクトリ内に保持**（完全削除しない）
3. `orchestrator_story_test.go` のテストケースが重要な仕様を示している場合、`story_mode_simple.go` に移植
4. `parquet_export_job.go` の使用状況を確認
5. 使用されていなければ、**archive ディレクトリから削除**

**検証方法**:
```bash
grep -r "complex_story_mode" /home/nyukimi/RenCrow/RenCrow_CORE --include="*.go" --exclude-dir=archive
grep -r "parquet_export_job" /home/nyukimi/RenCrow/RenCrow_CORE --include="*.go" --exclude-dir=archive
```

**実装担当モデルの判断**: **今すぐ実装してよい**（人間の承認済み）

---

### 2. Legacy ツールランナーの残存

**場所**:
- `internal/domain/tool/legacy.go`
- `internal/domain/tool/legacy_test.go`

**根拠**:
- `ManifestFromMetadata` で legacy metadata を manifest に変換している
- `NewLegacyRunner` が存在

**なぜ負債か**:
- 新しい Manifest ベースのツールシステムへの移行が不完全
- legacy と新システムが混在

**影響範囲**:
- ツール実行システム全体
- 既存ツールの互換性

**変更リスク**: 高（既存ツールが動かなくなる可能性）

**改善案**（人間の回答に基づく）:
1. 既存ツールが legacy runner を使用しているか確認
2. **利用中のツールを即時、新 Manifest 形式に移行**
3. 使用されていない legacy ツールは削除
4. migration guide を作成（`docs/tools/manifest_migration.md`）
5. すべての移行が完了したら、legacy runner を削除

**検証方法**:
```bash
# legacy runner を使用しているコードを検索
grep -r "NewLegacyRunner\|LegacyRunner" /home/nyukimi/RenCrow/RenCrow_CORE --include="*.go" --exclude-dir=test

# tools/ ディレクトリのツール定義を確認
find /home/nyukimi/RenCrow/RenCrow_CORE/tools -name "*.json" -o -name "*.yaml" | head -10
```

**実装担当モデルの判断**: **今すぐ実装してよい**（人間の承認済み、即時移行）

---

### 3. Deprecated 設定フィールド

**場所**:
- `internal/adapter/config/config_types.go:934` - "v3後方互換（deprecated: Model に統合済み）"
- `internal/adapter/config/config_defaults.go:992` - "ollama.chat_model is deprecated, use ollama.model instead"

**根拠**:
- `config_types.go` に deprecated フィールドが残っている
- `config_defaults.go` で warn ログを出している

**なぜ負債か**:
- 古い設定ファイルとの互換性維持のためだけに残っている
- 新規ユーザーが deprecated フィールドを使ってしまう可能性

**影響範囲**:
- 既存ユーザーの設定ファイル（`config.yaml`）
- ドキュメント

**変更リスク**: 中（既存設定ファイルの migration が必要）

**改善案**（人間の回答に基づく）:
1. deprecated フィールドの使用状況を調査
2. **一度きりのデータ変換スクリプトを提供**（`scripts/migrate_config_v3_to_v4.sh`）
3. migration ガイドをドキュメント化（`docs/config/config_migration.md`）
4. deprecated フィールドは削除（後方互換性は不要）

**変換スクリプトの内容**:
- `ollama.chat_model` → `ollama.model` への変換
- 既存の `config.yaml` を読み込み、新形式で出力
- バックアップを `.bak` で保存

**検証方法**:
```bash
# deprecated フィールドを grep
grep -n "deprecated" /home/nyukimi/RenCrow/RenCrow_CORE/internal/adapter/config/*.go

# 変換スクリプトのテスト
./scripts/migrate_config_v3_to_v4.sh config/config.yaml.example
diff config/config.yaml.example config/config.yaml.example.new
```

**実装担当モデルの判断**: **今すぐ実装してよい**（人間の承認済み）

---

### 4. 部分実装の統合

**場所**:
`docs/01_正本仕様/12_実装状況_20260505.md` §3「部分実装」に列挙

主な項目：
- 記憶システムの store 責務整理（Redis/DuckDB/VectorDB の3層構成）
- Knowledge DB の高度化（GitHub/Hugging Face/MediaWiki API）
- News の Recall trace 利用履歴
- 6エージェントのキャラ会話ランタイム
- SQLite L1 hot store の高度化

**根拠**:
実装状況メモに「部分実装」と明記されている

**なぜ負債か**:
- 機能が完成しておらず、ユーザー体験が中途半端
- テストが不完全

**影響範囲**: 各機能ごとに異なる

**変更リスク**: 中〜高（機能ごとに異なる）

**改善案**（人間の回答に基づく）:
このリファクタリングでは、**日常会話の記憶システムのみ**を完成させる：

1. **記憶システムの store 責務整理**（優先度：最高）
   - Redis/DuckDB/VectorDB の3層構成の境界を明確化
   - L0 → L1 → L2 → L3 のデータフローを完成
   - staging → validator → promoter の承認フローを完成
   - 各層のインターフェース定義とテスト追加
   - **完成の定義**: E2E テスト通過

**別タスクに回す項目**（別途実装仕様を作成）:
- Knowledge DB の高度化（GitHub/Hugging Face/MediaWiki API）
- News の Recall trace 利用履歴
- SQLite L1 hot store の高度化
- 6エージェントのキャラ会話ランタイム（大規模変更）

**検証方法**:
E2E テストシナリオ「日常会話の記憶システム」：
1. ユーザーとの会話 → L0 に保存
2. 12メッセージで flush → L1 staging に保存
3. validator で検証 → validated に昇格
4. promoter で昇格 → confirmed に昇格
5. L2 thread summary 生成
6. L3 Qdrant embedding 保存
7. 次回会話で RecallPack に含まれることを確認

**実装担当モデルの判断**: **今すぐ実装してよい**（人間の承認済み、焦点を絞った）

---

### 5. Viewer ハンドラーの肥大化

**場所**:
- `internal/adapter/viewer/complexity_hotspot_handler.go` (1034行)
- `internal/adapter/viewer/movie_catalog_handler.go` (939行)
- `internal/adapter/viewer/hobby_graph_handler.go` (895行)
- その他多数の Viewer ハンドラー

**根拠**:
Adapter層のハンドラーが 600〜1000行と大きい

**なぜ負債か**:
- Adapter層はインターフェース変換のみを担当すべき
- ビジネスロジックが混入している可能性

**影響範囲**:
Viewer API 全体

**変更リスク**: 中（リファクタリング時に挙動が変わる可能性）

**改善案**:
1. 各ハンドラーを読み、ビジネスロジックと判定される部分を Application層に移動
2. ハンドラーは HTTP Request/Response 変換のみに責務を限定
3. テストを追加（Application層のロジックテスト + Adapter層のHTTPテスト）

**検証方法**:
```bash
# Viewer ハンドラーのサイズを確認
find /home/nyukimi/RenCrow/RenCrow_CORE/internal/adapter/viewer -name "*_handler.go" -exec wc -l {} + | sort -rn | head -20
```

**実装担当モデルの判断**: **今すぐ実装してよい**（ただし、小さく段階的に）

---

### 6. Config ファイルの肥大化

**場所**:
- `internal/adapter/config/config_types.go` (934行)
- `internal/adapter/config/config_defaults.go` (992行)
- `internal/adapter/config/config_validation.go` (651行)

**根拠**:
Config 関連ファイルが 600〜1000行と大きい

**なぜ負債か**:
- 設定項目が増えすぎて、全体像が把握しにくい
- validation ロジックが複雑化

**影響範囲**:
設定ファイル全体

**変更リスク**: 低（内部リファクタリングなら影響少）

**改善案**:
1. 設定項目をドメインごとに分割（例: `LLMConfig`, `MemoryConfig`, `ViewerConfig`）
2. 各ドメインごとに validation メソッドを実装
3. `config_types.go` から各ドメイン config を import

**検証方法**:
```bash
# 既存の設定ファイルをロードして、新旧で差分がないことを確認
go test ./internal/adapter/config/... -v
```

**実装担当モデルの判断**: **今すぐ実装してよい**（小さく段階的に）

---

### 7. TODO マーカーの解消

**場所**:
- `internal/infrastructure/persistence/conversation/retry.go:43` - "TODO: より詳細なエラー判定ロジックを追加"

**根拠**:
コード中に TODO コメントが残っている

**なぜ負債か**:
- リトライ可能なエラーの判定が単純（すべてリトライ可能と判定）
- 一時的なネットワークエラーと恒久的なエラーを区別できない

**影響範囲**:
会話永続化のリトライ処理

**変更リスク**: 低（エラーハンドリングの改善）

**改善案**:
1. エラー型ごとにリトライ可否を判定（例: ネットワークタイムアウト → リトライ可、認証エラー → リトライ不可）
2. context.DeadlineExceeded, net.Error の Temporary() を活用
3. テストケースを追加

**検証方法**:
```bash
go test ./internal/infrastructure/persistence/conversation/... -v
```

**実装担当モデルの判断**: **今すぐ実装してよい**

---

### 8. 未実装機能の扱い

**場所**:
`docs/01_正本仕様/12_実装状況_20260505.md` §4「未実装」

主な項目：
- LangGraph Turn Controller
- LangGraph subgraph per character
- `@ルミナ`, `@クラリス`, `@ノクス`, `@all` 指名
- ルミナ / クラリス / ノクス（現行は Mio / Shiro / Aka / Ao / Gin / Kin）

**根拠**:
実装状況メモに「未実装」と明記されている

**なぜ負債か**:
- 「記憶OS 構想」の一部だが、現行実装と方向性が異なる
- Go 製 MessageOrchestrator と Python 製 LangGraph の共存が不明

**影響範囲**:
アーキテクチャ全体（大規模変更）

**変更リスク**: 極めて高（アーキテクチャの根本変更）

**改善案**:
このリファクタリングの対象外とする。別途、以下を検討：
1. LangGraph 移行の是非を判断（プロダクトオーナーの意思決定）
2. 移行する場合、段階的移行計画を立てる
3. 移行しない場合、「記憶OS 構想」を Go 実装で実現する設計を作る

**検証方法**: N/A（プロダクト判断）

**実装担当モデルの判断**: **このリファクタリングでは触れない**

---

## Implementation Phases

リファクタリングを以下の Phase に分けて実施する。

### Phase 0: 準備と調査（1-2日）

**目的**: 現在状態を完全に把握し、変更の影響範囲を特定する

**タスク**:
1. Baseline 検証結果を記録（`baseline_*.log` ファイル）

2. Archive コードの参照状況を調査
   ```bash
   grep -r "complex_story_mode\|parquet_export_job" --include="*.go" --exclude-dir=archive
   ```

3. Legacy ツールランナーの使用状況を調査
   ```bash
   grep -r "NewLegacyRunner\|LegacyRunner" --include="*.go" --exclude-dir=test
   find ./tools -name "*.json" -o -name "*.yaml"
   ```

4. Deprecated 設定フィールドの使用状況を調査
   ```bash
   grep -n "deprecated" ./internal/adapter/config/*.go
   ```

5. **データベーススキーマ変更の必要性を調査**（人間の承認: 方法1）
   ```bash
   # 現在のスキーマ確認
   find ./internal/infrastructure/persistence -name "*.sql" -o -name "*schema*"
   
   # migration が必要かコードから判断
   grep -r "ALTER TABLE\|CREATE TABLE" ./internal/infrastructure/persistence --include="*.go"
   
   # 既存 DB ファイルの確認
   find . -name "*.db" -o -name "*.duckdb" | grep -v ".serena"
   ```
   
   **調査観点**:
   - L1 SQLite のテーブル定義変更があるか？
   - L2 DuckDB のテーブル定義変更があるか？
   - 既存データとの互換性が壊れるか？
   - 変更が必要な場合、マイグレーション SQL を書く

6. 部分実装の各項目について、「完成」「未完成」を明確化
   - 特に「日常会話の記憶システム」の L0→L1→L2→L3 フローの現状を詳しく調査

**マイルストーン**: 調査レポート作成（`refactor_investigation_report.md`）
  - 各調査項目の結果
  - データベーススキーマ変更の必要性（必要/不要、変更内容）
  - Phase 1 で実施すべきタスクの明確化

---

### Phase 1: 低リスク・高効果の改善（3-5日）

**目的**: 既存挙動を壊さず、明らかに安全な整理を行う（人間の承認済み）

**タスク**:

#### 1.1 設定ファイル・データベースの変換スクリプト作成（人間の承認: 方法1）

**1.1.1 設定ファイル変換スクリプト**
- **一度きりの変換スクリプト作成**（`scripts/migrate_config_v3_to_v4.sh`）
  - `ollama.chat_model` → `ollama.model` への変換
  - 既存の `config.yaml` を読み込み、新形式で出力
  - バックアップを `.bak` で保存
- migration guide 作成（`docs/config/config_migration.md`）
- deprecated フィールドをコードから削除（後方互換性不要）

**検証**: 
```bash
./scripts/migrate_config_v3_to_v4.sh config/config.yaml.example
diff config/config.yaml.example config/config.yaml.example.new
go test ./internal/adapter/config/... -v
```

**1.1.2 データベーススキーマ変換スクリプト**（Phase 0 調査後に実施）
- Phase 0 で SQLite/DuckDB スキーマ変更の必要性を調査
- 必要な場合、**一度きりのマイグレーションスクリプト作成**（`scripts/migrate_db_v3_to_v4.sh`）
  - ALTER TABLE によるカラム追加
  - 既存データへのデフォルト値設定
  - バックアップを `.bak` で保存
- migration guide 作成（`docs/database/schema_migration.md`）

**検証**:
```bash
# バックアップ作成
cp data/l1_memory.db data/l1_memory.db.bak

# マイグレーション実行
./scripts/migrate_db_v3_to_v4.sh data/l1_memory.db

# スキーマ確認
sqlite3 data/l1_memory.db ".schema l1_memory"

# データ確認
sqlite3 data/l1_memory.db "SELECT * FROM l1_memory LIMIT 5"
```

#### 1.2 TODO マーカーの解消
- `retry.go` のエラー判定ロジック改善
  - `context.DeadlineExceeded`、`net.Error` の `Temporary()` を活用
  - エラー型ごとにリトライ可否を判定
- テストケース追加

**検証**: `go test ./internal/infrastructure/persistence/conversation/... -v`

#### 1.3 Archive コードの整理（人間の承認済み）
- `complex_story_mode` を参照しているコードを検索
  - 参照がなければ、archive ディレクトリ内に保持（完全削除しない）
  - `orchestrator_story_test.go` のテストケースが重要なら `story_mode_simple.go` に移植
- `parquet_export_job.go` の使用状況を確認
  - 使用されていなければ、archive ディレクトリから削除

**検証**: 削除後にテストが通ることを確認

#### 1.4 Legacy ツールランナーの即時移行（人間の承認済み）
- 既存ツールが legacy runner を使用しているか確認
- **利用中のツールを即時、新 Manifest 形式に移行**
- 使用されていない legacy ツールは削除
- migration guide 作成（`docs/tools/manifest_migration.md`）
- すべての移行が完了したら、`internal/domain/tool/legacy.go` を削除

**検証**:
```bash
grep -r "NewLegacyRunner\|LegacyRunner" --include="*.go" --exclude-dir=test
go test ./internal/domain/tool/... -v
```

**マイルストーン**: Phase 1 完了、全テスト通過、カバレッジ Domain 95%達成

**注**: 全体カバレッジ85%は Phase 3 完了後に実施（Phase 3 を優先）

---

### Phase 2: 責務分離の明確化（5-7日）

**目的**: Clean Architecture の境界を明確にし、依存方向を正す

**タスク**:

#### 2.1 Viewer ハンドラーの責務分離
- 各ハンドラーを読み、ビジネスロジックを Application層に移動
- 優先順位: `complexity_hotspot_handler.go` > `movie_catalog_handler.go` > `hobby_graph_handler.go`
- 1ハンドラーずつ、段階的に実施

**検証**:
- リファクタリング前後で API レスポンスが同一であることを確認
- E2E テスト追加

#### 2.2 Config ファイルの分割
- `config_types.go` をドメインごとに分割
  - `config_llm.go`: LLM 関連設定
  - `config_memory.go`: 記憶システム設定
  - `config_viewer.go`: Viewer 設定
  - `config_channel.go`: チャネル設定
- `config_validation.go` も対応する validation を各ファイルに移動

**検証**:
- 既存設定ファイルのロードテスト
- `go test ./internal/adapter/config/... -v`

**マイルストーン**: Phase 2 部分完了（Viewer分離は Phase 3 完了後に続行）

**注**: Viewer ハンドラーの完全分離は Phase 3 完了後に実施（Phase 3 を優先）

---

### Phase 3: 日常会話の記憶システムの完成（最優先、7-14日）

**目的**: L0/L1/L2/L3 の4層記憶システムを完成させ、E2E テストを通す（人間の承認済み）

**重要**: このPhaseを最優先で実施。カバレッジ85%達成やViewer分離完了は Phase 3 完了後に実施。

**現状**（2026-06-12時点）:
- ✅ E2E テスト追加済み（`test/e2e/memory_system_test.go`）
- ✅ Domain層カバレッジ 95.0% 達成
- ✅ internal全体カバレッジ 71.7%（Phase 3時点の目標70%超え達成）
- ⚠️ 実装の完成度を確認し、不足箇所を補完する必要あり

**現状の実装状況**（`refactor_investigation_report.md` より）:

#### ✅ 既に実装・テスト済み
- ✅ L0 active thread and turn lifecycle（`engine_impl_test.go`）
- ✅ L1 SQLite staging, validation, promotion（`l1_sqlite_store_test.go`）
- ✅ L1 memory state, user memory, source registry, news, knowledge（`l1_sqlite_store_test.go`）
- ✅ L2 DuckDB thread summaries and L1 archive export（`duckdb_store_test.go`）
- ✅ L3 Qdrant/vector store（unit + integration tests）
- ✅ E2E テスト（`test/e2e/memory_system_test.go`）: 15メッセージフロー、全層検証、role-filter

#### 📋 Phase 3 実装タスク（優先順位順）

#### 3.1 実装状況の詳細確認（1-2日）
**目的**: E2E テストが通っているが、各層の実装が完全かを確認

**タスク**:
1. `test/e2e/memory_system_test.go` を実行し、現状を確認
   ```bash
   go test ./test/e2e -run TestMemorySystem -v
   ```
2. L0→L1→L2→L3 の各フローのコードを読み、仕様通りか確認
3. 不足している機能や未実装の仕様を洗い出し
4. `refactor_investigation_report.md` に追記

**完成条件**: 不足箇所のリストが明確になる

#### 3.2 L0→L1 フロー完成（必要に応じて、1-2日）
**目的**: 12メッセージflush、staging保存が仕様通りか確認・修正

**確認項目**:
- [ ] 12メッセージで自動flush される
- [ ] L1 staging に正しく保存される
- [ ] observed/candidate/confirmed の状態管理が正しい
- [ ] Event Log に記録される

**不足があれば修正**: 
- `internal/infrastructure/persistence/conversation/engine_impl.go`
- `internal/infrastructure/persistence/conversation/l1_sqlite_store.go`

**検証**: 
```bash
go test ./internal/infrastructure/persistence/conversation -run TestEngine -v
go test ./internal/infrastructure/persistence/conversation -run TestL1 -v
```

#### 3.3 L1 validator → promoter フロー完成（必要に応じて、1-2日）
**目的**: staging→validated→confirmed の昇格が仕様通りか確認・修正

**確認項目**:
- [ ] URL 重複チェック
- [ ] raw_hash 重複チェック
- [ ] source trust チェック
- [ ] license チェック
- [ ] sensitive marker チェック
- [ ] validator 通過後、自動promoter昇格
- [ ] namespace（`user:`, `char:`, `kb:`）の正しい割り当て

**不足があれば修正**:
- `internal/infrastructure/persistence/conversation/l1_sqlite_store.go` (ValidateStagingItem, PromoteValidatedStagingItemToMemory)

**検証**:
```bash
go test ./internal/infrastructure/persistence/conversation -run TestValidate -v
go test ./internal/infrastructure/persistence/conversation -run TestPromote -v
```

#### 3.4 L1→L2 フロー完成（必要に応じて、1-2日）
**目的**: confirmed memory → DuckDB thread summary 生成が仕様通りか確認・修正

**確認項目**:
- [ ] confirmed memory から thread summary 生成
- [ ] DuckDB に保存される
- [ ] L2 から RecallPack に含まれる

**不足があれば修正**:
- `internal/infrastructure/persistence/conversation/duckdb_store.go`

**検証**:
```bash
go test ./internal/infrastructure/persistence/conversation -run TestDuckDB -v
```

#### 3.5 L2→L3 フロー完成（必要に応じて、1-2日）
**目的**: thread summary → Qdrant vector embedding が仕様通りか確認・修正

**確認項目**:
- [ ] thread summary から vector embedding 生成
- [ ] Qdrant に保存される
- [ ] L3 から RecallPack に含まれる
- [ ] vector search が動作する

**不足があれば修正**:
- `internal/infrastructure/persistence/conversation/vectordb_kb.go`
- `internal/infrastructure/persistence/conversation/vectordb_store.go`

**検証**:
```bash
go test ./internal/infrastructure/persistence/conversation -run TestVector -v
```

#### 3.6 RecallPack 統合確認（1-2日）
**目的**: L0/L1/L2/L3 から正しく recall され、role-filter が動作するか確認

**確認項目**:
- [ ] L0（短期）から recent messages が含まれる
- [ ] L1（hot store）から confirmed memory が含まれる
- [ ] L2（中期）から thread summary が含まれる
- [ ] L3（長期）から vector search 結果が含まれる
- [ ] role-filter（Chat/Worker/Wild）が正しく動作
- [ ] token 予算制御が動作
- [ ] RecallPack が正しい順序で組み立てられる

**不足があれば修正**:
- `internal/domain/conversation/recall_pack.go`
- `internal/domain/conversation/engine.go`

**検証**:
```bash
go test ./internal/domain/conversation -run TestRecallPack -v
go test ./test/e2e -run TestMemorySystem -v
```

#### 3.7 ドキュメント更新（1日）
**目的**: Phase 3 完了を記録

**タスク**:
1. `docs/01_正本仕様/12_実装状況_20260505.md` 更新
   - 「記憶システム」の項目を「実装済み」に変更
2. `refactor_investigation_report.md` 更新
   - Phase 3 完了を記録
3. `docs/memory/refactor_changes_summary.md` 更新
   - Phase 3 の実装内容を追記

**検証**: 
```bash
# 全層の個別テスト
go test ./internal/infrastructure/persistence/conversation -v
go test ./internal/domain/conversation -v

# E2E テスト
go test ./test/e2e -run TestMemorySystem -v

# 全テスト
go test ./...

# カバレッジ確認（記憶システム関連）
go test ./internal/infrastructure/persistence/conversation ./internal/domain/conversation -cover
```

**完成条件**:
- ✅ 全テスト通過
- ✅ E2E テスト（`TestMemorySystem`）が15メッセージフローを完全に検証
- ✅ L0→L1→L2→L3 の各フローが仕様通りに動作
- ✅ RecallPack が正しく組み立てられる
- ✅ role-filter が正しく動作
- ✅ ドキュメント更新済み

**マイルストーン**: 日常会話の記憶システム完成、E2E テスト通過、Phase 3 完了

**注意**: 
- Knowledge DB 高度化、News Recall trace、6エージェントのキャラ会話ランタイムは別タスク
- カバレッジ85%達成は Phase 3 完了後に実施
- Viewer ハンドラー分離完了は Phase 3 完了後に実施

---

### Phase 4: カバレッジ向上と最終検証（3-5日）

**前提**: Phase 3（記憶システム完成）が完了していること

**目的**: カバレッジ85%達成、Viewer分離完了、本番環境への移行準備

**タスク**:

#### 4.1 カバレッジ85%達成（2-3日）
**現状**: 71.7% → **目標**: 85%以上

**優先的にテスト追加すべきパッケージ**:
1. `internal/adapter/viewer` (現状: 63.1%)
   - 各ハンドラーのHTTPテスト追加
2. `internal/application/idlechat` (現状: 60.1%)
   - Story Mode、Forecast Mode のテスト追加
3. `internal/application/moviecatalog` (現状: 47.1%)
   - Catalog 操作のテスト追加
4. `internal/infrastructure` の各プロバイダー (<70%)
   - LLM provider のテスト追加

**検証**:
```bash
go test ./internal/... -coverprofile=/tmp/rencrow_internal_final.cover && go tool cover -func=/tmp/rencrow_internal_final.cover
# total が 85%以上であることを確認
```

#### 4.2 Viewer ハンドラー分離完了（1-2日）
Phase 2 の残りタスクを完了：
- `complexity_hotspot_handler.go` の分離
- `movie_catalog_handler.go` の分離
- `hobby_graph_handler.go` の分離

#### 4.3 全テストスイート実行と検証（1日）
1. 全テストスイート実行（`make check`）
2. カバレッジ確認（Domain層 95%以上、全体 85%以上、新規コード 90%以上）
3. 実機での動作確認（`make run` → 各種コマンド実行）
4. ドキュメント更新（人間の回答に基づく）
   - **実装変更項目リストを作成**（`docs/memory/refactor_changes_summary.md`）
   - `docs/01_正本仕様/実装仕様.md` は齟齬がある場合のみ修正
   - `docs/01_正本仕様/12_実装状況_20260505.md` の「記憶システム」項目を更新
   - migration guide 作成（`docs/config/config_migration.md`、`docs/tools/manifest_migration.md`）
5. リリースノート作成

**検証**:
- すべての Baseline コマンドを再実行し、差分を確認
- 既存挙動が壊れていないことを確認

**マイルストーン**: 
- カバレッジ85%達成
- Viewer分離完了
- リファクタリング完了
- リリース準備完了

**完成条件**:
- ✅ Domain層カバレッジ 95%以上
- ✅ internal全体カバレッジ 85%以上
- ✅ 新規追加コード 90%以上
- ✅ 全テスト通過
- ✅ Viewer ハンドラー分離完了
- ✅ ドキュメント更新完了
- ✅ リリースノート作成完了

---

## Verification Requirements

各 Phase ごとに以下の検証を必須とする：

### 必須検証項目

1. **ビルド成功**:
   ```bash
   make clean
   make build
   ```

2. **全テスト通過**:
   ```bash
   go test ./... -v
   ```

3. **カバレッジ向上**（人間の回答に基づく）:
   ```bash
   go test -cover ./internal/domain/...  # 95%以上（目標向上）
   go test -cover ./internal/...          # 85%以上（目標向上）
   # 新規追加コードは 90%以上
   ```

4. **Lint 通過**:
   ```bash
   go vet ./...
   go fmt ./...
   ```

5. **依存関係検証**:
   ```bash
   go mod verify
   go mod tidy
   git diff go.mod go.sum  # 差分がないことを確認
   ```

6. **起動確認**:
   ```bash
   ./build/rencrow health
   ./build/rencrow doctor
   ```

### E2E テスト（必須）

**人間の回答**: E2E テスト追加は必要

以下のシナリオを**自動テスト**として実装し、期待通りに動作することを確認：

1. **会話フロー**（既存）:
   - LINE Webhook に POST → Mio が応答
   - `/code` コマンド → Coder2 が Proposal 生成 → Worker が実行

2. **記憶システムフロー**（新規・最優先）:
   - ユーザーとの15メッセージの会話 → L0（Redis）に保存
   - 12メッセージで flush → L1 staging に保存
   - validator で検証 → validated に昇格
   - promoter で昇格 → confirmed に昇格
   - L2 thread summary 生成・DuckDB 保存
   - L3 Qdrant embedding 生成・保存
   - 次回会話で RecallPack に L0/L1/L2/L3 から recall
   - role-filter（Chat/Worker/Wild）の動作確認

3. **IdleChat フロー**（既存）:
   - トピック取得 → Story Mode 生成 → Viewer 表示

4. **Viewer フロー**（既存）:
   - `/viewer/memory/snapshot` → L1 memory 取得
   - `/viewer/source-registry` → Source Registry 一覧

**E2E テスト配置**:
- `tests/e2e/memory_system_test.go`: 記憶システムフロー（新規作成）
- `tests/e2e/conversation_test.go`: 会話フロー（既存）
- `tests/e2e/idlechat_test.go`: IdleChat フロー（既存）
- `tests/e2e/viewer_test.go`: Viewer フロー（既存）

### 性能検証（Phase 4 のみ）

- メモリ使用量 <10MB を維持していることを確認
- 応答時間が悪化していないことを確認（ベンチマーク比較）

---

## Reporting Format

各 Phase 完了時に以下のフォーマットでレポートを提出する：

```markdown
# Phase X 完了レポート

## 実施内容

- タスク1: xxx
- タスク2: yyy

## 変更ファイル

- `path/to/file1.go`: 変更内容の要約
- `path/to/file2.go`: 変更内容の要約

## 検証結果

### ビルド
\```
make build の出力
\```

### テスト
\```
go test ./... の出力（サマリーのみ）
\```

### カバレッジ
- Domain層: XX.X%
- 全体: XX.X%

### 起動確認
\```
./build/rencrow health の出力
\```

## 残課題

- xxx（Phase Y で対応予定）
- yyy（要確認事項、人間の判断が必要）

## 次のステップ

Phase X+1 に進む / 質問がある / 承認待ち
```

---

## Out-of-scope Items

以下は**このリファクタリングの対象外**とする（人間の承認済み）：

1. **LangGraph への移行**: 
   - **ペンディング**（人間の回答: GoとPythonのメリットデメリットがわかっていない）
   - 別途設計フェーズを設け、LangGraph 移行の是非を判断する
   - このリファクタリングでは、現行の Go 製 MessageOrchestrator を維持

2. **ルミナ/クラリス/ノクス への命名変更**: 
   - **不要**（人間の回答: これは古い仕様）
   - 現行の Mio/Shiro/Aka/Ao/Gin/Kin を維持

3. **その他の部分実装項目**:
   - **別タスク**（人間の回答: 別途実装仕様を作成）
   - Knowledge DB 高度化（GitHub/Hugging Face/MediaWiki API）
   - News の Recall trace 利用履歴
   - SQLite L1 hot store の高度化
   - 6エージェントのキャラ会話ランタイム

4. **新機能の追加**: リファクタリングは既存機能の改善のみ、新機能追加は別タスク

5. **パフォーマンス最適化**: 明らかな性能劣化がない限り、最適化は対象外

6. **UI デザインの変更**: Viewer の見た目変更は対象外、API のみリファクタリング

7. **外部依存の変更**: Redis → 別のDB など、外部依存の変更は対象外

8. **分散実行対応**: 実装仕様 v4.0 の分散実行は未完成だが、このリファクタリングでは触れない

---

## 実装時の制約

1. **最初に git status を確認する**: 既存の未コミット変更と自分の変更を混ぜない
2. **編集前に baseline の検証結果を記録する**: 上記「Baseline Commands」を実行
3. **変更は小さく戻しやすい単位にする**: 1コミット = 1機能変更
4. **無関係な整形やついでのリファクタリングをしない**: Scope を守る
5. **既存挙動を勝手に変えない**: テストが通っていても、挙動が変わっていないことを確認
6. **正しさが不明な場合は実装を止めて質問する**: 上記「Stop And Ask Conditions」を参照
7. **各フェーズごとに検証する**: Phase 完了時に必ず全テストを実行
8. **最後に実行したコマンドと結果を報告する**: 上記「Reporting Format」に従う

---

## 参考文献

- `docs/01_正本仕様/実装仕様.md`: 実装の一次参照
- `docs/01_正本仕様/02_設計原則.md`: 責務の三分割、Clean Architecture
- `docs/01_正本仕様/12_実装状況_20260505.md`: 実装済み・部分実装・未実装の一覧
- `AGENTS.md`: AI エージェント向けの最小実務ルール
- `CLAUDE.md`: RenCrow プロジェクトルール
- `rules/common/GLOBAL_AGENT.md`: AI 開発の共通方針
- `Makefile`: ビルド・テスト・検証コマンド

---

**最終更新**: 2026-06-12  
**人間の回答反映**: 2026-06-12（`refactor_questions.md` の回答を反映済み）  
**次回レビュー予定**: Phase 0 完了時

---

## 補足資料

- **実装前の質問と回答**: `refactor_questions.md`（人間による回答済み）
- **調査レポート**: Phase 0 完了時に `refactor_investigation_report.md` を作成
- **実装変更項目リスト**: Phase 4 完了時に `docs/memory/refactor_changes_summary.md` を作成
