# IdleChat 即時停止仕様

作成日: 2026-05-23
対象: RenCrow / PicoClaw Viewer の通常 Chat 入力、STT、IdleChat、TTS、LLM 応答処理
ステータス: 仕様案

## 1. 目的

通常 Chat へのユーザー介入が始まった瞬間に、IdleChat を最優先で停止する。

ここでいうユーザー介入は、メッセージ送信完了や STT 発話完了ではない。
Chat 入力欄への入力意図、または STT 開始意思が確認できた時点で割り込みを発火する。

本仕様の目的は次の 2 点である。

- IdleChat 由来の LLM / STT / TTS / 表示更新が通常 Chat を塞がないこと
- 停止できなかった古い応答が後から返っても Viewer、会話状態、音声、口パク、prompt 注入へ混入しないこと

## 2. 用語

| 用語 | 意味 |
|---|---|
| 入力開始割り込み | ユーザーが通常 Chat 入力または STT 操作を始めた瞬間に IdleChat を止める操作 |
| 即時停止 | 完了待ちをせず、状態を最速で停止方向へ切り替えること |
| 遅延応答破棄 | cancel できなかった古い LLM / STT / TTS 応答を、後から到着しても採用しないこと |
| active generation | 現在有効な IdleChat session / job / response の世代 |
| stale event | 割り込み以前の generation に属するイベント |

## 3. 停止トリガー

IdleChat は次のトリガーで即時停止する。

1. STT ボタンを押下した瞬間
2. Chat 入力ボックスにユーザーが 1 文字でも入力した瞬間
3. paste
4. compositionstart
5. IME 入力開始など、入力意図が確定できる操作

focus のみでは停止しない。

送信ボタン押下、Enter 送信、STT final、ユーザー発話完了を待ってはいけない。

## 4. 基本方針

### 4.1 止める処理と捨てる処理を分ける

この仕様では、停止処理を次の 2 系統に分ける。

| 系統 | 内容 |
|---|---|
| 即時停止 | context cancel、WebSocket close、TTS session cancel、UI pending 状態 reset |
| 遅延応答破棄 | cancel 不能または間に合わなかった応答を generation / session / response 照合で discard |

停止できる処理だけを止めても不十分である。
外部 LLM / STT / TTS は、リクエスト送信後に cancel できない場合がある。
そのため、古い応答を採用しない仕組みを必須とする。

### 4.2 stop API は完了待ちしない

入力開始割り込み API は、LLM、STT、TTS、summary、quality_review、artifact persistence の完了を待たない。

API の成功は「停止要求を受理し、現行 generation を無効化した」ことだけを意味する。
実際の外部処理が完全に止まったことを意味しない。

### 4.3 通常 Chat を優先する

入力開始割り込みは、通常 Chat 送信や手動 STT 開始より前に実行する。
ただし、割り込み API の応答待ちで通常 Chat を遅らせてはいけない。

Frontend はローカル状態を先に reset し、サーバ通知は fire-and-forget を基本とする。

## 5. 責務境界

| 層 | 責務 |
|---|---|
| Frontend | 入力開始検知、ローカル UI reset、STT 即時 abort、古い event の discard |
| Viewer handler | 入力開始割り込み API の即時 ACK、reason / generation のログ |
| Message Orchestrator | 通常 Chat 開始より前に IdleChat busy / interrupt を確定 |
| IdleChat Orchestrator | active generation 無効化、context cancel、IdleChat state reset |
| STT runtime | 通常停止と即時 abort を分離、古い STT event を採用しない |
| TTS bridge / playback | IdleChat 由来 session / queue / pending playback を破棄 |
| LLM provider | context cancel を伝播。止められない応答は呼び出し元で discard |

Chat / Worker / Coder の責務分離は変更しない。
通常 Chat の応答生成を IdleChat 停止処理に依存させない。

## 6. 状態遷移

### 6.1 IdleChat

```text
idle_off
  -> idle_active
  -> interrupting(reason=user_input|stt_button|chat_send|worker_busy)
  -> interrupted
  -> idle_off
```

`idle_active` 中に入力開始割り込みが発生した場合:

1. Frontend が即時に IdleChat 表示 / pending / TTS 再生状態を reset
2. Frontend が `/viewer/idlechat/interrupt` を fire-and-forget で送信
3. Server が active generation を invalid 化
4. IdleChat Orchestrator が現行 context を cancel
5. 旧 generation の event / response / tts.audio_chunk は discard
6. 通常 Chat 入力または手動 STT を優先して継続

