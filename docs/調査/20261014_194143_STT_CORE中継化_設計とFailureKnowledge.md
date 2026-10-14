# STT入力のCORE中継化の設計とFailure Knowledge（2026-10-14）

## 文書情報

- 日付: 2026-10-14 JST
- owner: RenCrow_CORE STT入力境界（`modules/stt`、`internal/infrastructure/stt`、`cmd/rencrow/stt_runtime_*`）
- 契約正本: RenCrow_STTの「CORE-STTストリーム契約」第3.3版（RenCrow_STT repositoryのcommit `81750f7`。実装は第3.1版の`1bbce60`を基に始め、第3.2版（`ee91243`）に合わせ、
  所見A（Failure Knowledge 2）で第3.3版の`lastStartID`の定義に合わせた）と、CORE側のレビュー結果（同契約の13節に対応表）。
  client側の契約は `docs/06_Public_API仕様.md`「WS /stt」、設定は `docs/05_設定リファレンス.md`の`stt`、経路は `docs/04_アーキテクチャ概要.md`のSTT経路
- 置き換えるもの: `docs/調査/20261013_171543_STT発話単位認識_設計とFailureKnowledge.md`（Invariant 1・4・5と、CORE内の蓄積・区切りの設計。同文書に「Superseded」の見出しを付けて残す）
- 状態: source実装済み・試験済み（Go、Viewerのnode試験、実ブラウザ1本）。**本番へは未配備**。本物のRenCrow_STTのストリーム口を通した正規経路のE2Eは、隔離したCOREで実施済み
  （契約9.4節）で、所見A（Failure Knowledge 2）の修正後の再測定が残る。

## 概要

`/stt`は、Viewerの`start`／`stop`で発話を区切る前提で、COREが発話を貯めて最大長・無通信・最小有声長の安全弁だけを持っていた（2026-10-13の設計）。
PORTALは`start`を1回送って無音を含むPCMを流し続けるため、**30秒かマイクOFFでしか`final`が出なかった**。利用者は、話し終わりの判定を
RenCrow_STTへ集約すると決めた。RenCrow_STTは音声ストリームの口（`GET /v1/audio/stream`）を持ち、無音で発話を区切り、発話ごとに認識結果を返す。
COREの`/stt`はそのストリームへの**中継**になり、発話の蓄積・区切り・WAV化・定型句の除去・認識要求を持たなくなった。

## ADR: 発話の区切りをRenCrow_STTへ移し、`/stt`を中継にする（2026-08-30の決定を覆す）

### 背景

2026-08-30のcommit `7c6fc835`（「STTを正規HTTP経路へ統一」）は、外部gatewayのWebSocketへ橋渡しする旧経路を削除し、COREの`/stt`が
`POST /v1/audio/transcriptions`だけを呼ぶ形にした。橋渡しでは外部gatewayが音声を貯めていたが、置き換えのhandlerは貯めず、2026-10-13に
発話単位の蓄積をCOREへ足した（上記の置き換える文書）。この設計は「音のエネルギーで発話を区切らない」（Viewer VADの二重化を避ける）を前提にし、
VADを持たないPORTALでは発話が終わらなかった。

### 決定

1. 話し終わりの判定（無音による区切り、プリロール、最小有声長、最大長、無通信の打切り）と定型句の除去は、RenCrow_STTが所有する（利用者の決定）。
2. COREの`/stt`は、RenCrow_STTの音声ストリームへ接続ごとに1本中継する。中継するのは音声と、`start`／`stop`／`cancel`の制御だけ。
3. COREが認識要求を出す経路は、HTTP経路（`/stt/file`、`/stt/chat-input`、rencrowctl）だけに残る。
4. STTが落ちているとき、`/stt`をHTTP経路へ切り替えるfallbackは置かない（旧新の併存になり、同じ発話に二つの区切りの規則が働くため）。接続ごとに明示のエラーを返す。

### 選択肢と退けた理由

