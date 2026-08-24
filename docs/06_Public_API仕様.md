# Public API 仕様

RenCrow_CORE の HTTP API は、RenCrow_ASSISTANT、RenCrow_PORTAL、Debug Viewer、CLI facade が利用するruntime contractです。endpointは互換性維持のため`/viewer/*`を中心に構成されますが、外部公開可否はclientごとのallowlistで制限します。

## 安定性区分

| 区分 | 対象 | 互換性方針 |
| --- | --- | --- |
| Core | `/health/live`, `/health/ready`, `/health`, `/ready`, Viewer entry、通常 chat recipient | 破壊的変更を避ける |
| Feature | status、jobs、workstreams、memory、advisor、revenue 等 | feature 単位で拡張し、既存 field を維持する |
| Operational | repair、LLM management、debug、admin action | local/authorized 利用を前提とし、明示 policy を必要とする |
| Experimental | AI workflow、研究・候補 feature | schema が変わる可能性を明示する |

## 主な endpoint 群

| endpoint / prefix | 用途 |
| --- | --- |
| `GET /health/live` | COREのHTTPイベントループ自身のliveness。外部依存を確認しない |
| `GET /health/ready` | CORE capabilityのreadiness。未ready時は503と`status=unavailable`を返す |
| `GET /health` | COREと設定済み依存serviceの総合health |
| `GET /ready` | request受付可否 |
| `POST /viewer/send`, `GET /viewer/events` | PORTAL／CMD等のmessage・添付送信とSSE event購読 |
| `POST /v1/agent/ops` | loopback・Bearer認証済みRenCrow_CMDからShiro／Worker OPSを一回実行 |
| `GET/POST /viewer/character-runtime` | Character一覧、複数Character Roundと会話ID |
| `/viewer/status`, `/viewer/agents` | runtime と agent の状態 |
| `GET /viewer/idlechat/status` | IdleChat状態と読み取り専用の`word_topic_stock`、`forecast_stock`、`episode_stock`、`topic_stock_playback` snapshot |
| `POST /viewer/idlechat/playback` | Topic Stockを`play`、`next`、`previous`で再生。任意選択時だけ`item_id`を指定する |
| `GET /viewer/idlechat/collection` | 日次収集の入力cache、次回04:00 JST、取得元、利用toolの読み取り専用snapshot。ユーザー向けニュース取得のAPIではない |
| `POST /viewer/idlechat/start`, `POST /viewer/idlechat/stop` | IdleChatの開始・停止。認可されたwrite clientだけが利用する |
| `POST /viewer/surface-presence` | PORTAL Chat／IdleChat画面の期限付き在席を通知し、COREが排他的な有効modeを決定する |
| `/viewer/jobs`, `/viewer/logs` | job と監査可能な log |
| `/viewer/backlog`, `/viewer/scheduler` | 継続作業の照会・操作 |
| `POST /viewer/superagent/runs/resume` | checkpoint付きRunを同一`run_id + checkpoint_revision`の冪等queueへ登録。checkpointなし、terminal run、非resumable runは409 |
| `GET/POST /viewer/superagent/run-queue` | durable resume queueの照会・登録。claim lease/tokenは内部schedulerだけが所有する |
| `/viewer/workstreams/*` | goal、artifact、annotation、heartbeat、review |
| `/viewer/advisors/*`, `/viewer/agents/profiles` | Advisor run/score と AgentProfile |
| `/viewer/revenue/*` | Opportunity、EconomicTask、RevenueEvent、Reflection、policy decision |
| `GET/POST /viewer/revenue/deliveries` | trace付き汎用Deliveryの一覧・draft/状態record作成 |
| `/viewer/memory/*` | memory event、Recall、ProfilePromotion job の観測 |
| `POST /v1/memory/import/chatgpt` | authenticated owner用Common Raw bundle upload。既定`apply=false`、whole-artifact検証後にCOREが内部batch化 |
| `GET /v1/memory/import/chatgpt/{percent-escaped-export-id}` | owner-scoped bounded import status／receipt。Raw本文・path・statementは返さない |
| `GET /v1/memory/import/chatgpt/{percent-escaped-export-id}/progress` | owner-scoped Raw／projection／ProfilePromotion進捗 |
| `POST /v1/memory/import/chatgpt/retry` | evidenceが残る同exportのfailed jobだけを再投入 |
| `POST /v1/memory/import/chatgpt/finalize` | 検証済みbindingと永続receiptをLLMなしで再照合し、終端receiptを作成 |
| `POST /viewer/hobby-graph/music/import` | `rencrow.music_catalog.v1`を最大100件検証・import。既定dry-run。未許諾歌詞本文と復元可能featureを拒否 |
| `GET /viewer/databases/conversation-archive` | Conversation Archive（`memory_archive.db`）の読み取り専用snapshot |
| `GET /viewer/databases/glossary` | Glossary DBの読み取り専用snapshot |
| `GET /viewer/databases/tool-registry` | production Worker runtimeとTool Registry DBを統合した有効Toolの読み取り専用snapshot |
| `GET /viewer/databases/catalog` | 起動時にAgentへ供給した同一Data Capability Catalogの読み取り専用metadata projection |
| `GET /viewer/capabilities` | production Worker Tool metadataのRuntime Capability Snapshot読み取り |
| `POST /viewer/capabilities/apply` | capability sourceを検証し、必要時だけCOREの再起動反映を予約 |
| `GET /viewer/capabilities/apply/{request_id}` | apply receiptと再起動後の検証状態を読み取り |
| `GET /viewer/movie-catalog` | 映画・人物catalogと利用者評価の一覧・詳細 |
| `GET /viewer/movie-catalog/person-related/people` | 明示評価済み人物の索引付き選択肢 |
| `GET /viewer/movie-catalog/person-related` | 人物ID・カテゴリ指定の関連作品／日本語サマリprojection |
| `GET /viewer/movie-catalog?action=cards` | 映画・人物ViewerのD0/D1派生カード投影 |
| `POST /viewer/movie-catalog/preference` | 映画・人物の認知・好み評価を保存 |
| `/viewer/active-control`, `/viewer/tts/*`, `/viewer/stt/*` | audio/control bridge |
| `WS /stt` | Viewerの同一origin音声入力。COREが音声chunkをRenCrow_STTのHTTP公開APIへ中継する |
| `POST /stt/chat-input` | CMD等が送るWAVをRenCrow_STT経由で文字起こしし、Chat入力用envelopeを返す |
| `POST /viewer/image/generate`, `GET /viewer/image/result?id=...` | Debug Viewerの画像生成と結果表示 |
| `POST /viewer/recipient-selection` | client-localなchat recipient選択の通知event |
| `POST /webhook/line` | LINE Messaging API Webhook。署名必須の正規path |
| `POST /internal/assistant/notifications/line` | localhostのRenCrow_ASSISTANT専用LINE push transport |
| `/viewer/ai-workflow/*` | AI engineering workflow の experimental API |
| `/viewer/games/*` | RenCrow_GAMES bridge（status/decision/result/sessions/events/launch/observer proxy） |
| `GET /viewer/trade/status` | RenCrow_TRADEのread-only状態projection。Broker／注文APIではない |
| `POST /viewer/trade/policy/evaluate` | Global PolicyとTRADE policyの純粋な診断評価。実行許可や注文APIではない |
| `POST /viewer/trade/risk-preview` | Global Policyに束縛した100万円Simulator購入前Risk Preview。Portfolio更新や注文APIではない |
| `POST /viewer/trade/simulation-commit` | Preview済みの仮想buyを失効検査して100万円Simulatorへ一度だけ反映。外部注文ではない |
| `POST /viewer/trade/shadow/observations` | Outcome開示前の無発注判断、context hash、採点契約hashを追記専用Shadow台帳へ固定 |
| `POST /viewer/trade/shadow/outcomes` | 固定済みOutcome Label Contractに従う結果を観測の後続eventとして追記 |
| `GET /viewer/trade/shadow/outcomes/report?study_id=<id>` または `?event_id=<audit_ref>` | Shadow Outcome台帳を検証して読み取り専用集計を返す。write→read handoffの`audit_ref`はexact `shadow-event/sha256:<64 lowercase hex>` |
| `POST /viewer/trade/shadow/outcomes/reviews` | Outcome reportのhashとlatest event hashを束縛した独立reviewを別台帳へ追記。promotion／Portfolio変更／外部実行は行わない |
| `GET /viewer/trade/shadow/outcomes/reviews/report?study_id=<id>` | Review ledgerを検証し、独立reviewの有無を返す読み取り専用projection |

`GET /viewer/databases/catalog`およびAgent Tool `data_capability.describe`の各entryは、
`name`、logicalな`physical_key`、`owner`、`categories`、`status`、安全なoperation、必要な場合の
`reason`を返します。`physical_key`は`storage.databases.<name>`という設定互換keyであり、物理DB pathを
意味せず、responseにもpathを出しません。`owner_route_only=true`は、そのentryをCOREのlocal DBへ
fallbackせず、宣言されたowner module routeだけで解決することを示します。現在は`investment`だけが
このflagを持ち、正本ownerは`RenCrow_TRADE`です。CORE側の`storage.databases.investment`が未設定または
local file不在でも、認証済みTRADE read／write routeがruntimeへ登録されていれば`status=available`に
なります。routeがない場合は`unavailable`または`blocked`となり、別DB・legacy route・fake endpointへ
fallbackしません。通常のCORE-owned entryでは`owner_route_only`は省略（false）されます。

`GET /viewer/runtime-config`の`runtime_readiness`は、会話runtimeのRedis projectionについて
`redis_configured`、`redis_reachable`、`redis_status`を返します。`redis_status`は
`disabled | available | unavailable`です。`available`はresponse生成時に既存runtime接続への
bounded PINGが成功したことを表し、URL、credential、backend error本文は返しません。RedisはL1 SQLite
正本ではないため、この独立statusはCOREの`/health/ready`判定へ混入しません。

`GET /viewer/databases/tool-registry`は、同じproduction Worker `RunnerV2`から得たruntime
metadataと、設定済みSQLite Tool Registryのplatform適合行を、名前順・runtime優先で重複排除して
返します。各itemの`origin`は`core_runtime | rencrow_tools | dynamic_registry`です。
SQLite未設定でもruntime toolを返し、SQLite読込失敗時はruntime toolを隠さず`error`へ部分失敗を
示します。全sourceが空の場合は`available=false`です。これは読み取りAPIであり、Tool登録、実行、
権限付与、RenCrow_Tools配下の実行file探索を行いません。

`GET /viewer/databases/catalog`は、起動時にChat／Workerへ供給した同一catalog instanceから、
20件の設定DB key、owner、data category、status、safe operation、Tool ID、sensitivity、reasonを
返します。絶対path、DB row、SQL、実DBのrow countは返さず、filesystem scanやTool実行も
行いません。成功responseは`available=true`、`total`、status別の`summary.available | unavailable |
restricted | blocked`、名前順の`items`を持ちます。providerが利用できない場合はHTTP 200で
`available=false`、空の`items`、一般化したerrorを返し、内部errorを公開しません。

itemのstatusは過去の実装段階を固定表示する値ではなく、startupで確認した現在状態です。
`knowledge_memory`はDB未配置なら`unavailable/database_unavailable`、schemaまたはindex不足なら
`blocked/schema_missing`、backfill中なら`blocked/indexing`、coverage／hash不一致なら
`blocked/integrity_failed`、public projectionがreadyで`knowledge.search`が同じRunnerのmetadataにある場合だけ
`available`と`tool_id=knowledge.search`を返します。authenticated private scopeが未接続の場合は
`safe_operations`を`public_search`だけとし、private照会を暗黙に許可しません。ViewerはCatalogを再計算せず、
Chat／Workerと同じstartup instanceを投影します。

### ProfilePromotion の診断と明示的な再試行

`GET /viewer/memory/profile-promotions` は、L1に永続化されたProfilePromotion jobの全件集計と、
限定したjob詳細を同じresponseで返します。`limit`は既定50、最大200です。`job_count`は詳細ページの
件数であり、全件数ではありません。全件の正本は`state_counts`です。

```json
{
  "status": "needs_review",
  "warnings": [],
  "jobs": [
    {
      "evidence_event_id": "opaque-event-id",
      "session_id": "opaque-session-id",
      "thread_id": 123,
      "state": "failed",
      "attempt_count": 5,
      "lease_expires_at": "2026-08-15T12:00:00Z",
      "next_attempt_at": "2026-08-15T12:00:00Z",
      "last_error": "safe-error-summary",
      "created_at": "2026-08-15T11:00:00Z",
      "updated_at": "2026-08-15T12:00:00Z"
    }
  ],
  "job_count": 1,
  "state_counts": {
    "pending": 0,
    "running": 0,
    "retry_wait": 0,
    "completed": 0,
    "failed": 1
  },
  "failed_count": 1,
  "retryable_failed_count": 1,
  "missing_evidence_failed_count": 0,
  "db_pool_stats": {
    "max": 1,
    "open": 1,
    "in_use": 0,
    "idle": 1,
    "pool_wait_count": 0,
    "pool_wait_duration_ms": 0
  }
}
```

`state_counts`は`pending`、`running`、`retry_wait`、`completed`、`failed`を0件でも含む全row集計です。
`failed_count`はterminal failedの全件、`retryable_failed_count`は`evidence_event_id`に対応する
L1 Raw eventが残るfailed、`missing_evidence_failed_count`は対応eventがないorphan failedです。
`jobs`の`lease_token`は返しません。`db_pool_stats`は`database/sql.DB.Stats()`のsnapshotで、
`pool_wait_count`と`pool_wait_duration_ms`はDB handleの生存期間にわたる累積値です。個別jobや個別requestの
待ち時間ではありません。L1 storeが利用できない場合もHTTP 200で`status=unavailable`と空の集計を返し、
不正な`limit`は400、読み取り失敗は500です。method不一致は、production outer guardのallowlist適用前提では
403としてfail closedになり、guard未適用のhandler単体では405です。

