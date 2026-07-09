# RenCrow ドキュメント

**最終更新**: 2026-03-31

---

## ディレクトリ構成

```
docs/
├── README.md                        # この索引
├── 01_正本仕様/                     # v3.0 Clean Architecture 実装仕様（正本）
├── 02_OpenClaw移植詳細仕様/         # OpenClaw 移植の分割詳細仕様
├── 03_設計文書/                     # 設計思想・要件定義・アーキテクチャ
├── 04_実装仕様_機能拡張/            # v4 以降の機能別実装仕様
├── 06_実装ガイド進行管理/           # 実装手順・進行記録
├── 07_IdleChat仕様/                 # IdleChat サブシステム仕様
├── STT_TTS/                        # 音声入出力ドキュメント（STT/TTS/共通）
├── LLM運用/                        # LLM 別運用仕様
├── tooling/                        # ツール開発ガイド
└── archive/                        # 旧仕様（読み取り専用）
```

---

## 仕様体系の全体像

```
[統合仕様（起点）]
  01_正本仕様/仕様.md  ← 全体理解はここから

[基盤思想]                            [実装仕様]
  TOOL_CONTRACT.md (リポジトリルート)   01_正本仕様/実装仕様.md (CA 詳細)
  03_設計文書/会話LLM仕様_v1.0.md      04_実装仕様_機能拡張/
  03_設計文書/拡張設計仕様.md

[設計文書]                            [運用仕様]
  03_設計文書/                         04_実装仕様_機能拡張/実装仕様_OpenClaw移植_v1.md
    Chat_Worker_Coder_アーキテクチャ    02_OpenClaw移植詳細仕様/
    仕様_エージェント構成               LLM運用/
```

### 読む順序

| 目的 | 最初に読む | 次に読む |
|------|----------|---------|
| 全体理解 | **01_正本仕様/仕様.md**（統合仕様） | 03_設計文書/Chat_Worker_Coder_アーキテクチャ.md |
| 実装作業 | 01_正本仕様/実装仕様.md | 04_実装仕様_機能拡張/ 配下の対象仕様 |
| 機能追加 | 04_実装仕様_機能拡張/実装仕様_OpenClaw移植_v1.md | TOOL_CONTRACT.md |
| 会話システム | 03_設計文書/会話LLM仕様_v1.0.md | 04_実装仕様_機能拡張/実装仕様_会話LLM_v5.md → v5.1 |
| データ基盤拡張 | 03_設計文書/拡張設計仕様.md | -- |
| IdleChat | 07_IdleChat仕様/IdleChat仕様.md | 07_IdleChat仕様/ 配下の各仕様 |

---

## 1. 基盤思想（設計原則・契約）

実装の前提となる不変のルール。コードより先に存在し、全仕様に優先する。

### 1.1 TOOL_CONTRACT.md（リポジトリルート）

**ツール契約** -- Coder が作るツールの入出力・安全・品質の根本ルール。

| 内容 | 要点 |
|------|------|
| 入出力の統一 | JSON一次経路、stdout=結果/stderr=ログ |
| 安全レール | dry-run必須、入力バリデーション、フィールド制限 |
| 予測可能性 | 非対話、タイムアウト固定、無限待ち禁止 |
| 増殖耐性 | tool_id+version、単一責務、SKILL同梱、廃止宣言 |
| DoD（完成条件） | 6項目チェックリスト |
| 受領フロー | Worker がゲートキーパー |

### 1.2 03_設計文書/会話LLM仕様_v1.0.md

**会話の設計原則** -- 時間軸で育つ対話を成立させるためのシステム構造。

| 内容 | 要点 |
|------|------|
| 会話の単位 | Message / Turn / Thread / Session / Conversation |
| 記憶レイヤー | 短期(RAM) / 中期(Redis→DuckDB) / 長期(VectorDB) / KB / Persona / UserProfile |
| 責務分離 | Chat=見た目 / Worker=想起+判断+記録 / Coder=実装 |
| 処理フロー | 入力→想起→判断→生成→記録（Spawn禁止、同期のみ） |
| JSON I/F | Chat→Worker / Worker→Coder / Worker→Memory の3契約 |
| 実装状況 | 付録Aに30項目の照合表（73%実装済み） |

