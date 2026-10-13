# STT発話単位認識の設計とFailure Knowledge（2026-10-13）

## 文書情報

- 日付: 2026-10-13 JST
- owner: RenCrow_CORE STT入力境界（`modules/stt`、`cmd/rencrow/stt_runtime_*`）
- 契約正本: `docs/06_Public_API仕様.md`「WS /stt」、`docs/04_アーキテクチャ概要.md`のSTT経路、`docs/05_設定リファレンス.md`の`stt`
- 範囲: Viewer → CORE `/stt`（WebSocket）の入力側のみ。CORE → RenCrow_STTのHTTP契約、port、target、本番設定は変えない。
- 状態: source実装済み・試験済み。本番配備と本物の音声認識によるE2Eは未実施（RenCrow_STTの外部engineを置き換える作業の完了後）。

## 概要

`/stt` WebSocketハンドラは、Viewerが送る100 ms（3200 byte、16 kHz PCM16）のframeごとに`provider.Transcribe`を呼び、
`final`を「最後のdraft」にしていた。0.1秒の断片は認識にならず、続けて話すと`busy_policy: queue_latest`が古い要求を捨てた。
これを、発話ごとにPCMを貯めて**発話につき1回だけ**認識する形（A）に直した。途中結果を出す形（B）は後から足せるように、
`utterance_id`・`seq`・予約`partial`の規則だけ先に固定した。

## 発生条件

- 旧WebSocket橋渡し（別gatewayのWSへ中継）を削除したcommit `7c6fc835`（2026-08-30「STTを正規HTTP経路へ統一」）以降、
  Viewerのマイク入力すべて。橋渡しでは外部gatewayが音声を貯めていたが、置き換え後のハンドラは貯めずに1 frameずつHTTPへ渡した。
- 本番のjournalに直近3日のSTT利用の記録は無く、利用実績では裏付けられていなかった。

## 事実

- 旧`handleSTTWebSocketProvider`は、非無音のframeごとに`provider.Transcribe`を呼び、結果を`draft`として送っていた。`final`は
  `stop`時の最後のdraft、またはdraft停止・無音から1.2秒後の自動finalだった。
- `provider`は`ActionProvider`で包まれ、`Transcribe`1回ごとにTask・Run・Actionを作る。frame単位の認識は、1秒の発話で10組を作っていた。
- `busy_policy`の既定`queue_latest`は待ち1件だけを保持して古い要求を捨てる。`stt.timeout_ms`の既定は8000だった。
- Viewerは発話ごとにWSを開き、`start`、binary frame、末尾300 msの無音、`stop`を送り、`final`を受けたらWSを閉じる。VADは
  Viewer側（開始レベル12、終了レベル8、無音700 ms）にある。プリロール、最小・最大発話長は無かった。
- 既存の本番配線試験は1 frameだけを送り、スタブのgatewayは任意のWAVを受理した。frame単位の認識でも通る形だった。

## 結論と設計

分類: 発話の貯蔵・長さ判定・WAV化・定型句除去・event生成は`CLI`（決定的処理、`modules/stt`）。WebSocket受付（同一originの
認証済みViewer）、HTTP契約、取り消しと切断、再試行しない失敗の返却は`Boundary`（`cmd/rencrow`）。`LLM`は使わない。

### 制御メッセージ

- `start{utterance_id?, sample_rate?, channels?, format?}` / `stop{utterance_id?}`（`final_pending`は別名）/ `cancel{utterance_id?}`。
  音声は16 kHz mono PCM16LEのbinary frame（WAV frameも受理）。`utterance_id`は任意で、無ければCOREが`srv-<n>`を割り当て、
  `speech_start`で通知する。`stop`は`utterance_id`を省略すると進行中の発話に適用する。
- eventは`session_info`、`ready{protocol,max_utterance_ms}`、`speech_start`、`final`、`error{code}`、`cancelled`。すべてに
  セッション内で単調増加する`seq`が付き、発話に属するものは`utterance_id`を持つ。
- 各発話は`final`／`error`／`cancelled`のどれかをちょうど1つ受け取る。認識しなかった発話（無音、短い、定型句、空の結果、
  進行中の発話が無い`stop`）は、`text=""`と`empty_reason`を持つ`final`で終わる。旧Viewerは`error`でtoastを出すため、空の`final`の方が
  安全で、`final`を待つクライアントが止まらない。

### 発話の終わりの判定

主はViewerのVADと`stop`制御。CORE側は安全弁だけを持つ。

- `max_utterance_ms`（既定30000）: 超えたら`end_reason=max_length`で`final`を返し、続きを新しい発話として受ける。
- `idle_timeout_ms`（既定2000）: 発話中にframeが届かなければ`end_reason=idle_timeout`で確定する。Viewerの無音700 ms＋末尾300 msより長い。
- 音のエネルギーによる発話の分割はCOREでは行わない（Viewer VADの二重化を避ける）。

### 最小長・最大長・プリロール

