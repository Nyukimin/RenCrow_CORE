# Viewer state inventory 2026-05-27

## 結論

`viewer.js` / `idlechat.js` には、UI 一時状態として妥当な state と、backend 正本の写しに留めるべき state と、正本に見えてしまう危険な派生 state が混在している。

追記: この棚卸し後の同日修正で、P0 と P1 の主要項目は working tree 上で対処済み。

- IdleChat の audio fallback 表示/`status=fallback` ACK を error 表示/`status=error` に変更。
- live IdleChat の speaker / first pending fallback matching を廃止。
- TTS event から `chatActive` / `idleLiveActiveSessionId` を更新しないように変更。
- `/viewer/tts/playback-ack` に `message_id` / `turn_index` を追加。
- `viewer_client_id` を tab scoped に変更。
- `idleLiveSnapshotKey` を `sid:length` から transcript 内容ベースに変更。
- SSE dedupe key に `message_id` / `response_id` / `utterance_id` / `turn_index` / `chunk_index` を追加。

追記2: P2 残課題も commit 単位で整理済み。

- `8f43b73 docs: Viewer stateの責務境界を明示`
  - `state` / `ttsPlayback` / `idleLiveRenderedLog` の責務境界をコードコメントとソース検査で固定。
- `eafab07 fix: TTS応答単位の再生状態を整理`
  - TTS sync 内部 state を session gate / response ACK lifecycle / chunk dedupe に分類し、ACK 後に response 単位の一時 state を clear。
- `444be11 test: IdleChat描画ログを診断専用に固定`
  - `idleLiveRenderedLog` が transcript / pending TTS / ACK / session 進行の入力に使われないことをソース検査で固定。

直近の懸念である「reload 後に過去の `idlechat.message` 履歴を live TTS pending として扱い、対応する `tts.audio_chunk` が来ず `TTS_CHUNK_TIMEOUT` になる」経路は、現状ではかなり抑制されている。

- live mode では `isIdleLiveHistoricalEvent()` が page boot より古い `idlechat.message` を pending 化しない。
- `/viewer/idlechat/status` / `/viewer/idlechat/logs` の `active_transcript` は `hydrateIdleLiveTranscript()` に入り、DOM へ transcript 本文を描画せず、TTS pending も作らない。
- pending timeout は本文 fallback 再表示ではなく、`TTS_CHUNK_TIMEOUT` と ID 情報を表示する。

ただし、Viewer が持つには強すぎる派生 state がまだ残っている。特に `idlePendingMessages`、`idleLiveActiveSessionId`、TTS playback ACK 周辺の local Set/Map、fallback 表示/ACK 経路、multi tab の `viewer_client_id` 共有は、TTS/ACK/ID 消化の真因を隠す、または Viewer が session 進行の正本に近づくリスクがある。

## State 一覧表

