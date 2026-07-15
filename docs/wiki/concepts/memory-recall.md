---
page_id: concept:memory-recall
type: concept
status: active
owner: core
canonical_source: docs/02_機能仕様.md
source:
  - docs/02_機能仕様.md
  - docs/07_安全・承認・データ方針.md
related:
  - docs/wiki/concepts/safety-approval.md
  - docs/wiki/index.md
summary: RecallPack は budget、role visibility、provenance を保って複数 source の文脈を選別する
updated: 2026-07-15
---

# Memory and Recall

会話履歴は発言ごとの `from` と `to` を保持する。RecallPack は L1、vector、DuckDB、Wiki index などの候補を context budget、role visibility、provenance とともに選別する。

Knowledge Relation は Entity、Topic、Project を制限付き 1-2 hop で横断し、採用理由を trace に残す。relation expansion は関連性を補う手段であり、source の確度を上げるものではない。

外部検索、Advisor 出力、Revenue 候補は candidate knowledge である。source と人間 review なしに確定 Knowledge へ自動 promotion しない。
