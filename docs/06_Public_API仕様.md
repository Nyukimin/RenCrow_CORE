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
| `POST /viewer/send`, `GET /viewer/events` | PORTAL／CMD等のmessage・添付送信とSSE event購読 |
| `GET/POST /viewer/character-runtime` | Character一覧、複数Character Roundと会話ID |
| `/viewer/status`, `/viewer/agents` | runtime と agent の状態 |
| `GET /viewer/idlechat/status` | IdleChat状態と読み取り専用の`word_topic_stock`、`forecast_stock`、`episode_stock`、`topic_stock_playback` snapshot |
| `POST /viewer/idlechat/playback` | Topic Stockを`play`、`next`、`previous`で再生。任意選択時だけ`item_id`を指定する |
| `GET /viewer/idlechat/collection` | 日次収集の入力cache、次回04:00 JST、取得元、利用toolの読み取り専用snapshot。ユーザー向けニュース取得のAPIではない |
| `POST /viewer/idlechat/start`, `POST /viewer/idlechat/stop` | IdleChatの開始・停止。認可されたwrite clientだけが利用する |
| `POST /viewer/surface-presence` | PORTAL Chat／IdleChat画面の期限付き在席を通知し、COREが排他的な有効modeを決定する |
| `/viewer/jobs`, `/viewer/logs` | job と監査可能な log |
| `/viewer/backlog`, `/viewer/scheduler` | 継続作業の照会・操作 |
| `/viewer/workstreams/*` | goal、artifact、annotation、heartbeat、review |
| `/viewer/advisors/*`, `/viewer/agents/profiles` | Advisor run/score と AgentProfile |
| `/viewer/revenue/*` | Opportunity、EconomicTask、RevenueEvent、Reflection、policy decision |
| `GET/POST /viewer/revenue/deliveries` | trace付き汎用Deliveryの一覧・draft/状態record作成 |
| `/viewer/memory/*` | memory event、Recall、ProfilePromotion job の観測 |
| `GET /viewer/movie-catalog` | 映画・俳優catalogと利用者評価の一覧・詳細 |
| `POST /viewer/movie-catalog/preference` | 映画・俳優の認知・好み評価を保存 |
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
| `GET /viewer/trade/shadow/outcomes/report?study_id=<id>` | Shadow Outcome台帳を検証して読み取り専用集計を返す |

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
このAPIは外部I/O、Portfolio更新、Proposal、Intent、本人承認artifact、Orderを一切作りません。

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
`GET /viewer/trade/shadow/outcomes/report`は`study_id`を一つだけ受け取り、hash-chainを再検証して
Outcome待ち、label別件数、return／benchmark／excess returnを再計算します。これは読み取り専用で、
`review_required`を返しても採点完了、knowledge promotion、Portfolio更新、実行許可を意味しません。
`environment=SHADOW`、`authorizes_external_execution=false`、`portfolio_mutated=false`、
`knowledge_promoted=false`を返します。

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
  proxy と同じ解決順: 設定 > `RENCROW_GAMES_OBSERVER_URL` >
  既定`http://127.0.0.1:18796`。
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

`POST /viewer/send`は`message`、`to`に加えて、clientを追跡できる場合は`viewer_client_id`、`input_source`（`text | stt | unknown`）、`user_id`、`device_name`を受けます。`input_source`の未知値は400で拒否します。`user_id`と`device_name`は観測用metadataであり、認証・認可には使用しません。PORTALに利用者認証がない現行構成では`user_id=viewer-user`、`device_name`はブラウザが公開するOS／platform名であり、端末hostnameではありません。

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

`GET /viewer/movie-catalog?action=movies|people`は一覧項目に`familiarity`、`sentiment`、`assessed`を返します。映画の`familiarity`は`seen | unseen | ""`、俳優の`familiarity`は`known | unknown | ""`、`sentiment`は共通で`like | dislike | ""`です。`POST /viewer/movie-catalog/preference`へ`kind`（`movie | person`）、`target_id`、`target_label`、`dimension`（`familiarity | sentiment`）、`value`、`generated_by`を送ると一方のdimensionだけを更新し、他方を維持します。空の`value`はそのdimensionを明示的な未選択へ戻します。Viewer内部のwrite APIであり、PORTALへ自動公開しません。

`POST /viewer/movie-catalog/fetch`は`kind`、`query`または`url`、`max_pages`、
`follow_links`、`include_person_filmography`を受けます。COREは
`RENCROW_MOVIE_CATALOG_CRAWLER_URL`のRenCrow_Tools Go sidecarへ
`POST /v1/movie-catalog/crawls`を送り、完了jobの`artifact_url`、`artifact_sha256`、
`artifact_bytes`を検証します。取得したJSONLはCOREがtransaction内でSQLite正本へimportし、
不正record、空artifact、hash／size不一致では全体を失敗させます。sidecarはCOREのSQLiteへ
直接書きません。sidecar未設定または利用不能時はHTTP 503、
`status=unavailable`、`error_code=MOVIE_CATALOG_CRAWLER_UNAVAILABLE`を返し、
Python crawlerや別endpointへfallbackしません。このViewer write APIもPORTALへ自動公開しません。

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
| `RenCrow_CMD` | `cmd-control` | process制御とrepair実行 |
| `RenCrow_ASSISTANT` | `assistant-core` | COREへのChat送信とevent購読 |

`cmd-diagnostics`と`cmd-control`は、CMDが実装本体を持たずCORE Public API経由で
診断・運用を行うためのprofileです。影響の大きい操作を分離するため、状態を変更しない
読み取りと、process制御・repair実行を別profileにしています。

`cmd-diagnostics`が許可するのはGETのみで、対象は次のpathです。

```text
/health、/health/live、/ready
/viewer/status、/viewer/logs
/viewer/evidence/{recent,detail,summary}
/viewer/source-registry、/viewer/knowledge-memory
/viewer/debug/system
/viewer/channels、/viewer/channels/probe
/viewer/web-gather/doctor
```

`GET /viewer/channels`は設定済みchannelの一覧、`GET /viewer/channels/probe`は各channelの
疎通結果、`GET /viewer/web-gather/doctor`はweb-gatherの依存構成の診断結果を返します。
いずれも読み取り専用です。

web-gatherのurl／search／webwright-fetchとimport系はPublic APIへ公開しません。外部への
HTTPアクセスを伴う操作を公開するとCOREが任意URL取得の踏み台になり、import系はCOREホスト上の
パスに依存する設計になるためです。これらはCOREのCLIから実行します。

`cmd-control`が許可するのは次のpathです。制御結果の確認に必要な読み取りだけを併せて
許可し、対話系（`/viewer/send`、`/viewer/events`、IdleChat）は対象外とします。

```text
POST /viewer/repair/run
POST /viewer/source-registry
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
