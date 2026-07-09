# RenCrow STT Streaming 暫定/確定字幕仕様

## 1. 背景

RenCrow の STT は、Viewer からマイク音声を逐次送信し、STT 側で途中認識を返し、発話確定後に `final` text を通常 chat 入力へ接続するための audio 境界である。

旧 STT 仕様では、字幕を次の 2 層で扱う方針が定義されていた。

- 暫定字幕: 短い chunk / window の認識結果。発話中に書き換わってよい。Chat / LLM には渡さない。
- 確定字幕: 無音、停止、VAD などを契機に長めの発話区間を再解釈した結果。Chat / LLM に渡してよい唯一の STT text。

現行 Viewer は、ブラウザのマイク入力を 16kHz mono PCM16 に resample し、WebSocket binary frame として chunk 送信している。207 STT server は、`start` control、PCM16 raw chunk、`stop` control を受け取り、`progress` / `partial` / `final` を返す。

この仕様では、旧仕様の「暫定字幕 / 確定字幕」方式を、207 STT server の現行 protocol と RenCrow Viewer / RenCrow STT bridge の実装境界に合わせて再定義する。

## 2. 用語定義

| 用語 | 定義 |
| --- | --- |
| audio chunk | Viewer から STT server へ逐次送る PCM16 little-endian mono binary frame。Chat / LLM 入力ではない。 |
| partial | STT server が直近 window から返す途中認識 text。Viewer 表示・ログ用。Chat / LLM には渡さない。 |
| draft | 旧 voice-bridge 仕様の途中認識 event。現行仕様では `partial` と互換扱いする。 |
| final | STT server が発話確定後に返す確定 text。通常 chat input に接続できる唯一の STT text。 |
| progress | STT server が受信済み音声量を返す event。認識 text ではない。 |
| utterance | 1 回の発話区間。VAD、`stop`、`final_pending`、close、最大発話長などで区切られる。 |
| session_id | STT 接続または発話セッションを追跡する ID。新しい ID を乱立させず、既存 ID で表現できる範囲を優先する。 |
| event_id | STT server が各 STT event / request を追跡する ID。server log との照合に使う。 |
| seq | STT server からの event 順序。`partial` / `final` の並びを確認するために使う。 |
| final text | `final.text` の trim 済み文字列。空文字は chat input にしない。 |
| chat input | RenCrow 通常 chat に渡すユーザー入力。STT では `final text` だけが該当する。 |

## 3. アーキテクチャ

```text
Browser Viewer
  -> Tailscale Serve / LAN HTTP(S)
  -> RenCrow Go /stt proxy
  -> 207 STT server /stt
  -> partial / final
  -> Viewer
  -> final text only
  -> normal chat input
```

### Browser Viewer

- マイク権限を取得する。
- 入力レベルを UI に表示する。
- audio input を 16kHz mono PCM16 に resample する。
- WebSocket を開き、`start` を送る。
- PCM16 raw chunk を binary frame として送る。
- 録音停止時に `stop` を送る。
- `partial` / `draft` は暫定字幕として表示・ログに保持する。
- `final` だけを通常 chat input へ渡す。
- IdleChat へ STT input を直接流さない。

### RenCrow STT bridge

- Viewer から見える `/stt` / `/stt-ws` / `/ws` の互換 endpoint を提供する。
- `STT_GATEWAY_URL` または `RENCROW_STT_URL` が設定されている場合、STT server へ WebSocket を透過する。
- text frame と binary frame を破壊せず転送する。
- STT server への接続失敗は `error` として Viewer に返す。
- RenCrow STT bridge は認識 text を生成しない。HTTP file inference と WS streaming を混同しない。

### 207 STT server

- WebSocket `/stt` を受ける。
- `start` control で sample rate / channels / format を受ける。
- PCM16 little-endian raw chunk を受ける。
- 受信済み音声量を `progress` として返す。
- 直近 window の途中認識を `partial` として返す。
- `stop` / `final_pending` / close / VAD / 最大発話長により `final` を返す。
- `final` は同一 `utterance` の終端 event として扱う。
- 同一 `utterance` で `final` を返した後に、同じ発話を否定・上書きする `error` を返さない。
- HTTP `/v1/audio/transcriptions` は file inference 用であり、WebSocket streaming と入力仕様が違う。