| 変数名 | ファイル | 役割 | 書き込み箇所 | 読み取り箇所 | lifecycle / clear 条件 | ID との関係 | 分類 | リスク | 推奨対応 |
|---|---|---|---|---|---|---|---|---|---|
| `state.logs` | `viewer.js` | SSE/log 表示用イベント履歴 | `ingestEvent()` | timeline / summary / render | 最大 500 件で古いものを破棄 | event 全体 | UI一時状態 | 正本扱いすると危険 | 表示専用を維持 |
| `seenEventKeys`, `seenEventQueue` | `viewer.js` | SSE 重複表示抑制 | `ingestEvent()` | `ingestEvent()` | 最大 1000 key | `eventKey()` は type/from/to/route/job/session/channel/chat/timestamp/content ベース | 危険な派生状態 | `message_id` / `response_id` を直接含まず、重複判定が content/timestamp 依存 | event に構造化 ID がある場合は dedupe key に含める |
| `state.sessions`, `state.jobs`, `state.evidence`, `state.agents` | `viewer.js` | Viewer 表示用 snapshot/cache | fetch / event reducer | 各 panel render | reload で消える | job/session | UI一時状態 | backend 正本の写し以上に扱うと危険 | 表示 cache と明記 |
| `state.idleChat.mode`, `manualMode`, `chatActive`, `currentTopic` | `viewer.js`, `idlechat.js` | IdleChat status 表示/制御 | `/viewer/idlechat/status`, `/logs`, `isIdleChatActiveForTTS()` | IdleChat UI / TTS gating | status/logs refresh で更新 | session/topic | backend正本にすべき状態 | `isIdleChatActiveForTTS()` が TTS event から `chatActive` を true にできる | TTS event で active 正本を更新しない |
| `state.idleChat.interrupted`, `interruptedAt`, `interruptedSessionId` | `viewer.js`, `idlechat.js` | stale event 抑制 | control / status / TTS gate | `isStaleIdleChatEvent()`, `isIdleChatActiveForTTS()` | status active で reset | session_id | backend正本にすべき状態 | Viewer local interrupt が stale 判定の正本に近い | backend status の写しに寄せる |
| `state.idleChat.history` | `idlechat.js` | logs 表示用履歴 | `refreshIdleLogs()` | history panel | refresh ごとに置換 | session/message | UI一時状態 | transcript 正本に見える | 表示専用。TTS pending に接続しない |
| `state.idleChat.openIndex`, `selectedMode`, `selectedView` | `viewer.js` | UI 選択状態 | UI handler / localStorage | UI render | reload 後 localStorage 復元あり | なし | UI一時状態 | 低い | 現状維持 |
| `ttsPlayback.queue` | `viewer.js` | browser audio playback queue | TTS enqueue / play / fallback | audio sync | playback, reset, audio off | session/response/utterance/message/chunk | UI一時状態 | queue が ACK 判定にも影響する | queue は playback だけに限定し、ACK 正本は backend |
| `ttsPlayback.audio`, `playing`, `audioEnabled`, `unlocked`, `blocked`, `audioError` | `viewer.js` | audio UI/playback 状態 | audio controls / audio event | buttons / playback | audio event, reset, disable | current IDs を付随保持 | UI一時状態 | audio 状態と transcript 表示を混ぜると危険 | 表示/再生のみに限定 |
| `ttsPlayback.currentSessionId`, `currentResponseId`, `currentUtteranceId`, `currentMessageId`, `currentTurnIndex`, `currentChunkIndex` | `viewer.js` | 現在再生中 chunk の ID | enqueue/play/start | display, lipsync, ack | chunk end/reset | session/response/utterance/message/turn/chunk | UI一時状態 | ACK payload に `message_id` / `turn_index` が出ていない | ACK/error trace に既存 ID を追加 |
| `ttsPlayback.fallbackActive`, `fallbackTimer`, `blockedFallbackUtteranceId` | `viewer.js` | audio unavailable 時の text fallback/timing | audio off/error/block | playback flow | timer/reset | utterance/response | 危険な派生状態 | fallback 表示/`status=fallback` ACK が音声不具合を隠す | IdleChat では fallback ではなく error 表示へ |
| `viewerControl.clientId` | `viewer.js` | Viewer client 識別子 | `loadViewerClientID()` localStorage | active-control / ACK URL | localStorage 永続 | viewer_client_id | 危険な派生状態 | 同一ブラウザ multi tab が同じ ID を共有し得る | tab scoped ID を検討 |
| `viewerControl.activeAudioViewerId`, `activeInputViewerId` | `viewer.js` | active audio/input owner の local copy | active-control fetch/SSE | TTS handle / ACK gate | release / event | viewer_client_id | backend正本にすべき状態 | local copy が古いと ACK/再生 owner 判定がズレる | backend owner の写しとして扱い、TTL/refresh を明示 |
| `centralTTSSpeech`, `idleTTSSpeech` | `viewer.js` | 表示中 TTS bubble/lipsync 状態 | `setCentralTTSSpeechText()` / reset | display/lipsync | reset/end/session change | session/response/chunk | UI一時状態 | transcript 正本にしてはいけない | 現状通り表示用 |
| `idlePendingMessages` | `viewer.js`, `idlechat.js` | `idlechat.message` と後続 TTS chunk の待ち合わせ | `queueIdleMessageForTTS()` | `consumeIdlePendingMessage()` | TTS consume, timeout, session/topic clear | session/message/turn/from/to | 危険な派生状態 | Viewer が live 発話 pending の正本に近い。fallback matching あり | backend ID 優先、ID 不足時は error 化 |
| `idleLiveTopicKey` | `viewer.js`, `idlechat.js` | topic 境界検出 | `clearIdleLiveTimelineForTopic()` | same | topic key 変更で clear | session/topic/content | 危険な派生状態 | content/topic key 由来で session 境界を判断 | backend session/topic boundary を優先 |
| `idleLiveActiveSessionId` | `viewer.js`, `idlechat.js` | live 表示対象 session | topic/status/TTS gate | add/render/TTS gate | session change | session_id | backend正本にすべき状態 | TTS event から設定される経路がある | `/status` 正本へ寄せる |
| `idleLiveSnapshotKey` | `viewer.js`, `idlechat.js` | active_transcript hydrate dedupe | `hydrateIdleLiveTranscript()` | same | `sid:length` 変更 | session + row count | 危険な派生状態 | 同じ length の transcript 変更を検知できない | 表示に使うなら content hash/updated_at が必要 |
| `idleLiveRenderedLog` | `viewer.js`, `idlechat.js` | 描画診断ログ | render/error/validate | test/debug | 最大 200 件 | session/message/turn/error | UI一時状態 | 正本にすると危険 | 診断専用を維持 |
| `idleLiveBootedAtMs` | `idlechat.js` | live reload 履歴除外基準 | load 時 | `isIdleLiveHistoricalEvent()` | reload ごと | event timestamp | 危険な派生状態 | client clock/timestamp 欠落に依存 | backend から historical flag が理想 |
| `completedSessions`, `completedResponses` | `viewer.js` 内 `createChatAudioSync()` | TTS 完了 event の local 記録 | `markSessionCompleted()` | start gate / ACK gate | reset なし、page lifetime | session/response | 危険な派生状態 | ACK/再生開始の判断に影響 | response 単位へ寄せ、backend ACK 状態の正本にしない |
| `acknowledgedResponses` | `viewer.js` 内 `createChatAudioSync()` | 同一 page 内 ACK 重複防止 | `maybeAcknowledgeResponsePlayback()` | same | page lifetime | response | 危険な派生状態 | reload で消えるため ACK 済み正本ではない | backend が ACK idempotency を持つ |
| `responsePlaybackCounts`, `responsePlaybackResults` | `viewer.js` 内 `createChatAudioSync()` | response ごとの再生中 count/result | enqueue/end/fallback/error | ACK gate | count 0 / page lifetime | response | 危険な派生状態 | local queue と ACK 判定が結合 | ACK payload に詳細 ID/result を出す |
| `seenAudioResponses`, `seenUtterances` | `viewer.js` 内 `createChatAudioSync()` | TTS chunk 観測済み判定 | audio chunk enqueue | session_completed ACK gate | page lifetime | response/utterance | 危険な派生状態 | reload/reconnect で消え、ACK 判定が揺れる | backend 側 idempotency 前提にする |
| `blockedAckKeys` | `viewer.js` 内 `createChatAudioSync()` | autoplay block ACK 重複防止 | blocked/error path | same | page lifetime | response/utterance | 危険な派生状態 | local only | error ACK も backend idempotent に |
| `timelineAutoFollow`, `timelineUserInteracting`, `suppressTimelineScroll`, `activeViewerTab` | `viewer.js` | UI 操作状態 | UI handler/render | timeline/tab render | reload で消える | なし | UI一時状態 | 低い | 現状維持 |
| `derivedDirty` | `viewer.js` | 派生 view 再計算フラグ | event/fetch | render loop | render 後 clear | なし | UI一時状態 | 低い | 現状維持 |
| `state.memory`, `state.ops`, `state.debug` | `viewer.js` | 各 panel の表示 cache | fetch/render | panel render | refresh/reload | user/log/job | UI一時状態 | 正本化すると危険 | 表示 cache として維持 |

