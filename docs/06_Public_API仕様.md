# Public API 仕様

RenCrow_CORE の HTTP API は、RenCrow_PORTAL、Debug Viewer、CLI facade が共通利用するruntime contractです。endpointは互換性維持のため`/viewer/*`を中心に構成されますが、外部公開可否はclientごとのallowlistで制限します。

## 安定性区分

| 区分 | 対象 | 互換性方針 |
| --- | --- | --- |
| Core | `/health`, Viewer entry、通常 chat recipient | 破壊的変更を避ける |
| Feature | status、jobs、workstreams、memory、advisor、revenue 等 | feature 単位で拡張し、既存 field を維持する |
| Operational | repair、LLM management、debug、admin action | local/authorized 利用を前提とし、明示 policy を必要とする |
| Experimental | AI workflow、研究・候補 feature | schema が変わる可能性を明示する |

## 主な endpoint 群

| endpoint / prefix | 用途 |
| --- | --- |
| `GET /health` | CORE process の health |
| `/viewer/api/chat` | Viewer chat request と response |
| `/viewer/status`, `/viewer/agents` | runtime と agent の状態 |
| `/viewer/jobs`, `/viewer/logs` | job と監査可能な log |
| `/viewer/backlog`, `/viewer/scheduler` | 継続作業の照会・操作 |
| `/viewer/workstreams/*` | goal、artifact、annotation、heartbeat、review |
| `/viewer/advisors/*`, `/viewer/agents/profiles` | Advisor run/score と AgentProfile |
| `/viewer/revenue/*` | Opportunity、EconomicTask、RevenueEvent、Reflection、approval |
| `/viewer/memory/*` | memory event と Recall の観測 |
| `/viewer/active-control`, `/viewer/tts/*`, `/viewer/stt/*` | audio/control bridge |
| `/viewer/ai-workflow/*` | AI engineering workflow の experimental API |

実際に有効な endpoint は build と config に依存します。利用前に `/health` と `/viewer/status` を確認し、feature が unavailable/degraded の場合は成功として扱わないでください。

## Chat recipient contract

Viewer 通常 chat の宛先は次の値を使用します。

```text
mio | shiro | kuro | midori
```

`model_alias` や旧 route alias は互換経路であり、新規 client の primary contract にしません。指定 recipient が利用不能な場合に別 recipient へ黙って fallback しません。

## Client の注意

- method、status code、content type を確認する。
- unknown field を許容し、既存 field の意味を推測で変更しない。
- write/action endpoint は approval、idempotency、request provenance を保持する。
- SSE は再接続と重複 event を考慮する。
- debug/admin API を public network へ直接公開しない。

## PORTAL公開境界

`RenCrow_PORTAL`はCOREの全APIを透過公開しません。

- `view`: `GET /viewer/events`、`GET /viewer/idlechat/status`などの読み取りだけを許可する。
- `lab`: viewの読み取りに加え、`POST /viewer/send`、`POST /viewer/idlechat/start|stop`だけを初期allowlistとする。
- Debug、Ops、Repair、LLM管理、設定変更APIはPORTALから遮断する。
- 新しい公開操作はCORE側のAPI追加だけで自動公開せず、PORTAL側でmethod/pathと契約テストを追加する。