### 6.2 STT

```text
stt_off
  -> recording
  -> stopping_wait_final
  -> stopped

recording
  -> aborting_immediate(reason=user_input|idle_interrupt)
  -> stopped
```

通常停止では final を待ってよい。
入力開始割り込みでは final を待ってはいけない。

## 7. cancel / discard / no-wait 分類

### 7.1 cancel するもの

- IdleChat の現行 context
- IdleChat の LLM generate
- IdleChat の stream callback
- IdleChat の summary
- IdleChat の quality_review
- IdleChat 由来 TTS session
- IdleChat 由来 TTS pending playback
- STT WebSocket
- STT AudioContext
- STT MediaStream track
- STT reconnect timer
- STT final wait timer

### 7.2 cancel できない場合に discard するもの

- 外部 LLM から遅れて返る IdleChat 応答
- 外部 STT provider から遅れて返る draft / partial / final
- 外部 TTS provider から遅れて返る audio chunk
- IdleChat summary / quality_review の遅延結果
- stale generation の `idlechat.message`
- stale generation の `idlechat.summary`
- stale response の `tts.audio_chunk`

### 7.3 完了待ちしてはいけないもの

- 入力開始割り込み API
- STT immediate abort
- TTS 生成完了
- TTS 再生完了
- 口パク完了
- IdleChat summary
- IdleChat quality_review
- STT artifact persistence
- STT autotest

## 8. reset 対象

### 8.1 Frontend

- IdleChat talking / on 表示
- IdleChat live pending 表示
- IdleChat 由来 TTS queue
- IdleChat 由来 now playing 表示
- IdleChat 由来 lip sync / VTuber trigger pending
- STT chunk buffer
- STT draft buffer
- STT reconnect timer
- STT final wait timer
- STT WebSocket
- STT AudioContext
- STT MediaStream track
- STT draft / partial / final caption

ただし、ユーザーが入力中の Chat 入力欄の値は消してはいけない。

### 8.2 Server

- IdleChat `manualMode`
- IdleChat `chatActive`
- IdleChat `sessionMode`
- IdleChat `currentTopic`
- IdleChat `sessionContext`
- active generation
- IdleChat TTS pending map
- IdleChat background summary / review の有効性 marker

## 9. API 仕様案

### 9.1 `POST /viewer/idlechat/interrupt`

入力開始割り込み専用 API。

#### Request

```json
{
  "reason": "user_input",
  "source": "viewer",
  "client_generation": "optional"
}
```

`reason` は次を想定する。

| reason | 意味 |
|---|---|
| `user_input` | Chat 入力欄への入力 |
| `paste` | paste |
| `composition_start` | IME 入力開始 |
| `stt_button` | STT ボタン押下 |
| `chat_send` | 通常 Chat 送信直前 |
| `worker_busy` | Worker / Chat の実行開始 |

#### Response

```json
{
  "ok": true,
  "interrupted": true,
  "generation": "idle-...",
  "mode": "",
  "manual_mode": false,
  "chat_active": false,
  "current_topic": ""
}
```

この response は停止完了を保証しない。
停止要求の受理と active generation の無効化を表す。

## 10. Frontend 実装方針

### 10.1 入力欄

対象: `internal/adapter/viewer/assets/js/viewer.js`

`#inp` に次の event listener を追加する。

- `beforeinput`
- `input`
- `paste`
- `compositionstart`

`input` は既存の auto resize と併用する。
プログラムによる STT final 投入では、必要に応じて二重 interrupt を抑制する。

### 10.2 STT ボタン

STT ボタン click handler の先頭で入力開始割り込みを発火する。

STT が既に録音中の場合は通常停止ではなく、入力開始割り込み由来の即時 abort を使う。
新規 STT 開始の場合は、IdleChat を止めてから手動 STT を開始する。

### 10.3 STT 即時 abort

既存の通常停止は final 待ちを含むため、別関数を用意する。

```text
abortSTTImmediately(reason)
```

この関数は次を行う。

- `isRecording=false`
- `isStopping=false`
- final wait timer clear
- reconnect timer clear
- WebSocket close
- ScriptProcessor disconnect
- AudioContext close
- MediaStream track stop
- chunk / draft buffer clear
- draft / partial / final caption clear