- 最小長: 20 msの窓で平均絶対値が`STT_SILENCE_ABS_THRESHOLD`（既定220）以上の窓の合計（有声長）が`min_speech_ms`（既定150）未満なら
  RenCrow_STTを呼ばない（`silent`／`too_short`）。クリック音や咳はここで除かれる。トリミングはしない（engine側のVADに任せる）。
- 最大長: 上記`max_utterance_ms`。
- プリロール: ViewerがVADの発火前の300 msを保持し、発話の先頭に付ける（`stt_utterance_utils.js`）。VADは音が閾値を超えてから
  発火するため、語頭が欠けていた。トリガのframeはプリロールに入れず、二重に送らない。

### final を捨てない busy

- セッションごとに、確定した発話を直列のworkerで認識する。続けて話しても順序どおりに全件が認識される。
- プロバイダ共通の`busy_policy`の既定を`queue_latest`から`direct`に変えた。engine（whisper-server）自身が直列化する。`queue_latest`は
  明示指定した場合だけ残る。空・未知の値は`direct`。
- 1セッションで認識待ち・認識中にできる発話は8件。超えた発話は`code=stt_busy`の`error`で、他の終端eventと同じ順序で返す（黙って捨てない）。
- 切断すると、認識中の要求を中断し（`context`取り消し。外部engineは切断で推論を止める）、待っている発話を捨てる。

### 幻覚フィルタ

保守的に、**発話全体が**既知の定型句と一致する場合だけ空の`final`（`empty_reason=filtered`）にする。小文字化・全角ASCIIの畳み込み・
空白／句読点／記号の除去のあとで完全一致を比べる。既定は動画の締めの挨拶に限り、「ありがとうございました」単独のように
実際に発話されうる語は入れない。`stt.noise_phrases`で追加できる。engine側のsilero VADによる抑制とは独立した最後の砦。

### 設定

`stt.min_speech_ms`／`max_utterance_ms`／`idle_timeout_ms`／`noise_phrases`を追加。`timeout_ms`の既定を8000から60000に、`busy_policy`の
既定を`direct`に変更（1回の要求が最大30秒の発話を運ぶため）。構造体が読まない旧key（`provider`／`language`／`model`／`provider_url`／
`stream_url`）は、起動を止めず、keyだけをWARNに出す。`timeout_ms`が`max_utterance_ms`より短い場合も警告のみ（既存の設定を拒否して
起動できなくしない）。

### Bへの拡張点

- `partial`eventと、`utterance_id`・`seq`の規則（同じ`utterance_id`で`seq`が大きいpartialが前を置き換え、`final`が確定する）は予約済み。
- 追加で要る変更は、`UtteranceBuffer`の途中経過を一定間隔でRenCrow_STTへ送るpartial用のレーンと、partialは古い要求を捨て（`queue_latest`相当）、
  finalは捨てない（直列）というレーン別のbusy方針。プロバイダ共通の方針を1つにしたままpartialを足さない。
- 切断時の取り消し、`cancelled`、`utterance_id`の一意性は既にAで固定している。

### 互換性

`utterance_id`の無い旧Viewerと`scripts/stt_e2e_probe.py`は、`start`／binary／`stop`／`final`待ちのまま動く。Viewerの`final`処理は空textを無視する。
`draft`はもう送らない。最小長により、旧来の「3200 byteを1回送って`final_pending`」は`too_short`の空`final`になる。

## Failure Knowledge

### Failure

`/stt`のWebSocket入力が、旧橋渡しを削除したcommit `7c6fc835`以降、実質的に認識できない状態だった。本番の直近3日にSTT利用の記録も無かった。

### Problem

100 msごとの断片を別々に認識し、最後の断片の結果を`final`にしていた。断片は認識にならず、続けて話すと古い要求が捨てられ、
frameごとにTask・Run・Actionが作られた。

### Cause

外部gatewayが音声を貯める前提の橋渡しを削除したとき、置き換えのハンドラが「貯める責務」を引き継がなかった。確認する試験も無かった:
本番配線試験は1 frameだけを送り、スタブgatewayは任意のWAVを受理したため、frame単位の認識でも通った。利用実績（ログ）で
壊れていることに気づく仕組みも無かった。

### Lesson

入力が連続する経路の配線試験は、複数frameを流して「外部呼出しが発話につき何回、どの形で行われたか」まで検査する。外部呼出しの
スタブは受け取った内容（形式、長さ、回数）を検証し、任意の入力を受理しない。利用実績のない機能は、動いている前提にしない。

### Invariant

1. 1発話は、RenCrow_STTへの認識要求をちょうど1つ生む（frame単位で呼ばない）。
2. 各発話は`final`／`error`／`cancelled`のどれかをちょうど1つ受け取り、終端eventは発話の順に届く。
3. 既定の設定で、認識した`final`を黙って捨てない（`queue_latest`は明示した場合だけ）。
4. CORE → RenCrow_STTの要求は`file`（`audio.wav`、16 kHz mono PCM16）と`response_format=json`だけ。
5. 発話の分割・定型句の除去・event形式の規則は`modules/stt`が持ち、`cmd/rencrow`はそれを呼ぶだけにする。

