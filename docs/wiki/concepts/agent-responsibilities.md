---
page_id: concept:agent-responsibilities
type: concept
status: active
owner: core
canonical_source: docs/03_キャラクター・エージェント仕様.md
source:
  - docs/03_キャラクター・エージェント仕様.md
related:
  - docs/wiki/concepts/routing-contract.md
  - docs/wiki/concepts/safety-approval.md
summary: Mio は route と応答、Shiro は実行、Coder と Advisor は提案、Tool は限定 capability を担う
updated: 2026-07-15
---

# Agent responsibilities

- Mio は通常 input、`DecideAction`、会話、最終 user response を所有する。
- Shiro は Worker/OPS の実行、side effect、approval boundary、Advisor 助言の採否を所有する。
- Kuro/Heavy は深い分析、Midori/Wild は発想・創作の chat route を担う。
- Coder は設計・差分・提案を返すが、外部 side effect の最終 owner ではない。
- Advisor は専門的助言を返すが、採否や実行責任を持たない。
- Tool は policy の下で限定 capability を実行し、人格・長期記憶・最終意思決定を持たない。

Shiro との通常会話と Shiro の OPS execution は別 route である。