### 1.3 03_設計文書/拡張設計仕様.md

**データ基盤の拡張ルール** -- エンタメDB（Core/Domain/Relations/Similarities）の成長戦略。

| 内容 | 要点 |
|------|------|
| 4原則 | Core安定 / Relations-Similarities分離 / TopKのみ / entity_id不変 |
| カテゴリ追加 | 6項目チェックリスト（A〜F） |
| 情報源追加 | entity_id中心の統合、provenance記録、コンフリクト解決 |
| Embedding | model別複数保存、metric命名規約、再計算ジョブ |
| クロスメディア | パターンA(Relations) → パターンB(IP上位エンティティ) |
| スケール | TopK O(N*K)、増分更新+定期リビルド |

---

## 2. 正本仕様（実装の一次参照）

実装時に直接参照する仕様書。変更がある場合は仕様を先に更新してから実装する。

### 2.0 01_正本仕様/仕様.md（統合仕様）★ 全体理解の起点

すべての仕様を整合性を持ってまとめた**マスター仕様書**。設計原則・エージェント定義・ルーティング・物理構成・IdleChat・実装状況・文書インデックスを一箇所で参照できる。

初めて全体を把握したい場合・仕様の矛盾を確認したい場合はここを読む。

**セクション別仕様**（各 §の詳細を独立ファイルに整理）:

| ファイル | 対応 §| 内容 |
|---------|------|------|
| `01_正本仕様/02_設計原則.md` | §2 | 責務三分割・CA4層・DDD・セーフガード |
| `01_正本仕様/03_エージェント定義.md` | §3 | Mio/Shiro/Coder の構造体・設定・ディスパッチ |
| `01_正本仕様/04_ルーティング.md` | §4 | v6.1 決定フロー・RuleDictionary・ループ制御 |
| `01_正本仕様/05_物理構成.md` | §5 | 3ノード・SSH Transport・LLM パラメータ |
| `01_正本仕様/06_会話エンジン.md` | §6 | 4層記憶・ConversationEngine・RecallPack |
| `01_正本仕様/07_機能拡張.md` | §7 | SubagentManager（実装済み）・AgentPersona・CapabilityAdaptation |
| `01_正本仕様/08_IdleChat.md` | §8 | 通常/未来展望/ストーリーモード・トピック戦略 |
| `01_正本仕様/09_セキュリティ.md` | §9 | API キー管理・クラウド制限・Worker セーフガード |
| `01_正本仕様/10_ログ.md` | §10 | 構造化ログ・必須フィールド・マスキング |
| `01_正本仕様/11_追加仕様_20260501_記憶OS構想.md` | 追加仕様 | 記憶OSつきマルチキャラクター会話基盤 |
| `01_正本仕様/12_実装状況_20260505.md` | 実装状況 | 最新追加仕様と現行実装の差分 |
| `01_正本仕様/13_仕様統合判断_20260505.md` | 統合判断 | 現行正本と最新追加仕様の採用判断 |
| `01_正本仕様/14_未実装仕様_20260505.md` | 未実装一覧 | 採用方針が残る未実装仕様と優先順位 |

### 2.2 03_設計文書/仕様.md（要件定義）

プロジェクトの目的、用語、ルーティングカテゴリ、セキュリティ、セッション、ログの要件。

### 2.3 01_正本仕様/実装仕様.md（Clean Architecture）-- 3,265行

v3.0 の完全実装仕様。**全実装の基盤**。旧コピー（`実装仕様_v3.md`）は `archive/11_旧仕様_20260326/` に移動済み。

| 内容 | 状態 |
|------|------|
| Clean Architecture 4層構造 | 実装完了 |
| Worker即時実行（Worker即時実行化） | 実装完了 |
| Domain/Application/Infrastructure/Adapter | 実装完了 |
| テストカバレッジ 87.1% | 達成済み |

### 2.3 04_実装仕様_機能拡張/実装仕様_分散実行_v4.md -- 2,334行

v3.0 の上に分散実行機能を追加する仕様。

