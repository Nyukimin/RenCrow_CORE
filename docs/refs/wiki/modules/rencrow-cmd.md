---
type: module
status: active
owner: cmd
canonical_source: ../RenCrow_CMD/AGENTS.md
source:
  - ../AGENTS.md
  - ../RenCrow_CMD/AGENTS.md
  - docs/wiki/modules/picoclaw-multillm.md
  - docs/10_新仕様/05_Viewer仕様.md
related:
  - docs/wiki/concepts/viewer-api.md
  - docs/wiki/modules/picoclaw-multillm.md
updated: 2026-06-25
---

# RenCrow_CMD

`RenCrow_CMD` は Viewer の CLI 版の立ち位置である。

独立した RenCrow runtime 正本ではなく、picoclaw_multiLLM サーバを起動し、Viewer と同じ API へ command を送るための別 repo である。

## 原則

- `picoclaw_multiLLM` が主、`RenCrow_CMD` が副。
- 実装判断の source of truth は picoclaw_multiLLM 側の runtime / Viewer API / docs。
- CLI の command 口は Viewer API と揃える。
- git / build / test / push は repo root ごとに分ける。

## Wiki との関係

RenCrow_CMD の説明は、Viewer API と picoclaw_multiLLM module page から参照される。
CLI 独自の仕様が増える場合も、Viewer API との差分として記録する。
