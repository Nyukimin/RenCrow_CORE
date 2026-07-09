# RenCrow_TTS 仕様

## 1. 目的

RenCrow_TTS は、LLM が生成した応答文字列と TTS Provider / Viewer の間に入る制御層である。

目的は、長い応答を短い発話単位へ分割し、音声出力開始を起点に Viewer の表示を同期させることである。

基本フロー:

```text
LLM出力
  -> RenCrow_TTS Controller
      -> 表示用 chunk（LLM原文寄り）
      -> TTS用 chunk（読み上げ用フィルタ後）
  -> SBV2 Provider
  -> tts.audio_chunk
  -> Viewer
```

現在の Provider は SBV2 だが、RenCrow_TTS Controller は Provider 非依存の制御層として扱う。

## 2. 責務分離

### 2.1 LLM / Chat

LLM / Chat は発話元の応答文字列を生成する。

LLM / Chat は、音声ファイル生成、TTS API 詳細、Viewer 再生順序を直接制御しない。

Controller へ渡す主な情報:

```text
session_id
response_id
character_id
voice_id
text
emotion または発話文脈
```

### 2.2 RenCrow_TTS Controller

RenCrow_TTS Controller は、LLM出力を音声・表示同期用の発話単位へ変換する。

主な責務:

- LLM出力を短い chunk へ分割する
- 表示用 `display_text` と TTS用 `text` を分離する
- chunk ごとに TTS Provider へ合成依頼する
- `chunk_index` を単調増加で採番する
- Provider 応答を `tts.audio_chunk` に変換する
- Viewer が再生順序を判断できる情報を付与する
- 音声出力開始を起点に Viewer 表示が更新されるよう保証する

### 2.3 表示用 text と TTS用 text

中央チャット領域に表示する文字列は、LLM出力を表示用に chunk 分割した `display_text` である。

TTS Provider に渡す文字列は、読み上げ用にフィルタした `text` である。

例:

```text
display_text: 今日のお題です、猫について話しましょう。
text:         きょうのおだいです、猫について話しましょう。
```

`text` は読み上げ都合で表記が変わってよい。`display_text` は Viewer 表示用なので、原文寄りの表記を保持する。

句読点は TTS用 `text` でも保持する。句読点はポーズ、抑揚、chunk 境界の手掛かりになるため、原則として削除しない。

### 2.4 SBV2 Provider

SBV2 Provider は、1つの TTS用 chunk を1つの音声ファイルへ変換する。

Provider は以下を受け取る。

```text
text
voice_id または解決済み voice 情報
output_dir
必要に応じた Provider 固有パラメータ
```

現在の SBV2 サーバ仕様では `/voice` を使う。

代表 voice 解決:

```text
mio / female_01 -> amitaro    -> model_id=0 / speaker_id=0 / style=Neutral
shiro / male_01 -> shi-gozaki -> model_id=6 / speaker_id=0 / style=Neutral
```

### 2.5 Viewer

Viewer は `tts.audio_chunk` を受け取り、音声再生キューへ積む。

Viewer は chunk 受信時や再生予約時には中央チャットを更新しない。更新の起点は音声出力開始である。

現在のブラウザ再生では、以下を音声出力開始の起点として扱う。

```text
HTMLAudio の playing イベント
または最初の timeupdate
```

Viewer は音声出力開始時に、その chunk の `display_text` を中央チャット領域へ反映する。`Now Playing` は TTS用 `text` を表示してよいが、中央チャット領域は `display_text` を使う。

Live Mode では配信用レイアウトを優先し、`Now Playing` を非表示にしてよい。ただし中央チャット領域と音声再生の同期は維持する。

## 3. chunk 分割

RenCrow_TTS Controller は、LLM出力を短い文またはワンフレーズ単位に分割する。

分割の基本方針:

- 1 TTS chunk は1つの音声ファイルとして生成する
- 表示用 chunk と TTS用 chunk は同じ順序で扱う
- 長文を丸ごと1本の音声にしない
- 長文を音声開始前に中央チャットへ一括表示しない
- 句点までの短い文を優先する
- 長すぎる文は自然な境界で追加分割する
- 自然境界がない場合は最大長で強制分割する

境界の優先順位:

1. 強い境界: `。`, `！`, `？`, `.`, `!`, `?`, 改行
2. 弱い境界: `、`, `，`, `,`, `;`, `；`, `:`, `：`
3. 空白
4. 最大長到達時の強制分割

現在の目安:

```text
最小長: 6文字
最大長: 42文字
```