### HTTP file inference との違い

| 項目 | WebSocket streaming | HTTP file inference |
| --- | --- | --- |
| 用途 | 実マイクの逐次入力 | 保存済み WAV などの一括推論 |
| 入力 | PCM16 raw chunk + control JSON | multipart file |
| 途中認識 | `partial` / `draft` | なし |
| 確定 | `final` event | HTTP response text |
| 検証注意 | WAV bytes を直送しない | WAV file をそのまま送ってよい |

## 4. 通信契約

### Viewer -> STT

#### WebSocket open

Viewer は `/viewer/runtime-config` の `stt_stream_url` を優先して接続する。Tailscale Viewer では browser-facing URL として `wss://<tailnet-host>/stt` を使い、LAN では設定された LAN URL を使う。

#### start

接続後、音声 chunk より前に送る。

```json
{
  "type": "start",
  "sample_rate": 16000,
  "channels": 1,
  "format": "pcm_s16le"
}
```

`language` を送る場合は `ja` を指定する。未指定時は STT server config の既定値を使う。

#### PCM16 binary chunk

- binary frame で送る。
- format: PCM16 little-endian raw。
- channels: 1。
- sample rate: `start.sample_rate` と一致させる。
- WAV header を含めない。
- WAV whole file / WAV chunk を streaming の正常入力として扱わない。

#### stop

録音停止時に送る。

```json
{ "type": "stop" }
```

`stop` は finalization request であり、`stop` 送信後にさらに audio chunk を送らない。

#### close

`stop` 送信後、`final` または `error` を受けるまで待つ。ブラウザやネットワーク都合で close-only になった場合でも、STT server 側が音声を保持していれば finalization を試みる。ただし RenCrow Viewer の正常系は `stop` 明示である。

#### final_pending

旧仕様互換 event として扱う。

```json
{ "type": "final_pending" }
```

新規実装では `stop` を優先する。`final_pending` は旧 `/stt-ws` / `/ws` 互換または既存クライアント移行用とする。

### STT -> Viewer

#### ready

```json
{
  "type": "ready",
  "event_id": "evt_stt_...",
  "provider": "whisperkit",
  "model": "large-v3-v20240930_turbo",
  "sample_rate": 16000
}
```

接続または `start` 受理を示す。`ready` は認識成功ではない。

#### progress

```json
{
  "type": "progress",
  "event_id": "evt_stt_...",
  "duration": 3.0,
  "bytes": 96000
}
```

`duration` / `bytes` は受信済み音声全体の値である。認識済み window の値ではなく、認識 text でもない。

#### partial / draft

```json
{
  "type": "partial",
  "session_id": "viewer-session",
  "event_id": "evt_stt_...",
  "text": "テストを",
  "seq": 2,
  "stability": 0.6,
  "start_ms": 1000,
  "end_ms": 3000,
  "is_final": false
}
```

`partial` は暫定字幕である。旧 `draft` は同じ暫定字幕として扱う。Viewer は表示・ログ・デバッグ trace には使ってよいが、Chat / LLM へ送ってはいけない。

#### final

```json
{
  "type": "final",
  "session_id": "viewer-session",
  "event_id": "evt_stt_...",
  "text": "テスト",
  "seq": 3,
  "language": "ja",
  "duration": 3.0,
  "is_final": true,
  "reason": "stop"
}
```

`final.text` が通常 chat input の唯一の入力元である。空文字、no speech、error は chat input にしない。

`final` は同一 `utterance` の終端 event である。STT server は `final` を送信した時点で、その `utterance` の認識結果を確定済みとして扱う。

`final` 後に同じ `utterance` に対して許可される event は、原則として次だけである。

- `closed`
- 接続維持のための protocol-level close / websocket close
- 次の `utterance` を明示する新しい `start` / 新しい `event_id` / 新しい `utterance_id` に属する event

`final` 後に同じ `event_id` / `utterance` で `progress`、`partial`、`error` を返してはいけない。特に `final` 後の `empty_transcript` / `NO_SPEECH_DETECTED` は、Viewer 側で確定済み text を壊す原因になるため禁止する。