`POST /viewer/memory/profile-promotions/retry` は、明示的に許可されたCMD control scopeだけが呼べる
state-changing APIです。必須headerは次の2つです。

| header | 必須値 |
| --- | --- |
| `X-RenCrow-Client` | `RenCrow_CMD` |
| `X-RenCrow-Interaction-Profile` | `cmd-control` |

このheader pairはserver-sideのinteraction allowlistを選択するscope情報であり、credentialそのものや
TLS／network境界の代替ではありません。

bodyは不要です。認可されたrequestだけが同一SQLite transactionで次を行います。

1. `failed` rowのうち、対応するL1 Raw eventが存在するものだけを`pending`へ戻す。
2. `attempt_count=0`、`lease_token=''`、`lease_expires_at=NULL`、`next_attempt_at=NULL`へ初期化する。
3. `last_error`と`evidence_event_id`、L1 Raw event本文・metadata・stateは保持する。
4. evidenceのないorphan failedは不変のまま`missing_evidence_count`へ数える。
5. 1件以上を戻した場合だけ`memory.profile_promotion_retry_requested`監査eventを同じtransactionで追記する。

responseは次の形です。

```json
{
  "requeued_count": 1,
  "missing_evidence_count": 0
}
```

同じrequestを二回送っても、二回目は既に`pending`へ戻ったrowを再更新せず、`requeued_count=0`となります。
orphanの件数は引き続き報告され、監査eventも二重追加されません。POSTのheader／profile不一致は403、
L1 store unavailableは503、transaction／storage errorは500、成功は200です。production公開経路では
outer `interaction_profile_guard`がhandlerより先にmethod／path allowlistを評価するため、正しい
`RenCrow_CMD`／`cmd-control` headerであっても、このretry pathへのGET／PUTなどallowlist外methodは403として
fail closedになります。handler単体、またはguard未適用の経路ではPOST以外を405として返します。人の返答を待つgrantやqueueを作らず、
requestごとにCOREが直ちに認可、拒否、利用不能、成功を確定します。直接DBを書き換える経路や別embedding／
Recall経路へのfallbackはこのAPIにありません。

### Memory CLI／API対応

`/viewer/memory/*`は既存Viewer／legacy caller向けの互換境界です。現行の`GET /viewer/memory/profile-promotions`と
`POST /viewer/memory/profile-promotions/retry`、既存ViewerのUserMemory list／create／state／forget／supersede、RecallPackは互換維持します。
legacy ChatGPT `import/confirm`はproduction登録から削除済みで、Common Raw owner APIのfallback／拡張に戻しません。
CMDはこのAPIのclientであり、owner serviceではありません。CMDが行うのはcommand構文の解析、認証tokenの
transport、method／path／bodyへの一対一mapping、boundedな単一JSON responseの原文relayだけです。認証user、actor、scope、request ID、
idempotency、state、policy、transaction、dry-run／applyの意味、receiptはすべてCOREが確定します。

UserMemory owner APIのsource実装済み第一段階は、loopbackと`local_agent_ops`のowner-only Bearer tokenを再利用する
`/v1/memory/user`と、Recall／Trace、archive、lifecycle、Parquet owner workflowです。tokenはMemory handlerの起動時に読み、
requestごとには読まず、CORE設定の`user_id`からtrusted user scopeを作ります。source／focused implementation completeですが、
installed binary、production config／DB migration、authenticated live CMD->CORE route、rebuild／restart、Agent／full DB E2Eは未確認です。
`X-RenCrow-Client: RenCrow_CMD`とinteraction profileはcapability selectionであり、credentialの代替ではありません。
認証方式だけが`local_agent_ops`のowner token policyを共有し、Memory APIはAgent OPS route、
Shiro／Worker実行、foreground leaseを使用しません。

| RenCrow_CMD | CORE Public API | profile | CORE所有の操作境界 |
| --- | --- | --- | --- |
| `memory list` | `GET /v1/memory/user` | `cmd-diagnostics` | authenticated ownerのbounded list |
| `memory show --id` | `GET /v1/memory/user/{id}` | `cmd-diagnostics` | owner-scoped exact-ID projection |
| `memory propose` | `POST /v1/memory/user/propose` | `cmd-control` | request ID生成、operator evidenceとcandidateのatomic commit、receipt |
| `memory confirm --id` | `POST /v1/memory/user/{id}/confirm` | `cmd-control` | exact-IDのcandidateからconfirmedへの検証済み遷移 |
| `memory pin --id` | `POST /v1/memory/user/{id}/pin` | `cmd-control` | exact-IDのpinned遷移とreason検証 |
| `memory forget --id` | `POST /v1/memory/user/{id}/forget` | `cmd-control` | owner-scoped無効化とatomic audit receipt |
| `memory supersede --id --replacement-id` | `POST /v1/memory/user/{id}/supersede` | `cmd-control` | replacementのowner／namespace検証とatomic audit receipt |

`{id}`はexact UserMemory IDをURLの一segmentとしてpercent-encodeします。ID中の`/`をroute separatorとして
解釈せず、COREで一度だけdecodeしてexact lookupします。listが受けるqueryは`state`、`include_inactive`、
`limit`だけです。showとwriteはqueryを受けず、caller指定の`user_id`、request／idempotency ID、DB情報、
未知field／queryを400で拒否します。bounded UserMemory projectionは`id`、`type`、`statement`、
`evidence_event_ids`、`confidence`、`sensitivity`、`state`、`persona_scope`、`active`、`superseded_by`、
`created_at`、`updated_at`だけです。

既存Viewer routeはlegacy compatibility routeとして互換維持します。`/v1/memory/user`と、下表のarchive／lifecycle／Parquet routeは
CMD専用owner routeであり、新しいowner receipt契約を満たすsource実装です。CMDのowner操作は上記APIだけを使います。
import status／progress／retry／finalize、Common Raw、LLM residual、typed EndTurnはsource／focused実装対象です。Import finalizeとexact-ID UserMemory confirmは別契約です。
installed binary、production config／DB migration、ChatGPT backfill、live CMD->CORE／Agent E2Eは未完了であり、source実装を配備済みとは扱いません。

| RenCrow_CMD target | CORE Public API target | profile | CORE所有の操作境界 |
| --- | --- | --- | --- |
| `memory recall --query [--limit]` | `GET /v1/memory/user/recall?query=<1..512 rune>&limit=<1..50,default12>` | `cmd-diagnostics` | authenticated ownerの決定論Recall、trace永続化、bounded items＋receipt |
| `memory trace list [--limit]` | `GET /v1/memory/user/traces?limit=<1..100,default20>` | `cmd-diagnostics` | owner_id一致のbounded trace summary＋receipt |
| `memory trace show --id` | `GET /v1/memory/user/traces/{escaped-trace-id}` | `cmd-diagnostics` | owner_id一致のexact-ID bounded trace projection＋receipt |
| `memory archive --id` | `POST /viewer/memory/user/archive` | `cmd-control` | source実装済み。新規exact-ID archive receipt。配備／E2E未確認 |
| `memory lifecycle plan` | `POST /viewer/memory/lifecycle/plan` | `cmd-control` | source実装済み。dry-run／planのみ。配備／E2E未確認 |
| `memory lifecycle run --plan-request-id ... --apply` | `POST /viewer/memory/lifecycle/run` | `cmd-control` | source実装済み。plan receipt／hash再検証後だけapply。配備／E2E未確認 |
| `memory knowledge-backfill [--apply]` | `POST /viewer/memory/knowledge-raw/backfill` | `cmd-control` | 既存`l1_knowledge_item`のCommon Raw backfill。strict JSON `{"apply":bool}`、既定dry-run、`allow_empty=false`のfail-closed coverage gate。IDとhashとcountだけを返す |
| `memory export parquet` | `POST /viewer/memory/export/parquet` | `cmd-control` | source実装済み。configured owner root内のbounded export。配備／E2E未確認 |
| `memory export verify --request-id` | `GET /viewer/memory/export/{escaped-request-id}` | `cmd-diagnostics` | source実装済み。exact targetのmanifest／hash／count verify。配備／E2E未確認 |
| `memory import chatgpt --manifest <file> --artifact <tar> [--apply]` | `POST /v1/memory/import/chatgpt` | `cmd-control` | CORE owner routeとCMD facadeはsource／focused実装済み。COREがwhole-artifact検証、内部batch、Raw／projection、receiptを所有。配備／E2E未確認 |
| `memory import status --export-id` | `GET /v1/memory/import/chatgpt/{percent-escaped-export-id}` | `cmd-diagnostics` | source／focused実装済み。owner-scoped bounded import status／receipt。配備／E2E未確認 |
| `memory import progress --export-id <id>` | `GET /v1/memory/import/chatgpt/{percent-escaped-export-id}/progress` | `cmd-diagnostics` | completed import ledgerの確定Raw／projection件数と、immutable export/job bindingから同一transactionで導出したbounded summaryのProfilePromotion内訳・evidence有無だけを返す。requestごとの全Raw／L3 JSON／全job再走査は行わない |
| `memory import retry-failed --export-id <id>` | `POST /v1/memory/import/chatgpt/retry` | `cmd-control` | 同じexportでevidenceが残るfailed jobだけを再投入し、別export／orphanを変更しない |
| `memory import finalize --export-id <id> [--apply]` | `POST /v1/memory/import/chatgpt/finalize` | `cmd-control` | LLMなしでbinding／hash／counts／job終端を検証し、apply時だけimmutable／idempotent receiptを保存。candidate状態は変えない |

#### ChatGPT Common Raw import owner API

このAPIは旧 `/viewer/memory/import/chatgpt*` を置換する唯一のruntime import routeです。CORE owner route、runtime wiring、CMD facade、
Toolsの旧network subcommand削除、CORE旧Viewer route削除、legacy candidate／confirmed／forgotten／superseded不変性testは
source／focused実装済みですが、production backfillと配備は未完了です。source-focusedな実装が存在しても、installed binary、production config／migration、rebuild／restart、live CMD->CORE／Agent
E2Eを完了とみなしません。CLIの正規構文は次のとおりです。

```text
rencrowctl memory import chatgpt --manifest <file> --artifact <tar> [--apply]
rencrowctl memory import status --export-id <id>
rencrowctl memory import progress --export-id <id>
rencrowctl memory import retry-failed --export-id <id>
rencrowctl memory import finalize --export-id <id> [--apply]
```

`--url`、`--token-file`、`--json`などの標準global optionは、既存memory commandと同じ配置規則に従います。
CMDのfile preflightはflagの有無、型、distinct path、open／stream可能なprivate regular file、multipartを構築できることだけです。
CMDはmanifest／TARをhashまたは解釈せず、schema、batch、checkpoint、request ID、receipt、owner／scope、state、policy、DB意味論を
持ちません。各commandはCOREへ一回だけrequestを送り、responseを表示します。CMDはretry、client-side batch、checkpoint再開を行いません。

新owner routeは次の5つです。

```text
POST /v1/memory/import/chatgpt
GET  /v1/memory/import/chatgpt/{percent-escaped-export-id}
GET  /v1/memory/import/chatgpt/{percent-escaped-export-id}/progress
POST /v1/memory/import/chatgpt/retry
POST /v1/memory/import/chatgpt/finalize
```

`progress`と`finalize`はRaw本文、statement、物理pathを返しません。`progress`のRaw／projection件数は
completed import eventにcommitされたimmutable ledger値を使い、ProfilePromotionは同じexportのeligible evidenceだけを
immutable bindingへ所属させ、既存job stateの変更transactionと同時に更新されるexport単位の派生集計を読みます。
binding件数、state合計、missing job／evidenceとledger `job_count`が不一致な場合はfail closedし、
他exportのjobや過去の非current-branch jobを補正値として混ぜません。`retry`と`finalize`のbodyはそれぞれ
`{"export_id":<id>}`、`{"export_id":<id>,"apply":<bool>}`に限定し、unknown fieldを拒否します。finalizeは取込時に
検証済みのhash bindingと永続済みledger／receiptを再照合し、全object再hashを繰り返しません。旧
`POST /v1/memory/import/chatgpt/confirm`はbulk candidate confirmとしては廃止し、互換応答が必要な場合もcandidateを変更せず
明示的にretiredを返します。

`progress`のread-only queryは、writeを1接続へ直列化する通常L1 poolとは分離したSQLite
`query_only` connectionで同一WAL snapshotを読みます。background writeのpool待ちをAPI timeoutへ持ち込まず、
このconnectionによる状態変更は拒否します。`retry`／`finalize --apply`は引き続きowner write transactionだけを使います。

uploadはauthenticated loopbackの一つのmultipart requestだけを受けます。`X-RenCrow-Client: RenCrow_CMD`と
`X-RenCrow-Interaction-Profile: cmd-control`を既存のcredential／scope guardと組み合わせ、profile headerをcredentialの代替にしません。
multipartのpartは次の順序、name、media typeに固定します。

| 順序 | name | media type | 内容 |
| --- | --- | --- | --- |
| 1 | `apply` | `text/plain` | ASCII bool `true`／`false`。CLIの`--apply`省略時はCMDが`false`を送る |
| 2 | `manifest` | `application/json` | manifest JSON file |
| 3 | `artifact` | `application/x-tar` | uncompressed deterministic TAR file |

unknown／duplicate／missing part、順序違反、name／media type不一致、part内部のEOF不備、multipart trailing bytes／part、
malformed boundaryはCOREが400で拒否します。uploadはquery/bodyのcaller fieldsを持たず、user、scope、export／request／idempotency ID、
DB／path、batch size／index、checkpoint、owner、policyは送信しません。request IDとidempotency identityはCOREが生成します。

statusは`cmd-diagnostics`のGET一回で、path segmentはpercent-escaped export IDを一つだけ受けます。decoded後の空、malformed escape、
encoded slash／backslash、追加segmentは拒否します。statusはowner／scope一致するexportだけを返し、別ownerやunknown exportを公開しません。
confirmは`cmd-control`のJSON POST一回で、bodyはJSON object`{"export_id":<id>,"reason":<reason>,"apply":<bool>}`だけです。
`reason`はdry-runでも必須、`apply`既定値はfalseで、callerはrequest ID、user、scope、DB、candidate IDを別途指定しません。