| 内容 | 状態 |
|------|------|
| Transport層（Local/SSH） | 実装完了 |
| picoclaw-agent（スタンドアロン） | 実装完了 |
| DistributedOrchestrator | 実装完了 |
| 本番有効化 | Claude クレジット補充待ち |

### 2.4 04_実装仕様_機能拡張/実装仕様_会話LLM_v5.md

3層記憶インフラ（Redis/DuckDB/Qdrant）の実装仕様。

| 内容 | 状態 |
|------|------|
| Phase 1: ドメイン層 | 実装完了 |
| Phase 2: 3層ストア | 実装完了 |
| Phase 3: Embedder/Summarizer | 実装完了 |
| 統合テスト 9件 | 全通過 |

### 2.5 04_実装仕様_機能拡張/実装仕様_会話エンジン_v5.1.md

ConversationEngine（RecallPack + Persona）の実装仕様。

| 内容 | 状態 |
|------|------|
| ConversationEngine I/F | 実装完了 |
| RecallPack 生成 | 実装完了 |
| Persona 注入 | 実装完了 |
| Thread 自動判定 | 実装完了（best-effort） |
| UserProfile 自動抽出 | 実装完了（in-memory / best-effort） |

### 2.6 04_実装仕様_機能拡張/実装仕様_チャネル拡張_v1.md

Discord / Slack / 音声入出力のアダプター追加仕様。

| 内容 | 状態 |
|------|------|
| ChannelAdapter 共通インターフェース | 実装完了（HTTP/Webhook基盤） |
| Discord アダプター (WebSocket Gateway) | 部分実装（Webhook/Interaction） |
| Slack アダプター (Socket Mode) | 部分実装（Events API） |
| 音声アダプター (STT + TTS) | 部分実装（TTS/Audio Router、STT未実装） |
| セッション ID 規約（チャネル横断） | 実装完了 |
| 設定ファイル拡張 (channels) | 実装完了（Telegram/Discord/Slack） |

### 2.7 04_実装仕様_機能拡張/実装仕様_OpenClaw移植_v1.md

OpenClawの実装実行能力をGo基盤へ段階移植するための仕様。

| 内容 | 状態 |
|------|------|
| Execution Contract（依頼→実行契約） | 部分実装（正規化/検証） |
| Autonomous Executor（Plan→Apply→Verify→Repair） | 部分実装（最小ループ / `/entry` 経路） |
| TTS Capability Pack（OpenAI→ElevenLabs→local） | 部分実装（TTS運用系あり、正本仕様は整理中） |
| Evidence（execution_report） | 部分実装（Execution監査ログ） |

### 2.8 04_実装仕様_機能拡張/実装仕様_ケイパビリティ適応_v1.md（設計段階）

単一ソース・ケイパビリティ適応型エージェントの実装仕様。v4.0 分散実行の上に構築。

| 内容 | 状態 |
|---|---|
| NodeCapabilities 起動時検出（OS/メモリ/LLM疎通） | 実装完了（起動時検出・profile 判定・ログ出力） |
| ケイパビリティベース LLM ルーティング（静的配線から動的選択へ） | 部分実装（分散 Coder 選択で接続済み・品質要件ベースの選択と縮退） |
| ToolRegistry（Shiro生成ツールの永続化・共有） | 実装完了（DuckDB 永続化・platform filter・CapabilityDetector 連携） |
| Shiro → Coder ツール共有フロー（プラットフォームフィルタ・承認） | 部分実装（ToolRegistry 読み込みと platform filter。承認フローは継続） |
| クロスプラットフォームバイナリ戦略（Makefile + ビルドタグ） | 部分実装（linux/amd64 DuckDB 実装と非対応環境 stub） |

### 2.9 04_実装仕様_機能拡張/実装仕様_エージェントペルソナ_v1.md（設計段階）

Shiro/Coder に軽量なペルソナ（AgentPersona）とインメモリ短期記憶（LightMemory）を付与する実装仕様。

