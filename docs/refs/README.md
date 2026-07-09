# Reference Documents

- status: active
- owner: 未定
- source: existing `docs/` materials moved under `docs/refs/`
- last_reviewed: 2026-07-01
- scope: `docs/` 直下の正本入口だけでは足りない場合に参照できる文書の実体置き場

## 役割

`docs/` 直下は通常入口です。この `docs/refs/` は、正本入口だけでは判断できない場合に見る参照置き場です。

このフォルダには、元 `docs/` 直下にあった参照可能な文書を実体として移動しています。正本判断はまず `docs/02_正本仕様/` を優先し、ここにある資料は補助参照として扱います。

## 参照フォルダ

| folder | 用途 | 注意 |
|---|---|---|
| `01_正本仕様/` | 既存 repo 側の正本・履歴上の正本確認 | `docs/02_正本仕様/` と競合する場合は、即採用せずチケット化する |
| `10_新仕様/` | 新仕様、移行仕様、近年の実装仕様確認 | 量が多いので必要章だけ読む |
| `05_LLM運用プロンプト設計/` | LLM provider、prompt、Coder3、常駐管理 | prompt 資産は正本へ無審査に戻さない |
| `06_実装ガイド進行管理/` | 実装メモ、runbook、過去作業記録 | 日付付きログは通常入口にしない |
| `07_IdleChat仕様/` | IdleChat 詳細実装、停止、会話ID、品質制御 | `docs/02_正本仕様/04_IdleChat.md` を入口にする |
| `STT_TTS/` | STT/TTS 詳細契約、音声検証、移植仕様 | `old/` や tmp は原則戻さない |
| `codebase-map/` | 仕様と実装の対応、潜在バグ、影響範囲 | 実装影響確認時だけ使う |
| `09_Viewer/` | Viewer UI / 入力 / 表示仕様 | Viewer 変更時だけ使う |
| `memory/` | Memory system の詳細実装仕様 | `docs/03_記憶検索/` の方針と矛盾しないか確認する |
| `refactor/` | リファクタリング指針 | 正本仕様変更ではなく作業指針として扱う |
| `LEGACY_DOCS_README.md` | 移動前の docs 全体 README | 旧 docs 構成の索引として参照する |

## 使い方

1. まず `docs/README.md` と `docs/00_読む順番.md` を読む。
2. 正本入口だけで判断できない場合、`docs/復帰候補一覧.md` と `docs/99_整理/除外ファイル一覧.csv` を見る。
3. 対応する参照フォルダから必要な文書だけ読む。
4. 参照資料の内容を採用する場合は、関連 DOC チケットへ根拠を記録する。
5. 参照資料を正本扱いにしない。仕様変更が必要なら `docs/02_正本仕様/` 側の更新候補として扱う。

## 移動方針

`docs/` と元 ZIP 以外の docs 直下資料は、実体として `docs/refs/` へ移動済みです。

どこからも参照されていない旧資料は `docs/archive/` へ移動します。2026-07-01 の初回整理分は `docs/archive/unreferenced_20260701/MANIFEST.md` を参照してください。