## UI 一時状態として妥当なもの

- DOM 表示用の `state.logs`, `state.sessions`, `state.jobs`, `state.evidence`, `state.memory`, `state.ops`, `state.debug`
- UI 選択用の `selectedMode`, `selectedView`, `openIndex`, `timelineAutoFollow`, `activeViewerTab`
- browser 再生機器としての `ttsPlayback.audio`, `playing`, `audioEnabled`, `unlocked`, `blocked`
- 表示中 bubble と口パク用の `centralTTSSpeech`, `idleTTSSpeech`
- 診断専用の `idleLiveRenderedLog`

これらは reload で消えてよく、backend status / live event で上書きされる前提なら Viewer が持ってよい。

## backend 正本へ寄せるべきもの

- IdleChat active session: `state.idleChat.chatActive`, `idleLiveActiveSessionId`
- transcript の正本: `state.idleChat.history`, `active_transcript` は写しに限定
- TTS ACK / playback completed: `acknowledgedResponses`, `responsePlaybackCounts`, `responsePlaybackResults` は local 判定に留める
- 発話 ID 消化: `idlePendingMessages.consumed`, `seenUtterances`, `seenAudioResponses`
- session/topic boundary: `idleLiveTopicKey`, `idleLiveSnapshotKey`
- interrupt/cancel/stale: `state.idleChat.interrupted*`, `isStaleIdleChatEvent()`

