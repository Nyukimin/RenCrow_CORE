# RenCrow Memory Index

## User Decisions
- **[No Approval/Ask Flow](feedback_no_approval_flow.md)** — approval/ask フローは実装しない（ユーザー意思決定 2026-03-20）

## Design Principles (MUST FOLLOW)
- **既存コードの設計が間違っていたら、踏襲せず最初に指摘して正す。** 問題のあるパターンに合わせて追加コードを書くのではなく、根本から直す提案をすること。
- **一般的・標準的な手法が存在するなら、それを最初に採用する。** 独自実装を書く前に「これは標準ライブラリやフレームワークの一般的なパターンで解決できないか？」を必ず確認する。
- **トライアンドエラーは2回まで。** 2回試して解決しなければ、立ち止まって原因を分析し直す。同じアプローチの微修正を繰り返さない。別の仮説を立てるか、ユーザーに相談する。
- **E2Eテストは本番と同じ経路を通す。** ハードコードや `os.Getenv` 直読みではなく、本番コードと同じ設定読込パス（config.yaml → Config struct）を使う。本番で動くことを証明するのがE2Eの目的。
- **指摘は原則として吸収する。** 1箇所で受けた指摘を、その場だけの修正で終わらせない。同じ原則が当てはまる他の箇所にも適用する。

## Branch Strategy (CRITICAL)
- **main**: 本番用ブランチ（現在の作業対象）
- **proposal/clean-architecture**: 🗑️ 廃止予定（刈り取って新規立ち上げ）
  - ⚠️ **今後 main ブランチへの移植を提案しない**
  - ⚠️ **このブランチの成果を main へ反映する提案は禁止**
  - 実験終了、参考実装として保持のみ

## Active Work
- [Autonomous Executor 残課題](autonomous_executor_remaining_tasks_20260319.md)
  - route 別 runtime 検証
  - Viewer/evidence 整合
  - TTS Capability Pack の完成
  - TTS 正本仕様の更新

## Recent Completions (実験ブランチのみ - 2026-03-07)
- **Phase 4.2 KB自動保存** — ✅ 完了（proposal/clean-architecture）
  - ToolResponse Metadata拡張
  - web_search V2実装
  - Mio ConversationManager統合
  - main.go DI統合
  - E2Eテスト追加（3件）
  - テスト結果: 15/15 PASS
  - **⚠️ main ブランチには未反映・移植予定なし**

## Previous Completions (main ブランチ)
- **[サブエージェント v1](subagent_status.md)** — 全完了。Chat()実装済み（Claude/OpenAI/DeepSeek ToolCallingProvider準拠）
- **[ルーティング v6](routing_v6_status.md)** — Phase 1-4 全完了・push済み
- **[Conversation LLM v5.0](conversation_llm_v5_status.md)** — Phase 1-3完了、E2E動作確認済み（Redis/Qdrant/DuckDB実接続OK）
- [E2Eテスト本番経路統一](e2e_testing_progress.md) — 全4ファイル完了
- [上流→CA移植](upstream_port_progress.md) — MemoryStore✓ Heartbeat✓ ContextBuilder✓ Subagent✓
- [Web Search](web_search_implementation_status.md) — Google Custom Search API統合完了
- [OPS ルート ReActLoop 統合](ops_react_loop_completion.md) — Worker自律ツール実行完了

## Next Actions (優先順)
未定（実験ブランチ作業完了、main ブランチでの次タスク待ち）

## Key Facts
- Go 1.23, Clean Architecture (domain/application/infrastructure/adapter)
- **注意: `Nyukimin` のタイポ (`Nyukimi`) が繰り返し発生。import パスを書くとき必ず `Nyukimin`（末尾 n）を確認すること。**
- Ollama remote: kawaguchike-llm (100.83.207.6:11434), model: chat-v1 (qwen3-vl 8.8B)
- LINE webhook: `https://fujitsu-ubunts.tailb07d8d.ts.net/webhook`
- Tailscale Funnel: `tailscale funnel --bg 18790` (systemd版は不安定→廃止)
- 秘密値: `~/.picoclaw/.env` (chmod 600) → `source` or systemd `EnvironmentFile=`
- Config: `${ENV_VAR}` 展開 (`os.ExpandEnv`)、`loadFromEnv()` は廃止済み
- Config読込: `PICOCLAW_CONFIG` 環境変数 (--config フラグは未実装)
- ログローテーション: `~/.picoclaw/bin/log-rotate.sh` (cron 毎日04:00)
- [Web Search Config](web_search_config.md) — Chat（即答/一般知識）とWorker（RESEARCH/エンタメDB）の検索対象分離

# currentDate
Today's date is 2026-03-07.
