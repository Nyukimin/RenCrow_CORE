# Public API 仕様

RenCrow_CORE の HTTP API は、RenCrow_ASSISTANT、RenCrow_PORTAL、Debug Viewer、CLI facade が利用するruntime contractです。endpointは互換性維持のため`/viewer/*`を中心に構成されますが、外部公開可否はclientごとのallowlistで制限します。

## 安定性区分

| 区分 | 対象 | 互換性方針 |
| --- | --- | --- |
| Core | `/health/live`, `/health`, `/ready`, Viewer entry、通常 chat recipient | 破壊的変更を避ける |
| Feature | status、jobs、workstreams、memory、advisor、revenue 等 | feature 単位で拡張し、既存 field を維持する |
| Operational | repair、LLM management、debug、admin action | local/authorized 利用を前提とし、明示 policy を必要とする |
| Experimental | AI workflow、研究・候補 feature | schema が変わる可能性を明示する |

## 主な endpoint 群

| endpoint / prefix | 用途 |
| --- | --- |
| `GET /health/live` | COREのHTTPイベントループ自身のliveness。外部依存を確認しない |
| `GET /health` | COREと設定済み依存serviceの総合health |
| `GET /ready` | request受付可否 |
| `/viewer/api/chat` | Viewer chat request と response |
| `POST /viewer/send`, `GET /viewer/events` | PORTAL／CMD等のmessage送信とSSE event購読 |
| `GET/POST /viewer/character-runtime` | Character一覧、複数Character Roundと会話ID |
| `/viewer/status`, `/viewer/agents` | runtime と agent の状態 |
| `GET /viewer/idlechat/status` | IdleChat状態と読み取り専用の`forecast_stock` snapshot |
| `GET /viewer/idlechat/collection` | IdleChat日次収集cache、次回04:00 JST、取得元、利用toolの読み取り専用snapshot |
| `POST /viewer/idlechat/start`, `POST /viewer/idlechat/stop` | IdleChatの開始・停止。認可されたwrite clientだけが利用する |
| `/viewer/jobs`, `/viewer/logs` | job と監査可能な log |
| `/viewer/backlog`, `/viewer/scheduler` | 継続作業の照会・操作 |
| `/viewer/workstreams/*` | goal、artifact、annotation、heartbeat、review |
| `/viewer/advisors/*`, `/viewer/agents/profiles` | Advisor run/score と AgentProfile |
| `/viewer/revenue/*` | Opportunity、EconomicTask、RevenueEvent、Reflection、approval |
| `GET/POST /viewer/revenue/deliveries` | trace付き汎用Deliveryの一覧・draft/状態record作成 |
| `/viewer/memory/*` | memory event、Recall、ProfilePromotion job の観測 |
| `GET /viewer/movie-catalog` | 映画・俳優catalogと利用者評価の一覧・詳細 |
| `POST /viewer/movie-catalog/preference` | 映画・俳優の認知・好み評価を保存 |
| `/viewer/active-control`, `/viewer/tts/*`, `/viewer/stt/*` | audio/control bridge |
| `POST /viewer/recipient-selection` | client-localなchat recipient選択の通知event |
| `POST /webhook/line` | LINE Messaging API Webhook。署名必須の正規path |
| `POST /webhook` | LINE Webhookの旧互換path |
| `POST /internal/assistant/notifications/line` | localhostのRenCrow_ASSISTANT専用LINE push transport |
| `/viewer/ai-workflow/*` | AI engineering workflow の experimental API |
| `/viewer/games/*` | RenCrow_GAMES bridge（status/decision/result/sessions/events/launch/observer proxy） |

`POST /viewer/api/chat`の`character_id`は`mio`、`shiro`、`kuro`、`midori`を受け付け、通常CHATのrecipientとしてorchestratorへ引き渡します。responseの`character_id`表示だけを切り替えて、実際のAgentをMioへ固定してはなりません。

### Game Launch（マルチペルソナ WP5）

`POST /viewer/games/launch` は、ペルソナが「遊びたい時に自分で起動する」
ための起動口（上位方針: `RenCrow_GAMES/docs/09_マルチペルソナプレイ仕様.md`）。

- Request: `{game_id, personas[], turns?, mode?, reason?}`。
  CORE 側の検証は `game_id` 必須のみ。タイトル・人数の capability 検証は
  observer 側 launcher が正本であり、その 400 をそのまま透過する
  （二重管理によるドリフト防止）。
- 共有 observer の `POST /games/launch` へ転送する（base URL は observer
  proxy と同じ解決順: 設定 > `RENCROW_GAMES_OBSERVER_URL` > 既定。
  Linux 常駐では `systemd/user/rencrow.service.d/30-games-observer.conf`
  で設定する）。
