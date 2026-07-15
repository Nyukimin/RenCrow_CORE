---
generated_at: "2026-07-15T17:09:40+09:00"
run_id: run_20260715_170940
phase: 0
step: "0a"
profile: RenCrow_CORE_20260715_refined
artifact: refs_mapping
---

# 正本仕様マッピング

## 概要

refs は docs/02_正本仕様 を指定した。現行契約、Ver0.80 module構造、外部Bridge、To-Beを分け、コードの「現状」とTo-Beの「目標」を混同しない。

## 資料分類

| 正本 | 種別 | 主な対象module | 実装照合点 |
| --- | --- | --- | --- |
| 00_正本仕様Tree.md | canonical index | 全体 | 優先順位、外部module境界 |
| 01_仕様.md と子文書 | 現行behavior | cmd_runtime, domain_application | route、権限、表示、安全 |
| 02_実装仕様.md と子文書 | 現行implementation contract | domain_application, adapter_viewer_channels, infrastructure_persistence | I/O、loop、Memory、TTS、Viewer |
| 03_Runtime_Config.md | runtime topology | cmd_runtime, infrastructure_persistence | Config選択、endpoint、token_env、timeout |
| 04_IdleChat.md | feature-specific | domain_application, adapter_viewer_channels | session、TTS、Viewer、並行安全 |
| 05-08 Ver0.80 | module migration | feature_facades, 全code group | contract、facade、registrar、legacy-body |
| 09_Game_Bridge_Observer_API.md | external Bridge | adapter_viewer_channels | read-only proxy、limit、failure |
| 10 To-Be と子文書 | target state | domain_application, docs_config_data | Advisor、AgentProfile、Relation、Economic |
| 11_ToBe_統合実装仕様.md | migration / acceptance | 全code group | phase、feature flag、test、非破壊 |
| 12_CORE_機能台帳.md | current inventory | feature_facades, 全code group | contracted / implemented / facade_only / legacy_body |

## module別マッピング

| module | 強い参照 | 補助参照 |
| --- | --- | --- |
| cmd_runtime | 01_仕様/01、02_実装仕様/01、03_Runtime_Config、05 | 04、09、11 |
| domain_application | 01_仕様/02-03、02_実装仕様/01-07、10-11 | 04、12 |
| adapter_viewer_channels | 02_実装仕様/08-09、04、09 | 01_仕様/03、05 |
| feature_facades | 05、06、07、12 | 11 |
| infrastructure_persistence | 02_実装仕様/03-04・07-08、03_Runtime_Config | 10、11 |
| docs_config_data | 00、10、11、12 | docs/README.md |

## 仕様の読み分け

- 現行動作の判定は 01-05 とcode/testを優先する。
- 10-11はTo-Beであり、記載だけを実装済みの証拠にしない。
- 12の状態は入口であり、production wiringはcodeとtestで再確認する。
- LLM/TTS/STT/Vision/Gamesの本体は外部moduleが正本で、COREは接続契約だけを所有する。

## 関連ドキュメント

- profile_refined.yaml
- survey_mapping.md
- トレーサビリティマトリクス.md