### Enforcement

- `cmd/rencrow`の本番配線試験が、10 frameの1発話でRenCrow_STTがちょうど1回、契約どおり呼ばれることを検査する（スタブgatewayが形式・長さ・回数を拒否する）。
- `modules/dependency_rules_test.go`の`TestCompositionRuntimeUsesModuleSTTSessionRules`が、ハンドラがmodule規則へ委譲し、event形式や`utterance_id`等を
  直書きしないことを強制する。
- `internal/infrastructure/stt`の契約試験が、multipartの形（`file`と`response_format`だけ、`X-RenCrow-Request-ID`）を固定する。
- `modules/stt`と`internal/adapter/config`の既定値試験が、`busy_policy=direct`・`timeout_ms>=2*max_utterance_ms`を固定する。
- `config.yaml.example`の読込み試験が、exampleに未知の`stt`keyが無いことを検査する。

### Tests

- `modules/stt`: `utterance_test.go`、`noise_phrases_test.go`、`control_test.go`、`utterance_events_test.go`、`busy_policy_test.go`、`runtime_plan_test.go`
- `cmd/rencrow`: `stt_runtime_websocket_utterance_test.go`（発話ごとに1回、`stop`で`final`、最大長・無通信での区切り、最小長、続けて話した`final`の保持と順序、
  定型句、切断とcancelでの取り消し、WAV形式、旧クライアント、満杯時の順序つきerror）、`stt_runtime_factory_test.go`（本番配線＋HTTP契約）、`main_stt_gateway_test.go`
- `internal/infrastructure/stt`: `busy_default_test.go`、`provider_contract_test.go`
- `internal/adapter/config`: `config_stt_test.go`
- Viewer: `internal/adapter/viewer/stt_utterance_utils.test.mjs`、`viewer_stt_https.test.mjs`

## 未解決・要判断

- **本物の音声認識によるE2E**は未実施。外部engineの配備後に、Viewerまたは`scripts/stt_e2e_probe.py`で正規の経路（`/stt`→RenCrow_STT→engine）を通す。
- **Viewerは、`stop`後に`final`を待つ間の次の発話を送らない**（`isStopping`中の音声は破棄され、`pendingSpeechRestart`は立つだけで使われていない）。
  Aで`stop`から`final`までが数秒以下なら取りこぼしは小さいと見込むが、実測が要る。Viewerの`stop_to_final`メトリクスで測り、必要なら
  同一WSで`stop`直後に次の`start`を送る形へ変える（CORE側は対応済み）。
- **Viewerは、発話が完了するたびに`persistSTTArtifacts`で同じWAVを`/viewer/stt/autotest`経由で`/stt`へ再送する**（STT負荷が実質2倍になり、
  次の発話の`final`が認識の待ちに入りうる）。今回は変更していない。デバッグ用途なので、本番では無効化を検討する。
- **本番の`core.yaml`**の`stt.timeout_ms: 8000`は明示値なので既定の変更では直らない。60000への変更が要る（変えないと、30秒近い発話は
  タイムアウトしうる。CORE起動時に`stt.timeout_ms`のWARNが出る）。旧key（`provider`／`language`／`model`／`provider_url`／`stream_url`）は削除してよい。
  `busy_policy`が`queue_latest`と明示されているなら`direct`（または削除）へ。
- **Viewerは`end_reason=max_length`の`final`を受けると`finalReceived`を立てる**。その後にマイクを手動でOFFにすると、`stop`を送らずWSを閉じるため、
  30秒を超えた発話の残りは捨てられる（無音による自動終了の経路は影響しない）。必要ならViewerで`max_length`のfinalは`finalReceived`を立てない。
- `scripts/stt_e2e_probe.py`は、空text（`too_short`等）の`final`を「timed out」ではなく`empty final (<empty_reason>)`の失敗として報告するようにした。
- RenCrow_STT側: whisper-serverの応答`text`に改行が入るため、RenCrow_STTで整形する（CORE側では整形しない）。
- 1セッション内の直列待ち（CORE側）は`timeout_ms`に含まれない。HTTP要求を出してからの時間だけが対象で、engineの直列待ちはそこに含まれる。
  複数セッションを同時に使うと、engineの直列待ちが積み上がって`timeout_ms`を超えうる。

## 関連ファイル

- `modules/stt/utterance.go`、`noise_phrases.go`、`control.go`、`session_rules.go`、`busy_policy.go`、`runtime_plan.go`
- `cmd/rencrow/stt_runtime_websocket.go`、`stt_runtime_config.go`、`stt_runtime_factory.go`
- `internal/infrastructure/stt/busy_provider.go`、`provider.go`
- `internal/adapter/config/config_stt.go`、`config_defaults.go`、`config_validation.go`
- `internal/adapter/viewer/assets/js/stt_utterance_utils.js`、`viewer.js`、`viewer.html`
- `config/config.yaml.example`
