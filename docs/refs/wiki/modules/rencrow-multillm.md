---
type: module
status: active
owner: core
canonical_source: docs/10_新仕様/02_モジュール構成仕様.md
source:
  - ../AGENTS.md
  - docs/10_新仕様/01_新仕様_概要.md
  - docs/10_新仕様/02_モジュール構成仕様.md
  - docs/10_新仕様/05_Viewer仕様.md
related:
  - docs/wiki/concepts/viewer-api.md
  - docs/wiki/modules/rencrow-cmd.md
updated: 2026-06-25
---

# RenCrow_CORE

`RenCrow_CORE` は RenCrow の Core / Chat / Viewer / Server の主 repo である。

RenCrow の runtime 仕様、Viewer API、conversation memory、RecallPack、Source Registry、主要 handler はこの repo を正本として扱う。

## 主な担当

- Chat / Worker / Coder orchestration
- Viewer HTTP API / SSE / event log
- Memory / Source Registry / L1 SQLite
- RecallPack / recall trace
- prompt bundle / persona / skills context

## RenCrow_CMD との関係

RenCrow_CMD はこの repo の CLI クライアントである。
RenCrow_CMD が独自 runtime 正本を持つのではなく、RenCrow_CORE サーバを起動し、Viewer API と同じ口に command を送る。
