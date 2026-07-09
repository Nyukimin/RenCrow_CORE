---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 2
step: "10"
profile: RenCrow_CORE
artifact: module
module_group_id: docs_config_data
---

## 概要

`docs_config_data`は実装の正本仕様、runtime config、prompt/skill/schema、Knowledge Wiki、`rencrow-data`運用基盤を含む非Goの重要surfaceである。コード変更の判断材料であり、同時にViewer/APIから参照される実行時データでもある。

## 関連ドキュメント

- [../アーキテクチャ総合.md](../アーキテクチャ総合.md)
- [../結合ポイントマップ.md](../結合ポイントマップ.md)
- [../ユースケース逆引き.md](../ユースケース逆引き.md)

## モジュール名: Docs / Config / Runtime data surface

### 役割と責務（Why）

- `docs/01_正本仕様/実装仕様.md`を実装判断の一次参照として保持する。
- `config/`、`prompts/`、`skills/`、`schemas/`がruntimeのprovider/role/tool/validationに影響する。
- `docs/wiki`と`rencrow-data`はViewer/CLI/knowledge indexと接続する運用データ面である。

### ナビゲーション

| ディレクトリ/ファイル | 役割 | 読むべき場面 |
|---|---|---|
| `docs/01_正本仕様/実装仕様.md` | Clean Architecture、routing、I/O、Viewer/evidence/job、module registryなどの正本 | 実装判断で迷った時 |
| `docs/10_新仕様/` | 新仕様・配置ガバナンス・Knowledge Wiki等 | 新しい運用/仕様を確認する時 |
| `docs/09_Viewer/`, `DESIGN.md`, `rules/rules_viewer_ui.md` | Viewer/UI方針 | UI/Viewer変更時 |
| `config/` | runtime configとpersona config | provider、workspace、STT/TTS、Viewer設定を見る時 |
| `prompts/` | role別prompt/idle chat/skill prompt | LLM挙動や役割文脈を見る時 |
| `skills/` | repo-local skills | agent作業手順を確認する時 |
| `schemas/` | JSON/data contract | 入出力検証を見る時 |
| `docs/wiki` | Knowledge Wiki | `rencrow knowledge index-wiki`やRecall Packを見る時 |
| `rencrow-data` | 投資研究基盤 | `/viewer/investment/*`やCLI運用を確認する時 |

### モジュール間の関係

- **依存元**: human/agent実装判断 -> `docs/01_正本仕様/実装仕様.md`。
- **依存元**: runtime -> `config/`、`prompts/`、workspace data。起動時configとprompt bundleに影響する。
- **依存元**: application/knowledge and persistence/conversation -> `docs/wiki`。Wiki index/recallのsourceになる。
- **依存元**: Viewer investment handlers -> `rencrow-data` DB/config。運用データがViewer表示へ出る。
- **依存先**: docsは実装の写像ではなく正本/設計/運用を分担する。古いdocsやarchiveはAGENTSに従って直接参照しない。

### 大関数の構造マップ（50行超の関数のみ）

非Go文書・設定面が中心のため、関数構造ではなくドキュメント構造を読む。`docs/01_正本仕様/実装仕様.md`はClean Architecture、routing、Worker即時実行、Viewer/Evidence/Job、module registry、parallel jobs、instruction steeringなどの章を持つ。

### 落とし穴・注意点

- `docs/codebase-map`はAGENTSで「古いものを参照しない」とされていた場所だが、今回の生成物は`manifest.json`付きの新規run成果物である。鮮度はrun_idとgit commitで判定する。
- docsは仕様正本・新仕様・調査・運用ガイドが分かれる。実装の一次参照は`docs/01_正本仕様/実装仕様.md`。
- `config.yaml`はsymlinkで`config/config.yaml`を指す。secret値やlive configの扱いには注意する。
- `rencrow-data`はPython/DB/CLI系の別運用面を含み、Go package一覧だけでは把握できない。

### 設計意図

- 実装判断をコード断片だけに閉じず、正本仕様・rules・調査・運用データを参照可能にする。
- Knowledge Wikiやrencrow-dataのようなデータ面をViewer/APIと接続し、会話/記憶/運用へ還元する。

### 初期化

- **module_init() 登録**: なし。
- **優先度**: docs/rulesで仕様を確認 -> config/prompts/schemaでruntime条件を確認 -> codeを読む。
- **注意点**: external knowledge intakeはcandidate-only、人間確認前提。検索結果をそのまま正本化しない。

