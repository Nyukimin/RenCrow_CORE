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
| `/viewer/status`, `/viewer/agents` | runtime と agent の状態 |
| `GET /viewer/idlechat/status` | IdleChat状態と読み取り専用の`forecast_stock` snapshot |
| `POST /viewer/idlechat/start`, `POST /viewer/idlechat/stop` | IdleChatの開始・停止。認可されたwrite clientだけが利用する |
| `/viewer/jobs`, `/viewer/logs` | job と監査可能な log |
| `/viewer/backlog`, `/viewer/scheduler` | 継続作業の照会・操作 |
| `/viewer/workstreams/*` | goal、artifact、annotation、heartbeat、review |
| `/viewer/advisors/*`, `/viewer/agents/profiles` | Advisor run/score と AgentProfile |
| `/viewer/revenue/*` | Opportunity、EconomicTask、RevenueEvent、Reflection、approval |
| `/viewer/memory/*` | memory event と Recall の観測 |
| `/viewer/active-control`, `/viewer/tts/*`, `/viewer/stt/*` | audio/control bridge |
| `POST /viewer/recipient-selection` | client-localなchat recipient選択の通知event |
| `/viewer/ai-workflow/*` | AI engineering workflow の experimental API |
| `/viewer/games/*` | RenCrow_GAMES bridge（status/decision/result/sessions/events/launch/observer proxy） |

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

Scheduler run logの`status`は`completed`、`failed`に加えて`deferred`を返す場合があります。`deferred`はGPUなどの実行資源が使用中で、ジョブの`next_run_at`を近い再試行時刻へ更新した状態です。

## Chat recipient contract

Viewer 通常 chat の宛先は次の値を使用します。

```text
mio | shiro | kuro | midori
```

`model_alias` や旧 route alias は互換経路であり、新規 client の primary contract にしません。指定 recipient が利用不能な場合に別 recipient へ黙って fallback しません。

`POST /viewer/recipient-selection`は`viewer_client_id`と`recipient`を受け、`viewer.recipient_selected`を観測eventとして発行します。選択状態はclient-localであり、COREのglobal stateにはせず、実際の送信先は`POST /viewer/send`の`to`を正とします。

`POST /viewer/send`は`message`、`to`に加えて、clientを追跡できる場合は`viewer_client_id`、`input_source`（`text | stt | unknown`）、`user_id`、`device_name`を受けます。`input_source`の未知値は400で拒否します。`user_id`と`device_name`は観測用metadataであり、認証・認可には使用しません。PORTALに利用者認証がない現行構成では`user_id=viewer-user`、`device_name`はブラウザが公開するOS／platform名であり、端末hostnameではありません。

COREは受付時に`job_id`を発行し、HTTP responseの`job_id`、`viewer_client_id`、`recipient`と、同じ処理から発行する`message.received`、`agent.response`、error eventの`job_id`を一致させます。受付・開始・完了・errorログには同じ`job_id`とともに、`operation_source`、`input_source`、`user_id`、`device_name`、`source_ip_masked`、`source_ip_hash`、`user_agent`を記録します。接続元IPは生値を記録せず、IPv4は末尾octetをマスク、IPv6は`/64`へマスクし、同一接続元の相関用hashを併記します。`session_id`は会話sessionの単位であり、1 request / responseの完了判定には使いません。

対話clientは、送信受付から同じ`job_id`を持つ利用者向け`agent.response`または終端error eventまで、送信時のrecipientを固定します。この区間に別recipientへ切り替えたり、別`job_id`の応答でpending状態を解除したりしてはいけません。

TTSの`tts.audio_chunk`と`tts.session_completed`は同じ`session_id`、`response_id`を持ちます。clientは全chunkの再生終了とsession完了の両方を確認してから、response単位で`POST /viewer/tts/playback-ack`を1回だけ送ります。
`GET /viewer/tts/audio?url=...`が取得できるremote音声は、COREのTTS設定にあるbase URLと同一hostのものだけです。

`GET /viewer/idlechat/status`の`forecast_stock`は、`enabled`、`total`、`capacity`、`missing`、`filling`、最終生成状態と、6ドメインの`topics`を返します。これは観測用snapshotであり、GETによって生成・消費・補充を開始しません。

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
capabilityで制限します。profile名を使う統一認可設定と共通SDKは未実装であり、現行の
endpoint allowlistとserver-side authorizationを置き換えたとは扱いません。

## Client の注意

- method、status code、content type を確認する。
- unknown field を許容し、既存 field の意味を推測で変更しない。
- write/action endpoint は approval、idempotency、request provenance を保持する。
- SSE は再接続と重複 event を考慮する。
- debug/admin API を public network へ直接公開しない。

## PORTAL公開境界

`RenCrow_PORTAL`はCOREの全APIを透過公開しません。

- `view`: `GET /viewer/events`、`GET /viewer/idlechat/status`などの読み取りだけを許可する。
- `lab`: viewの読み取りに加え、chat、recipient通知、active audio/input ownership、TTS再生、STT入力に必要な公開契約だけをallowlistとする。
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
