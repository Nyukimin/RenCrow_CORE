---
page_id: spec:public-api
type: spec
status: active
owner: core
canonical_source: docs/06_Public_API仕様.md
source:
  - docs/06_Public_API仕様.md
related:
  - docs/wiki/concepts/routing-contract.md
  - docs/wiki/concepts/runtime-state.md
summary: RenCrow CORE の HTTP API は Viewer と CLI facade が共有する runtime contract
updated: 2026-07-15
---

# Public API

RenCrow CORE の HTTP API は `/health` と `/viewer/*` を中心に構成され、Viewer と CLI facade が共有する。

主な群は chat、status/agents、jobs/logs、backlog/scheduler、workstreams、advisors/profiles、revenue/economic、memory、STT/TTS/control、AI workflow である。有効な endpoint は build と config に依存する。

通常 chat recipient は `mio|shiro|kuro|midori`。旧 `model_alias` や route alias は新規 client の primary contract にしない。write/action endpoint は approval、idempotency、request provenance を保持する。