COREは認証とscope検証が成功した後だけ、configured absolute `storage.memory.raw_source_dir`配下のprivate stagingへ受け入れます。
manifestは64 MiB以下、artifact TARは64 GiB以下です。COREはfirst Raw／domain commit前にmanifest、TAR、EOF／trailing、records、
source-files index、全objects、content hash／counts、schema／converter version、canonical manifest hash、source reconstructionを
全件検証します。staging／tempは0700 directory／0600 file、同一filesystemのatomic finalize、failure cleanup／quarantineを使い、
repository／runtime home／OS temp／backup媒体へfallbackしません。crash／orphan stagingはactiveとせず、安全にreconcile／removeします。
partial commitで閉じたrequestは`blocked` receiptで終端し、retryは同じartifactを一つのupload requestで再送して収束させます。

physical namespaceは`raw_source_dir/.chatgpt-import-staging/<CORE-generated-stage-id>/`だけです。stage IDは
pathless ASCII alphanumeric／hyphen、request directoryは0700、`manifest.json`／`artifact.tar`は0600とし、unknown entry、
symlink、hardlink、device、root escapeを拒否します。stage copyは成功／拒否／`blocked`応答後に削除し、resumeや
backupの正本にしません。process中断で残ったsafe stageは次回route開始前に削除し、unsafe／unknown entryは
自動削除せずstartup errorで閉じます。startup順はstage reconcile -> Common Raw object reconcile -> active import ledger
reconcileで、全stepの成功前にrouteをlisten-readyにしません。

COREの内部batchはsource record最大100件、decoded Common Raw payload合計64 MiB以下、stable artifact order、CORE採番のbatch indexです。
CMD／clientがbatch size、index、checkpointを決めることはありません。全元fileはordered 32 MiB chunk-backed synthetic Raw source record（64 MiB境界を守るため必要数のchunk-ref record）として
表現し、message recordはfull canonical Raw payloadを持ちます。Common Raw manifest／record／stateとimmutable `pending` projection receiptを
先にcommitし、L3／jobs／completed receiptを一つのtransactionで確定します。failed／blocked receiptは追記し、同じartifact bindingのreplayは
idempotentに収束します。assetsはchunk ingestとsource reconstructionが成功するまでmigratedと数えません。

response／read statusはRaw本文、path、statementを返さず、export／binding hashとversion、source／message／file／chunk／object counts、
batch／Raw／projection／job aggregate、terminal `import_state`、warnings、audit reference、idempotent replayだけを返します。active requestの
過渡値は`validating | committing`ですが、人の返答を待つstatusやgrant／queueは作りません。terminal stateは
`completed | rejected | blocked`です。

confirmはauthenticated owner-scoped candidate operationだけを対象とします。dry-runはread-only、applyはprojectionがcompletedでfailedがなく、
同じowner／scopeであることをCOREが確認した後だけ、candidate stateとaudit receiptを一つのatomic transactionで更新します。hardcoded
`user:ren`をowner判定に使わず、別ownerのstatus／candidateを返しません。

| HTTP | 条件／意味 |
| --- | --- |
| 200 | typed receiptまたはidempotent replay |
| 400 | invalid flags、multipart、schema、count、unknown／duplicate／missing part |
| 401 | authentication missing／invalid |
| 403 | profile、owner、scope拒否 |
| 404 | status対象export unknown（別ownerも同じ） |
| 409 | source changed、artifact binding／idempotency conflict |
| 413 | manifest／artifact sizeまたはcount bound超過 |
| 422 | artifact semantic、record／object hash、source reconstruction不一致 |
| 503 | configured raw root／storage unavailable（`blocked`） |
| 500 | storage／transaction／receipt failure（successを主張しない） |

旧Tools `import/confirm`とCORE direct-L3 `/viewer/...`はproduction経路から削除済みです。新owner APIはこれらへfallbackせず、
旧routeへネットワークimport、request ID、hash／checkpoint責務を戻しません。

#### Recall／Trace owner APIの確定契約

上表の3 read routeはlegacy `/viewer/memory/*`／`/viewer/recall/*`の互換routeを再利用しません。COREのconfigured authenticated
userとtrusted `ToolExecutionScope`／`DataScopeUser`からownerを決定し、callerの`user_id`、session ID、request ID、DB情報を
受け付けません。`query`は1〜512 rune、`limit`はrecall 1〜50（既定12）、trace list 1〜100（既定20）です。空query、未知／重複
query、範囲外limitは400、認証なしは401、profile／scope拒否は403、owner不明legacy rowまたは別ownerのexact traceは404、owner store
不可用は503／`blocked`とします。source read／index failureを空の200へ変換しません。

Recallは最大100件をscanするCORE決定論rankingです。active、非superseded、非decayed、`confirmed | pinned`、normal sensitivity、
scope適合だけを選び、candidate、sensitive、inactive、superseded、decayed、budget dropはstatus／reason付きtrace itemへ残します。
LLMは実行しません。responseの`items`は`UserMemoryOwnerView`のbounded fields＋`score`、`trace`は`id`、`status`、
`query_text_redacted`、`total_candidates`、`selected_count`、`created_at`、receiptのoperationは`recall`、`audit_reference`はtrace IDです。

Trace list itemは`id`、`status`、`route`、`persona`、`total_candidates`、`injected_count`、`total_injected_tokens`、`created_at`、
receipt operationは`trace_list`です。Trace showはsummary fields、`query_text_redacted`、bounded items（`item_id`、`memory_id`、
`kind`、`source_id`／`source_type`、optional `summary`、`score`、`status`、`reason`、`prompt_section`、`token_count`、`memory_state`、
`sensitivity`）を返します。sensitive、short_context、full transcriptはsummaryを空にし、その他summaryも240 rune以内です。receipt
operationは`trace_show`です。

`recall_trace.owner_id`とowner indexを追加し、新規Agent／owner traceではowner_idを必須にします。`owner_id=''`のlegacy rowはowner
APIへ公開しません。BeginTurn traceもowner_idとUserMemoryの選択／除外itemを保持します。TraceはStart時`started`、items／injection成功後に
`completed`または`partial`へFinishし、途中失敗をcompletedと主張しません。CMDは上記GETをBearer＋`cmd-diagnostics`で一回だけ発行し、
ranking、scope、state、receiptを保持または決定しません。

target CLIは`user_id`、request／idempotency ID、DB path、table／column、SQL、physical output pathを送らず、
authenticated user、actor、scope、request ID、idempotency keyはserver contextとCORE APIから決定します。
legacy callerが`user_id`を送った場合もauthenticated userと一致しなければ403です。
targetのreadは認証済みownerが明示要求したbounded UserMemory projectionと状態だけを返し、Raw payload、全会話
transcript、sensitive valueをstdoutへ出さず、通常logにはUserMemory statementとRecall `query`も残しません。state mutationの単一writeは
verb自体を明示操作とし、bulk mutation／lifecycle／importはplan／dry-runを既定として`--apply`時だけ変更します。
Parquet exportはsource DBを変更しない明示artifact生成commandです。loopbackとowner Bearer scopeを要求し、POSTは`cmd-control`で
空bodyまたは`{}`だけ（queryなし）を受け付け、GETは`cmd-diagnostics`で一つのescaped export IDだけ（query／bodyなし）を受け付けます。
GETのtargetはdecoded後に空でない一segmentでなければならず、malformed escape、encoded slash／backslash、additional／trailing segmentは拒否します。
COREはcurrent request IDを生成し、`storage.memory.cold_export_dir`をrootに、Archive SQLiteの一つのread snapshotから5つのdeterministic
relative Parquet fileとrows／bytes／SHA-256 manifestを生成し、private staging、atomic finalize、receipt、cleanup／quarantine、exact target
verify／replay artifact再検証を行います。empty rootは503／`blocked`、非空で不正なrootはstartup config errorです。responseはtyped relative metadataだけで、
caller path／user／current request ID／DB情報は含みません。
CMDは各operationをCORE APIへ一回だけ送信し、receipt／hashを計算せず、DBにも接続しません。全effectful operationはCOREが生成またはAPI契約で
検証する`request_id`を持ち、memory stateを変えるoperationはCOREが`reason`を必須検証します。許可待ちqueueや人の返答待ちstatusは作りません。

新routeの共通response／receiptには`request_id`、`operation`、`status`、`owner_route`、`policy_revision`、
`idempotency_key`、`idempotent_replay`、input／output counts、`completed_at`、warnings、`audit_reference`を含めます。
`status`は`completed`、`rejected`、`blocked`のいずれかでrequestを閉じ、既存status／retry endpointのresponse形は
互換維持します。HTTP結果は次の境界です。

| HTTP | 意味 |
| --- | --- |
| 200 | typed `completed`、または同じrequestの`idempotent_replay=true` |
| 400 | 引数、schema、reason、query、plan bindingが不正（`rejected`） |
| 401 | authenticated owner contextがない（`rejected`） |
| 403 | client／profile不一致、scope拒否、legacy `user_id`不一致（`rejected`） |
| 404 | bounded exact-ID、trace、exportが存在しない（`rejected`） |
| 409 | state、source／artifact hash、idempotency payloadの衝突（`rejected`） |
| 503 | owner store、Common Raw、必要なmodule routeが利用不能（`blocked`） |
| 500 | transaction／outbox／receiptの永続化失敗。`completed`を主張せず、可能ならtyped `blocked` errorを返す |

ProfileExtractor／ThreadSummarizerのLLM応答はCOREの正規RenCrow_LLM routeを通るbounded semantic residualだけです。
COREがL1 Raw evidence、既存UserMemoryのbounded projection、schema、scopeを準備し、出力のtype／enum、長さ、confidence、
evidence binding、dedupe、sensitivityを検証してからcandidate／summaryを保存します。invalid output、assistant-only
evidence、synthetic evidenceは保存せず、retry可能性をreceiptへ記録します。EndTurnのconversation responseとmemory
persistence、trace、archive followerは別typed outcomeとし、部分成功をcompletedへ丸めません。
ThreadSummarizerは一threadにつき一回のLLM requestとし、64KiB以下のexact JSON、summary 1〜1024 rune、
3〜5個のunique keyword（各1〜64 rune）をCOREが検証します。CORE由来のevidence SHA-256、roles、provider、
`llm | deterministic_fallback | legacy_unverified`のgeneration modeと固定failure codeをarchive receiptへ保存し、
raw provider error、invalid LLM output、物理pathをAPI responseやreceiptへ含めません。
Canonical AgentのEndTurnはroot `job_id`を維持したtyped internal requestであり、L1 SQLiteの同一transactionへ
Recall trace、User／Agent 2 message、ProfilePromotion job、event log、turn receipt、required follower outboxを保存します。
resultは`status=completed | partial | failed`、turn／trace／2 message／receipt ID、follower status、固定error codeだけを
返します。同一turn＋同一payload hashはidempotent replay、異なるhashは`conflict`です。Redis／archive／VectorDBは
outbox followerとしてcommit直後とstartupに再生し、外部follower failureを成功へ丸めず`partial`として保持します。
新規作成は`candidate`固定で、`confirmed`／`pinned`はexact-ID state APIだけが同一transactionのaudit receiptとともに
設定します。Agent readもauthenticated userと`DataScopeUser`、active、`confirmed | pinned`、non-sensitiveをserver側で
検証し、source failureを空の200へ変換せずRecall traceへ記録します。

### 認証済みAgent OPS

`POST /v1/agent/ops`は通常Chatとは別のOperational APIです。`local_agent_ops.enabled=true`でのみ存在し、
接続元IPがloopbackでないrequestは404、Bearer token不一致は401、client/profile不一致は403で拒否します。
認証、header、body、request IDの検証に成功したrequestだけがWorker foreground leaseを取得します。
leaseはShiro実行の成功、失敗、client cancelのいずれでも解放し、並行requestでは最後の1件が終わるまで保持します。
この間、IdleChatのbackground Worker生成はOPSより先に再queueしません。
次のheaderを各1個だけ必須とします。

| header | 値 |
| --- | --- |
| `Authorization` | `Bearer <owner-only token fileの値>` |
| `X-Request-ID` | 1から128 bytesのsafe opaque ID |
| `X-RenCrow-Client` | `RenCrow_CMD` |
| `X-RenCrow-Interaction-Profile` | `agent-ops` |
| `Content-Type` | `application/json` |

queryは受けず、request bodyは最大64 KiBのstrict JSONです。唯一のfield `message`は空白でなく、
encoded bytesで最大32 KiBです。

```json
{"message":"全DBのOwner routeを確認して"}
```

COREはtokenに束縛したserver設定の`user_id`からHTTP user scopeを作り、Shiro／`worker`／`ops`の
child scopeへ導出して実在Shiro Agentへ渡します。clientはuser、Agent、role、scope、route、model、job IDを
指定できません。認証済み`X-Request-ID`はShiro child scopeの`request_id`として保持し、task／responseの
`job_id`は別に生成します。同じrequest IDとcanonical payloadの再送は下流child request／idempotency identityを
再現できますが、job IDは実行ごとに別です。成功時は次の6 fieldだけをHTTP 200で返します。

```json
{
  "request_id": "ops-opaque",
  "job_id": "job-opaque",
  "agent_id": "shiro",
  "role": "worker",
  "route": "OPS",
  "output": "実行結果"
}
```

不正JSON／未知field／trailing JSON／request ID不正は400、非JSONは415、上限超過は413、
Shiro実行失敗または空出力は500です。error responseはsafe codeだけを返し、token、入力本文、内部errorを
含めません。`POST /viewer/send`の観測用`user_id`はこの認証契約の代替になりません。

### Worker data.write receipt

Worker専用Tool `data.write` の成功結果は、Owner routeが確定したreceiptです。`audit_ref`はOwnerが生成した
正規record／artifact IDであり、後続の`data.recall` queryに使う唯一のhandoff tokenです。`request_id`は実行相関、
`idempotency_key`は再実行判定の値として内部receipt／auditに保持しますが、model向けJSONからは除外します。
後続のOwner record照会に`request_id`または`idempotency_key`を使いません。