| 選択肢 | 退けた理由 |
|---|---|
| COREにVADを足す | 判定のownerが二つになる。PORTALのためにCORE側の閾値を持つことになり、STTの録音環境ごとの閾値と分かれる。利用者の決定でもある |
| STTのHTTP転写にサーバ側VADを足す | 1回のHTTP要求は発話の終わりを知らない。連続PCMの発話の区切りを返せない |
| 接続（セッション）単位のRunで記録する | `CreateAndStartRun`は並列枠を消費し、既定の`Global: 3`は`ReadOnly`のTaskも数える。PORTALはマイクONの間、何時間も接続を保ち、枠の1/3を占有し続ける |
| `speech_start`でRun admissionし、拒否なら`utterance.cancel`を送る | storeの書込みが遅いと、cancelが`final`の後に届いて効かない |

### 結果として受け入れるもの

- COREの`/stt`はRenCrow_STTのstream口に依存する。「新CORE＋旧STT」ではストリームを開けず、`/stt`は使えない（`/viewer/modules/health`の`stt.stream`が`stream_not_supported`を示す）。
  配備の順序は、STT（追加のみ）→ PORTAL → 隔離したCOREでの確認 → 本番COREの置き換え。ロールバックはCOREを先に戻し、STTはその後。
- 記録の順序が逆転する。従来は「Run admission → 外部効果」だったが、認識の外部効果はSTTの中で起きるため、「外部効果 → 事後の記録」になる。
  拒否の理由は並列枠（容量の帳簿）であり、policyの判定ではない。副次効果として、STTの可否が`Global`枠に左右される状態が解消し、枠に左右されるのは記録の側になる。
- 発話の区切りの既定は700ms。TTSの日本語12本を700msで区切ると、文の中の息継ぎで22発話に割れる特性があり、PORTALでは割れた片ごとに別々のchat送信になる。
  利用者の判断（反応の速さを優先）で受け入れた。値はRenCrow_STT側のstt.jsonで変える。
- `final.audio_ms`の意味が「貯めた長さ」から「認識に渡した長さ」に変わる。`end_reason`に`silence`が加わる。`error.code`が`PROVIDER_*`から`STT_TARGET_*`等に変わる。

## 発生条件

- PORTALからの音声入力すべて。`start`を1回送り、無音を含む連続PCMを流し続けるため、COREの安全弁（最大長30秒、またはマイクOFFの`stop`）でしか`final`が出なかった。
- 今回の変更で新たに現れる条件（ViewerでSTTが先に発話を閉じる）: RenCrow_STTが`silence`で発話を閉じ、`final`がViewerの`stop`より先に届く。
  ノイズでViewerのVADが誤発火して空の`final`が即返る、運用者が`silence_end`を短くする、上流の異常でCOREが接続を閉じる、のとき。

## 事実

- 旧Viewerは`stop`の後に`final`を待つ作りで、`final`が`isStopping=false`のときに届いてもWSを閉じなかった。`error`の早着も、COREが上流の異常の後に接続を閉じた場合も、
  `ws.onclose`が`!isStopping`のときは何もしなかった。VADが無音で`stop`を試みると、`ws`が`null`か、STTが閉じた発話を指す`stop`で、`STT_FINAL_WAIT_TIMEOUT_MS`（90秒）を待った。
  実ブラウザ（`scripts/stt_viewer_browser_e2e.js --fake-stt-early-final`）で、旧`viewer.js`は`stop`を送り、WSを閉じず、次の発話を受けられないことを再現した。修正後は同じ場面で通る。
- `golang.org/x/net/websocket`（v0.54.0）の挙動は、実測でCORE側の設計に影響した: (1) pongはライブラリ内で消費され、アプリから見えない。(2) 握手を拒否されると
  HTTP statusが`ErrBadStatus`に潰される。(3) `SetWriteDeadline`は書き込みごとに残るため、受信側がpongやcloseを自分で書くときに、期限切れで黙って失敗する。
  (4) 断片化したframeの継続はOpCodeが直前の種別に書き換えられ、検出できない。
