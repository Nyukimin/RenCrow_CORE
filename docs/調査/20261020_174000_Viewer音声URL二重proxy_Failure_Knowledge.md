# Viewer 音声 URL の二重 proxy 化（2026-10-20）

## 文書情報

- 日付: 2026-10-20 JST
- 引き継ぎ元: RenCrow × Irodori-TTS SpeechPlan v1.0 の 206／207 Viewer E2E
- 契約正本: `docs/02_機能仕様.md` の「Main LLM発話統合（SpeechPlan v1.0）」
- 検出・根拠: 206 の `speech.transport.observed` は Gateway 合成成功後に `speech_playback_failed`。実 Viewer の `/viewer/tts/audio?url=...` 取得は 404。

## 結論

### Failure / Problem

Mio の WAV は Gateway で合成されて CORE の `tts.audio_chunk` まで届いたが、Viewer が音声を取得できず実再生に至らなかった。

### Cause

CORE が発行する同一 origin の相対 URL `/viewer/tts/audio?path=...` を、Viewer の `resolveTTSPlaybackURL` が絶対 URL に解決してから `/viewer/tts/audio?url=...` へ包み直した。後者は許可された Gateway host のリモート音声だけを proxy するため、CORE 自身を指す URL を 404 で拒否した。

### Lesson / Invariant

CORE のローカル音声 URL は Viewer でそのまま取得する。リモートの絶対 URL と protocol-relative URL だけを既存の許可 host 検証付き proxy へ渡す。

### Enforcement / Tests

`viewer_audio_button.test.mjs` はローカル root-relative URL の維持、絶対リモート URL と protocol-relative リモート URL の proxy を検証する。`resolveTTSPlaybackURL` は単一 slash の root-relative URL を変換せず返す。

## 実測

| 境界 | 結果 |
|---|---|
| SpeechPlan と Gateway 合成 | 有効。Mio WAV と `tts.audio_chunk` を確認 |
| Viewer 音声取得 | 修正前は 404、実再生失敗 |
| 修正後の実画面 E2E | CORE `747fee5c` でローカル音声取得は HTTP 206（WAV の Range 応答）。3.36 秒の実音声で `playing`／`ended` を確認し、CORE の `playback_duration_ms=3428`、`fallback_reason=null` |

## 未確認

2 発話 turn `turn_01a1587d-2bc3-7e32-9f17-673bb763c8fd` でも両 WAV の `ended` と CORE の再生時間 2565／4773 ms、`fallback_reason=null` を確認した。割り込み turn `turn_01a1587b-8e8b-7812-8c6a-615f84beb934` では cancel API 204 の後に未再生の 2 発話目は再生されなかった。別ブラウザや他 OS の実画面結果は未確認。
