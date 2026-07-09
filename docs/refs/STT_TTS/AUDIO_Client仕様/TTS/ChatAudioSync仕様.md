# ChatAudioSync 仕様

本書は、RenCrow Viewer の TimeLine 文字列描画と TTS 音声再生を同期させるための仕様である。

TTS / Viewer 同期の正本は `docs/01_正本仕様/15_TTS_Viewer同期.md` とする。
IdleChat の本文表示・TTS 同期については `docs/01_正本仕様/08_IdleChat.md` も正本とする。
本書は一般 TTS / 通常 Chat の下位仕様であり、正本と矛盾する場合は正本を優先する。

## 1. 目的

TimeLine の文字列表示と音声再生を、同じ TTS chunk 単位で必ず対応付ける。

現状の課題:

- `agent.response`、`tts.audio_chunk`、audio playback fallback が別々に TimeLine を更新し、表示順が崩れる。
- 音声ファイルと表示文字列の対応が UI 上で曖昧になる。
- IdleChat は聞き手の滑らかさを優先したいが、現在は chunk 到着単位で動きやすい。

この問題を避けるため、今後 TimeLine の発話描画、TTS 音声再生、口パク制御は `ChatAudioSync` モジュールだけが制御する。

## 2. 責務

`ChatAudioSync` は次を担当する。

- `tts.audio_chunk` payload の正規化
- session / track / chunk_index 単位の順序制御
- 音声再生キュー管理
- 音声再生開始と同じ chunk の TimeLine 描画
- 口パク開始/停止の同期点管理
- autoplay block / audio error 時の同一 chunk fallback 表示
- IdleChat の再生開始バッファ制御
- 重複 chunk の排除

`ChatAudioSync` 以外が発話 TimeLine を直接更新してはならない。

内部は次の 3 adapter に分ける。

- `audio`: `Audio` 要素、再生キュー、autoplay unlock、再生失敗 fallback を扱う。
- `text`: TimeLine / IdleChat Live Log への chunk 文字列描画を扱う。
- `lipSync`: 現時点では Viewer の Mio/Shiro 口パク画像 ON/OFF を扱う。将来的には VTuber bridge への同期イベントもここへ集約する。

禁止:

- Mio / Shiro の `agent.response` を直接 TimeLine へ描画する。
- `tts.audio_chunk` handler が直接 TimeLine へ描画する。
- audio error handler が別経路で発話 bubble を作る。

許可:

- `ChatAudioSync.onChunkReady(chunk)`
- `ChatAudioSync.onAudioStarted(chunk)`
- `ChatAudioSync.onAudioEnded(chunk)`
- `ChatAudioSync.onAudioFailed(chunk)`

## 3. 同期単位

同期単位は `utterance_chunk` とする。

```json
{
  "session_id": "chat-xxx",
  "message_id": "chat-xxx:msg:0001",
  "utterance_id": "chat-xxx:0002",
  "speaker": "mio",
  "chunk_index": 2,
  "track": "default",
  "display_text": "TimeLine に表示する文字列",
  "speech_text": "TTS に読ませた文字列",
  "audio_url": "http://...",
  "audio_path": "viewer-tts-xxx.wav",
  "duration_ms": 1800,
  "mode": "chat"
}
```

必須:

- `session_id`
- `message_id`（発話表示イベントと対応付ける場合）
- `utterance_id`
- `speaker`
- `chunk_index`
- `display_text`
- `speech_text`
- `audio_url` または `audio_path`
- `mode`

任意:

- `track`。未指定時は `default`
- `duration_ms`

## 4. 基本ルール

- `display_text` と `audio_url` / `audio_path` は同じ chunk に必ず紐づく。
- `display_text` と `speech_text` は同じ chunk 計画から作る。別々に分割して同じ index で対応したものとして扱ってはいけない。
- TimeLine は `display_text` を描画する。
- 音声は同じ chunk の `audio_url` / `audio_path` を再生する。
- 同一 `(session_id, track)` 内では `chunk_index` 順に処理する。
- 描画だけ先行、音声だけ先行は禁止する。
- 再生失敗時のみ、同じ chunk の `display_text` を fallback 表示する。
- 同一 `utterance_id` は一度だけ処理する。

## 5. 通常 Chat の同期仕様

通常 Chat は即時性を優先する。

1. `tts.audio_chunk` を受信する。
2. `ChatAudioSync` が `utterance_chunk` に正規化する。
3. session queue に投入する。
4. 再生可能なら `chunk_index=0` から順に再生開始する。
5. audio が `playing` または `canplay` 相当になった時点で、同じ chunk の `display_text` を TimeLine に描画する。
6. 同じタイミングで `lipSync.start(chunk.speaker)` を呼ぶ。
7. audio `ended` で `lipSync.stop(chunk.speaker)` を呼び、次 chunk に進む。
8. 次 chunk の再生開始時に、その chunk の `display_text` を描画する。

通常 Chat では、音声再生開始が TimeLine 表示タイミングの基準である。

## 6. IdleChat の同期仕様

IdleChat の詳細な正本は `docs/01_正本仕様/08_IdleChat.md` の「IdleChat 本文表示と TTS 同期」に従う。
IdleChat では `idlechat.message` / `idlechat.summary` が本文表示の正本であり、TTS chunk は音声再生、口パク、ACK、再生中 marker の補助情報である。