- 旧handler（約800行）は、蓄積・job queue・finalize・transcribe・WAV化・有声判定・未送信event上限を持っていた。これらと`stt_runtime_audio.go`、
  `modules/stt`の蓄積・定型句・WAV変換、`stt`設定の4 keyと環境変数`STT_SILENCE_ABS_THRESHOLD`を削除した。
- 記録のtxの実測（軽い負荷の窓。契約13節のR6）: write_commitがp50 12ms、p95 19ms、lock待ち0s。実害の主因は`ErrParallelLimit`（`Global=3`、`ReadOnly`のTaskも数える）。

## 結論と設計

分類: 区切り・定型句・WAV化・event生成・認識要求は**RenCrow_STTの`CLI`**（決定的処理）。COREの中継は**`Boundary`**（WebSocket受付、上流の接続・timeout・死活、
終端の保証、書込み、記録、切断）で、COREの`CLI`の規則（`modules/stt`の`RelayTable`、`ParseSessionReady`、`SplitStreamFrames`）は決定的に書かれている。`LLM`は使わない。

| 層 | 場所 | 責務 |
|---|---|---|
| 規則（I/Oなし） | `modules/stt/relay.go` | client制御の写像、上流eventのallow-listとseq検査、`RelayTable`（終端待ちid表、`lastStartID`、重複した終端の破棄、記録対象の判定、watchdog期限）、`session_ready`の検証、frameの分割 |
| 健康診断 | `modules/stt/stream_health.go` | STTの`/health`の`stream`を`/viewer/modules/health`の`stt.stream`に写す（ready / blocked / live） |
| ストリームのclient | `internal/infrastructure/stt/stream_client.go` | frameの種別を保つCodec、dial（3秒）とhandshake＋`session_ready`（合わせて5秒）の自前のtimeout、固定Origin、最後の受信時刻、握手拒否のHTTP status |
| 記録 | `internal/infrastructure/stt/utterance_recorder.go` | 1発話＝短命なTask／Run／Action。非同期・有界・best-effort |
| 中継handler | `cmd/rencrow/stt_runtime_websocket.go` | 接続の組立。1つのsession loopが全状態と全書込みを持つ（clientの切断と上流の異常が競合しても、先にloopへ届いた方が経路を決める） |

中継の要点:

- **終端はちょうど1つ（中継の境界で強制）**: 終端待ちのid表（上限64）で、同じidの2つ目の終端と終端の後のeventを捨てる。上流が切れた・1011で閉じた・30秒黙った・
  契約を破った・watchdogが期限切れのときは、終端のない発話ごとに`error(stt_failed)`を開始順に返し、続けてsession単位の`error`を返して閉じる。
  STTが自分でsession単位の`error`を返していれば、COREは重ねない。補った終端は記録しない。
- **Run admissionは発話に先行しない**: 記録は終端をclientへ転送した後に、非同期でqueueへ渡す。認識は並列枠に左右されない。
- **記録は有界**: queue 32、満杯はdropして計数、`ErrParallelLimit`だけ有限回（3回、4秒・8秒・16秒）再試行、shutdownは5秒の有界のflush。dropと失敗は
  `request_id`と`utterance_id`を付けたERRORログと、`stt.stream`のmetadataの計数に残る。
- 記録の規則は1つ: 終端の`target_called`が`true`。STT発の発話単位の終端で欠けていれば、記録する側に倒してWARNと計数する。clientの切断で終端を受ける前に残った発話は
  「clientの切断で中断」のActionとして記録し、`target_called`は`unknown`と書く。
- transport receiptは作らない。receiptはRun・Action・Attemptの束縛を要求するが、dialの時点ではActionが無く、接続単位のRunも発話に先行するRunも禁じられている。
  相関は、Action名`stt.stream:<request_id>:<utterance_id>`と要約に載せる`request_id`・`utterance_id`で取る。
