# 15. TTS / Viewer 同期 正本仕様

**対応仕様**: 仕様.md §12-f / IdleChat §8
**最終更新**: 2026-05-27

この文書は RenCrow の TTS chunk、Viewer 表示、口パク、再生 ACK の正本仕様である。
TTS / Viewer 同期に関して、`docs/STT_TTS/` 配下の一般仕様、実装メモ、過去仕様と矛盾する場合は本書を優先する。

## 1. 責務境界

| 対象 | 責務 |
| ---- | ---- |
| 表示イベント / 表示 state | 本文表示の正本 |
| TTS chunk | 音声再生、口パク、ACK、再生中 marker、診断 |
| `message_id` / `turn_index` | 表示イベントと TTS chunk の対応付け |
| `chunk_index` | 同一 response / track 内の再生順 |
| pending / queue | 一時状態。表示済みの真実ではない |

TTS chunk の `text` / `speech_text` / `display_text` で、表示本文を埋める、置換する、再構成してはいけない。
表示正本が無い場合は、本文を補完せず、診断表示またはログへ倒す。

## 2. 正本の優先順位

TTS / Viewer 同期の判断は次の順で行う。

1. 本書
2. `docs/01_正本仕様/08_IdleChat.md`
3. `docs/STT_TTS/AUDIO_Client仕様/TTS/ChatAudioSync仕様.md`
4. `docs/STT_TTS/RenCrow_TTS_仕様.md`
5. `docs/STT_TTS/` 配下の provider 別仕様、移植メモ、archive

IdleChat 固有の本文表示、TTS 待ち合わせ、ACK、表示正本については `08_IdleChat.md` を併せて正本とする。
一般 TTS 仕様は、IdleChat 正本に反しない範囲で適用する。

## 3. TTS chunk の必須単位

TTS chunk は、音声・表示補助・再生状態を同じ発話へ対応付ける同期単位である。
発話表示イベントと対応する TTS chunk は、同一 chunk 単位で次を持つ。

- `session_id`
- `message_id`
- `turn_index`
- `response_id`
- `utterance_id`
- `chunk_index`
- `character_id` または `speaker`
- `display_text`
- `speech_text`
- `audio_path` または `audio_url`
- `track`（未指定時は `default`）

現行 payload の `text` は `speech_text` の互換 alias として扱う。
新規実装では `speech_text` を優先し、互換のために `text` を併記してよい。

## 4. chunk 計画

`display_text` と `speech_text` は、同じ chunk 計画から生成する。
別々に chunk 分割して、同じ `chunk_index` で対応したものとして扱ってはいけない。

通常会話では `display_text` と `speech_text` は完全一致を原則とする。
読み替えが必要な場合でも、同一 chunk 内で理由を説明できる正規化だけを許可する。

許可される例:

- topic の読み上げ用表記を同一 chunk 内で `今日のお題` から `きょうのおだい` へ正規化する
- URL やコードブロックを TTS 対象から除外し、その除外理由を診断可能にする

禁止される例:

- `display_text` と `speech_text` を別々に `SplitTTSChunks` する
- 表示用 chunk と発話用 chunk の個数が違うまま index で対応させる
- TTS chunk の `display_text` を使って IdleChat 本文を再構成する

## 5. IdleChat の表示正本

IdleChat の本文表示の正本は `idlechat.message` と `idlechat.summary` である。
TTS chunk は対応する `message_id` の再生中 marker、口パク、ACK、診断にだけ使う。

IdleChat 通常会話では、TTS chunk は必ず `idlechat.message.message_id` に従属する。
`message_id` または `turn_index` が無い TTS chunk は、推測で pending 発話を消費せず、診断へ倒す。

## 6. ACK と完了判定

TTS playback ACK は、active audio Viewer が実際に観測した再生結果だけを成功根拠にする。

- `tts.session_completed` は TTS 生成完了であり、再生完了ではない。
- 非 active Viewer の ACK は観測ログであり、IdleChat の TTS pending 完了に使わない。
- `status=fallback` は禁止。受信した場合は明示エラーとして扱う。
- ACK / error trace には `session_id`, `response_id`, `utterance_id`, `message_id`, `turn_index`, `chunk_index`, `viewer_client_id`, `active_audio`, `status`, `error_code` を追跡可能にする。

## 7. 実装上の禁止事項

- 表示正本を持たない TTS chunk から本文 bubble を作る
- TTS chunk の到着順だけで本文表示順を決める
- queue / pending / cache を表示済みの真実として扱う
- 音声再生 reset で表示正本、ID 対応、履歴、診断根拠を消す
- TTS 生成成功、`chunk_ready`、`tts.session_completed` を再生成功として扱う
- fallback 文で本文欠落を補完する

## 8. 実装完了条件

TTS / Viewer 同期を変更する実装は、最低限次を確認する。

- 同一 `message_id` の各 chunk が `display_text`, `speech_text`, `audio_path` / `audio_url` を同一単位で持つ
- `display_text` と `speech_text` が同じ chunk 計画から作られている
- IdleChat 本文は `idlechat.message` / `idlechat.summary` から描画される
- TTS chunk は再生中 marker、口パク、ACK、診断に限定される
- active audio Viewer の ACK だけが TTS pending 完了に使われる
- ブラウザ再生を含む E2E で、表示本文、TTS chunk、音声再生、ACK を `message_id` 単位で突合できる
