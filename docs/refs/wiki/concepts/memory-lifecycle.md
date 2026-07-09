---
type: concept
status: active
owner: core
canonical_source: docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md
source:
  - docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md
  - docs/10_新仕様/09_Memory_SourceRegistry仕様.md
related:
  - docs/wiki/concepts/recall-pack.md
  - docs/wiki/concepts/source-registry.md
updated: 2026-06-25
---

# Memory Lifecycle

Memory Lifecycle は、RenCrow における保存、昇格、忘却、prompt 注入、recall trace を統一する仕様である。

重要なのは「どこに保存されたか」より、「Mio が読んでよいか」「どの根拠で正式記憶になったか」「どの turn で何を読んだか」である。

## 分離するもの

- UserMemory
- Conversation Memory
- Character / Persona
- Knowledge
- OperationMemory
- Runtime / Status Logs
- Knowledge Wiki

## 原則

- candidate / staging / raw observation は正式記憶ではない。
- Mio は DB 全体を直接読まない。
- Mio が読むのは RecallPack と confirmed / pinned UserMemory など選別済み context だけである。
- Wiki page に書いた内容は、そのまま UserMemory にならない。

## Wiki との関係

Wiki は記憶そのものではなく、記憶・仕様・module へ戻る地図である。
Mio が Wiki を使う場合も、RecallPack に採用された snippet と trace を通す。