receiptのJSON宣言順は、モデルがhandoff値を先に認識できるよう、`owner`、`owner_route`、`audit_ref`、
identity／evidence fieldsの順です。`request_id`と`idempotency_key`はGo内部receipt fieldとして保持しますが、
model向けJSONには現れません。field名、schema、Owner logicは変更しません。

`investment/ensure_portfolio_initialized`の成功receiptは`audit_ref=sha256:<64 hex>`を返します。後続の
`data.recall investment/portfolio_snapshot`は、その完全一致する`audit_ref`をwrite-to-read handoffの唯一の
queryとして使えます。`query=current`はhandoffに依存しない独立したcurrent readとして引き続き使用できます。

### 人物関連作品Viewer projection

`GET /viewer/movie-catalog/person-related/people`は、映画カタログで`known`または`like`を
明示設定した人物だけを名前順で返します。`limit`は既定100、1以上1000以下です。候補取得は
assessment用named indexから始め、未評価人物、legacy favorite、DB path、内部errorを返しません。
1000件上限は無制限一覧を避けながら現行deploymentの評価済み人物をViewerで選べるようにする
bounded contractです。

`GET /viewer/movie-catalog/person-related`は、必須`person_id`、必須`category`、任意`limit`を受けます。
`category`は`movie | drama | award | music | anime | novel | manga`、`limit`は既定20、1以上50以下です。
外部収集やTool実行は開始せず、startup時に固定したread-only lookupから、検証済み名称、原名、関係、
sourceと`summary_state | summary_ja`、`summary_coverage`だけを返します。日本語名の根拠がない作品へ
邦題を生成せず、説明がない場合は`summary_state=unavailable`として表示します。

### Capability Applyとstatus

`GET /viewer/capabilities`は、起動中のproduction Worker `RunnerV2`の`ListTools`を同じ
観測時点で一度だけ読み取り、`Runtime Capability Snapshot`として投影します。成功responseは
次の3項目だけを持ちます。

```json
{
  "available": true,
  "total": 1,
  "items": [
    {
      "tool_id": "person_related_catalog.lookup",
      "version": "1.0.0",
      "category": "query",
      "origin": "core_runtime",
      "description": "人物関連作品catalogの読み取り",
      "available": true
    }
  ]
}
```

`items`は`tool_id`を主キーに名前順で安定ソートし、各itemは`tool_id`、`version`、`category`、
`origin`、`description`、runtime時点の`available`だけを返します。Tool parameter schema、
filesystem path、credential／secret、内部errorは返しません。Worker Runnerが未接続、または
`ListTools`に失敗した場合もHTTP 200で`available=false`、`total=0`、空の`items`を返します。
このGETは認識用の読み取りprojectionであり、Tool登録、Tool実行、権限付与、Skill本文の任意path
読込、MCP接続、再起動を行いません。Skill／MCPを含む将来のSnapshot拡張とCapability Applyは
別契約として扱います。

Agentが作成したToolはdynamic registryへ登録後、許可済みWorker経路で実行・一覧取得できる場合が
あります。しかし稼働中AgentのStable RuntimeContextはstartup snapshotを保持するため、dynamic
registryへの反映だけでこのAPIのSnapshotまたは全Agentの認識が更新されたとは扱いません。Skillの
catalogとMCPの接続／一覧もstartup固定です。完全反映は以下のapply契約を使います。

`POST /viewer/capabilities/apply`のrequestは次を必須とします。

```json
{
  "request_id": "capreq_<opaque>",
  "idempotency_key": "capapply_<opaque>",
  "capability_expectations": [
    {
      "kind": "tool|skill|mcp",
      "canonical_name": "tool-or-skill-or-mcp-name",
      "source_revision": "source-revision",
      "source_hash": "sha256"
    }
  ],
  "current_snapshot_revision": "snapshot-revision",
  "current_snapshot_hash": "sha256"
}
```

`request_id`と`idempotency_key`はopaque値です。requesterとauthenticated scopeはbodyの自己申告を
受け取らず、COREが認証済みHTTP／Agent execution contextから確定してreceiptへ記録します。COREは
requestのscope、source/list、production wiring、Worker policy、deployment availability、current
Snapshot revision／hashを同期検証します。
requestへ任意unit、path、binary、command、PORT、provider、別Toolを指定するfieldはありません。
Agentの発話やTool／Skill本文は認証scopeの代用にならず、Model／provider／Execution Roleはrequestの
actorになりません。

検証後のresponseとreceiptは、同期decisionと後段phaseを別fieldで表します。

| decision | phase | 意味 |
| --- | --- | --- |
| `execute` | `validated` | source、policy、scope、requestの整合性を確認済み |
| `execute` | `completed` | 期待する全capabilityが現行Snapshotへkind／name／source revision／hash一致で存在し、再起動不要 |
| `execute` | `restart_scheduled`以降 | receiptをatomicに永続化し、応答返却後のCORE再起動と検証を予約 |
| `rejected` | なし | request不正、stale Snapshot、policy不適合。人待ちへ遷移しない |
| `blocked` | なし | source、依存、scheduler等が利用不能。人待ちや自動fallbackを作らない |

再起動が必要なaccepted requestは、次のphaseだけを順に通ります。

```text
validated -> restart_scheduled -> stopping -> starting -> verifying -> completed | failed
```

COREが現在の応答を返す前にreceiptと`restart_scheduled`をatomicに確定し、CORE distributionの
native Go supervisor worker／subcommandへrequest_idを渡します。supervisorは既知の
`task_id=rencrow-core`だけを対象に、既存のReservedPort置換起動契約で旧instanceの停止、listener解放、
同じPORTでの起動を行います。再起動後は新instanceのidentity、`/health/live`、`/health/ready`、
期待capabilityのkind／canonical name／source revision／source hash、receiptに固定した最終Snapshot
revision／hashを別processから検証します。終了したCORE自身が成功を自己証明してはいけません。

同じ`idempotency_key`と同じcanonical requestは既存receiptを返し、二重再起動しません。異なるpayload
で同じkeyを使う場合はconflictです。再接続後は`GET /viewer/capabilities/apply/{request_id}`または
CLI statusでreceiptと新instanceの観測を再結合します。receiptが存在しないrequestは再起動を推測せず
`CAPABILITY_REQUEST_NOT_FOUND`です。

HTTPと安定error codeは次の通りです。

| HTTP | error／decision | 条件 |
| --- | --- | --- |
| 400 | `CAPABILITY_INVALID_REQUEST`／`rejected` | 必須field、kind、canonical name、hash、scope形式の不正 |
| 403 | `CAPABILITY_POLICY_REJECTED`／`rejected` | CORE／module／Worker policyの積集合が拒否 |
| 409 | `CAPABILITY_SNAPSHOT_MISMATCH`／`rejected`、`CAPABILITY_IDEMPOTENCY_CONFLICT` | current Snapshotまたはidempotency payloadの不一致 |
| 404 | `CAPABILITY_REQUEST_NOT_FOUND` | status対象のreceiptが存在しない |
| 503 | `CAPABILITY_SOURCE_MISSING`／`CAPABILITY_DEPENDENCY_BLOCKED`／`CAPABILITY_SCHEDULE_FAILED`／`blocked` | source、依存、応答後supervisorへの引継ぎが利用不能 |
| 200 | `completed`またはstatus取得 | 再起動不要、またはreceiptの最終状態を返す |
| 202 | `restart_scheduled`等 | accepted requestをreceiptへ確定し、後段検証中 |

accepted後の後段失敗はstatus responseを成功へ丸めず、receiptの`phase=failed`と次のerror codeで
返します。応答後supervisorへの引継ぎ前に失敗した`CAPABILITY_SCHEDULE_FAILED`も同じ扱いです。
`PORT_OWNERSHIP_CONFLICT`は所有不明または別TaskのPORTを停止しない場合、
`PORT_RELEASE_TIMEOUT`は同一Taskの解放確認失敗、`TASK_START_FAILED`は新instance起動失敗、
`CAPABILITY_READINESS_TIMEOUT`はreadiness期限超過、`CAPABILITY_SNAPSHOT_MISMATCH`は期待Snapshotの
membershipまたはhash不一致です。いずれも別PORT、別unit、別provider、未観測能力へfallbackしません。

RenCrow_CMDの正本CLIは次です。

```text
rencrowctl capability apply --request-id <id> --idempotency-key <key> \
  --expect <kind>:<canonical-name>:<source-revision>:<source-sha256> \
  --snapshot-revision <revision> --snapshot-hash <sha256>
rencrowctl capability status <request_id>
```

CLIはCORE Public APIのfacadeであり、snapshot計算、policy判定、receipt管理、systemd／service／
launchd操作を直接行いません。stdoutはmachine-readableなresponse／receipt、stderrは運用logに分離し、
COREの`request_id`、`trace_id`、phase、error codeを利用者が追跡できる形で表示します。Agent-originated
requestもCORE内の認証済みscopeを通る同じAPI／receipt経路を使い、Agentが任意commandを直接実行しません。

### Trade status

`GET /viewer/trade/status`はCOREからRenCrow_TRADEの正規private APIへ接続した結果だけを返します。
未設定時は`bridge_status=disabled`、接続・認証・contract検証失敗時は
`bridge_status=unavailable`です。成功時も現在のExecution Mode、Kill Switch、Broker、Ledger、
Market Data、policy revisionを区別して表示します。COREはtoken、TRADE base URL、内部error本文を
応答へ含めません。TRADEが100万円Simulatorを設定した場合は`portfolio.status`と検証済みの
cash、position、NAV snapshotも含みます。このrouteは状態変更、注文、Paper／LIVE実行を提供しません。

`POST /viewer/trade/policy/evaluate`は`request_id`、`capability`、
`request_scope_revision`、`request_allowed`を受けます。COREはactive Global Policy snapshotから
Global capabilityとdeployment制限を解決し、認証済みprivate routeでTRADEへ評価を依頼します。
未知field、欠落値、inactive Global Policy、TRADE不通、contract不一致、証跡保存失敗はfail closedです。
結果は共通Policy Decision storeへappendされますが、`authorizes_execution=false`であり、
このAPIは外部I/O、Portfolio更新、Proposal、Intent、追加の人間判断artifact、Orderを一切作りません。

`POST /viewer/trade/risk-preview`は`request_id`、明示boolの`request_allowed`、
`risk-preview-plan/v1`を受けます。COREは未知fieldと1 MiB超過を拒否し、planのcanonical JSON
SHA-256をrequest scopeへ束縛して`portfolio_risk_preview`をactive Global Policyで評価します。
Policyが`allowed`で、planの`policy_revision`がactive Bundle revisionと一致する場合だけ、
認証済みTRADE private APIを呼びます。responseはPolicy Decision evidenceとRisk Preview decisionを
返しますが、`authorizes_execution=false`、`mutates_portfolio=false`です。Portfolio未設定／破損、
policy block、stale revision、TRADE contract不一致ではfail closedにし、別のPortfolioや旧workflowへ
fallbackしません。

`POST /viewer/trade/simulation-commit`は明示bool `allow_commit=true`、idempotency key、直前Previewの
Portfolio event count/hash、input snapshot SHA-256、同じplanを必須にします。COREとTRADEの両方で
`portfolio_simulation_commit` Policyを評価し、active Bundle revision、request scope、current snapshot、
再計算したRisk Previewが全て一致して`pass`の場合だけ`SIMULATION`台帳へappendします。成功しても
`authorizes_external_execution=false`であり、Broker、Paper、LIVE、注文artifactを作りません。

`POST /viewer/trade/shadow/observations`は明示bool `allow_record=true`を必須にします。選択、除外、
見送り、保有継続、撤退判断を同じschemaで受け取り、context snapshot SHA-256とOutcome Label Contract
SHA-256へPolicy request scopeを固定します。成功responseは`environment=SHADOW`、
`authorizes_external_execution=false`、`portfolio_mutated=false`、`knowledge_promoted=false`です。
既存判断の更新・削除、Outcome付与、採点、promotionはこのv1 routeに含めません。

`POST /viewer/trade/shadow/outcomes`は`allow_record=true`、既存`decision_id`、Outcome label、
Outcome observed time、Outcome data hash、元観測と一致する採点契約hashを必須にします。同じdecisionへ
二つ目のOutcomeは拒否し、同じpayloadの再送だけを冪等に処理します。成功responseも
`GET /viewer/trade/shadow/outcomes/report`は独立readなら`study_id`、write→read handoffならreceipt由来の
exact `event_id=shadow-event/sha256:<64 lowercase hex>`のどちらか一つだけ受け取り、event lookup時は
返却されたstudy IDとOwnerEvidence provenanceをevent IDへexact bindingしたうえでhash-chainを再検証して
Outcome待ち、label別件数、return／benchmark／excess returnを再計算します。これは読み取り専用で、
`review_required`を返しても採点完了、knowledge promotion、Portfolio更新、実行許可を意味しません。
`environment=SHADOW`、`authorizes_external_execution=false`、`portfolio_mutated=false`、
`knowledge_promoted=false`を返します。

`POST /viewer/trade/shadow/outcomes/reviews`は明示bool `allow_record=true`、Outcome reportのcanonical
SHA-256、reportのlatest event hash、reviewer、decision、reason、evidenceを必須にします。TRADEは
reportを再計算してstale／改ざんを拒否し、Outcome台帳とは分離したreview hash-chainへ冪等追記します。
review成功後も`authorizes_external_execution=false`、`portfolio_mutated=false`、
`knowledge_promoted=false`であり、reviewはpromotionや注文の実行条件ではありません。

`GET /viewer/trade/shadow/outcomes/reviews/report`はOutcome ledgerとreview ledgerを再検証し、
`pending_review`、`review_required`、`independently_reviewed`の状態を返します。独立review済みでも
学習昇格、Portfolio変更、Broker／Paper／LIVE実行の許可にはなりません。

