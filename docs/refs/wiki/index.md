---
type: index
status: active
owner: core
canonical_source: docs/10_新仕様/89_RenCrow_Knowledge_Wiki運用仕様.md
source:
  - docs/10_新仕様/89_RenCrow_Knowledge_Wiki運用仕様.md
related:
  - docs/wiki/log.md
  - docs/wiki/concepts/recall-pack.md
updated: 2026-06-25
---

# RenCrow Knowledge Wiki

この Wiki は RenCrow の正本仕様ではなく、AI agent が既存 docs / rules / code / memory 境界を探すための索引である。

判断の正本は各 page の `canonical_source` と `source` に残す。

## Concepts

- [ChatWorker](concepts/chat-worker.md)
- [Viewer API](concepts/viewer-api.md)
- [RecallPack](concepts/recall-pack.md)
- [Source Registry](concepts/source-registry.md)
- [Memory Lifecycle](concepts/memory-lifecycle.md)

## Modules

- [picoclaw_multiLLM](modules/picoclaw-multillm.md)
- [RenCrow_CMD](modules/rencrow-cmd.md)

## 使い方

1. まず Wiki page で概念と source を確認する。
2. 実装判断は source に戻って確認する。
3. Mio / Worker / Coder へ渡す場合は SQL index から短い snippet だけを RecallPack に入れる。