Viewer は ACK を送る endpoint の caller ではあるが、ACK 済み判定・発話消化・session 進行の正本になってはいけない。

## 危険な派生状態

### P0: TTS/ACK/ID 消化を壊し得るもの

1. `idlePendingMessages` の fallback matching
   - `consumeIdlePendingMessage()` は `message_id`、`turn_index` の後に、topic/speaker/first pending で消費できる。
   - `message_id` がある場合は優先されているが、ID 欠落時に別発話を消化し得る。
   - live IdleChat では ID 不足を推測で吸収せず、`IDENTITY_MISSING` / `MATCH_FAILED` のようなエラー表示にするべき。

2. `isIdleChatActiveForTTS()` が TTS event から active session を補正する
   - TTS chunk を受けた時点で `chatActive = true`、`idleLiveActiveSessionId = sid` にできる。
   - TTS event は playback の材料であり、IdleChat active session の正本ではない。
   - `/viewer/idlechat/status` または backend の session event を正本にするべき。

3. fallback text / fallback ACK
   - `startTextFallbackInternal()` は audio disabled/displayOnly 等で text fallback 表示し、条件次第で `status=fallback` ACK を送る。
   - IdleChat では「発話できなかった」ことを見えるエラーにするべきで、本文再表示や fallback ACK は真因を隠す。
   - pending timeout 側は `TTS_CHUNK_TIMEOUT` になっているが、audio unavailable 経路の fallback は別に残っている。

4. ACK payload の追跡 ID 不足
   - `/viewer/tts/playback-ack` には `session_id`, `response_id`, `utterance_id`, `viewer_client_id`, `status`, `error` が送られる。
   - `message_id` / `turn_index` は `ttsPlayback` item にあるが ACK payload に含まれていない。
   - backend が `response_id` 正本で扱えるとしても、Viewer-visible error とログ追跡では不足する。

### P1: reload / reconnect / multi tab で誤表示し得るもの

1. `idleLiveBootedAtMs`
   - historical event 除外として有効だが、client clock と event timestamp に依存する。
   - timestamp 欠落/未来時刻/clock drift では再発の余地がある。

2. `idleLiveSnapshotKey = sid + ':' + rows.length`
   - active transcript の dedupe が行数だけなので、同じ行数で内容や ID が変わっても検知しない。
   - 現状 transcript を描画/TTS pending 化していないため直撃しないが、将来の再利用で危険。

3. `viewerControl.clientId` が localStorage
   - 同一ブラウザ内の複数 tab が同じ `viewer_client_id` を共有し得る。
   - active audio owner や ACK sender の識別が tab 単位で曖昧になる。