### Game Launch（マルチペルソナ WP5）

`POST /viewer/games/launch` は、ペルソナが「遊びたい時に自分で起動する」
ためのCORE側起動口です。CORE Public API、Agent起動判断、candidate memory、
autoplay設定の正本は本書と`docs/05_設定リファレンス.md`です。
タイトル固有の人数制約、world、rules、game loop、Observer contractは
RenCrow_GAMES側の仕様を参照します。

Game lifecycleの向きは次で固定します。

```text
CORE Agent
  -> POST /viewer/games/launch
  -> RenCrow_GAMES Observer / title process
  -> POST /viewer/games/decision
  -> CORE Agent decision
  -> GAMES validation / game execution
  -> ObserverFrame / POST /viewer/games/result
  -> CORE observer proxy / candidate memory
  -> user
```

ゲーム起動とターン判断の主体はCOREのAgent、ゲーム状態と実行の正本はGAMESです。
LLM、Model、provider、Agent Runtime、Execution RoleはAgentの推論・実行機構であり、
プレイヤーそのものではありません。
ユーザー向けの実行表示はGAMES Observerを
`/viewer/games/observer`と`/viewer/games/observer-api/*`でsame-origin proxyします。

- Request: `{game_id, personas[], turns?, mode?, reason?}`。
  CORE 側の検証は `game_id` 必須のみ。タイトル・人数の capability 検証は
  observer 側 launcher が正本であり、その 400 をそのまま透過する
  （二重管理によるドリフト防止）。
- 共有 observer の `POST /games/launch` へ転送する（base URL は observer
  proxy と同じ解決順: `games.observer_url` > 既定`http://127.0.0.1:18796`）。
  Linux 常駐でもlive `core.yaml`の`games.observer_url`を使用する。
- `reason`（動機）があれば起動成功時に**参加ペルソナ全員**の candidate
  イベントとして記録する（i 番目のペルソナは Turn=-(i+1)。言い出しっぺは
  `play_game`、誘われた側は `invited_to_play` + `invited_by`）。
  observer は `launching` を楽観返却するため、spawn がその後失敗しても
  動機イベントは残る（「遊ぼうとした」経験として扱う）。candidate store
  未設定時は記録されず `motive_recorded=false` になる。
  記録失敗は起動失敗にしない。
- Response: `{ok, game_id, session_id, status, motive_recorded}`。
  upstream 到達不能は 503、upstream エラーは status code を透過する。

### Game Agent decision

`POST /viewer/games/decision`は、Agent所有sessionでGAMESが作成した
`ObservationRequest`を対象のCORE Agentへ渡すturn判断口です。

- Requestは`game_id`、`session_id`、非負の`turn`、`persona`、
  `observation`、`available_actions`、`request`を持つ。
- COREは`persona`から実Agentを解決し、Agent固有のPersona／Execution Role／
  推論Target経路でstrict JSONの`BrainDecision`を生成する。
- Game turnのLLM requestは`ResponseFormatJSONObject`を指定し、non-streamで実行する。
  RenCrow LLM Runtimeが`response_format.type=json_object`に基づいてModel固有の外装を
  正規化し、COREはコードフェンスを受理せずstrict JSONだけをdomain検証する。
- Responseは`agent_id`を必須とし、`agent_id`と`persona`はrequestの`persona`に
  一致しなければならない。
- GAMESは`intent`と`action_plan[].action`を`available_actions`に対して再検証して
  からExecutorへ渡す。COREはworld stateを直接変更しない。
- Agentが利用不能、応答が不正、またはCOREへ到達不能の場合はturnを失敗させる。
  `RuleBasedBrain`や`DummyBrain`へfallbackしてAgent判断として記録しない。

`GET /viewer/games/status`はこの経路が配線済みのとき
`decision_mode: "agent"`と`/viewer/games/decision`を返す。

### Game Bridge status／candidate event契約

`GET /viewer/games/status`の`supported_games`は、Agent decision E2Eへ移行済みの
titleだけを示します。現在は`nethack`です。GAMESのlocal simulation対応title一覧とは
別のcapabilityです。
autoplayの既定ロースターは`mio`、`shiro`、`kuro`、`midori`の4人です。

`POST /viewer/games/result`で保存するcandidate eventの重複排除キーは
`(game_id, session_id, turn, persona)`です。event IDは次の形式です。

```text
game:<game_id>:<session_id>:turn_<turn>:persona_<persona>
```

例:

```text
game:survival_garden:sg_shared_turn:turn_7:persona_mio
game:survival_garden:sg_shared_turn:turn_7:persona_shiro
```

同じturnでもpersonaが異なれば別eventとして全件保存します。同じ4要素を持つ
同一personaのretryは既存eventを返し、新しい行を追加しません。
candidate memory IDはevent IDへ`:candidate`を付けた値です。

実際に有効な endpoint は build と config に依存します。process supervisorは`/health/live`だけを再起動判定に使います。利用者向け機能の確認では`/health`と`/viewer/status`も確認し、featureがunavailable/degradedの場合は成功として扱わないでください。

LINE WebhookはPOSTだけを受け付けます。Tailscale公開guardはtailscaledが`Tailscale-Funnel-Request`を付けたinternet trafficでは`POST /webhook/line`だけを追加許可し、Viewer／Debug／Ops pathを404にします。tailnet内のServe trafficはViewer系allowlistを維持します。`GET /webhook/line`の404、署名なしPOSTの401は故障判定に使いません。LINE Developersへ登録するendpointはdeployment時点の公開hostを確認して`https://<current-host>/webhook/line`とします。外部到達確認ではMessaging APIのWebhook testを使い、署名検証済みeventが200になることを確認します。

`POST /internal/assistant/notifications/line`はloopback remote addressだけを許可します。
`assistant-core` profileと`RenCrow_ASSISTANT` client headerの組み合わせが必要で、Tailscale、
LAN、Funnel向けAPIではありません。requestは`delivery_id`、`trace_id`、ASSISTANTの
`user_id`、`title`、`body`を必須とします。`200`は送信済み、`409`は送信結果不明、
`503`はchannel／credential／送信先の準備不足、`502`は外部送信失敗を表します。
remote配置へ拡張する場合は相互認証を別途仕様化するまでこのendpointを公開しません。

Scheduler run logの`status`は`completed`、`failed`に加えて`deferred`を返す場合があります。`deferred`はGPUなどの実行資源が使用中で、ジョブの`next_run_at`を近い再試行時刻へ更新した状態です。

## Chat recipient contract

Viewer 通常 chat の宛先は次の値を使用します。

```text
mio | shiro | kuro | midori
```

recipientは物理ModelやExecution Roleの直接選択ではありません。COREがroute、Agent、
Execution Roleを確定した後、RenCrow_LLM Gatewayへ論理execution aliasを送ります。

### execution alias契約

| alias | 現在のAgent／Role binding | 備考 |
| --- | --- | --- |
| `mio` | Mio／Chat | Agent IDやModel名として再解釈しない |
| `shiro` | Shiro／ChatWorker | Shiroの通常CHAT |
| `worker` | Shiro／Worker | 主にShiroのOPS。内部background利用時もCOREがAgent contextを保持する |
| `midori` | Midori／Wild | Agent IDやModel名として再解釈しない |
| `kuro` | Kuro／Heavy | Codex、Heavy、Model名そのものではない |

これらはCOREからRenCrow_LLMへ送るopaqueなwire keyであり、Agent ID、Execution Role ID、
物理Target名のいずれか一つを表す汎用fieldではありません。PORTAL、CMD、ASSISTANTはaliasを
選択せず、COREへrecipient Agentを送ります。Agent／Role bindingが同じままTargetだけを
変更する場合はaliasを変更しません。COREはGateway requestの`rencrow` metadataへ
`agent_id`、`execution_role`、`execution_alias`を明示し、`mio_chat`や`kuro_heavy`等への
renameを移行要件にしません。

物理targetの情報は通常のCORE Public APIとCMDへ公開しません。認可された運用status／logでは、
同一requestを追跡できるよう、次のfieldを意味上区別します。

| field | 意味 |
| --- | --- |
| `agent_id` | COREが選んだAgent |
| `execution_role` | COREが選んだRole |
| `execution_alias` | Gatewayへ送った論理wire key |
| `role_profile_revision` | Gatewayが解決したRole profileのrevision |
| `target_id` | Gateway内部のTarget識別子 |
| `provider` | local／外部API／Agent Runtimeのprovider |
| `model` | 実際に使用したModel。取得不能時はunknown |

通常client向けresponseは物理Target情報を必須とせず、監査・診断surfaceだけが権限に応じて
表示します。未知Agentは`UNKNOWN_AGENT`、既知Agentに対する未対応Roleは
`UNSUPPORTED_ROLE`、有効なbinding先の停止は`TARGET_UNAVAILABLE`として区別し、別Agentや
別providerの成功へ丸めません。

`POST /viewer/recipient-selection`は`viewer_client_id`と`recipient`を受け、`viewer.recipient_selected`を観測eventとして発行します。選択状態はclient-localであり、COREのglobal stateにはせず、実際の送信先は`POST /viewer/send`の`to`を正とします。

`POST /viewer/send`は`message`、`to`に加えて、clientを追跡できる場合は`viewer_client_id`、`input_source`（`text | stt | unknown`）、`user_id`、`device_name`、`audio_output`（`requested | disabled`）をJSONと`multipart/form-data`の両方で受けます。`audio_output=disabled`は送信開始時点のsnapshotであり、そのrequestではTTS sessionを作成せず、合成を要求せず、TTS完了を待ちません。`requested`は従来のTTS処理を要求し、省略時も後方互換のため従来のTTS処理を維持します。未知の空でない`audio_output`と未知の`input_source`は400で拒否します。`user_id`と`device_name`は観測用metadataであり、認証・認可には使用しません。PORTALに利用者認証がない現行構成では`user_id=viewer-user`、`device_name`はブラウザが公開するOS／platform名であり、端末hostnameではありません。

画像・動画を送る場合、`POST /viewer/send`は`multipart/form-data`を使い、
`attachments`または`attachments[]`にfileを入れます。clientはRenCrow_VisionやWildのURLを
指定せず、COREだけへ送信します。COREは添付を保存し、画像・動画を
`CORE -> RenCrow_Vision -> Wild backend -> RenCrow_Vision -> CORE`の順で処理します。
利用者の`to`は会話recipientであり、Visionの解析providerを変更しません。

COREからRenCrow_Visionへの内部requestは`POST /v1/vision/analyze`の
`multipart/form-data`とし、`file`を必須、`prompt`、`kind`、`request_id`、
`session_id`、`language`、`max_frames`、`output_format`を任意fieldとします。
COREはroot `trace_id`を`request_id`として送り、RenCrow_Visionは同じ値をresponseとlogへ
保持します。成功responseは`ok=true`と`request_id`、`provider`、`model`、`kind`、
`summary`、`text`、`segments`、`metadata`を返します。認識backendとmodelは
RenCrow_Vision内部の責務であり、COREのPublic APIへ公開しません。

失敗responseは`ok=false`、`request_id`、`error_code`、`message`を返します。
COREは`VISION_PROVIDER_UNAVAILABLE`、`VISION_MODEL_NOT_READY`、
`VISION_UNSUPPORTED_MEDIA`、`VISION_FILE_TOO_LARGE`、`VISION_VIDEO_TOO_LONG`、
`VISION_DECODE_FAILED`、`VISION_INFERENCE_TIMEOUT`、`VISION_EMPTY_RESULT`を
同じ`trace_id`の終端errorとしてclientへ通知します。

Debug Viewerの画像生成は`POST /viewer/image/generate`へ
`{"prompt":"...", "negative_prompt":"", "seed":-1}`を送ります。
COREは同じrequestをRenCrow_Imageの`POST /v1/images/generations`へ転送し、
responseのopaqueなimage IDだけを保持して
`GET /viewer/image/result?id=...`からPNGを中継します。
ViewerとCOREはRenCrow_Imageへの接続とopaqueな生成IDを扱います。
RenCrow_Imageがunavailableの場合は明示的な503を返します。

`POST /viewer/send`で`to=midori`を指定した画像生成依頼も、同じCORE-to-RenCrow_Image contractを
使用します。成功時はMidoriの利用者向け`agent.response`へ
`/viewer/image/result?id=<opaque-image-id>`を含め、Viewerは検証済みのこの内部URLだけを同じ
Chat吹き出し内のPNGとして表示します。Chat経路で新しい画像配信APIを増やさず、既存の
`GET /viewer/image/result`を再利用します。

COREは受付時に`job_id`、root `trace_id`、利用者発話の`message_id`を発行します。`POST /viewer/send`の受付responseは`job_id`、`trace_id`、`message_id`、`viewer_client_id`、`recipient`を返します。現行のroot `trace_id`は`job_id`と同じopaque値です。同じ処理から発行する`message.received`、`agent.response`、error eventは同じ`trace_id`を持ち、`message.received.message_id`は受付responseの`message_id`と一致します。Agent発話は利用者発話とは別の`message_id`を持ちます。

`message_id`は`msg_` prefix付きUUIDのopaque値です。clientは形式を解析せず、SSE再接続・再送時の重複排除と、同じ発話に由来する表示・保存の対応付けに使用します。`turn_index`は表示順の補助であり、IDの代替にしません。受付・開始・完了・errorログには同じ`trace_id`と`job_id`を、会話本文を持つlogには対応する`message_id`を記録します。TTS eventはmessage確定後なら同じ`message_id`を持ち、stream開始時に未確定なら従来どおり`response_id`で応答へ対応付けます。

`POST /viewer/character-runtime`は1 Roundの`trace_id`、`user_message_id`、各Turnの`message_id`と`turn_index`を返します。`trace_id`は全Turnで共通、`message_id`は利用者発話と各Character発話で別のUUIDです。

受付・開始・完了・errorログには`operation_source`、`input_source`、`user_id`、`device_name`、`source_ip_masked`、`source_ip_hash`、`user_agent`も記録します。接続元IPは生値を記録せず、IPv4は末尾octetをマスク、IPv6は`/64`へマスクし、同一接続元の相関用hashを併記します。`session_id`は会話sessionの単位であり、1 request / responseの完了判定には使いません。