- **id無しの`stop`／`cancel`に補うid（`lastStartID`）**: その接続で`start{utterance_id}`を転送していれば、補うidは**RenCrow_STTが最後に通知した`speech_start`の`utterance_id`**で、
  `speech_start`のたびに更新する（契約5.1・3.7のj.）。`start`のidは、最初の`speech_start`までの仮の値にすぎない。`utterance_id`を付けた`stop`はそのidのまま転送し、
  `start`を転送していない接続（PORTAL）のid無しの`stop`は、id無しのまま進行中の発話を閉じる。最後に通知された発話がすでに閉じていれば、補ったidは終わった発話を指し、STTは何もしない
  （silence-firstで`final`が先に届いたViewerに、2つ目の`final`が出ない）。
- `seq`の空間のownerはCOREに一つだけ置く。STTの`seq`はhop内の単調増加の検査にだけ使い、転送しない。
- 死活はアプリ層（ping 10秒ごと、最後の受信から30秒）で判定する。SSHトンネル越しのTCP keepaliveは死活の検知にならない。
- Viewerは、発話中に終端（`max_length`以外の`final`、`error`）か予期しない`close`を受けたら、`stop`を送らずに発話を終えてsocketを閉じる（判定は`stt_utterance_utils.js`の純関数）。
  `final`のtextは先にchatへ渡し、閉じる処理はその結果に依らず実行する。判定は`final_ignored`と`error`の`finalReceived`の早いreturnより前に置く。

## Failure Knowledge

### Failure

PORTALから音声を流しても、30秒かマイクOFFにするまで`final`が出なかった。また、話し終わりの判定をSTTへ移すと、Viewerが`final`の早着で90秒止まる欠陥が表に出る状態だった。

### Problem

発話の区切りを、音声を持たない側（COREとクライアントのVAD）が決める設計だった。クライアントごとにVADの有無が違い（Viewerは持ち、PORTALは持たない）、
VADを持たないクライアントは発話を終わらせる手段を持たなかった。COREは「Viewer VADの二重化を避ける」ために音のエネルギーを見ないと決めていたので、
PORTALの側で補う場所も無かった。

### Cause

発話の終わりを決める責務と、音声を持つ責務が別の層に置かれていた（2026-10-13の設計の前提）。さらに、Viewerの「`stop`の後に`final`を待つ」という暗黙の順序の前提は、
CORE側にしかなかった不変条件（Viewerだけが発話を区切る）に依存していて、検査もなかった。

### Lesson

- 発話の終わりを決める責務は、音声を最初から最後まで見ている層が持つ。判定のownerを一つにし、他の層はヒントとして扱う。
- 中継は「終端がちょうど1つ」を境界で証明する。上流の保証を信じて素通ししない（上流が落ちた・遅い・重複したときの補完と破棄をここで持つ）。
- 判定のownerを動かすと、暗黙の順序を前提にしていたクライアントが壊れる。クライアントの前提を、実ブラウザの再現（旧実装では失敗する試験）で確かめてから動かす。
- 中継の試験は、相手の本物のprotocolを話す代役を実WebSocketで立て、**呼ばれてはならない経路が呼ばれたら失敗する**ようにする。
- 外部ライブラリが隠す事実（pongの消費、握手のstatus、書込み期限の残り方）は、実測で確かめてから設計に入れる。

### Invariant

1. 認識要求を出すのはRenCrow_STTだけ。COREの`/stt`は認識要求を出さず、1発話につき`final`のための認識要求はSTTの中でちょうど1つ（STTの契約）。
   COREはその証跡（`target_called`）を発話ごとに記録する。〔旧1「1発話はRenCrow_STTへの認識要求をちょうど1つ生む（COREが呼ぶ）」を置き換える〕
