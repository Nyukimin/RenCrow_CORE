---
page_id: index:rencrow-core
type: index
status: active
owner: core
canonical_source: docs/README.md
source:
  - docs/README.md
related:
  - docs/wiki/log.md
  - docs/wiki/modules/rencrow-core.md
summary: RenCrow CORE の選別済み Knowledge Wiki と収録基準の索引
updated: 2026-07-15
---

# RenCrow CORE Knowledge Wiki

この Wiki は RenCrow の LLM Recall に渡すための、短く、現行で、出典を追跡できる Knowledge です。Public 仕様を正本とし、実装詳細や旧資料をそのまま取り込みません。

## 収録ページ

| page | 内容 |
| --- | --- |
| [CORE module](modules/rencrow-core.md) | CORE の責務と外部 module 境界 |
| [Agent responsibilities](concepts/agent-responsibilities.md) | Mio、Shiro、Coder、Advisor、Tool の責務 |
| [Routing contract](concepts/routing-contract.md) | 通常 message と明示 command の route |
| [Memory and Recall](concepts/memory-recall.md) | RecallPack、Knowledge Relation、provenance |
| [Safety and approval](concepts/safety-approval.md) | side effect、approval、draft-only |
| [Runtime state](concepts/runtime-state.md) | success、unavailable、degraded の区別 |
| [Public API](specs/public-api.md) | API 群と chat recipient |
| [Runtime config](specs/runtime-config.md) | config 読込、secret、endpoint |
| [Update log](log.md) | Knowledge 更新履歴 |

## Promotion rule

1. 正本 source と現行 code/test を照合する。
2. 一つの page に一つの安定した概念だけを書く。
3. `canonical_source`、`source`、`updated` を必須にする。
4. 候補、旧仕様、解析結果は自動投入しない。
5. 外部検索や Advisor 出力は人間 review 後にだけ `active` へ昇格する。

全候補と分類理由は archive branch の `docs/分類/Knowledge候補一覧.csv` に保存されています。