| 内容 | 状態 |
|------|------|
| AgentPersona 型（domain layer） | 実装完了（`internal/domain/agent/persona.go`） |
| LightMemory 型（インメモリ FIFO） | 実装完了（`internal/domain/agent/light_memory.go`。Shiro / Coder の LLM request に直近 turn を注入し、成功応答を記録） |
| config.yaml `agents:` セクション拡張 | 現行 config へ統合済み（`worker.persona_file` / `worker.light_memory`、`coder1..4.persona_file/personality/light_memory`） |
| ShiroAgent / CoderAgent への Builder 注入 | 実装完了（`WithPersona` / `WithLightMemory`、runtime config から注入） |
| IdleChat personalities との統合（Phase 1） | 実装完了（Persona runtime recorder / canonical responses と Coder runtime personality 解決を接続） |

### 2.10 02_OpenClaw移植詳細仕様/（分割仕様）

| ファイル | 内容 | 状態 |
|---------|------|------|
| 詳細実装仕様_02_チャネル網羅不足.md | Telegram/Discord/Slack追加と共通イベント契約 | 現行実装ベース |
| 詳細実装仕様_03_Tools体系の差.md | ToolManifest/Registry/ExecutionEnvelope | 現行実装ベース |
| 詳細実装仕様_04_Nodes_デバイス能力の差.md | NodeCapabilityと要件ベース選定 | 現行実装ベース |
| 詳細実装仕様_05_Gateway_Ops_CLIの差.md | gateway/channels/status/health/doctor/logs | 現行実装ベース |
| 詳細実装仕様_06_Security_Sandboxの差.md | SecurityProfileと権限スコープ・監査 | 現行実装ベース |
| 詳細実装仕様_07_App_Platform導線の差.md | Unified Entryと進行イベント統一 | 現行実装ベース |

補助資料:
- `OpenClaw機能差分比較表_20260310.md`（OpenClawとの機能差分サマリ）

---

## 3. 設計文書（03_設計文書/）

### 3.1 Chat_Worker_Coder_アーキテクチャ.md

Chat/Worker/Coder の役割・責務・指揮命令系統。分散実行の設計思想を含む。

### 3.2 仕様_エージェント構成.md

RenCraw を複数の物理機体上で分散動作させる際の、Chat / Worker / Coder の責務分離・物理配置・自己拡張フローを定義する構成仕様。（著: ルミナ, v0.1）

### 3.3 ログViewer仕様.md

Viewer のタブ構成、SSE、evidence API、IdleChat 制御を含む運用/UI 仕様。

### 3.4 実装仕様_ログViewer_v1.md

ログViewerの実装責務、EventHub、SSE、evidence、IdleChat連携、および目標仕様との差分を整理した実装仕様。

### 3.5 実装仕様_操作ログJSON保持_v1.md

Chat / Worker / Coder の操作ログを JSONL で永続化し、TTL で削除するための実装仕様。

### 3.6 KB運用ガイド.md

Knowledge Base の運用方針・スキーマ定義・更新手順。

---

## 4. 機能仕様（07_IdleChat仕様/）

### 4.1 IdleChat仕様.md

IdleChat の全体仕様。セッション構造、ブレイク体系、TTS/Viewer 連携、ループ検出、要約読み上げの共通仕様。

### 4.2 未来展望セッション仕様.md

IdleChat の未来展望モード（Forecast Session）仕様。6ドメインを順に回しトレンドから未来展望を議論する番組形式。Google Trends / Reddit / はてブ / NHK RSS によるトピック生成パイプラインを含む。

### 4.3 実装仕様_ストーリーモード_v1.md

IdleChat のストーリーモード実装仕様。8ステップパイプライン（Step 1〜6 決定論的、Step 7〜8 LLM）、5改変スタイル、17アクティブ作品のコーパス、品質検証・フォールバック階層を含む。

---

## 5. 運用仕様

### 5.1 運用ガイド/

| ファイル | 内容 |
|---------|------|
| **Coder設定ガイド.md** | Coder1-4 の設定方法（API キー取得、環境変数、SSH 分散実行、トラブルシューティング）★ユーザー必読 |
| 分散実行_前提条件とセットアップ.md | SSH 分散実行の詳細セットアップ手順 |

### 5.2 LLM運用/