2. 各発話は`final`／`error`／`cancelled`のどれかを**ちょうど1つ**受け取り、終端eventは発話の開始順に届く。**COREの中継の境界で強制する**（id表、重複した終端の破棄、上流の異常の補完）。
3. 認識した`final`を、COREは黙って捨てない（捨てるのは、同じ発話の2つ目の終端と、終端の後のeventだけ）。
4. COREとRenCrow_STTの間の契約はストリーム（`stt.stream.v1`）で、`/stt`では`session.start`、固定のOrigin、音声（binary）と制御（text）をframeの種別で送る。HTTP経路の契約
   （`file`と`response_format=json`だけ、`X-RenCrow-Request-ID`）は`/stt/file`・`/stt/chat-input`だけに残る。〔旧4を置き換える〕
5. 中継のhandlerは、client・ストリームのeventとmessageを自前で組み立てず、制御の写像・上流eventの受理・終端の管理・event形式を`modules/stt`の規則へ委譲する。〔旧5を置き換える〕
6. **CORE側のRun admissionは発話に先行しない**。認識は並列枠に左右されず、枠に左右されるのは事後の記録だけ。
7. **記録は有界**で、欠けは計数される。queueは有界、満杯はdropして計数、再試行は有限、shutdownは有界のflush。失われた記録は`request_id`と`utterance_id`付きのERRORで残る。
8. 書込みの期限は書いた後に外す（受信側のライブラリがpongやcloseを自分で書く接続では、残った期限で黙って失敗するため）。
9. Viewerは、発話中にサーバが発話を終えた（`max_length`以外の`final`、`error`、予期しない`close`）とき、`stop`の後の`final`を待たずに発話を終える。

### Enforcement

- `modules/dependency_rules_test.go`の`TestCompositionRuntimeUsesModuleSTTSessionRules`が、中継のhandlerが`"type": "final"`等のeventやstreamのmessage、`"utterance_id"`等のfieldを
  自前で書かないこと、`ParseControl`・`MapControl`・`ParseUpstreamEvent`・`HandleUpstream`・`StampUtteranceEvent`等へ委譲することを強制する（Invariant 5）。
- `internal/application/taskmanager/started_run_closure_architecture_test.go`の`startedRunLedger`にrecorderを登録し、`taskmanagertest.VerifyStartedRunClosure`を適用する（Invariant 6・7の前提）。
- `cmd/rencrow`の本番配線試験（`stt_runtime_factory_test.go`）が、`buildSTTRuntime`から偽のストリームまで通し、`session.start`の項目、固定のOrigin、音声と制御の種別、
  HTTP転写routeが呼ばれないこと（代役が失敗させる）、transport receiptが作られないこと、記録されたActionを検査する（Invariant 1・4）。
- `modules/stt`と`cmd/rencrow`の中継の試験が、終端がちょうど1つ（重複の破棄、上流の断・遅延・seq逆行・id表の上限・watchdogの補完）、順序、allow-list、
  分け直し、WAVの拒否、不正なJSONの破棄、`seq`の付け替えを検査する（Invariant 2・3）。mutation試験で、重複の破棄・補完・記録・死活・watchdog・分割・種別の取り違えを
  一つずつ外すと試験が落ちることを確かめた。
- `internal/infrastructure/stt`の試験が、Codecの種別の保持、握手拒否のHTTP status、受信時刻（pongを含む）、期限の段階、**Send後の書込み期限が外れていて遅れたpingに答えること**
  を実WebSocketと、TCPの中継で検査する（Invariant 8）。recorderは、有界のqueue・drop計数・`ErrParallelLimit`の有限の再試行・有界のflush・`target_called`の欠け・切断の中断を検査する（Invariant 7）。
- Viewerの純関数のnode試験（V1〜V5）、`viewer.js`の`onmessage`・`onclose`を実際に動かす試験（V7を含む）、source順序の試験、`viewer.html`の`?v=`の試験が、Invariant 9を強制する。
  実ブラウザ（`scripts/stt_viewer_browser_e2e.js --fake-stt-early-final`、V6）は、旧`viewer.js`では失敗し、修正後に通る。

### Tests