- `reason`（動機）があれば起動成功時に**参加ペルソナ全員**の candidate
  イベントとして記録する（i 番目のペルソナは Turn=-(i+1)。言い出しっぺは
  `play_game`、誘われた側は `invited_to_play` + `invited_by`）。
  observer は `launching` を楽観返却するため、spawn がその後失敗しても
  動機イベントは残る（「遊ぼうとした」経験として扱う）。candidate store
  未設定時は記録されず `motive_recorded=false` になる。
  記録失敗は起動失敗にしない。
- Response: `{ok, game_id, session_id, status, motive_recorded}`。
  upstream 到達不能は 503、upstream エラーは status code を透過する。

実際に有効な endpoint は build と config に依存します。process supervisorは`/health/live`だけを再起動判定に使います。利用者向け機能の確認では`/health`と`/viewer/status`も確認し、featureがunavailable/degradedの場合は成功として扱わないでください。

LINE WebhookはPOSTだけを受け付けます。Tailscale公開guardはtailscaledが`Tailscale-Funnel-Request`を付けたinternet trafficでは`POST /webhook/line`だけを追加許可し、旧`/webhook`とViewer／Debug／Ops pathを404にします。tailnet内のServe trafficは従来のViewer系allowlistを維持します。`GET /webhook/line`の404、署名なしPOSTの401は故障判定に使いません。LINE Developersへ登録するendpointはdeployment時点の公開hostを確認して`https://<current-host>/webhook/line`とし、旧hostを仕様へ固定しません。外部到達確認ではMessaging APIのWebhook testを使い、署名検証済みeventが200になることを確認します。

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

`model_alias` や旧 route alias は互換経路であり、新規 client の primary contract にしません。指定 recipient が利用不能な場合に別 recipient へ黙って fallback しません。

recipientは物理ModelやExecution Roleの直接選択ではありません。COREがroute、Agent、
Execution Roleを確定した後、RenCrow_LLM Gatewayへ現行互換の論理execution aliasを送ります。

### 現行execution aliasの互換契約

| alias | 現在のAgent／Role binding | 備考 |
| --- | --- | --- |
| `mio` | Mio／Chat | Agent IDやModel名として再解釈しない |
| `shiro` | Shiro／ChatWorker | Shiroの通常CHAT |
| `worker` | Shiro／Worker | 主にShiroのOPS。内部background利用時もCOREがAgent contextを保持する |
| `midori` | Midori／Wild | Agent IDやModel名として再解釈しない |
| `kuro` | Kuro／Heavy | Codex、Heavy、Model名そのものではない |

これらはCOREからRenCrow_LLMへ送るopaqueな互換wire keyであり、Agent ID、Execution Role ID、
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
| `execution_alias` | Gatewayへ送った現行互換wire key |
| `role_profile_revision` | Gatewayが解決したRole profileのrevision |
| `target_id` | Gateway内部のTarget識別子 |
| `provider` | local／外部API／Agent Runtimeのprovider |
| `model` | 実際に使用したModel。取得不能時はunknown |

通常client向けresponseは物理Target情報を必須とせず、監査・診断surfaceだけが権限に応じて
表示します。未知Agentは`UNKNOWN_AGENT`、既知Agentに対する未対応Roleは
`UNSUPPORTED_ROLE`、有効なbinding先の停止は`TARGET_UNAVAILABLE`として区別し、別Agentや
別providerの成功へ丸めません。

`POST /viewer/recipient-selection`は`viewer_client_id`と`recipient`を受け、`viewer.recipient_selected`を観測eventとして発行します。選択状態はclient-localであり、COREのglobal stateにはせず、実際の送信先は`POST /viewer/send`の`to`を正とします。

`POST /viewer/send`は`message`、`to`に加えて、clientを追跡できる場合は`viewer_client_id`、`input_source`（`text | stt | unknown`）、`user_id`、`device_name`を受けます。`input_source`の未知値は400で拒否します。`user_id`と`device_name`は観測用metadataであり、認証・認可には使用しません。PORTALに利用者認証がない現行構成では`user_id=viewer-user`、`device_name`はブラウザが公開するOS／platform名であり、端末hostnameではありません。

COREは受付時に`job_id`、root `trace_id`、利用者発話の`message_id`を発行します。`POST /viewer/send`の受付responseは`job_id`、`trace_id`、`message_id`、`viewer_client_id`、`recipient`を返します。現行のroot `trace_id`は`job_id`と同じopaque値です。同じ処理から発行する`message.received`、`agent.response`、error eventは同じ`trace_id`を持ち、`message.received.message_id`は受付responseの`message_id`と一致します。Agent発話は利用者発話とは別の`message_id`を持ちます。