artifact persistence と autotest は待たない。

## 11. Server 実装方針

### 11.1 IdleChat Orchestrator

`StopManualMode()` とは別に、割り込み専用の `Interrupt(reason string)` を追加する。

`Interrupt` は次を行う。

- active generation を invalid 化
- 現行 session context を cancel
- `manualMode=false`
- `chatActive=false`
- `sessionMode=""`
- `currentTopic=""`
- `sessionContext=""`
- `lastActivity=now`
- interrupt reason をログ

`StopManualMode()` は UI の明示停止。
`Interrupt()` は通常 Chat / STT 介入による強制停止。
責務を混同しない。

### 11.2 IdleChat event emit

IdleChat が `idlechat.message`、`idlechat.summary`、TTS chunk を emit する前に、generation の有効性を確認する。

無効な generation の event は emit しない。
emit 済みで Viewer に到達した event は Frontend 側でも discard する。

### 11.3 LLM context

IdleChat の LLM generate、summary、quality_review は、session context から派生した context を受け取る。
`Interrupt()` 時に context cancel が伝播すること。

外部 provider が cancel に応じない場合でも、戻り値は generation check で破棄する。

## 12. race condition 対策

次の競合を明示的に扱う。

| 競合 | 対策 |
|---|---|
| 入力開始と IdleChat LLM 応答が同時 | generation check で旧応答を discard |
| 入力開始と TTS chunk 到着が同時 | response_id / session_id で旧 chunk を discard |
| STT final と手入力が同時 | 手入力 generation を優先。旧 STT final は discard |
| interrupt API 応答前に Chat send | Chat send を進める。interrupt は fire-and-forget |
| IdleChat stop 後に summary が完了 | stale summary として discard |
| WebSocket close 後に STT message | stt generation 不一致で discard |

## 13. テスト計画

### 13.1 Frontend contract test

- Chat 入力欄に 1 文字入力した時点で `/viewer/idlechat/interrupt` が呼ばれる
- `/viewer/idlechat/interrupt` は `/viewer/send` より前に呼ばれる
- `paste` で interrupt する
- `compositionstart` で interrupt する
- `focus` だけでは interrupt しない
- STT ボタン押下で STT 開始より前に interrupt する
- STT immediate abort は final wait timer を作らない
- stale `idlechat.message` は描画されない
- stale `tts.audio_chunk` は再生 queue に入らない

### 13.2 Go test

- `IdleChatOrchestrator.Interrupt()` が `manualMode/chatActive/sessionMode/currentTopic/sessionContext` を即時 reset する
- `Interrupt()` が active generation を invalid 化する
- interrupt 後の旧 generation event は emit されない
- interrupt API は LLM / TTS 完了を待たずに ACK する
- 通常 `StopManualMode()` と `Interrupt()` の責務が混ざらない

### 13.3 E2E

- IdleChat 発話中に Chat 入力欄へ 1 文字入力し、即座に IdleChat 表示が off になる
- 入力後、古い Mio / Shiro の IdleChat 応答が Timeline / IdleChat live / TTS に混入しない
- IdleChat 発話中に STT ボタンを押し、IdleChat が即 off になり、手動 STT の結果だけが入力欄へ入る
- IdleChat summary / quality_review が後から完了しても、現在の Viewer 表示を更新しない

## 14. 未確認事項

- IdleChat の各 session runner が共通の cancellable context を使っているか
- summary / quality_review が session context の cancel を受け取るか
- IdleChat TTS pending map の一括破棄ポイント
- Viewer event stream で response_id / session_id / job_id の discard が十分にできるか
- STT provider / gateway が WebSocket close で推論中断できるか
- TTS provider が request cancel に応じない場合の audio chunk discard 境界

## 15. 実装時の注意

- 通常 Chat を速くするため、interrupt API の完了を Chat 送信前に長時間待ってはいけない。
- fallback で成功扱いしない。停止要求が失敗した場合は visible error として残す。
- 表示、音声、口パク、ログ、会話履歴、prompt 注入データを混同しない。
- 新しい ID を乱立させない。既存の session_id / job_id / response_id で表現できる場合はそれを使う。
- テスト通過だけで完了扱いしない。Viewer で実際に入力開始瞬間の停止と stale event 不採用を確認する。
