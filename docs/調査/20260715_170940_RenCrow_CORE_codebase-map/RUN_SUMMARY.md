---
generated_at: "2026-07-15T17:27:44+09:00"
run_id: run_20260715_170940
phase: 3
step: "14"
profile: RenCrow_CORE_20260715_refined
artifact: run_summary
---

# RenCrow_CORE codebase解析 RUN_SUMMARY

## 実行概要

- run_id: `run_20260715_170940`
- 要求phase: `all`
- 実行日時: `2026-07-15T17:09:40+09:00` ～ `2026-07-15T17:27:44+09:00`
- 対象ディレクトリ: `/home/nyukimi/RenCrow/RenCrow_CORE`
- 入力profile: `codebase-analysis-profile.yaml`
- 補正profile: `profile_refined.yaml`
- refs: `docs/02_正本仕様`
- baseline: 未指定
- git: `main` / `21929006dee7176327de00fc4b0b868d13832537`

## ステップ結果

| step_id | name | status | output |
| --- | --- | --- | --- |
| 0a | docsマッピング | done | `refs_mapping.md` |
| 0b | 既存調査マッピング | done | `survey_mapping.md` |
| 0c | モジュール自動発見 | skipped | 静的profileを使用 |
| 0d | CLI事前解析 | skipped | `cli-cache/README.md`。cloc/ctags/cscope不在、Go/rg/find/wcへfallback |
| 1 | 全体概要スキャン | done | `全体概要_draft.md` |
| 2 | モジュール切り分け・確認 | done | `モジュール一覧.md`、`profile_refined.yaml` |
| 3-1..3-6 | ファイル確認 | done | `modules/*/ファイル一覧.md` |
| 4-1..4-6 | 関数確認 | done | `modules/*/関数一覧.md` |
| 5-1..5-6 | ファイル内容構築 | done | `modules/*/ファイル解析.md` |
| 6-1..6-6 | モジュール内容構築 | done | `modules/*.md` |
| 7 | 結合ポイントマップ構築 | done | `結合ポイントマップ.md` |
| 8 | 全体概要構築 | done | `アーキテクチャ総合.md` |
| 9 | ユースケース逆引き | done | `ユースケース逆引き.md` |
| 10 | トレーサビリティ | done | `トレーサビリティマトリクス.md` |
| 11 | ギャップ分析 | done | `ギャップ分析.md` |
| 11.5 | 履歴チェック | done | `履歴チェック.md` |
| 12 | リスク分析 | done | `リスク分析.md` |
| 13 | レポート生成 | done | `reports/` |
| 14 | 最終品質チェック | done | `RUN_SUMMARY.md` |

## 成果物一覧

| パス | 説明 |
| --- | --- |
| `manifest.json` | 実行metadataとstep状態 |
| `RUN_SUMMARY.md` | 本サマリ |
| `profile_refined.yaml` | `internal/features`を追加した補正profile |
| `refs_mapping.md` | 正本仕様とmoduleの対応 |
| `survey_mapping.md` | 既存調査の採否と再検証結果 |
| `全体概要_draft.md` | Phase 1のtop-down概要 |
| `モジュール一覧.md` | 6 module groupの責務境界 |
| `modules/{id}/ファイル一覧.md` | group別のfile構成 |
| `modules/{id}/関数一覧.md` | group別の主要関数と大関数 |
| `modules/{id}/ファイル解析.md` | 代表fileのWhy/API/flow/注意点 |
| `modules/{id}.md` | group別bottom-up解析 |
| `結合ポイントマップ.md` | module依存、chain、data flow |
| `アーキテクチャ総合.md` | 再構築した全体architecture |
| `ユースケース逆引き.md` | 8 use caseの入口からsourceまで |
| `トレーサビリティマトリクス.md` | 正本14要求の双方向trace |
| `ギャップ分析.md` | 8カテゴリ6所見とprofile網羅率 |
| `履歴チェック.md` | gapごとのgit履歴判定 |
| `リスク分析.md` | 中リスク4件の影響波及図 |
| `reports/` | 技術、管理、レビュー向けレポート |

## ギャップ分析サマリ

- 総検出数: 6件
- 深刻度分布: 高0件 / 中4件 / 低2件 / 情報0件
- 最重要項目: ERR-001 signal shutdownのHTTP drain欠落、INCONSIST-001 Opsへのroute集中、TEST-001 remote agent test欠落
- 即時設計推奨: 1件（ERR-001）
- 正本trace: 14/14要求に実装参照あり、13完了、1部分実装
- profile網羅率: 99.5%（967/972 production source files、明示除外5）

## 失敗時

失敗stepなし。0cは静的profile利用、0dは任意CLI不在のため契約どおりskippedとした。

## 次の一手

`reports/レビュー会議資料.md`でERR-001のshutdown contractを先に合意し、route ownership移行とremote agent testを小さな変更単位で計画する。