`message_id`は`msg_` prefix付きUUIDのopaque値です。clientは形式を解析せず、SSE再接続・再送時の重複排除と、同じ発話に由来する表示・保存の対応付けに使用します。`turn_index`は表示順の補助であり、IDの代替にしません。受付・開始・完了・errorログには同じ`trace_id`と`job_id`を、会話本文を持つlogには対応する`message_id`を記録します。TTS eventはmessage確定後なら同じ`message_id`を持ち、stream開始時に未確定なら従来どおり`response_id`で応答へ対応付けます。

`POST /viewer/character-runtime`は1 Roundの`trace_id`、`user_message_id`、各Turnの`message_id`と`turn_index`を返します。`trace_id`は全Turnで共通、`message_id`は利用者発話と各Character発話で別のUUIDです。

受付・開始・完了・errorログには`operation_source`、`input_source`、`user_id`、`device_name`、`source_ip_masked`、`source_ip_hash`、`user_agent`も記録します。接続元IPは生値を記録せず、IPv4は末尾octetをマスク、IPv6は`/64`へマスクし、同一接続元の相関用hashを併記します。`session_id`は会話sessionの単位であり、1 request / responseの完了判定には使いません。

`X-RenCrow-Client: RenCrow_CMD`で送られたterminal text chatは音声を消費しないため、COREはTTS sessionを開始しません。PORTAL／Debug Viewerなど音声再生能力を持つclientのTTS契約は維持します。client provenanceは観測と出力能力の選択に使う情報であり、認証・認可の代替にはしません。

streaming生成では、COREはRenCrow_LLMから受けた本文deltaを`agent.thinking`として逐次発行し、終端後に完成本文を`agent.response`として1回発行します。対話clientはdeltaを逐次表示してよいが、永続化と完了判定には`agent.response`を使用します。backendが最終SSE chunkに`usage.completion_tokens`と`timings.predicted_per_second`を返す場合、COREは同じ`job_id`の`metrics.latency` eventを`kind=llm`、`point=throughput`、`completion_tokens`、`tokens_per_second`付きで発行します。clientはこの値がある場合だけ実token throughputとして表示し、本文delta数をtoken数として扱いません。

対話clientは、送信受付から同じ`job_id`を持つ利用者向け`agent.response`または終端error eventまで、送信時のrecipientを固定します。この区間に別recipientへ切り替えたり、別`job_id`の応答でpending状態を解除したりしてはいけません。

TTSの`tts.audio_chunk`と`tts.session_completed`は同じ`session_id`、`response_id`を持ちます。clientは全chunkの再生終了とsession完了の両方を確認してから、response単位で`POST /viewer/tts/playback-ack`を1回だけ送ります。
`GET /viewer/tts/audio?url=...`が取得できるremote音声は、COREのTTS設定にあるbase URLと同一hostのものだけです。

`GET /viewer/idlechat/status`の`forecast_stock`は、`enabled`、`total`、`capacity`、`missing`、`filling`、最終生成状態と、6ドメインの`topics`を返します。これは観測用snapshotであり、GETによって生成・消費・補充を開始しません。

`GET /viewer/idlechat/collection`は、`status`、`skill_id`（`core.build-daily-source-brief`）、`schedule`、`timezone`、`fetched_at`、`next_run_at`、ニュース件数、Wikipedia件数、カテゴリ／source別件数、`items`、`sources`、`tools`を返します。分析状態は`enrichment_status`（`pending`、`enriching`、`ready`、`partial`、`fallback`）、`enrichment_provider`、`enrichment_error`、`enriched_at`で確認できます。収集後の分析は`Worker`が記事を1件ずつ完了させ、`enriching`中も完了済み記事を順次snapshotへ反映します。`ChatWorker`は使用しません。`items`はtitle、category、source、`source_type`、元URL、`source_read_status`（`ready`／`unavailable`／`unprocessed`）、`source_read_url`、原文の日本語訳`translated_body`、`summary`、事実と分離したShiroの`perspective`、`term_notes`を持ちます。`term_notes`は用語、説明、確認方法、確認元URL、`contextual`／`confirmed`／`unresolved`／`unavailable`の状態を返します。表示順は「原文翻訳 → サマリ → Shiroの見解 → 用語補足」です。`sources`はcredentialを除いた取得先設定を持ちます。このGETは現在のプロセス内cacheをコピーして返す観測用snapshotであり、収集、分析、再収集、cache消費、Memory昇格を開始しません。