`X-RenCrow-Client: RenCrow_CMD`で送られたterminal text chatは音声を消費しないため、COREはTTS sessionを開始しません。PORTAL／Debug Viewerなど音声再生能力を持つclientのTTS契約は維持します。client provenanceは観測と出力能力の選択に使う情報であり、認証・認可の代替にはしません。

streaming生成では、COREはRenCrow_LLMから受けた本文deltaを`agent.thinking`として逐次発行し、終端後に完成本文を`agent.response`として1回発行します。対話clientはdeltaを逐次表示してよいが、永続化と完了判定には`agent.response`を使用します。backendが最終SSE chunkに`usage.completion_tokens`と`timings.predicted_per_second`を返す場合、COREは同じ`job_id`の`metrics.latency` eventを`kind=llm`、`point=throughput`、`completion_tokens`、`tokens_per_second`付きで発行します。clientはこの値がある場合だけ実token throughputとして表示し、本文delta数をtoken数として扱いません。

対話clientは、送信受付から同じ`job_id`を持つ利用者向け`agent.response`または終端error eventまで、送信時のrecipientを固定します。この区間に別recipientへ切り替えたり、別`job_id`の応答でpending状態を解除したりしてはいけません。

TTSの`tts.audio_chunk`と`tts.session_completed`は同じ`session_id`、`response_id`を持ちます。clientは全chunkの再生終了とsession完了の両方を確認してから、response単位で`POST /viewer/tts/playback-ack`を1回だけ送ります。
`GET /viewer/tts/audio?url=...`が取得できるremote音声は、COREのTTS設定にあるbase URLと同一hostのものだけです。

IdleChat episodeのTTS先読みでは、`playback-ack`は再生結果、再生位置、cache解放の観測に使います。
同じepisodeの次発話をTTSへ送る条件にはせず、ACK欠落を理由に先読みqueueを直列停止しません。
PORTALは`turn_index`順に音声chunkを再生し、browserの`ended`でlocal queueを進めます。bufferが
下限を割った場合は`buffering`を表示し、字幕だけを次発話へ進めません。

`GET /viewer/idlechat/episodes`はepisode在庫の読み取り専用snapshotです。各episodeの
`episode_id`、revision、`episode_kind=dialogue|story_reading`、category、topic、source参照、完成作品名`story_title`、`generator=codex_exe`、`generation_id`、
`character_revision`、`input_hash`、制作状態、再生状態、生成日時、有効期限、発話数、品質判定、
固定prefix長、`repair_from_turn`、suffix再生成回数、
現在の再生位置、buffer秒数、先読み発話数、最終TTS error、最終ACK時刻を返します。
storyでは元話名`source.title`と完成作品名`story_title`を分離し、reader、listener、`transformation_axis`、genre、`interest_direction`、`interest_contract`、
物語台帳revision、検出済み整合性error、補充生成jobとの相関も返します。
台本本文は明示した`episode_id`の詳細要求でだけ返し、一覧へ全件展開しません。このGETはepisodeの
生成、検証、expire、再生、TTS合成を開始しません。

`story_reading`一覧snapshotの最小形は次です。`episodes`はreadyだけへfilterせず、storeに残る現在revisionを
返します。`untitled_ready`は旧ready artifactのタイトル補完待ち件数であり、ready件数から除外しません。

```json
{
  "ok": true,
  "ready": 3,
  "target": 3,
  "missing": 0,
  "needs_repair": 6,
  "failed": 0,
  "untitled_ready": 0,
  "filling": false,
  "episodes": [
    {
      "episode_id": "story-...",
      "revision": 3,
      "episode_kind": "story_reading",
      "story_title": "報酬欄を読む犬",
      "source": {"title": "桃太郎", "synopsis": "..."},
      "reader": "mio",
      "listener": "shiro",
      "story_contract": {"genre": "near_future_sf", "interest_direction": "funny"},
      "production_status": "ready",
      "validation": {"valid": true},
      "utterance_count": 12
    }
  ]
}
```

`GET /viewer/idlechat/episodes?episode_id=<id>`は`{"ok":true,"episode":{...}}`を返します。
`episode`には一覧fieldに加え、`story_ledger`と全`turns`を含みます。Viewerは一覧のrevisionより保持中詳細の
revisionが古い場合、利用者が別episodeを選んだ場合、または明示的に再読込した場合にこの詳細GETを行います。
同revisionの定期一覧更新だけでは詳細GETを繰り返しません。古い選択のresponseを
現在選択中の詳細へ適用しません。`episode_id`が存在しない場合は404、一覧または詳細の取得失敗は生成失敗や
検証失敗へ読み替えません。

`story_ledger.entities`は人物の同一性と本文上の呼称を分離します。`id`と
`semantic_role`はepisode内の関係、時系列、所有物、登場turnを結ぶ安定参照です。
`proper_name`は作品上必要な場合だけ設定し、空文字を許可します。`primary_label`と
`aliases`は語りの表面呼称であり、同じentityへの別参照です。entity IDを姓名や役割名から
派生させないため、呼称が人物関係の変化に応じて変わっても同一人物として追跡できます。

```json
{
  "id": "gatekeeper_01",
  "semantic_role": "入口を管理し主人公を止める人物",
  "proper_name": "",
  "primary_label": {
    "surface": "入域審査官",
    "reading": "にゅういきしんさかん"
  },
  "aliases": [
    {
      "surface": "制服の人",
      "reading": "せいふくのひと",
      "valid_from_turn": 1,
      "valid_to_turn": 3,
      "perspective_entity_id": "hero",
      "reason": "主人公がまだ役職を知らない"
    }
  ]
}
```

`primary_label.surface`と`primary_label.reading`は必須です。aliasは必要な場合だけ追加し、
`surface`、`reading`、1始まりの`valid_from_turn`、任意の`valid_to_turn`、任意の
`perspective_entity_id`、`reason`を持ちます。`valid_to_turn`省略時はepisode終了まで有効です。
人物の知識や関係が変化していない場合に無制限なaliasを追加しません。同一場面で複数entityへ
解決できる呼称は不正です。`display_text`の表記と`speech_text`の読みは、当該turnで有効な
`primary_label`またはaliasへ一致させます。

作品間の呼称多様性は詳細APIの永続fieldではなく生成入力の`recent_naming_context`で管理します。
COREは直近の完成story episodeから主呼称、姓名構文、役割名の傾向を抽出し、CodexExeへ渡します。
このcontextは同じ語を決定的に禁止するblacklistではなく、時代、genre、視点、人物関係が異なる作品で
同じ姓名templateや役割名セットを機械的に反復しないための参考情報です。

Debug Viewerでは、お題在庫を`Topic Stock`、episode化した物語在庫を`Story Stock`として分離します。
`Story Stock`の物語専用リストは、`ready`だけでなく`needs_repair`と`failed`を含むsnapshot内の
全episodeを表示し、状態によって行を暗黙に除外しません。利用者が一覧または選択欄からepisodeを
選んだ時だけ`episode_id`付きの詳細GETを行い、全発話、story contract、物語台帳、検証結果を読み取り
専用で表示します。検証NGのepisodeでは`validation.errors`のcode、`turn_index`、field、evidenceを
本文と対応付け、直接NGと判定されたturnを明示します。さらに`first_invalid_turn`以降をsuffix再生成対象
として直接NGとは別の表示にし、turnを持たない全体errorも隠しません。一覧取得失敗または詳細取得失敗
では直前に取得済みの内容を消去せず、取得工程とHTTP状態を画面へ表示します。

`POST /viewer/idlechat/episodes/prepare`は`count`と任意の`categories`を受け、低優先度のepisode
準備jobを登録して`job_id`を返します。`count`は1から10までとし、空の場合はConfigの不足数を使います。
HTTP request内で台本生成完了を待たず、既存の同一準備jobと重複する要求は冪等に同じ実行へ集約します。
前景Chatまたは明示Workerが始まった場合、jobは失敗ではなく`deferred`となり、次回Idleへ延期します。
生成または検証がNGのepisodeは`needs_repair`として保持し、ready数へ含めません。prepare jobはその
修復や破棄判断を待たず、別`episode_id`で不足数を追加生成します。元episodeと補充episodeは
`replacement_for_episode_id`またはjob相関で追跡し、本文やidentityを共有しません。

`POST /viewer/idlechat/episodes/validate`は`episode_id`を受け、台本の全発話、speaker帰属、順序、
話題重複、発話反復、Persona、category固有禁止、品質判定、source鮮度、本文hashを検証します。
storyでは固定reader、listenerの合いの手頻度と長さ、面白さ契約、entity関係、時系列、場所、
所有物、世界規則、造語、主呼称とaliasのentity解決、aliasの適用turnと視点、表示表記、TTS読みも検証します。
検証はepisode本文を変更せず、`valid`、turn別状態、`first_invalid_turn`、NG理由、固定可能なprefix長、
`repair_required`、`replacement_requested`、補充job IDを返します。NG理由は`schema_violation`、`speaker_confusion`、`repetition`、
`topic_violation`、`persona_violation`、`factual_violation`、`meta_leak`、`quality_violation`、
`content_mode_violation`、`title_violation`、`lexical_corruption`、`entity_relation_violation`、
`entity_reference_violation`、`entity_naming_violation`、
`continuity_violation`、`world_rule_violation`、`reading_violation`、
`interest_contract_violation`、`story_performance_violation`です。episodeおよび検証結果は`content_mode=serious|assertive|free`と
判定理由を返し、戦争・武力衝突・災害等を`serious`、それ以外の政治・思想を`assertive`、
その他を`free`として扱います。複数条件では`serious`を優先します。
prepare job内の自動修復は最小の`first_invalid_turn=k`を起点に`turn k`以降を破棄し、固定prefixと
NG理由をCodexExeへ渡して最終turnまで再生成します。prefixの`message_id`は維持し、suffixへは
新しい`message_id`を発行します。NG判定時点から当該episodeはready在庫へ含めず、修復とは別に
補充episodeを生成します。`max_suffix_regenerations`到達時はepisodeを`failed`にしますが、
自動削除は行いません。
旧ready episodeに`story_title`がない場合は、本文とmessage IDを保持したままタイトルだけをCodexExeで補完し、revisionを増やして追記します。補完失敗は旧ready状態を壊さず`title_generation`として観測し、GETによる一覧・詳細表示自体では補完を開始しません。
`POST /viewer/idlechat/episodes/expire`は`episode_id`を受け、再生中でないepisodeを`expired`へ遷移させます。
再生中のepisodeはHTTP 409と`IDLECHAT_EPISODE_PLAYING`を返し、暗黙に中断しません。これらは
Debug Viewer／localhost運用CLI向けのadmin APIであり、RenCrow_PORTALからproxyしません。

`GET /viewer/idlechat/status`の`word_topic_stock`は1ワード／2ワード、`forecast_stock`は6ドメインの準備済みお題を返します。`episode_stock`は完成、要修復、失敗などの物語在庫を返します。`topic_stock_playback`は現在項目、履歴位置、`can_previous`、`can_next`を返します。これは観測用snapshotであり、GETによって生成・消費・補充・再生・TTS合成を開始しません。

`POST /viewer/idlechat/playback`は`{"action":"play|next|previous","item_id":"..."}`を受け付けます。`play`の`item_id`省略時は現在項目を再生し、現在項目がなければ未再生Stockの先頭を使います。`next`は未再生の次項目を消費し、`previous`はCORE内の再生履歴を使って完成物を再生し直します。`previous`で項目をStockへ戻さないため、補充や生成の重複を起こしません。

`GET /viewer/idlechat/collection`は、`status`、`skill_id`（`core.build-daily-source-brief`）、`schedule`、`timezone`、`fetched_at`、`next_run_at`、ニュース件数、Wikipedia件数、カテゴリ／source別件数、`items`、`sources`、`tools`、`word_pool`を返します。`word_pool`は固定語数、当日最新語数、合計数、上限、当日最新語とその`source_type`を返します。分析全体の状態は`enrichment_status`（`pending`、`enriching`、`ready`、`partial`、`fallback`）、`enrichment_provider`、`enrichment_error`、`enriched_at`で確認できます。収集後の分析は`Worker`が記事を1件ずつ完了させ、`enriching`中も完了済みまたは工程失敗が確定した記事を順次snapshotへ反映します。`ChatWorker`は使用しません。

`items`はtitle、category、source、`source_type`、元URL、`source_read_status`、`source_read_url`、`processing_status`、`processing_error`、原文の日本語訳`translated_body`、`summary`、事実と分離したShiroの`perspective`、`term_notes`を持ちます。収集phaseで見出しとURLを取得した項目は後続工程が未着手または失敗でも`items`から除外せず、`total`は常に`len(items)`と一致します。`source_read_status_counts`と`processing_status_counts`は全`items`の状態別件数を返します。`source_read_status`は原文取得だけを表し、`unprocessed`は未着手、`ready`は取得済み、`unavailable`は取得失敗です。`processing_status`は後続処理を表し、値は`pending`、`ready`、`source_unavailable`、`translation_failed`、`term_extraction_failed`、`brief_failed`です。`pending`は未着手であり失敗ではありません。`processing_error`は空、または利用者へ表示可能な工程別の日本語理由であり、providerやbackendの生errorを含みません。原文取得後に翻訳が失敗した項目は`source_read_status=ready`と`processing_status=translation_failed`を返します。用語抽出またはサマリ・見解生成が失敗した場合も、それ以前の工程で完成した値を保持します。`term_notes`は用語、説明、確認方法、確認元URL、`contextual`／`confirmed`／`unresolved`／`unavailable`の状態を返します。表示順は「原文翻訳 → サマリ → Shiroの見解 → 用語補足」です。`sources`はcredentialを除いた取得先設定を持ちます。このGETは現在のプロセス内cacheをコピーして返す観測用snapshotであり、収集、分析、再収集、cache消費、Memory昇格を開始しません。