- `modules/stt`: `relay_test.go`、`stream_health_test.go`、`runtime_urls_test.go`、`control_test.go`、`limits_test.go`、`session_events_test.go`、`session_rules_test.go`、`audio_rules_test.go`
- `internal/infrastructure/stt`: `stream_client_test.go`、`utterance_recorder_test.go`、`action_provider_test.go`
- `cmd/rencrow`: `stt_runtime_websocket_test.go`、`stt_runtime_websocket_time_test.go`、`stt_runtime_relay_testutil_test.go`、`stt_runtime_factory_test.go`、
  `stt_runtime_stream_health_test.go`、`main_stt_gateway_test.go`、`stt_timing_trace_test.go`、`module_health_test.go`
- `modules/core`: `runtime_health_test.go`（`stt.stream`）。`internal/adapter/config`: `config_stt_test.go`（移した4 keyが無視され、名前だけが警告に出る）
- Viewer: `internal/adapter/viewer/stt_utterance_utils.test.mjs`、`viewer_stt_https.test.mjs`、`viewer_memory_panel.test.mjs`、`scripts/stt_viewer_browser_e2e.js`

## Failure Knowledge 2: id無しの`stop`が割れた後の続きの発話に届かない（所見A。2026-10-14の隔離E2E）

### Failure

隔離したCOREでViewer型（`start{id}`、音声、先に`stop`を送る）を流すと、息継ぎが700msを超えてRenCrow_STTが1つの発話を無音で2つに割った場合だけ、最後の`final`が`end_reason=idle_timeout`で閉じ、
`stop`から`final`まで約2.2秒かかった。割れなかった発話は、`stop`から`final`までp50 163msだった。

### Problem

Viewerの`stop`は`utterance_id`を持たず、COREが`lastStartID`を補って転送する。補うidが、息継ぎで先に閉じた発話を指していた。RenCrow_STTは「終わった発話を指す`stop`は何もしない」ので、
`stop`は無視され、続きの発話は無音の判定かidle_timeoutまで閉じなかった。

### Cause

`lastStartID`を、転送した`start`に対応する最初の`speech_start`のidで1回だけ確定し、以後は更新しない規則にしていた（契約第3.2版の5.1節のとおり）。
その前提は「続きは元のidでも宛先になる」で、これは最大長（`max_length`）の分割の続きにだけ成り立つ。無音で割れた後の続きは自動開始の別の発話で、元のクライアントのidを引き継がない
（契約第3.3版の3.7のi.）。COREは、STTが分割の種類で続きのidの扱いを変えることを知らずに、1つの規則で両方を扱えると仮定した。
当初のCOREの試験は、STTの代役が`stop`の宛先を見ないため、補ったidが終わった発話を指しても失敗しなかった。

### Lesson

- 補うidのような「相手の状態の写し」は、相手が最後に知らせた値に追随させる。クライアントが付けた値を固定して持たない。
- 相手が「宛先が違えば何もしない」と定めている操作では、宛先のずれが失敗として見えない（遅延として現れる）。代役の試験は、宛先を検査する。
- 実機の隔離E2Eで、分割（息継ぎが閾値を超える素材）とstop-firstの順序を両方流すまで、この欠陥は見えなかった。

### Invariant

1. COREがid無しの`stop`／`cancel`に補うidは、その接続でRenCrow_STTが最後に通知した`speech_start`の`utterance_id`である。`start`のidを固定して補わない。
2. `utterance_id`を付けた`stop`／`cancel`は、そのidのまま転送する（補うのはid無しだけ）。
3. `start`を転送していない接続のid無しの`stop`／`cancel`は、id無しのまま転送する。
4. 最後に通知された発話がすでに閉じていれば、補ったidは終わった発話を指し、STTは何もしない。COREは2つ目の終端を作らず、抑止もしない（意味判断を持たない）。

### Enforcement