`GET /viewer/movie-catalog?action=movies|people`は一覧項目に`familiarity`、`sentiment`、`assessed`を返します。映画の`familiarity`は`seen | unseen | ""`、俳優の`familiarity`は`known | unknown | ""`、`sentiment`は共通で`like | dislike | ""`です。`POST /viewer/movie-catalog/preference`へ`kind`（`movie | person`）、`target_id`、`target_label`、`dimension`（`familiarity | sentiment`）、`value`、`generated_by`を送ると一方のdimensionだけを更新し、他方を維持します。空の`value`はそのdimensionを明示的な未選択へ戻します。Viewer内部のwrite APIであり、PORTALへ自動公開しません。

Economic APIで新しいOpportunityを作ると、未指定の`trace_id`はCOREが生成します。EconomicTask、Delivery、RevenueEvent、Reflectionの作成では、参照元Opportunityまたは上流entityの`trace_id`を引き継ぎ、別の値へ黙って付け替えません。`POST /viewer/revenue/deliveries`は`delivery_id`、`trace_id`、`delivery_kind`、`status`、任意の上流IDとtarget/evidenceを受けます。`external_action=true`かつ`status=completed`では`approval_id`と`evidence`が必須です。

`POST /viewer/revenue/opportunities/workstream-goal`は`opportunity_id`と`workstream_id`を受け、draft Goal、pending-review Artifact、`decision_type=economic_opportunity_execution`のpending Approvalを同じ`trace_id`で保存して返します。既存Opportunityに`trace_id`がない場合は、このuse caseが生成してOpportunityへ保存します。responseの`external_actions_applied`は`false`であり、このAPI自体は外部side effectを実行しません。

## Interaction client共通意味論

PORTAL、CMD、ASSISTANTは、COREとのInteractionで次の意味論を共有します。

| 能力 | contract |
| --- | --- |
| Chat | requestごとに利用者scopeと明示recipientを持ち、別recipientへ黙ってfallbackしない |
| IdleChat | status／event購読と開始／停止を分け、write権限のないclientから操作しない |
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
| `RenCrow_PORTAL` | `portal-chat` | PORTAL Chat allowlist |
| `RenCrow_PORTAL` | `portal-idlechat` | IdleChatの読み取り |
| `RenCrow_CMD` | `cmd-chat` | Chat送信とevent購読 |
| `RenCrow_CMD` | `cmd-idlechat` | IdleChat status／event／start／stop |
| `RenCrow_ASSISTANT` | `assistant-core` | COREへのChat送信とevent購読 |

COREは既知clientのprofile欠落、client/profile不一致、profile外method/pathを403で拒否します。
profile headerは認証credentialではなく、既存のendpoint allowlist、TLS、network境界、
server-side authorizationを置き換えません。共通SDKは実caller間の重複が確認されるまで
先行作成しません。

## Client の注意

- method、status code、content type を確認する。
- unknown field を許容し、既存 field の意味を推測で変更しない。
- write/action endpoint は approval、idempotency、request provenance を保持する。
- SSE は再接続と重複 event を考慮する。
- debug/admin API を public network へ直接公開しない。

## PORTAL公開境界

`RenCrow_PORTAL`はCOREの全APIを透過公開しません。

- `IdleChat`: `GET /viewer/events`、`GET /viewer/idlechat/status`などの読み取りだけを許可する。
- `Chat`: IdleChatの読み取りに加え、chat、recipient通知、active audio/input ownership、TTS再生、STT入力に必要な公開契約だけをallowlistとする。
- COREへのproxy requestはmodeに応じて`portal-chat`または`portal-idlechat` profileを付ける。
- 旧`view`、`live`、`lab`のpage modeとAPI prefixは受理しない。
- Debug、Ops、Repair、LLM管理、設定変更APIはPORTALから遮断する。
- 新しい公開操作はCORE側のAPI追加だけで自動公開せず、PORTAL側でmethod/pathと契約テストを追加する。

## ASSISTANT連携境界

`RenCrow_ASSISTANT`はAgent対話、調査、生成、継続Taskへ昇格する場合だけCORE Public APIを利用します。利用者ID、household、許可scope、request／task相関IDを維持し、必要最小限のcontextだけを送ります。

- 目覚まし、生活Routine、PUSH、acknowledgement、snooze、端末retryはASSISTANT側の契約とする。
- COREのDebug、Ops、Repair、LLM管理APIをASSISTANTから利用しない。
- CORE unavailable時はASSISTANTがAgent処理をdegradedとして扱い、別Agentの成功へ丸めない。
- 専用endpointを追加する場合は、既存Viewer内部APIの無制限な再公開ではなく、認証、scope、idempotency、監査を含むpublic contractとして定義する。
- ASSISTANTのPUSHを第二の会話systemにせず、CORE応答を利用者、source、category、
  correlation ID付きのInteraction outputとして元のdeliveryへ戻せるようにする。
