---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 2
step: "10"
profile: picoclaw_multiLLM
artifact: module
module_group_id: infrastructure_persistence
---

## 概要

`infrastructure_persistence`はLLM/STT/TTS/WebGather/MCP/transport/tool runnerなどの外部接続と、SQLite/JSONL/DuckDB/VectorDBなどの保存実装を担う。application/domainの抽象を実際のプロセス、DB、HTTP APIへ接続する層である。

## 関連ドキュメント

- [../アーキテクチャ総合.md](../アーキテクチャ総合.md)
- [../結合ポイントマップ.md](../結合ポイントマップ.md)
- [../ユースケース逆引き.md](../ユースケース逆引き.md)

## モジュール名: Infrastructure / Persistence / external provider層

### 役割と責務（Why）

- Provider実装、DB/JSONL store、ToolRunner、security policy、transport、external process bridgeを保持する。
- application層が扱うstore/service interfaceを満たし、runtime configに応じて具象実装を選ぶ。
- 外部I/Oの失敗、timeout、retry、sandbox、安全レールを閉じ込める。

### ナビゲーション

| ファイル/ディレクトリ | 役割 | 読むべき場面 |
|---|---|---|
| `internal/infrastructure/llm/` | LLM provider factory/middleware/provider群 | model/provider/context budgetを追う時 |
| `internal/infrastructure/persistence/conversation/` | L1 SQLite、Source Registry、Recall Trace、Wiki、VectorDBなど | memory/knowledge/wiki保存境界を見る時 |
| `internal/infrastructure/persistence/*` | feature別JSONL/SQLite/DuckDB store | Viewer dynamic routeの保存先を見る時 |
| `internal/infrastructure/tools/` | ToolRunner、file/shell/web/search/browser actor/subagent runner | Worker/Coder tool実行の安全境界を見る時 |
| `internal/infrastructure/security/` | policy engine/runner/sandbox guard | command/file操作の制限を見る時 |
| `internal/infrastructure/stt`, `tts`, `audiorouter` | 音声provider/player/router | voice chatやTTS再生問題を見る時 |
| `internal/infrastructure/webgather`, `browseractor`, `mcp` | web収集/ブラウザ/MCP連携 | 外部情報取得やbrowser sidecarを見る時 |
| `tools/` | repo内互換/本体密結合の補助ツール | 既存toolの互換を確認する時 |

### モジュール間の関係

- **依存元**: `cmd/picoclaw/runtime_*.go` -> infrastructure constructors。起動時にstore/provider/runnerを具象化する。
- **依存先**: infrastructure -> external HTTP/DB/filesystem/process。失敗やtimeoutはこの層で扱うべき。
- **依存元**: application services -> store interfaces。具象DBを直接知らない。
- **依存先**: `internal/infrastructure/tools` -> security policy/sandbox guard。Worker/Coder実行の防波堤。
- **依存先**: persistence/conversation -> docs/wiki / Source Registry / L1 memory DB。Knowledge Wikiと会話記憶を接続する。

### 大関数の構造マップ（50行超の関数のみ）

| 関数名 | 行数 | 構造 | 行範囲の目安 |
|---|---:|---|---|
| `NewToolRunner()` | 50行級 | config/defaults/runner dependenciesを初期化 | `internal/infrastructure/tools/runner.go:77`以降 |
| conversation L1 SQLite methods | 50行超の複数関数 | query構築 -> scan -> domain typeへ変換 | `internal/infrastructure/persistence/conversation/l1_sqlite_*.go` |
| provider factory methods | 50行超の可能性 | config role別provider作成、middleware適用 | `internal/infrastructure/llm/factory/factory.go` |
| STT/TTS bridge methods | 50行超の複数関数 | HTTP request/response、audio path/url、retry、chunk plan | `internal/infrastructure/stt`, `internal/infrastructure/tts` |

### 落とし穴・注意点

- `persistence/conversation`はファイル数が多く、memory、Source Registry、Wiki、Recall Trace、web gather cacheが同居する。対象機能のDB table/namespaceを絞って読む。
- JSONL storeとSQLite storeが同じdomainを別実装していることがある。runtime configがどちらを使うかを`cmd/picoclaw/runtime_*.go`で確認する。
- `tools/`は新規横断toolの正本ではない。AGENTS上、新規の横断再利用toolは`RenCrow_Tools`が正本。
- 外部検索・web gatherは「発見」と「証拠」の境界を守る必要がある。Source Registryやcandidate intakeは人間確認前提。

### 設計意図

- Clean Architectureの外側として、失敗しやすい外部I/Oをapplicationから隔離する。
- store実装をfeature別に分け、Viewer/API増加に対して永続化の影響範囲を限定する。

### 初期化

- **module_init() 登録**: なし。runtime configからconstructorで生成。
- **優先度**: config -> infrastructure provider/store -> application service -> adapter route。
- **注意点**: DB pathやworkspace pathは実行WorkingDirectoryとconfigに依存する。調査時はlive serviceのWorkingDirectory確認が必要。