STT server が `final` 後にも音声 frame を受信した場合、次のいずれかで扱う。

1. 現行 `utterance` では無視し、必要なら debug log に `ignored_after_final` として記録する。
2. 新しい `utterance` として扱う場合は、新しい `event_id` または `utterance_id` を割り当て、前の `final` とは別系列の event として返す。
3. protocol 違反として扱う場合でも、前の `final` と同じ `event_id` で `error` を返さない。返すなら connection-level error として、確定済み `final` を取り消さない形にする。

推奨ログ:

```json
{
  "event": "stt_utterance_finalized",
  "session_id": "viewer-session",
  "event_id": "evt_stt_...",
  "utterance_id": "utt_...",
  "final_text": "テスト",
  "final_reason": "stop",
  "received_bytes": 96000,
  "audio_duration_sec": 3.0
}
```

#### closed

```json
{
  "type": "closed",
  "event_id": "evt_stt_...",
  "reason": "client_closed"
}
```

接続終了を示す。`closed` は `final` の代替ではない。`final` がない `closed` は未確定終了として扱う。

#### error

```json
{
  "type": "error",
  "event_id": "evt_stt_...",
  "error_code": "NO_SPEECH",
  "message": "speech was not detected"
}
```

`error` は通常 chat 成功として隠さない。Viewer は session state と STT log に残す。

`error` は未確定の `utterance` にだけ返す。すでに `final` を返した同一 `utterance` に対して、後続の `error` を返してはいけない。

`partial` を返した後に `final` で `empty_transcript` / `NO_SPEECH_DETECTED` へ落とす場合は、server log に次を必ず残す。

- `partial` を返した window の start / end / duration
- `final` 対象にした audio range
- VAD speech detected true/false
- final 推論に渡した PCM bytes / duration
- `partial` text が final 候補として採用されなかった理由

ただし、この logging requirement は `partial` を final として流用することを求めるものではない。通常 chat input に渡してよいのは `final.text` だけである。目的は、`partial` が出た発話で final が no speech になる原因を追跡可能にすることである。

## 5. 状態遷移

```text
idle
  -> connecting
  -> ready
  -> recording
  -> receiving partial
  -> finalizing
  -> finalized
  -> closed

任意状態
  -> error
  -> closed
```

| 状態 | 意味 | 主な event |
| --- | --- | --- |
| idle | STT 未開始 | mic off |
| connecting | WebSocket 接続中 | open pending |
| ready | server が受理可能 | `ready` |
| recording | audio chunk 送信中 | binary chunk / `progress` |
| receiving partial | 暫定 text 受信中 | `partial` / `draft` |
| finalizing | `stop` / VAD / close 後の確定待ち | `stop`, `final_pending` |
| finalized | `final` 受信済み | `final` |
| error | STT 失敗 | `error`, timeout, proxy failure |
| closed | 接続終了 | close / `closed` |

## 6. テキスト確定ルール

- `partial` / `draft` は Viewer 表示・STT log・debug trace 用である。
- `partial` / `draft` は Chat / LLM に渡さない。
- `progress` は音声量の進捗であり、文字列ではない。
- `final.text` だけを通常 chat input にできる。
- `final.text` が空の場合は通常 chat input にしない。
- `final` が来る前に `partial` を chat input として送る fallback は原則禁止する。
- fallback が必要な診断モードを設ける場合は、通常 chat input ではなく「暫定扱い」として UI と log に明示する。
- STT input は通常 chat のみに接続する。IdleChat へ直接流さない。

## 7. Viewer UI 仕様

### マイク入力レベル表示

Viewer は録音中の PCM16 chunk から入力レベルを算出し、マイクボタン上に表示する。これはマイク入力の有無を示す UI であり、STT 認識成功ではない。

### STT 接続状態

Viewer は少なくとも次を表示する。

- mic on / off
- STT off / connecting / connected / waiting
- session_id または `(unknown)`
- action error

### partial 表示

`partial` / `draft` は暫定字幕として表示できる。確定前であることが分かる UI とし、通常 chat input と混同しない。

### final 表示

