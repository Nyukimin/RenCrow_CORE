# RenCrow プロジェクト概要

## プロジェクトの目的
RenCrow は超軽量なパーソナル AI アシスタントです。Go 言語で実装され、<10MB のメモリ使用量で $10 ハードウェア上でも動作することを目標としています。

## 主要機能
- **マルチチャネル対応**: Telegram、Discord、LINE、Slack、QQ、DingTalk、Feishu、WhatsApp、OneBot
- **マルチ LLM プロバイダー**: Ollama、OpenRouter、Anthropic、OpenAI、Zhipu、Gemini、Groq、DeepSeek など
- **ルーティング拡張**: Chat（Mio）、Worker（Shiro）、Coder（Aka/Ao/Gin）の役割分担
- **ツール**: Web 検索、ファイル操作、シェル実行、MCP統合、スケジュール管理（cron）、スキル管理
- **ヘルスチェックと自動復旧**: Ollama 監視と自動再起動
- **Worker即時実行**: Coderが生成したpatchをWorkerが自動実行（✅ 実装完了）
- **分散実行**: Agent間通信をSSH+JSON化、複数CPU/マシンで並列実行（📋 仕様完成、実装準備完了）

## アーキテクチャ概要

### v3.0 Clean Architecture（✅ 完成）
```
入力（LINE/Slack/etc.）
  → Adapter層（LINE Handler等）
  → Application層（MessageOrchestrator、WorkerExecutionService）
  → Domain層（Mio/Shiro/CoderAgent、Routing、Session）
  → Infrastructure層（LLM Providers、MCP、Tools）
  → 結果返却
```

**現状**: ✅ 核心機能100%完成（2026-03-02）
- ✅ Domain層: 100%完成（agent, routing, patch, proposal, session, task）
- ✅ Infrastructure層: 100%完成（llm, mcp, tools, routing, persistence）
- ✅ Application層: 100%完成（orchestrator、worker_execution_service）
- ✅ Adapter層: 100%完成（config, line）
- ✅ Worker即時実行: 100%完成（Proposal生成→即時実行フロー）

### v4.0 分散実行拡張（📋 仕様完成、実装準備完了）
```
[CPU1: Mio]  ←→ [Transport Layer (Local/SSH)]
                   ↓
[CPU2: Shiro] ←→ [Transport Layer]
                   ↓
[CPU3: Gin]   ←→ [Transport Layer]
```

**特徴**:
- Transport層抽象化（Local/SSH透過的切り替え）
- SSH + JSON通信（stdin/stdout、1行1メッセージ）
- Agentスタンドアロンモード（リモート実行）
- 階層的Session同期（Coder → Worker → Mio）

**期待効果**:
- CPU使用率 70% → 40%
- 応答時間 50%短縮（並列実行）
- スケーラビリティ 1マシン → 4マシン対応
- ダウンタイム 90%削減

### レガシーアーキテクチャ（v2以前）
```
入力（LINE/Slack/etc.）
  → RenCrow Gateway（受信/送信・セッション管理）
  → Router/LoopController（分岐・制約・回数管理）
  → ワーカー（Chat/Worker/Coder）
  → 入口へ返信
```

**状態**: pkg/agent/等に残存（main.goで未使用、削除候補）

## 技術スタック
- **言語**: Go 1.23
- **ビルドシステム**: Makefile、GoReleaser
- **デプロイ**: Docker Compose、systemd
- **パッケージ構造**: 
  - `cmd/picoclaw/` - メインエントリーポイント
  - `internal/` - v3.0クリーンアーキテクチャ実装（domain/application/adapter/infrastructure）
  - `pkg/` - レガシー実装（v2以前、削除候補）
- **テスト**: 全体カバレッジ 87.1%（internal/配下）

## ブランチ戦略
- **main**: 安定版（v2アーキテクチャ）
- **proposal/clean-architecture**: v3.0クリーンアーキテクチャ完成、v4.0仕様策定済み（現在のブランチ）

## ドキュメント

### 正式仕様（実装の一次参照）
- **要件定義**: `docs/仕様.md`
- **v3.0実装仕様**: `docs/実装仕様_v3.md` (3,067行、完成)
- **v4.0実装仕様**: `docs/実装仕様_分散実行_v4.md` (2,334行、完成)
- **アーキテクチャ設計**: `docs/Chat_Worker_Coder_アーキテクチャ.md`