| ファイル | 内容 |
|---------|------|
| README.md | LLM 運用仕様の入口 |
| 最新情報/README.md | 現行構成、公開 model 名、主要仕様へのリンク |
| LLM/LLM仕様.md | LLM role、モデル割り当て、起動方式 |
| LLM/PromptBundle仕様.md | Prompt Bundle と KV キャッシュ向け固定 prefix |
| サーバとクライアント/ | OpenAI互換API、管理API、Viewer連携、メモリ監視 |

### 5.4 06_実装ガイド進行管理/

| ファイル | 内容 |
|---------|------|
| 20260309_OpenClaw移植_runbook.md | OpenClaw移植の実機検証手順（E2E実再生完了判定） |
| 20260317_idlechat_story_tuning_memo.md | IdleChat ストーリーモード調整メモ |
| 20260321_ストーリーモード_仕様と実装状況.md | ストーリーモード仕様と実装状況の照合 |

### 5.5 STT_TTS/

音声入出力ドキュメントの集約ディレクトリ。`STT` / `TTS` / `COMMON` の3区分で管理する。

| 区分 | 内容 | 代表ファイル |
|------|------|-------------|
| STT | Whisper/voice-bridge の仕様・実装状況 | `STT/STT仕様.md`, `STT/STT_実装状況.md` |
| TTS | SBV2 等の TTS 契約・運用 | `TTS/12_SBV2_TTS_現状仕様.md` |
| COMMON | STT/TTS 共通方針・移行・CORS | `COMMON/STT_TTS_接続基本事項.md` |

導線: `STT_TTS/README.md`

---

## 6. 仕様間の依存関係

```
01_正本仕様/仕様.md（統合仕様・全体索引）
  |
  +-- 03_設計文書/仕様.md（要件）
  |
  +-- 01_正本仕様/実装仕様.md（CA基盤）
  |     |
  |     +-- 04_実装仕様_機能拡張/実装仕様_分散実行_v4.md（v3の上に追加）
  |     |
  |     +-- 04_実装仕様_機能拡張/実装仕様_会話LLM_v5.md（3層記憶インフラ）
  |     |     |
  |     |     +-- 04_実装仕様_機能拡張/実装仕様_会話エンジン_v5.1.md（RecallPack + Persona）
  |     |
  |     +-- 04_実装仕様_機能拡張/実装仕様_チャネル拡張_v1.md（Discord/Slack/音声）
  |     |
  |     +-- 04_実装仕様_機能拡張/実装仕様_分散実行_v4.md
  |           |
  |           +-- 04_実装仕様_機能拡張/実装仕様_ケイパビリティ適応_v1.md
  |                 |
  |                 +-- 04_実装仕様_機能拡張/実装仕様_サブエージェント_v1.md（ReActLoop、前提）
  |
  +-- 03_設計文書/Chat_Worker_Coder_アーキテクチャ.md（設計思想）
  |
  +-- 04_実装仕様_機能拡張/実装仕様_OpenClaw移植_v1.md（OpenClaw移植の正本）

03_設計文書/会話LLM仕様_v1.0.md（設計原則）
  |
  +-- 04_実装仕様_機能拡張/実装仕様_会話LLM_v5.md + v5.1 の上位思想

03_設計文書/拡張設計仕様.md（データ基盤）
  |
  +-- RenCrow 外のエンタメDB基盤に適用

TOOL_CONTRACT.md（ツール契約）
  |
  +-- 全ツール実装に適用（Coder/Worker が参照）
```

---

## 7. メンテナンスルール

| ルール | 詳細 |
|--------|------|
| 仕様先行 | 実装変更前に仕様を更新する |
| 正本は 01_正本仕様/ | 旧 docs / archive / old / codebase-map は参照しない |
| 実装仕様は 04_実装仕様_機能拡張/ | 新規実装仕様はここに追加 |
| TOOL_CONTRACT はルート | docs/ に移動しない |
| 旧資料は削除済み | 必要な内容は正本仕様または `docs/10_新仕様/` に統合する |
| この README を更新 | 仕様追加時は必ずこの索引を更新する |

---

**プロジェクトルート**: `/home/nyukimi/picoclaw_multiLLM/`
**ブランチ**: `feature/rencrow`