- `modules/stt/relay_test.go`: `TestRelayTableAnIDLessStopFollowsTheLastAnnouncedSpeechStart`（STTがidを置き換えた場合、無音で割れた後の続き、最大長の続き。stop・final_pending・cancel）、
  `TestRelayTableAStopAfterTheLastAnnouncedUtteranceClosedNamesTheClosedOne`、`TestRelayTableNeverAddsAnIDOnAConnectionThatSentNoStart`、
  `TestRelayTableAnIDTheClientChoseIsNeverReplacedByTheAnnouncedOne`
- `cmd/rencrow/stt_runtime_websocket_test.go`: `TestSTTRelayAnIDLessStopNamesTheContinuationSTTAnnouncedAfterASplit`（実WebSocketの代役のSTTに、`start{id}`→`speech_start`→音声→先に閉じる`final`→続きの`speech_start`→
  id無しの`stop`／`final_pending`／`cancel`を流し、上流へ届く制御の宛先が続きであること、終端が発話ごとに1つであること）、
  `TestSTTRelayAnIDLessStopAfterSTTClosedTheLastUtteranceNamesTheClosedOne`（silence-first）、`TestSTTRelayAnIDedStopReachesSTTWithItsOwnID`
- 隔離したCOREでのE2E（`stop-first`と`silence-first`のViewer型）。割れた発話の`stop`から`final`、silence-firstで2つ目の`final`が出ないこと。

### Tests

上記。変更前の実装で、`TestRelayTableAnIDLessStopFollowsTheLastAnnouncedSpeechStart`と`TestSTTRelayAnIDLessStopNamesTheContinuationSTTAnnouncedAfterASplit`が、
続きの`srv-2`ではなく終わった`v1-1`が補われることで失敗することを確かめてから直した。

### 既知の挙動（契約第3.3版の範囲）

- 新しい`speech_start`が出た直後に、それを知らないCOREが`stop`を上流へ送る往復1回分の隙は残る。COREは埋めない（契約第3.3版の3.7のj.が定める範囲）。
- silence-firstで、`final`が先に届いた後の`stop`は終わった発話を指して何もしない。補わない場合に出る`no_active_utterance`の空`final`（2つ目の終端）は出ない。
- 契約の3.7のe.（id無しの`stop`は進行中の通知済みの発話に当たる）と5.1節（COREが補う）は、第3.2版では食い違っていた。第3.3版で、補うidの定義を改めて整合した。

## 資源上限

2026-10-13の表のうち、COREが持つ上限は次のとおり。蓄積・認識待ち・未送信eventの上限はRenCrow_STTへ移った（STTは接続ごとに認識待ち8、送信待ち64、同時接続32）。

| 経路 | 上限 |
|---|---|
| client → CORE の1 frame | 最大長30秒のPCM＋8 KiB（約0.96 MB）。超えれば終端のない発話へ`stt_limit_exceeded`を返して閉じる |
| 制御message | 4096 byte。超えるtext frameは捨てる（上流へは決して送らない） |
| 終端を待つ発話（id表） | 64。STTの待ちは8なので、届けばSTTの異常としてWARNを出して閉じる |
| 同時接続 | 16（STTは32。CORE側で解放済みの枠がSTT側でまだ数えられていても、16本目が503にならない） |
| 接続のidle | 5分。終端を待つ発話がある間は数えない |
| 書込み | 10秒（client・上流）。期限は書いた後に外す |
| 上流の握手と`session_ready` | 5秒（TCPは3秒） |
| 上流の死活 | pingは10秒ごと、最後の受信から30秒 |
| 終端の約束 | `speech_start`から`max_utterance_ms`＋`terminal_deadline_ms`＋60秒（`stop`を転送した発話は、転送から`terminal_deadline_ms`＋60秒）。値は`session_ready.limits`から取る |

認証の状態は変わらない: `/stt`のhandler自体に認証は無く、HTTP層のguardとクライアント側にだけある。中継しても権限は増えない。固定Originは認証ではない。

## 未解決・要判断

- **本物のRenCrow_STTのストリーム口を通した正規経路のE2E**: 隔離したCOREで2026-10-14に実施済み（結果は契約9.4節。順序違反0・意図しない`error`0。所見Aは終端の遅延で、不変条件の違反ではない）。
  所見Aの修正後の再測定（Viewer型のstop-firstとsilence-first）は未実施。
