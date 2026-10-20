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
| 修正後の実画面 E2E | この記録時点では未確認 |

## 未確認

配備後の Viewer 音声取得 200、実再生、再生 ACK、利用者割り込み後の遅延音声破棄は実経路で確認する。