`final` は確定字幕として表示し、通常 chat input へ接続する。`final` を受けたら入力欄に反映し、通常 chat 送信処理へ進める。

### セッション ID 表示

STT server から `session_id` / `event_id` が返る場合、Viewer はコピー可能または log で追跡可能にする。

### STT ログ保存

Viewer は次を保存できるようにする。

- client URL
- ws URL
- test time
- session_id
- `progress`
- `partial` / `draft`
- `final`
- `error`
- 保存した client WAV

STT log は観測証跡であり、Chat / LLM 入力そのものではない。

## 8. エラー仕様

| エラー | 判定 | Viewer 表示 | Chat 入力 |
| --- | --- | --- | --- |
| no speech | STT server が発話を検出できない | error / no speech | 送らない |
| provider timeout | STT provider が timeout | error / timeout | 送らない |
| websocket close | `final` なしで close | 未確定終了 | 送らない |
| invalid audio format | WAV header 直送、sample width 不一致など | invalid audio | 送らない |
| sample rate mismatch | `start.sample_rate` と実 chunk が一致しない | config error | 送らない |
| 207 STT unreachable | 207 `/stt` に接続不可 | STT unreachable | 送らない |
| RenCrow STT bridge failure | RenCrow STT bridge 失敗 | bridge failure | 送らない |

Error path を fallback 成功として扱わない。Unit test や HTTP health が OK でも、実 microphone -> WS -> `final` -> normal chat input が成立しない場合は E2E 成功ではない。

## 9. 検証仕様

### Viewer 実マイク E2E

- Tailscale Viewer または LAN Viewer を開く。
- マイク権限を許可する。
- 入力レベルが反応することを確認する。
- `ready` / `progress` / `partial` / `final` を STT log で確認する。
- `final.text` が通常 chat input として処理されることを確認する。

### 207 direct WS

WAV を検証に使う場合は、WAV whole file bytes を WebSocket に送らない。WAV から PCM16 raw を取り出し、`start` と `stop` を明示して送る。

```text
WAV file
  -> decode header
  -> extract PCM16 raw
  -> ws://192.168.1.207:8766/stt
  -> start(sample_rate=16000, channels=1, format=pcm_s16le)
  -> binary PCM16 chunks
  -> stop
  -> expect final
```

### RenCrow `/stt` proxy WS

207 direct WS と同じ PCM16 raw + `start` + `stop` を `ws://127.0.0.1:18790/stt` または Tailscale `wss://<host>/stt` へ送る。Direct と proxy の event 内容が大きくズレないことを確認する。

### HTTP `/v1/audio/transcriptions`

HTTP file inference は、保存 WAV の一括推論確認に使う。WS streaming の代替確認として扱わない。

### `scripts/stt_e2e_probe.py` の修正方針

- WS 検証時に WAV whole file bytes を送らない。
- WAV を `wave` 等で decode し、PCM16 raw を chunk 分割して送る。
- 接続後に `start` を送る。
- 終了時に `stop` を送る。
- `partial` と `final` を別々に記録する。
- `final` がない成功を E2E 成功扱いしない。ただし partial-only の診断は別項目に記録する。

## 10. 移行方針

1. Viewer から `start` を明示送信する。
2. Viewer 停止時に `stop` を明示送信し、`final` を待ってから close する。
3. `partial` / `draft` を暫定字幕 UI として表示する。
4. `final` のみ通常 chat input へ接続する。
5. 旧 `draft` は `partial` と互換扱いする。
6. `final_pending` は旧仕様互換として残すが、新規正常系は `stop` に寄せる。
7. `/stt-ws` / `/ws` は互換 endpoint として維持する。
8. `scripts/stt_e2e_probe.py` を PCM16 raw + `start` + `stop` に修正する。

## 11. 実装作業仕様

STT streaming の実装作業、変更対象、検証チェックリスト、確認メモ、Goal 実行ルールは次の作業専用文書へ切り出した。

- `docs/10_新仕様/40_STT_Streaming実装作業仕様.md`

この文書は protocol / behavior の仕様を扱う。実装作業の進行、commit 単位、runtime 証跡、未確認項目は 40 を参照する。
