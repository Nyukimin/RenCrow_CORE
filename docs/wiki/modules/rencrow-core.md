---
page_id: module:rencrow-core
type: module
status: active
owner: core
canonical_source: docs/01_システム概要.md
source:
  - docs/01_システム概要.md
  - docs/04_アーキテクチャ概要.md
related:
  - docs/wiki/concepts/agent-responsibilities.md
  - docs/wiki/concepts/routing-contract.md
summary: RenCrow CORE は会話、実行、記憶、承認、観測を統合する orchestration runtime
updated: 2026-07-15
---

# RenCrow CORE module

RenCrow CORE は入力統合、route decision、agent contract、Persona、Recall、approval、Workstream、Economic Objective、Viewer projection を所有する orchestration runtime である。

LLM 推論、STT、TTS、Vision、ゲーム世界、横断 tool の実装本体はそれぞれ RenCrow_LLM、RenCrow_STT、RenCrow_TTS、RenCrow_Vision、RenCrow_GAMES、RenCrow_Tools が所有する。CORE は接続契約、runtime selection、health、状態表示を担う。

外部 module の未起動は CORE の未実装と同義ではない。利用時は `unavailable` または `degraded` として明示する。