4. `seenEventKeys`
   - structured `message_id` / `response_id` を直接 dedupe key に含めない。
   - reconnect 時に同一 ID だが timestamp/content 差があるイベントを別物扱いする可能性がある。

### P2: 将来事故りやすい複雑 state

- `state` monolith が Viewer 全体の表示 cache、IdleChat status 写し、runtime 操作状態を一つに持っている。
- TTS sync 内部の Set/Map が多く、session / response / utterance / message / chunk の lifecycle が読み取りづらい。
- `idleLiveRenderedLog` は有用だが、rendered log と transcript/log/status を混ぜる実装が入ると正本化しやすい。

## IdleChat/TTS 経路の詳細

### `idlechat.topic`

- `addIdleMsgToTimeline()` から `clearIdleLiveTimelineForTopic()` に入り、topic key 変更時に DOM、`idlePendingMessages`、`idleTTSSpeech` を clear する。
- live mode では topic 自体は中央 chat に描画しない。
- topic key は `session_id` / content / timestamp 由来であり、backend session/topic boundary の代替にしてはいけない。

### `idlechat.message`

- `ingestEvent()` から `addIdleMsgToTimeline()` に入る。
- stale/interrupt と historical guard を通過した live message だけが `queueIdleMessageForTTS()` で pending になる。
- live mode では pending bubble を即 DOM 表示せず、TTS chunk が来てから表示する。
- `message_id` と `turn_index` がある場合は dedupe/matching/sequence validation に使われる。

### `tts.audio_chunk`

- `createChatAudioSync().handleEvent()` で normalize され、active audio viewer と audio availability を見て queue される。
- IdleChat chunk は `idleLiveActiveSessionId` と異なる session の場合 drop される。
- chunk 表示時に `consumeIdlePendingMessage()` が pending `idlechat.message` を消費し、message bubble を chunk text で埋める。
- `message_id` exact match が優先される点は妥当。
- ただし ID 欠落時の fallback matching は危険。

### `tts.session_completed`

- `markSessionCompleted()` が local `completedSessions` / `completedResponses` を更新する。
- observed chunk がない response は ACK しないテストがある。
- playback count が 0 になってから ACK する設計は妥当だが、local state は reload で消えるため ACK 済み正本ではない。

### `/viewer/tts/playback-ack`

- Viewer が playback 完了/失敗/一部 fallback を通知する endpoint。
- 現状 ACK payload は `message_id` / `turn_index` を含まない。
- ACK の idempotency と正本は backend 側で持つべき。

### `/viewer/idlechat/status`

- `refreshIdleStatus()` が mode/manual/chatActive/currentTopic と active transcript を取得する。
- `hydrateIdleLiveTranscript()` は session change の clear と snapshot key 更新のみで、transcript rows を live TTS pending にしない。

### `/viewer/idlechat/logs`

- `refreshIdleLogs()` が history 表示用に `state.idleChat.history` を置換する。
- active transcript があれば同じく `hydrateIdleLiveTranscript()` に渡す。
- history は display-only に留める必要がある。

### live mode reload

- `idleLiveBootedAtMs` より古い `idlechat.message` は historical として捨てる。
- status/logs transcript は TTS pending を作らない。
- この範囲では「reload で過去発話が TTS pending になる」懸念は解消済みと言える。

### SSE reconnect

- `seenEventKeys` による dedupe がある。
- ただし dedupe key は structured ID 優先ではないため、同一 message/response の再送に強いとは言い切れない。

### audio OFF/ON

- audio OFF は active audio owner を release し、queue/playback を止める。
- 一方で text fallback 経路があり、IdleChat の発話失敗を fallback 表示/ACK にできる。
- no fallback 方針とはまだ衝突する。

### multiple viewer tabs

- active-control により audio owner は一つにしようとしている。
- しかし `viewer_client_id` が localStorage 由来なので、同一ブラウザ multi tab は同一 client と見なされ得る。
- tab 単位の ACK/owner 追跡としては弱い。

## 不変条件の確認

