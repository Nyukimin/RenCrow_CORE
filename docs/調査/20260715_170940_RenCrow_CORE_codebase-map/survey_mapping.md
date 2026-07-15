---
generated_at: "2026-07-15T17:09:40+09:00"
run_id: run_20260715_170940
phase: 0
step: "0b"
profile: RenCrow_CORE_20260715_refined
artifact: survey_mapping
---

# 既存調査マッピング

## 概要

docs/調査 の4件を再利用可能な過去観測としてマッピングした。いずれも仕様根拠ではなく、今回のcode/testで再検証する対象である。

## 調査一覧

| 調査 | 関連module | 今回の扱い |
| --- | --- | --- |
| 20260701_170923_RenCrow_CORE_Ver0.80_現状モジュール検証.md | cmd_runtime, feature_facades, adapter_viewer_channels | 旧P0/P1/P2を現状と再照合 |
| 20260701_RenCrow_CORE_Ver0.80_AgentList_機能リスト整理.md | domain_application, feature_facades | Agent/feature inventoryの比較 |
| 20260702_080327_RenCrow_CORE_Ver0.80_Public起点化_QAトラッカー.md | 全体 | Public seed、clean clone、代表E2Eの履歴 |
| 20260708_131947_記憶システムリファクタ_残検証と実機側対応事項.md | domain_application, infrastructure_persistence | Memoryの実機E2E・CI未確認点 |

## 再検証結果

- 2026-07-01のmodule dependency coverage不足は modules/dependency_rules_test.go の動的directory確認で解消済み。
- Viewer recipient contractは現在もtestを持つ。
- cmd/rencrow と Viewer adapter の肥大化はlegacy-bodyとして残り、feature registrarへの移行は部分完了。
- Memoryはunit/integration testが豊富だが、過去調査が残した実機full-runtime確認は今回の静的解析範囲外。

## 前回解析成果物

同一output pathのmanifestは存在せず、前回run取り込みは該当なし。

## 関連ドキュメント

- refs_mapping.md
- ギャップ分析.md
- 履歴チェック.md