### 実装ガイド
- **Worker即時実行設計**: `docs/06_実装ガイド進行管理/20260228_Worker即時実行ロジック設計.md` (774行)

### LLM運用
- **Coder3仕様**: `docs/LLM運用/Coder3_Claude_API仕様.md`
- **Worker仕様**: `docs/LLM運用/LLM_Worker_Spec_v1_0.md`
- **DeepSeek運用**: `docs/LLM運用/LLM_deepseek運用仕様.md`
- **Ollama管理**: `docs/LLM運用/LLM_Ollama常駐管理.md`

### アーカイブ
- `docs/archive/` - 旧仕様、実装ガイド、調査レポート等（82ファイル）

## 現在の実装状況（2026-03-02時点）

### v3.0 Clean Architecture（✅ 完成）

| カテゴリ | 完成度 | 詳細 |
|---------|--------|------|
| **インフラ基盤** | ✅ 100% | LLM/MCP/Tools/Config/Session |
| **ドメイン層** | ✅ 100% | Agent/Routing/Patch/Proposal定義 |
| **アダプター層** | ✅ 100% | LINE統合、設定管理 |
| **アプリケーション層** | ✅ 100% | Orchestrator、WorkerExecutionService |
| **Worker即時実行** | ✅ 100% | Proposal生成→即時実行フロー |
| **全体** | ✅ 87% | **核心機能100%完成**、付加機能未実装 |

**完成日**: 2026-03-02
**テストカバレッジ**: 87.1%（internal/配下）

### v4.0 分散実行（✅ 実装完了）

**ステータス**: 実装完了（2026-03-03）、カバレッジ85-100%

**期待効果**:
- CPU使用率 70% → 40%
- 応答時間 50%短縮（並列実行）
- スケーラビリティ 1マシン → 4マシン対応
- ダウンタイム 90%削減

**実装完了**: 2日で完了（予定8週間）
- ✅ Phase 1: Config v4整合、Transport層
- ✅ Phase 2: LocalTransport、SSHTransport、MessageRouter
- ✅ Phase 3: スタンドアロンAgent、SSH通信
- ✅ Phase 4: 分散環境（Memory、LoggingTransport、IdleChat）
- ✅ Phase 5: DistributedOrchestrator、Worker並列実行

**主要コンポーネント**:
- `internal/domain/transport/` - Transport抽象化（Domain層）
- `internal/infrastructure/transport/` - Local/SSH実装（Infrastructure層）
- `cmd/picoclaw-agent/` - Agentスタンドアロンモード
- `config.yaml` の `distributed` セクション

**ドキュメント**: 
- `docs/実装仕様_分散実行_v4.md` - v4.0完全仕様（2,334行）
- `docs/Chat_Worker_Coder_アーキテクチャ.md`（484-661行）- 分散実行設計思想

**実装ファイル**:
- `internal/domain/transport/` - Transport抽象化（カバレッジ100%）
- `internal/infrastructure/transport/` - Local/SSH実装（カバレッジ85.3%）
- `cmd/picoclaw-agent/main.go` - スタンドアロンモード
- `internal/application/orchestrator/distributed_orchestrator.go` - 分散Orchestrator
- `internal/application/idlechat/` - IdleChat（94.3%）

**次のマイルストーン**: 本番デプロイ、運用監視、性能測定

**v3.0との互換性**: 
- ✅ 設定フラグ（`distributed.enabled`）で切り替え
- ✅ 既存コードの破壊的変更なし
- ✅ 5分以内でロールバック可能

## 次のアクション

### v3.0（完成済み）
- ✅ 本番デプロイ準備
- ✅ 運用監視（ログ、Git履歴）
- ✅ 付加機能追加（必要に応じて）

### v4.0（実装準備完了）
1. **Phase 1実装開始**（Week 1-2）
   - Transport インターフェース定義
   - LocalTransport 実装
   - MessageRouter 実装
   - 単体テスト作成（カバレッジ90%以上）

2. **事前準備**（Phase 2開始前）
   - SSH鍵ペア生成（Ed25519推奨）
   - リモートホストへの公開鍵配置
   - `known_hosts` にホスト鍵登録

**最終更新**: 2026-03-02