| 不変条件 | 現状 |
|---|---|
| Viewer reload で過去発話が TTS pending にならない | 概ね満たす。historical guard と transcript hydrate 非描画がある |
| status/logs 由来 transcript は live TTS 待ちを発生させない | 満たす。`hydrateIdleLiveTranscript()` は pending を作らない |
| TTS ACK の正本は backend 側にある | 設計上そうあるべき。Viewer local ACK dedupe は正本ではない |
| Viewer は ACK を送るが、ACK 済み判定の正本にならない | backend 実装確認が別途必要。Viewer 側は local dedupe を持つ |
| message_id がある場合は推測 matching より message_id を優先する | 満たす |
| message_id がない場合の fallback matching は危険として明示される | コード上は明示されていない。今回 P0 として分類 |
| session/topic 境界をまたぐ pending が残らない | topic/session change clear はあるが、topic key 由来なので完全ではない |
| fallback 表示でエラーを隠さない | pending timeout は満たす。audio fallback 経路は未解消 |
| エラー表示には session_id / message_id / response_id / utterance_id / error_code が出る | pending timeout は session/message/turn/error_code が出る。response/utterance は経路により不足 |

## fallback / error visibility の確認

解消済み:

- pending TTS timeout は本文全体を fallback 表示せず、`TTS_CHUNK_TIMEOUT` と ID 情報を出す。
- live historical `idlechat.message` は pending timer をセットしないテストがある。

未解消:

- audio disabled / displayOnly / audio error の経路には text fallback が残る。
- `status=fallback` ACK は「発話できていないが表示は済んだ」状態を正常系に近づける。
- ACK payload に `message_id` / `turn_index` がなく、Viewer error と backend ACK の突合が弱い。

## 修正優先度

### P0

1. IdleChat の audio fallback 表示/`status=fallback` ACK を error 表示へ変える。
2. live IdleChat の pending 消費から speaker/first pending fallback matching を外す、または明示 error にする。
3. TTS event から `chatActive` / `idleLiveActiveSessionId` を正本更新しない。
4. ACK/error trace に `message_id` / `turn_index` を含める。

### P1

1. `viewer_client_id` を tab scoped にする、または active-control に tab instance ID を追加する。
2. `idleLiveSnapshotKey` を `sid:length` から backend updated_at / transcript version / content hash に寄せる。
3. `seenEventKeys` を structured ID 優先にする。

### P2

1. `state.idleChat` と TTS playback local state の ownership コメントまたは型を追加する。
2. TTS sync 内部 Set/Map の lifecycle を response 単位に整理する。
3. `idleLiveRenderedLog` を診断専用として明文化する。

## 推奨する最小修正案

1. IdleChat の no fallback を P0 として固定する。
   - `startTextFallbackInternal()` / `showFallbackChunkInternal()` の IdleChat 経路では本文表示や `status=fallback` ACK を禁止し、`error_code` と既存 ID を出す。

2. pending matching を ID strict にする。
   - `message_id` があれば必ず `message_id`。
   - なければ `turn_index + session_id`。
   - それもなければ speaker fallback せず、`TTS_IDENTITY_MISSING` として表示する。

3. active session の正本を backend status に戻す。
   - `isIdleChatActiveForTTS()` は TTS event を受け入れるかどうかだけ判断し、`chatActive` や `idleLiveActiveSessionId` を昇格しない。

4. ACK payload を既存 ID で拡張する。
   - 新 ID は増やさず、すでに chunk にある `message_id`, `turn_index` を送る。

## 追加テスト案

- live reload 後、古い `idlechat.message` が pending timer を作らない。
- `/viewer/idlechat/status` の active transcript が同一 session / 同一 length で変化しても、TTS pending を作らない。
- IdleChat `tts.audio_chunk` に `message_id` / `turn_index` がない場合、first pending を消費せず error 表示になる。
- audio disabled / audio_url missing / audio error で IdleChat 本文 fallback と `status=fallback` ACK が出ない。
- 同一ブラウザ multi tab で `viewer_client_id` または tab instance が区別され、非 active tab が ACK しない。
- `/viewer/tts/playback-ack` に `message_id` / `turn_index` が含まれる。
