---
type: concept
status: active
owner: core
canonical_source: docs/10_新仕様/09_Memory_SourceRegistry仕様.md
source:
  - docs/10_新仕様/09_Memory_SourceRegistry仕様.md
  - docs/10_新仕様/86_Search_Discovery_Browse_Evidence分離仕様.md
related:
  - docs/wiki/concepts/memory-lifecycle.md
  - docs/wiki/concepts/recall-pack.md
updated: 2026-06-25
---

# Source Registry

Source Registry は外部 source の登録、取得、検証、promote を管理する L1 SQLite 内の境界である。

外部情報は、search snippet や discovery 結果のまま正式 Knowledge / UserMemory / Wiki source にしてはいけない。

## 状態遷移

```text
observed
  -> candidate / staging
  -> validated or rejected
  -> promoted to memory / news / knowledge
```

## Wiki との関係

Wiki page の `source` に使えるのは、読まれた docs / code / rules / validated artifact である。

Source Registry の staging item は、検証前なら source ではなく候補として扱う。
validated / promoted 後は Knowledge や Domain Graph の source になり得るが、Wiki へは正本参照として短く記録する。