IdleChat は聞き手の滑らかさを優先する。

再生開始条件:

- 未再生 chunk が 2 つ以上溜まったら開始する。
- ただし session completed 済みの場合は、1 chunk でも開始する。

再生開始後:

- 通常 Chat と同じく、音声再生開始時に同じ chunk の文字列を描画する。
- 2 chunk buffer は開始前だけの条件とする。
- 再生中に次 chunk が不足した場合は、次 chunk 到着または session completed まで待つ。

判定:

```text
mode == idlechat:
  start if buffered_chunks >= 2
  or session_completed == true
```

IdleChat でも、再生ファイルと表示文字列の同期は必須である。

## 7. 状態遷移

### 7.1 Chunk 状態

```text
queued
  -> buffered
  -> loading_audio
  -> playing
  -> displayed
  -> done
  -> failed_audio_display_only
```

### 7.2 Session 状態

```text
open
  -> ready_to_play
  -> playing
  -> draining
  -> completed
```

## 8. TimeLine 描画ルール

- 1 chunk につき 1 回だけ描画する。
- `utterance_id` で重複排除する。
- 同一 speaker、同一 session、連続 chunk は同じ bubble に追記してよい。
- speaker が変わったら必ず別 bubble にする。
- session が変わったら原則別 bubble にする。
- IdleChat の `topic` / `speech` / `summary` は種類別に分ける。
- audio が正常再生される場合、表示タイミングは audio start に合わせる。
- 口パク開始タイミングも audio start に合わせる。
- audio が使えない場合だけ、fallback 表示タイマーで進める。

## 9. 音声再生ルール

- 1 つの `Audio` 要素で直列再生する。
- 同時再生は禁止する。
- queue sort key は `(priority, session_id, track, chunk_index, arrival_seq)` とする。
- 通常 Chat は IdleChat より優先する。
- 通常 Chat が来た場合、IdleChat は再生中 chunk の終了後に譲る。
- 再生中 chunk を途中停止して別 session へ切り替える仕様は初期実装では採用しない。
- audio start 時に text と lipSync を同じ chunk で開始する。
- audio end 時に lipSync を停止し、次 chunk の audio start まで次の文字列は表示しない。

## 10. エラー時仕様

### 10.1 `audio_url` / `audio_path` がない

- 同じ chunk の `display_text` を fallback 表示する。
- 状態は `failed_audio_display_only` とする。

### 10.2 audio 再生失敗

- 同じ chunk の `display_text` を fallback 表示する。
- 次 chunk へ進む。
- 別 chunk の文字列を代理表示してはならない。

### 10.3 chunk 欠番

- UI 待ち上限として 15 秒を基準に待つ。
- timeout 後、欠番を skip して次へ進む。
- skip は viewer debug log に記録する。
- timeout した chunk の音声が後から届いても、現在の session / utterance / chunk と一致しない場合は再生しない。

### 10.4 session drain timeout

- session 終了時の drain は 15 秒を基準に待つ。
- drain timeout 後に残る音声は `session_audio_timeout` として扱い、次 session へ持ち越さない。
- 表示本文は display-only として区切りのよい状態まで描画してよいが、音声再生成功や lipSync 成功として扱わない。

## 11. 既存実装からの移行方針

現在の Viewer 実装では次が分散している。

- `handleTTSAudioEvent`
- `enqueueTTSAudio`
- `enqueueTTSDisplayFallback`
- `playNextTTSAudio`
- `setCentralTTSSpeechText`
- `setLipSyncSpeaking`

移行後は、これらを `ChatAudioSync` の内部責務に統合する。

最小実装ステップ:

1. `tts.audio_chunk` payload を `utterance_chunk` に正規化する関数を作る。
2. `ChatAudioSync` queue を作り、通常 Chat / IdleChat の開始条件を分ける。
3. TimeLine 描画を `onAudioStarted(chunk)` に一本化する。
4. IdleChat に 2 chunk buffer before start を入れる。
5. `utterance_id` 重複排除を入れる。
6. audio error 時の同一 chunk fallback を入れる。
7. 既存の直接描画経路を削除する。

## 12. 受け入れ基準

- 通常 Chat で、音声再生開始時に同じ chunk の文字列が TimeLine に出る。
- 通常 Chat で、chunk 0, 1, 2 が順番通りに再生・描画される。
- IdleChat で、chunk が 1 つだけの間は再生も描画も開始しない。
- IdleChat で、chunk が 2 つ溜まると chunk 0 から再生・描画を開始する。
- IdleChat で、session completed 済みなら 1 chunk でも再生・描画を開始する。
- audio 再生失敗時、同じ chunk の文字列だけが fallback 表示される。
- speaker 変更時に bubble が分かれる。
- `utterance_id` が重複した chunk は二重描画されない。
- 通常 Chat が来た場合、IdleChat は現在再生中 chunk の終了後に通常 Chat へ譲る。

## 13. テスト観点

JS / Playwright テストで固定する。

- `normal chat displays chunk text on audio start`
- `normal chat plays chunks in chunk_index order`
- `idlechat waits until two chunks before playback`
- `idlechat starts with one chunk after session completed`
- `audio failure displays matching chunk only`
- `speaker change starts a new bubble`
- `duplicate utterance_id is ignored`
- `normal chat preempts idlechat at chunk boundary`