- **契約が暫定とする値の確定**: `target_timeout`（30秒）と`terminal_deadline`（75秒）は、GPU共有時の長い発話の`processing_ms`と、PORTALとViewer同時使用時の`queue_ms`の実測で確定する
  （契約の9.4節）。極端に遅いtargetでは`queue_depth`の末尾が`stt_deadline_exceeded`になる。
- **契約の曖昧さ・STTへの問い合わせ（記録のみ。CORE側は契約どおりに実装した）**:
  1. 〔解消: 第3.3版の定義と所見Aの修正で〕転送した`start`に対応する`speech_start`の取り違え。`lastStartID`を「最初の`speech_start`で1回だけ確定」にしていたため、自動開始の`speech_start`が先に
     届くと別の発話のidで確定し、以後のid無し`stop`が別の発話を指すおそれがあった。契約第3.3版は「STTが最後に通知した`speech_start`のid」に改めたので、取り違えの余地が無い。
  2. 〔解消: 同上〕`lastStartID`が終端の後も残り、`stop`が終わった発話を指す。契約第3.3版では、閉じ済みの発話を指す`stop`はSTTが何もしないと明記された（3.7のj.）。これは仕様で、
     silence-firstで2つ目の`final`を出さないために必要。実害は逆の場合（所見A）で、`start`のidを固定したために、無音で割れた後の続きの発話に`stop`が届かなかった。
  3. 「id付きの終端は必ず`speech_start`の後」を契約に明文化してほしい。CORE（`RelayTable`）は、待っていないidの終端を重複として捨てるので、STTが`utterance.start`を拒否して
     `speech_start`なしにid付きの`error`を返す経路があると、Viewerはその`error`を受け取れない。
  4. 接続の途中のWAV frame（S14）: CORE入口の拒否の形は、STT自身の形（session単位の`unsupported_audio_format`、そのframeだけ捨てて接続を維持）に揃えた。
     PORTALはsession単位のerrorでマイクを止める。
- **shutdownで記録のRunを残さない**: recorderの`Close`は、flush期限が切れた後、Runを開始済みの記録だけは、そのRunを閉じる書込み（期限と切り離され、最大`StartedRunTerminalWriteTimeout`）が終わるまで待つ。
  `Dependencies.Shutdown`のflushは5秒で、storeが詰まった最悪の場合だけ、プロセスの終了がその分（最大約61秒。systemdの停止猶予90秒以内）遅れる。
- 元から使われていないコード（`ApplyTimeoutFailure`等）と`debug.save_*`は、別の単位で消す。ViewerのVADをやめてPORTAL型へそろえることも後続。
- 本番`core.yaml`の旧key（`min_speech_ms`等）は、起動時のWARNが出るだけなので、配備の直後は消さない。安定後の運用作業で消す。
- 記録のdrop・失敗の数が許容できるかは、本番の負荷で見る（`stt.stream`のmetadataの`recorder_*`）。重い負荷のときのtxの所要と`ErrParallelLimit`の頻度は未確認。

## 関連ファイル

- `modules/stt/relay.go`、`stream_health.go`、`runtime_urls.go`、`control.go`、`session_rules.go`、`audio_rules.go`
- `internal/infrastructure/stt/stream_client.go`、`utterance_recorder.go`
- `cmd/rencrow/stt_runtime_websocket.go`、`stt_runtime_factory.go`、`stt_runtime_config.go`、`stt_runtime_stream_health.go`、`stt_timing_trace.go`、`module_health.go`
- `modules/core/runtime_health.go`
- `internal/adapter/config/config_stt.go`、`config_defaults.go`、`config_validation.go`、`config/config.yaml.example`
- `internal/adapter/viewer/assets/js/stt_utterance_utils.js`、`viewer.js`、`viewer.html`、`scripts/stt_viewer_browser_e2e.js`