短い相づちや感嘆など、6文字未満でも自然な独立発話として扱うべき場合は、最終 flush 時に chunk 化してよい。

## 4. セッションと chunk

RenCrow_TTS Controller は、応答ごとに TTS セッションを開始する。

セッション開始時の情報:

```text
session_id
response_id
character_id
voice_id
speech_mode
conversation_mode
```

各 chunk には以下を付与する。

```text
session_id
response_id
utterance_id
chunk_index
character_id
text
display_text
audio_path または audio_url
track
```

`chunk_index` は同一 `(session_id, track)` 内で `0` から単調増加する。

`utterance_id` は以下を推奨する。

```text
{session_id}:{chunk_index4桁}
```

例:

```text
idle-123-tts-456:0002
```

## 5. `tts.audio_chunk` 契約

RenCrow_TTS Controller は、音声生成が完了した chunk ごとに `tts.audio_chunk` を発行する。

`tts.audio_chunk` は、TTS用 text、表示用 display_text、音声ファイルを結びつける同期単位である。

TTS / Viewer 同期は `docs/01_正本仕様/15_TTS_Viewer同期.md` を正本とする。
IdleChat の本文表示・TTS 同期は `docs/01_正本仕様/08_IdleChat.md` も正本とする。
IdleChat では `idlechat.message` / `idlechat.summary` が本文表示の正本であり、TTS chunk は音声再生、口パク、ACK、再生中 marker の補助情報である。

Payload 例:

```json
{
  "session_id": "idle-123-tts-456",
  "response_id": "resp-789",
  "message_id": "idle-123:msg:0001",
  "turn_index": 1,
  "utterance_id": "idle-123-tts-456:0000",
  "chunk_index": 0,
  "character_id": "mio",
  "text": "きょうのおだいです、猫について話しましょう。",
  "display_text": "今日のお題です、猫について話しましょう。",
  "audio_path": "viewer-tts-abc.wav",
  "audio_url": "",
  "track": "default"
}
```

必須:

- `session_id`
- `message_id`
- `turn_index`
- `utterance_id`
- `chunk_index`
- `character_id`
- `text`
- `display_text`
- `audio_path` または `audio_url`

推奨:

- `response_id`
- `track`

`text` は音声合成対象であり、再生される音声と対応する。

`display_text` は中央チャット領域の表示対象であり、LLM出力の表記を保つ。

IdleChat では `display_text` は本文表示の正本ではなく、対応する `message_id` の再生中 marker と診断の補助情報である。

`utterance_id` は追跡・デバッグ用の共通IDとして推奨する。Viewer の同期保証は `tts.audio_chunk` payload 単位で成立する。

`display_text` と `text` / `speech_text` は同じ chunk 計画から作る。
別々に chunk 分割して同じ `chunk_index` で対応したものとして扱ってはいけない。

## 6. 再生順序

Viewer は `(session_id, track, chunk_index)` を優先して昇順再生する。

`track` が空の場合は `default` とみなす。

基本順序:

```text
chunk 0 の音声出力開始
  -> chunk 0 の display_text を中央チャットへ反映
  -> chunk 0 の text を Now Playing へ反映してよい
chunk 0 の音声終了
  -> chunk 1 の音声出力開始
  -> chunk 1 の display_text を中央チャットへ反映
...
全chunk終了
  -> Now Playing はクリア
  -> 中央チャット領域の履歴は保持
```

## 7. Viewer 表示契約

Viewer の中央チャット表示は、音声出力開始と同期する。

禁止:

- TTS対象の長文全文を、音声出力開始前に中央チャットへ一括表示する
- chunk 受信時、再生予約時、`audio.play()` 呼び出し時だけを根拠に中央チャットを更新する
- 中央チャットに TTS用読み変換済み `text` をそのまま表示する
- `mio` / `shiro` の `agent.response` や `idlechat.message` を中央チャットへ全文描画する

必須:

- 音声出力開始時に、該当 chunk の `display_text` を中央チャットへ反映する
- `Now Playing` は必要に応じて該当 chunk の `text` を表示する
- 音声再生が停止・失敗した場合、`Now Playing` と口パク状態は停止状態へ戻す
- 中央チャット領域の会話履歴は保持する

中央チャット領域:

- 同一話者の発言が連続する場合は、前のバルーン末尾へ追記する
- 話者が切り替わる場合は、新しいバルーンを作る
- 発話終了後もバルーンは消さない
- スクロールアップにより過去の会話データを保持する

通常 Viewer:

- 中央チャット領域は `display_text` を累積表示する
- `Now Playing` は現在再生中の TTS用 `text` を表示してよい

Live Mode:

- 配信用レイアウトでは `Now Playing` を非表示にしてよい
- 中央チャット領域を表示する場合は、音声出力開始を起点に `display_text` を反映する

## 8. SBV2 Provider 契約

現在の SBV2 Provider は `POST /voice` を利用する。

リクエスト形式:

```text
POST /voice?text=<urlencoded>&model_id=<id>&speaker_id=0&style=Neutral
```

Provider は1回の呼び出しにつき、1つの TTS用 chunk 音声を返す。

RenCrow_TTS Controller 側は、Provider の `/voice` 仕様を Viewer に漏らさない。

Viewer が知るのは `tts.audio_chunk` の `audio_path` または `audio_url` と、表示同期に必要な metadata だけである。

## 9. エラー時の扱い

1 chunk の音声生成に失敗した場合、Controller は以下のいずれかで処理する。

- 当該 chunk をスキップして次 chunk へ進む
- セッションを中断する
- 再試行してから失敗扱いにする

推奨:

- transport error は最大2回まで再試行
- Provider の不正入力エラーは再試行しない
- `engine_unavailable` 系は短いバックオフ後に再試行
- 失敗時もログに `session_id`, `chunk_index`, `character_id`, `text`, `display_text` を残す

Viewer には、音声が存在しない `tts.audio_chunk` を送らない。

## 10. 現行実装との対応

現行実装で対応する主な場所:

```text
internal/application/orchestrator/tts_support.go
  - SplitTTSChunks
  - 表示用 chunk と TTS用 chunk の分割
  - TTS用 FilterSpeakableText 適用

internal/application/orchestrator/tts_bridge.go
  - TTSBridge
  - TTSDisplayBridge

internal/infrastructure/tts/provider_tts_bridge.go
  - Provider直結TTS Bridge
  - PushTextWithDisplay

internal/infrastructure/tts/rencrow_tts_bridge.go
  - RenCrow /synthesis Bridge
  - PushTextWithDisplay

internal/infrastructure/tts/sbv2_provider.go
  - SBV2 /voice Provider
  - ensureTTSPunctuation

cmd/picoclaw/tts_client_bridge.go
  - TTS Bridge 構築
  - tts.audio_chunk 発行
  - display_text 付与

cmd/picoclaw/idlechat_tts.go
  - IdleChat の TTS用 text と display_text 分離
  - 「きょうのおだいです」と「今日のお題です」の分離

internal/adapter/viewer/viewer.html
  - tts.audio_chunk 受信
  - 再生キュー
  - HTMLAudio playing / timeupdate 起点の表示更新
  - 中央チャット領域への display_text 累積表示
```

現行方針:

- 通常 Chat ストリーミング経路では、`SplitTTSChunks` により句点優先で分割する
- final text も丸ごと1 chunkにせず、同じ分割器を通す
- Provider直結経路も Provider 呼び出し前に分割する
- TTS用 `text` でも句読点を保持する
- 通常 Chat の中央チャット領域は、必要に応じて `tts.audio_chunk.display_text` を音声出力開始時に反映する
- IdleChat の本文表示は `idlechat.message` / `idlechat.summary` を正本とし、TTS chunk の `display_text` で本文を埋める、置換する、再構成しない

## 11. 完了条件

RenCrow_TTS Controller の実装完了条件:

- LLM の長文応答が短い chunk に分割される
- chunk ごとに SBV2 Provider が呼ばれる
- TTS用 `text` と再生音声が一致する
- 通常 Chat の中央チャット領域は `display_text` を表示できる
- IdleChat の本文表示は `idlechat.message` / `idlechat.summary` を正本にする
- `text` / `speech_text` と `display_text` は同じ chunk 計画から作られている
- TTS用 `text` に句読点が保持される
- Viewer が音声出力開始時に、該当 chunk の `display_text` を中央チャットへ反映する
- 同一話者の連続発言は同じバルーン末尾へ追記される
- 話者切替時は新しいバルーンになる
- `chunk_index` が同一 session 内で単調増加する
- `Now Playing` は表示・非表示に関係なく中央チャット同期の必須要件ではない
- Live Mode では不要な `Now Playing` を出さず、再生同期だけを維持できる

## 12. 参照

- `docs/STT_TTS/AUDIO_Client仕様/TTS/仕様.md`
- `docs/STT_TTS/AUDIO_Client仕様/TTS/実装仕様.md`
- `docs/STT_TTS/SBV2_サーバ仕様.md`