### Movie Catalog API実装契約

`GET /viewer/movie-catalog?action=movies|people`は一覧項目に`familiarity`、`sentiment`、`assessed`を返します。映画の`familiarity`は`seen | unseen | ""`、人物の`familiarity`は`known | unknown | ""`、`sentiment`は共通で`like | dislike | ""`です。`POST /viewer/movie-catalog/preference`へ`kind`（`movie | person`）、`target_id`、`target_label`、`dimension`（`familiarity | sentiment`）、`value`、`generated_by`を送ると一方のdimensionだけを更新し、他方を維持します。空の`value`はそのdimensionを明示的な未選択へ戻します。Viewerの通常一覧は映画／人物とも「みた」「すき」の2入力だけを表示し、人物の「みた」は`known`へmapします。Viewer内部のwrite APIであり、PORTALへ自動公開しません。

`GET /viewer/movie-catalog?action=cards`は映画catalogからD0/D1カードを派生して返します。D0は映画の`seen`または`like`、人物の`known`または`like`、および成功した明示映画名／人物名／URL取得対象です。D0のroot `kind`は現段階では`movie`または`person`だけです。`unseen`、`unknown`、`dislike`単独はD0にしません。明示assessmentの行が存在しない場合だけ、映画のwatch eventを`seen`、人物の正のfavorite signalを`like`としてfallbackします。responseの各itemは少なくとも`kind`、`target_id`、`target_label`、`target_url`、`depth`、`root_ids`、`relation_type`、`relation_source`、`validation_state`、`provenance_urls`を持ち、`kind`は少なくとも`movie | person | music | source_work`を許容します。D0は`depth=0`、D1はD0のvalidated direct relationだけを`depth=1`として返します。D1には出演・監督・脚本・音楽担当・原作者等の`person`、映画.comが作品名を明示した音楽作品・主題歌・劇伴・サウンドトラック等の`music`、小説・漫画・舞台・ゲーム等の原作・参照作品の`source_work`を含めます。D1から先は展開せず、validated cardは同じ`kind`と`target_id`の一件にまとめ、`target_id`のない`partial`または`unresolved` cardは正規化label、relation、provenanceで一件にまとめます。複数rootでは最小`depth`を返します。汎用work cardは`hobby_graph`のvalidated itemを正本とし、映画側credit／relationのprovenanceは`movie_catalog`から返します。文字列だけでitemへ確定できない場合は`target_id`を空にした`partial`または`unresolved` cardとしてlabelとprovenanceを返し、推測で補完しません。`depth`、root経路、D1カードは派生値であり、Memory L1へ保存しません。個人評価やroot状態を通常会話のCategoryRecallへ渡さず、公開catalog projectionとの境界を維持します。

`POST /viewer/movie-catalog/fetch`は`kind`、`query`または`url`、`max_pages`、
`follow_links`、`include_person_filmography`を受けます。COREは
`movie_catalog.crawler_url`のRenCrow_Tools Go sidecarへ
`POST /v1/movie-catalog/crawls`を送り、完了jobの`artifact_url`、`artifact_sha256`、
`artifact_bytes`を検証します。取得したJSONLはCOREがtransaction内でSQLite正本へimportし、
不正record、空artifact、hash／size不一致では全体を失敗させます。sidecarはCOREのSQLiteへ
直接書きません。sidecar未設定または利用不能時はHTTP 503、
`status=unavailable`、`error_code=MOVIE_CATALOG_CRAWLER_UNAVAILABLE`を返し、
Python crawlerや別endpointへfallbackしません。名前queryが複数候補に一致した場合は候補を返すだけで、
COREは対象を勝手に確定しません。利用者が候補または正規化されたURLを明示選択し、取得、artifact検証、
importが成功した対象だけをD0 rootとして記録します。映画.comの`/search` queryを取得経路にせず、
robots.txt、rate limit、その他のアクセス制約を迂回しません。このViewer write APIもPORTALへ自動公開しません。

`query`と`url`はtrim後に必ず一方だけを選びます。sidecarが入力を検証する場合、両方空は
`400 D0_INPUT_REQUIRED`、両方指定は`400 D0_INPUT_CONFLICT`です。`url`を選んだ場合は既存の正規化済み
`https://eiga.com/movie/{id}/`または`https://eiga.com/person/{id}/`を`seed_url`として送信し、既存の
URL contractを変更しません。`query`を選んだ場合だけsidecar requestへ`query`を入れ、`seed_url`を
空にします。COREはqueryをURLへ変換せず、sidecarの候補・robots・rate limit判定を迂回しません。

queryが一意に解決できず、利用者の明示選択が必要な場合は次の形で`409`を返します。候補の表示名やURL
を見てCOREが自動選択してはなりません。

```json
{
  "available": true,
  "status": "candidates",
  "kind": "movie",
  "query": "作品名",
  "error_code": "D0_RESOLUTION_AMBIGUOUS",
  "candidates": [{"kind":"movie","label":"候補作品","url":"https://eiga.com/movie/101/"}]
}
```

候補はsidecarの`url`、`kind`、`label`をresponseから保持します。既存local候補との互換のため
`id`、`title`、`name`が存在する場合も破棄せず、`CrawlerServiceError`も
HTTP status、upstream error code、message、候補配列を一体で保持し、handlerが候補を落としません。
既存のURL取得と旧sidecar responseは同じendpoint、job、artifact、hash、size fieldで処理します。
`candidates`と`query`は後方互換な追加fieldです。旧sidecarがqueryを理解しないときは、COREはURLや
候補を推測せず、明示的なunsupported／upstream errorとして返し、旧Python crawlerへfallbackしません。

実装で固定するerror codeとHTTP statusは次のとおりです。sidecarの`D0_*` codeは`message`と
`CrawlerServiceError.Code`へ保持し、CORE adapterで別の意味へ変換しません。COREのViewer handlerが
surface固有codeを返す場合も、upstream codeと候補配列を失わないことを別contractで検証します。

| error code | HTTP | owner | 意味 |
| --- | ---: | --- | --- |
| `D0_INPUT_REQUIRED` | 400 | sidecar | `query`と`url`のどちらもない |
| `D0_INPUT_CONFLICT` | 400 | sidecar | `query`と`url`を同時指定した |
| `D0_RESOLUTION_AMBIGUOUS` | 409 | sidecar | queryが複数候補で、候補選択が必要 |
| `D0_RESOLUTION_NOT_FOUND` | 404 | sidecar | queryに一致する検証済み候補がない |
| `MOVIE_CATALOG_CRAWLER_UNAVAILABLE` | 503 | CORE | sidecar未設定または到達不能 |
| `MOVIE_CATALOG_IMPORT_FAILED` | 502 | CORE | artifact importのtransaction失敗 |
| `MOVIE_CATALOG_ARTIFACT_MISSING` | 502 | CORE | sidecarがartifactを返さない |
| `D1_OUTBOUND_FORBIDDEN` | 422 | sidecar／CORE | D1から先のedgeまたはoutboundを検出 |

sidecar artifactはv1のmovie／person JSONLを受け付け続け、v2では`rencrow.movie_catalog.v2`の
`manifest`、`node`、`edge` recordを一つのroot import transactionで検証・取り込みます。v2 nodeの
`node_kind`は公開item kindとして`movie | person | music | source_work`、rootは`movie | person`だけです。公開cardsの
`depth`、`root_ids`、D1 itemは保存値ではなく、assessmentまたは成功した明示取得rootからvalidated
direct edgeを最大1回だけ評価する派生値です。複数rootは最小depthへまとめ、D1から別のedgeを辿りません。
artifact nodeの`depth`はsidecar取得境界の検証metadataであり、公開Card `depth`へコピーしません。
`partial`／`unresolved`は明示labelとprovenanceを返しますが、空のtarget IDを推測で埋めません。
汎用work cardの正本は`hobby_graph`、映画側のcredit／relation／provenanceは`movie_catalog`です。

Economic APIで新しいOpportunityを作ると、未指定の`trace_id`はCOREが生成します。EconomicTask、Delivery、RevenueEvent、Reflectionの作成では、参照元Opportunityまたは上流entityの`trace_id`を引き継ぎ、別の値へ黙って付け替えません。`POST /viewer/revenue/deliveries`は`delivery_id`、`trace_id`、`delivery_kind`、`status`、任意の上流IDとtarget/evidenceを受けます。`external_action=true`かつ`status=completed`では、許可された`policy_decision_id`と`evidence`が必須です。

`POST /viewer/revenue/opportunities/workstream-goal`は`opportunity_id`と`workstream_id`を受け、draft Goal、pending-review Artifact、`decision_type=economic_opportunity_execution`のPolicy Decisionを同じ`trace_id`で保存して返します。既存Opportunityに`trace_id`がない場合は、このuse caseが生成してOpportunityへ保存します。responseの`external_actions_applied`は`false`であり、このAPI自体は外部side effectを実行しません。後続の実行requestは同期policy判定で許可または拒否します。

## ニュースIntent contract

ニュース要求は、IdleChatの入力やViewerのcollection endpointを経由させず、Chatの前段Intentとして扱います。

| Intent | 代表的な入力 | 最初に読むデータ | 第一応答者 | 外部検索 |
| --- | --- | --- | --- | --- |
| `daily_news_brief` | 「今朝のニュースを教えて」「朝のニュースは？」 | 当日04:00 JSTの`DailyNewsBrief` | Mio | cacheが空、未準備、または古い場合のみ |
| `live_news_search` | 「最新のニュース」「速報」「今のニュース」 | `LiveNewsSearch` | Mio | 必須 |

`daily_news_brief`は`ready`または`partial`の準備済み項目を番号付きリストで返し、追加入力の「2番を詳しく」は同じbriefのitem IDを参照します。`pending`、`enriching`、`fallback`、空cacheでは確認不能な内容を推測せず、`LiveNewsSearch`へフォールバックしたか、朝刊が未準備であることを回答へ明記します。Mioが利用できない場合に限り、Shiroが同じ準備済みデータを要約できます。`DailyNewsBrief`の対象日・取得時刻と、`LiveNewsSearch`の検索時刻は必ず区別して返します。

`GET /viewer/idlechat/collection`は観測専用snapshotであり、Chatがユーザー向けニュースを取得する経路ではありません。ChatはCORE内部の`DailyNewsBriefReader`を介して`DailyNewsBrief`を読み取ります。

## ニュースartifact API境界

`NewsCollectionArtifact`と`NewsAnalysisArtifact`のschema、意味、hash系譜はCOREが所有しますが、現行Public APIには収集または考察を起動する専用endpointを公開していません。`GET /viewer/idlechat/collection`のresponseを収集artifactとして保存したり、そのGETでjobが起動すると仮定したりしてはいけません。

採用済みの`RenCrow_Tools` CLI `rencrow-news analyze`は、実装時にCORE所有の考察portへ接続します。具体的なHTTP method、path、interaction profile、request／response、非同期job相関を追加する場合は、CLI実装より先にこのPublic API正本へ記載します。それまではCLIを任意のLLM、RenCrow_LLM Gateway、物理Backendへ直接接続して代替しません。`RenCrow_CMD`にはニュース専用commandを追加しません。

## X Bookmark Viewer API

`GET /viewer/x-bookmarks`はX Bookmark専用の読み取り専用HTML画面です。Debug Viewerの
左端ナビゲーションから開き、下記API以外の収集・分類・昇格処理を開始しません。

`GET /viewer/source-registry?action=x-bookmarks`は、COREの
`l1_staging_item.meta.collection=x_bookmark`だけをViewer用に投影する読み取り専用APIです。
収集、再分類、validation、promotionは行いません。

queryは`major`、`minor`、`review=needs_review|classified`、`q`、`limit`、`offset`です。
`limit`の既定値は12、上限は50、`offset`は0以上、`q`は200文字以下です。responseは絞り込み後の
`items`、`total`、`limit`、`offset`と、全X Bookmarkを母数にした`summary.total`、
`summary.needs_review`、`summary.major_counts`、`summary.minor_counts`を返します。各itemは`id`、
`title`、`source_url`、`raw_text`、`validation_status`、`needs_review`、分類method、
`use_case_tags`、投稿者、画像・参照リンク件数、更新時刻に加え、`references`を公開します。
各referenceは`kind`、`url`、`resolved_url`、`status_url`、`capture_status`、`display_text`、
`preview_text`、`page_title`、`page_description`、`body_text`、`body_char_count`、`body_truncated`、
`fetched_at`、`fetch_error`、X投稿参照用の`text`とauthor表示名・usernameを持ちます。credential、物理LLM route、
分類に不要な内部metaは返しません。

## Interaction client共通意味論

PORTAL、CMD、ASSISTANTは、COREとのInteractionで次の意味論を共有します。

| 能力 | contract |
| --- | --- |
| Chat | requestごとに利用者scopeと明示recipientを持ち、別recipientへ黙ってfallbackしない |
| IdleChat | status／event購読、明示的な開始／停止、PORTALのsurface在席による排他制御を分ける |
| recipient | UI選択通知は観測event、実送信先はmessage requestの`to`を正とする |
| event | reconnectと重複を前提に、event IDまたは相関IDで二重処理を防ぐ |
| session | request、response、Task、audio、外部deliveryへ追跡可能な相関を保つ |
| STT／TTS | input、合成、audio取得、再生、ACKを別々の成功条件として報告する |
| Task | 受付と完了を同一視せず、status、result、error、provenanceを追跡する |
| error | unavailable、degraded、denied、expired、failedを区別する |

各clientは同じ意味論を、Web、terminal、PUSH／Deviceへ異なる形で表示できます。
すべてのclientが全能力を公開する必要はなく、client profile、認証scope、mode、Device
capabilityで制限します。

既知の外部clientは次のprofileを使用します。

