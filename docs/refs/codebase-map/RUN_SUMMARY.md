---
generated_at: "2026-07-01T13:29:00+09:00"
run_id: run_20260701_131925
phase: 2
step: summary
profile: RenCrow_CORE
artifact: run_summary
---

## 実行概要

- **run_id**: run_20260701_131925
- **要求 phase**: all
- **実行日時**: 2026-07-01T13:19:25+09:00 ～ 2026-07-01T13:29:00+09:00
- **対象ディレクトリ**: `/home/nyukimi/RenCrow/RenCrow_CORE`
- **プロファイル**: `codebase-analysis-profile.yaml`
- **git**: `feature/RenCrow_Start` / `29d17e4d`

## ステップ結果

| step_id | name | status | output |
|---|---|---|---|
| 0b | 既存調査マッピング | done | `refs_mapping.md` |
| 1-1 | 起動・HTTPルーティング・runtime配線 | done | `modules/cmd_runtime.md` |
| 1-2 | Domain / Application層 | done | `modules/domain_application.md` |
| 1-3 | Adapter / Viewer / Channel層 | done | `modules/adapter_viewer_channels.md` |
| 1-4 | Infrastructure / Persistence / external provider層 | done | `modules/infrastructure_persistence.md` |
| 1-5 | Docs / Config / Runtime data surface | done | `modules/docs_config_data.md` |
| 7 | 結合ポイントマップ | done | `結合ポイントマップ.md` |
| 8 | ユースケース逆引き | done | `ユースケース逆引き.md` |
| 9 | アーキテクチャ総合 | done | `アーキテクチャ総合.md` |
| 10 | フォルダ概要の修正・補強 | done | `modules/*.md` |
| 11 | 結合・UC の修正・補強 | done | `結合ポイントマップ.md`, `ユースケース逆引き.md` |
| 12 | 全体概要の最終化 | done | `アーキテクチャ総合.md` |
| 13 | 異常一覧の生成 | done | `modules/潜在バグ一覧.md` |

## 成果物一覧

| パス | 説明 |
|---|---|
| `manifest.json` | 実行メタデータ |
| `RUN_SUMMARY.md` | 本サマリ |
| `refs_mapping.md` | 既存調査マッピング |
| `modules/*.md` | module group別の地図 |
| `結合ポイントマップ.md` | module間結合 |
| `ユースケース逆引き.md` | ユースケース別処理フロー |
| `アーキテクチャ総合.md` | 全体レポート |
| `modules/潜在バグ一覧.md` | 潜在バグ・乖離候補 |

## 失敗時

errorsは空。部分成功ではなく全ステップdone。

## 次の一手

- Viewer route一覧とhandler/JS/test/CLI facade対応表を機械生成すると、今回の地図を保守しやすくなる。
- Memory/Knowledge persistenceは別途詳細解析対象にすると有用。

