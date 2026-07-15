# TTS・Viewer同期実装契約

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../../refs/01_正本仕様/15_TTS_Viewer同期.md`、`../../refs/10_新仕様/07_STT_TTS仕様.md`
- last_reviewed: 2026-07-15
- scope: CORE側のTTS provider接続、audio event、Viewer再生、ACK、表示同期

## 1. module境界

| owner | 責務 |
| --- | --- |
| `RenCrow_TTS` | TTS engine、voice asset、synthesis本体、provider固有運用 |
| `RenCrow_CORE` | provider request、chunk event、session / response対応、Viewer配信、再生ACK、timeout、diagnostics |
| Viewer browser | audio queue、実再生、local playback state、ACK送信、口パクtrigger |

COREは外部TTS実装の内部仕様を正本化せず、provider非依存の接続契約を所有する。

## 2. 表示と音声の分離

- 表示本文の真実は表示eventまたは表示stateである。
- TTS chunkは音声再生、口パク、ACK、再生中marker、診断の入力である。
- TTS chunkの`text`、`speech_text`、`display_text`だけから表示本文を新規作成・置換・再構成しない。
- 表示正本が無い場合、本文を補完せず診断表示またはlogへ倒す。
- pending / queueは一時状態であり、表示済み・再生済みの監査事実ではない。

## 3. synthesis request

`modules/tts.SynthesisRequest`の必須入力は`speech_text`とする。`display_text`、session / response / utterance、character / voice、emotionは接続上の補助情報である。

providerへ渡す値とViewerへ返す値を混同しない。provider固有payloadへの変換はTTS adapter / module内へ閉じ込める。

## 4. `tts.audio_chunk`

chunk eventは最低限、次の単位を追跡可能にする。

| field | 役割 |
| --- | --- |
| `session_id` | 会話またはIdleChat session |
| `response_id` | 応答単位 |
| `message_id` | 表示messageとの対応 |
| `turn_index` | session内の発話順 |
| `chunk_index` | 同一track内の再生順 |
| `character_id` | 発話character |
| `speech_text` | 読み上げ文字列 |
| `display_text` | 表示補助文字列。本文正本ではない |
| `audio_url` / `audio_path` | 再生対象 |

`message_id`または`turn_index`が無いchunkを、推測で他の発話へ結び付けない。Viewerは診断を残し、安全側に処理する。

## 5. 再生、ACK、timeout

1. COREはpending playbackを登録する。
2. Viewerはaudioをqueueし、browserで実再生する。
3. Viewerは成功または明示errorを`/viewer/tts/playback-ack`へ送る。
4. COREはidentityが一致するpendingだけを完了させる。
5. ACKが来ない場合はbounded timeoutで解放し、timeoutとして記録する。

deprecated fallback ACKを成功扱いしない。late ACK、重複ACK、すでにtimeout済みのACKは別発話を完了させない。

## 6. SSEと再送

`tts.audio_chunk`はlive再生用のtransient eventである。Viewer SSEのhistory replayでは再送しない。再接続後に過去音声を自動再生しない。

## 7. 口パク

口パクは音声再生またはaudio chunkをtriggerにする。本文表示の成否と口パクの成否を同一状態にしない。

## 8. 検証

- module contract: request、event payload、chunk identity、text分離
- unit: pending、ACK、timeout、late / duplicate ACK、public route cleanup
- integration: TTS bridgeからEventHub / Viewer routeまで
- browser E2E: 実再生、speaker OFF、blocked playback、desktop / narrow、console / network error

音声生成成功だけでViewer再生成功と報告しない。