| `X-RenCrow-Client` | `X-RenCrow-Interaction-Profile` | 許可する主な能力 |
| --- | --- | --- |
| `RenCrow_PORTAL` | `portal-chat` | PORTAL Chat allowlistとChat surface在席通知 |
| `RenCrow_PORTAL` | `portal-idlechat` | IdleChatの読み取りとIdleChat surface在席通知 |
| `RenCrow_PORTAL` | `portal-games` | Agent-owned gameの選択、起動、観戦、session lifecycle |
| `RenCrow_CMD` | `cmd-chat` | Chat送信、event購読、CORE経由のWAV文字起こし |
| `RenCrow_CMD` | `cmd-idlechat` | IdleChat status／event／start／stop |
| `RenCrow_CMD` | `cmd-diagnostics` | 診断・状態取得の読み取り専用 |
| `RenCrow_CMD` | `cmd-control` | process制御、repair実行、ProfilePromotion retry |
| `RenCrow_ASSISTANT` | `assistant-core` | COREへのChat送信とevent購読 |

`cmd-diagnostics`と`cmd-control`は、CMDが実装本体を持たずCORE Public API経由で
診断・運用を行うためのprofileです。影響の大きい操作を分離するため、状態を変更しない
読み取りと、process制御・repair実行を別profileにしています。

`cmd-diagnostics`が許可するのはGETのみで、対象は次のpathです。

```text
/health、/health/live、/ready
/viewer/status、/viewer/logs
/viewer/capabilities
/viewer/evidence/{recent,detail,summary}
/viewer/source-registry、/viewer/knowledge-memory
/viewer/debug/system
/viewer/channels、/viewer/channels/probe
/viewer/memory/profile-promotions
/viewer/web-gather/doctor
```

`GET /viewer/channels`は設定済みchannelの一覧、`GET /viewer/channels/probe`は各channelの
疎通結果、`GET /viewer/web-gather/doctor`はweb-gatherの依存構成の診断結果を返します。
`GET /viewer/memory/profile-promotions`はProfilePromotionの全row集計、limited job詳細、
retryable／orphan failed件数、DB pool統計を返します。いずれも読み取り専用です。

CMDのmemory診断・再試行用の正規CLI名は`rencrowctl memory status`と
`rencrowctl memory retry-failed`として実装され、前者は`cmd-diagnostics`、後者は
`cmd-control`のheader／allowlistを使用します。

web-gatherのurl／search／webwright-fetchとimport系はPublic APIへ公開しません。外部への
HTTPアクセスを伴う操作を公開するとCOREが任意URL取得の踏み台になり、import系はCOREホスト上の
パスに依存する設計になるためです。これらはCOREのCLIから実行します。

`cmd-control`が許可するのは次のpathです。制御結果の確認に必要な読み取りだけを併せて
許可し、対話系（`/viewer/send`、`/viewer/events`、IdleChat）は対象外とします。

```text
POST /viewer/repair/run
POST /viewer/source-registry
POST /viewer/memory/profile-promotions/retry
POST /viewer/capabilities/apply
GET  /viewer/capabilities/apply/{request_id}
```

COREは既知clientのprofile欠落、client/profile不一致、profile外method/pathを403で拒否します。
profile headerは認証credentialではなく、既存のendpoint allowlist、TLS、network境界、
server-side authorizationを置き換えません。共通SDKは実caller間の重複が確認されるまで
先行作成しません。

terminal Chat clientの正本はRenCrow_CMDの`rencrowctl chat`です。COREの`rencrow`
server binaryはChat client commandを持ちません。`rencrowctl chat --audio`は
`cmd-chat` profileで`POST /stt/chat-input`を呼び、転写結果を`POST /viewer/send`へ
送ります。`--audio-direct`はWAVを`/viewer/send`の添付としてCOREの
`input_audio`経路へ渡します。

## PORTAL surface在席API

`POST /viewer/surface-presence`はPORTALの画面表示をIdleChat runtimeへ反映する専用APIです。
browserからCOREへ直接送らず、PORTAL serverがmodeに対応するInteraction profileを付けて
中継します。

request:

```json
{
  "viewer_client_id": "tab-scoped-opaque-id",
  "surface": "chat",
  "action": "claim"
}
```

| field | contract |
| --- | --- |
| `viewer_client_id` | 必須。browser tabごとに生成し、同じtabの再送で維持する不透明ID |
| `surface` | `chat`または`idlechat`。`portal-chat`は`chat`、`portal-idlechat`は`idlechat`だけを送信可能 |
| `action` | `claim`、`heartbeat`、`release`のいずれか |

`claim`と`heartbeat`は受理時点から30秒のleaseを作成または更新します。可視状態のclientは
10秒ごとに`heartbeat`を送り、`visibilitychange`でhiddenになった時と`pagehide`時に
`release`を送ります。COREはlease失効をreleaseと同じに扱います。未知fieldは互換性のため
無視できますが、必須field不足、未知の値、profileとsurfaceの不一致は400または403で拒否します。

response:

```json
{
  "ok": true,
  "surface": "chat",
  "action": "claim",
  "effective_mode": "chat",
  "idlechat_active": false,
  "chat_presence_count": 1,
  "idlechat_presence_count": 0,
  "lease_expires_at": "2026-08-04T00:00:30Z"
}
```

`release`では`lease_expires_at`を省略できます。countは有効leaseの集約値です。状態遷移は
CORE内で原子的に行い、同一requestの再送で二重開始／停止しません。優先順位は次を正とします。

1. 有効な`chat`在席が1件以上ならIdleChatを停止し、`effective_mode=chat`とする。
2. `chat`在席が0件で`idlechat`在席が1件以上ならIdleChatを開始し、`effective_mode=idlechat`とする。
3. 両方0件ならPORTALを理由にIdleChatを開始せず、`effective_mode=none`とする。

Chat在席による停止はIdleChatの自動再開より優先し、未送信のIdleChat TTS queueも取り消します。
明示的な`POST /viewer/idlechat/start|stop`は`cmd-idlechat`等の認可client用として維持し、
PORTALは使用しません。`portal-idlechat`は利用者操作としては引き続き読み取り専用であり、
このsurface在席APIだけをstate-changingな例外として許可します。

## Client の注意

- method、status code、content type を確認する。
- unknown field を許容し、既存 field の意味を推測で変更しない。
- write/action endpoint は policy decision、idempotency、request provenance を保持する。
- SSE は再接続と重複 event を考慮する。
- debug/admin API を public network へ直接公開しない。

## PORTAL公開境界

`RenCrow_PORTAL`はCOREの全APIを透過公開しません。

- `IdleChat`: `GET /viewer/events`、`GET /viewer/idlechat/status`などの読み取りと、`POST /viewer/surface-presence`の`surface=idlechat`、`POST /viewer/idlechat/playback`だけを許可する。手動の開始／停止は許可しない。
- `Chat`: chat、recipient通知、active audio/input ownership、TTS再生、STT入力と、`POST /viewer/surface-presence`の`surface=chat`だけをallowlistとする。IdleChatの手動開始／停止は許可しない。
- `Games`: 下記のGames allowlistだけを許可し、Agent decision／result callbackを公開しない。
- COREへのproxy requestはmodeに応じて`portal-chat`、`portal-idlechat`、`portal-games` profileを付ける。
- Debug、Ops、Repair、LLM管理、設定変更APIはPORTALから遮断する。
- 新しい公開操作はCORE側のAPI追加だけで自動公開せず、PORTAL側でmethod/pathと契約テストを追加する。

`GET /viewer/events`の会話表示filterはmodeごとに固定します。Chatは
`message.received`、利用者向け`agent.response`、既存契約で公開対象の
`agent.progress`／`agent.acknowledge`を会話欄へ表示し、
`idlechat.message`とIdleChat TTSを表示・再生しません。IdleChatは`idlechat.message`だけを
会話欄へ表示し、通常ChatのmessageとTTSを表示・再生しません。SSE再接続で過去eventを
受信した場合も、現在のmodeを基準に同じfilterを適用します。

`portal-games`のallowlistは次を正とします。

```text
GET|HEAD /health
GET|HEAD /viewer/games/status
GET|HEAD /viewer/games/sessions
GET|HEAD /viewer/games/events
GET|HEAD /viewer/games/observer
GET|HEAD /viewer/games/observer-api/*
POST     /viewer/games/launch
POST     /viewer/games/observer-api/games/sessions/{opaque_session_id}/retry
POST     /viewer/games/observer-api/games/sessions/{opaque_session_id}/start_over
```

`POST /viewer/games/decision`、`POST /viewer/games/result`、Observer APIの
`/games/launch`、frame／summary ingest、Debug／Admin APIは許可しません。
`retry`と`start_over`はGAMESのsession lifecycle操作であり、turnのActionIntentでは
ありません。browserからのPOSTはsame-originを必須とし、`session_id`は解析せず
path segmentとしてURL encodeします。

## ASSISTANT連携境界

`RenCrow_ASSISTANT`はAgent対話、調査、生成、継続Taskへ昇格する場合だけCORE Public APIを利用します。利用者ID、household、許可scope、request／task相関IDを維持し、必要最小限のcontextだけを送ります。

- 目覚まし、生活Routine、PUSH、acknowledgement、snooze、端末retryはASSISTANT側の契約とする。
- COREのDebug、Ops、Repair、LLM管理APIをASSISTANTから利用しない。
- CORE unavailable時はASSISTANTがAgent処理をdegradedとして扱い、別Agentの成功へ丸めない。
- 専用endpointを追加する場合は、既存Viewer内部APIの無制限な再公開ではなく、認証、scope、idempotency、監査を含むpublic contractとして定義する。
- ASSISTANTのPUSHを第二の会話systemにせず、CORE応答を利用者、source、category、
  correlation ID付きのInteraction outputとして元のdeliveryへ戻せるようにする。

## Atlas owner API

読み取りprojectionは`GET /viewer/atlas`、`/viewer/atlas/items`、`/viewer/atlas/items/{id}`、
`/viewer/atlas/radar`、`/viewer/atlas/backlog`、`/viewer/atlas/queue`、`/viewer/atlas/active`、
`/viewer/atlas/evidence/{implementation_unit_id}`で提供します。Debug Viewerおよび認証済み
`cmd-diagnostics` profileはこのprojectionだけを読みます。

`GET /viewer/atlas/items/{id}`はDesign Cardと解決済みSpecification metadataを返します。
Design Cardの`owner_module`はLifecycle ownerとして`RenCrow_CORE`に固定し、
code配置先の`target_modules`、利用先の`consumer_modules`、検証影響範囲の
`affected_modules`を別fieldで返します。
`GET /viewer/atlas/specifications/{spec_id}`はallow-list登録されたSpecificationだけを返し、local artifactでは
hash検証済み本文と`body_available=true`、external artifactでは本文を複製せず参照metadataと
`body_available=false`を返します。encoded slash、backslash、traversal、未知IDを拒否し、任意filesystem pathは受理しません。

状態変更は`POST /v1/atlas/intake`と`POST /v1/atlas/items/{id}/{candidate|adopt|defer|reject|revise}`
に限定します。`cmd-control` profile、Bearer credential、owner identity、bounded JSON bodyを必須とし、
COREがstate transition、dedupe、Evidence Gate、WIP leaseを検証してtyped resultを返します。
Viewer、CMD、LLMがstateや`check_ok`を直接確定しません。legacy `GET|POST /viewer/backlog`は互換入口として
維持しますが、Atlas lifecycleのauthoritative writeではありません。

`revise`は`implementation_revision`、隣接`target_stage`、Evidence Refを受け取りますが、入力の
`passed=true`をruntime合格へ直接代入しません。COREはkind別owner verifierで参照先を検証し、
`unit_id + implementation_revision + target_stage`の冪等receiptを返します。失敗revisionは
`reason_code`、`invalidated_from_stage`、変更軸を保持し、過去stageを上書きしません。

`BLOCKED`では失敗終端、Queue Freeze、Lease解放結果を同一closure receiptへ束縛します。Freeze解除は
`POST /v1/atlas/queue-freezes/{freeze_id}/resolve`に限定します。bodyは`request_id`、
`expected_freeze_revision`、事前Adoption済みの`replacement_unit_id`、旧`supersedes_unit_id`、
`blocker_resolution_refs`を必須とします。COREが旧UnitのBLOCKED、supersedes完全一致、blocker解消、dependency、
他Lease不在を同じdecisionで検証し、成功時だけFreeze解除receiptとreplacement Lease取得を冪等に確定します。
同一request IDのpayload違いはconflictとし、再起動やLease不在を解除requestとして扱いません。

`POST /v1/atlas/intake`はSchema v2 Design Cardの次のoptional fieldをそのまま保存します。
`feature_id`、`problem`、`idea`、`background`、`expected_effect[]`、`relation_refs[]`、
`specification_refs[]`。Radar／Candidateのpartial inputにこれらを要求せず、`purpose`以外の内容をCOREが生成しません。
`specification_refs[]`がある場合は、COREへembeddedされた固定Backfill packageの11 ID（local 8 / external 3）へ
照合し、manifestまたはlocal本文hashの検証失敗・未知IDはItem Save前に拒否します。GET itemのresolved Specificationは
同じallow-listから解決し、任意pathを受けません。

Revision 2の`revise`は`request_id`、`expected_revision`、target stage、Evidence Refを受け取り、COREが
`item_id + implementation_unit_id + implementation_revision + target_delivery_state`のtyped contextを構成します。
`passed=true`はclaimのままで、fixed owner verifierの成功結果だけをverified projectionとstage receiptへ反映します。
stage receiptのidempotency keyは`unit + implementation_revision + target_stage`です。

Freezeの読み取りは`GET /viewer/atlas/queue-freezes`、解除は固定の
`POST /v1/atlas/queue-freezes/{freeze_id}/resolve`です。RenCrow_CMDのtransport facadeはそれぞれ
`atlas queue-freezes`、`atlas resolve-freeze`へ1 requestで対応し、後者は
`--freeze-id --request-id --expected-freeze-revision --replacement-unit-id --supersedes-unit-id`と、
blocker Evidence group（stage/kind/ref、任意のrepository/revision/sha256/observed-at）を要求します。
ID、revision、Evidence、replacement、atomicity、receiptの意味はCOREだけが決定します。
