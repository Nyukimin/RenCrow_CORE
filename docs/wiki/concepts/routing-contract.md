---
page_id: concept:routing-contract
type: concept
status: active
owner: core
canonical_source: docs/02_機能仕様.md
source:
  - docs/02_機能仕様.md
  - docs/03_キャラクター・エージェント仕様.md
related:
  - docs/wiki/concepts/agent-responsibilities.md
  - docs/wiki/specs/public-api.md
summary: 通常 message の route owner は Mio で、明示 command は recipient 選択より優先される
updated: 2026-07-15
---

# Routing contract

通常 message は、明示 command、Mio `DecideAction`、rule dictionary、classifier、決定 route の実行という順で処理する。Worker は route を決め直さず、決定済み route の実行を担う。

Viewer の通常 recipient は `mio|shiro|kuro|midori` である。明示 command は recipient 選択より優先する。指定 runtime が利用不能な場合、Mio や別 character へ黙って fallback しない。

内部 reasoning、route detail、管理情報を user-facing response や TTS 文面に混ぜない。Job ID、from/to、route、provenance は監査可能に残す。
