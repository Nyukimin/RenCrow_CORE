---
page_id: concept:safety-approval
type: concept
status: active
owner: core
canonical_source: docs/07_安全・承認・データ方針.md
source:
  - docs/07_安全・承認・データ方針.md
related:
  - docs/wiki/concepts/agent-responsibilities.md
  - docs/wiki/concepts/memory-recall.md
summary: 公開、送信、請求、契約、価格決定などは approval まで draft-only に留める
updated: 2026-07-15
---

# Safety and approval

外部公開、投稿、送信、請求、支払、契約、価格決定、重要データの破壊的変更、credential や network boundary の変更は human approval を必要とする。

Approval は scope、requester、対象 artifact、TTL、decision、execution result を保持する。一つの承認を別目的へ流用しない。Advisor と Coder は提案者で、Shiro/Worker が side effect と承認境界の責任を持つ。

自律 discovery は Opportunity や artifact の draft 作成までに制限する。外部 action を暗黙に実行しない。
