# TTS_API（AUDIO_Server仕様）

## 1. 対象
本書は TTSサーバが提供する API 契約をベンダー非依存で定義する。

## 2. Health API

### `GET /health/live`
Response:
```json
{ "status": "live" }
```

### `GET /health/ready`
Response:
```json
{
  "status": "ready",
  "voices": ["female_01", "mio"],
  "device": "cpu",
  "device_setting": "auto",
  "cuda_available": false,
  "capabilities": {
    "streaming": true,
    "multi_chunk": true,
    "multi_track": false
  }
}
```

## 3. Direct Synthesis API

### `POST /synthesis`
Request:
```json
{
  "text": "こんにちは",
  "voice_id": "voice_a",
  "emotion": "calm",
  "speed": 1.0,
  "pitch": 0.0,
  "track": "main"
}
```

Response:
```json
{
  "audio_path": "cache/oneshot-abc123.wav",
  "audio_url": "https://<host>/audio/oneshot-abc123.wav",
  "duration_ms": 1234,
  "voice_id": "voice_a",
  "track": "main"
}
```

## 4. Bridge Fallback API

### `POST /synthesize`
Request:
```json
{
  "text": "こんにちは",
  "voice_id": "voice_a",
  "emotion_state": { "primary_emotion": "calm" },
  "session_id": "sess-123"
}
```

Response:
```json
{
  "text": "こんにちは",
  "audio_path": "cache/oneshot-xyz.wav",
  "audio_url": "https://<host>/audio/oneshot-xyz.wav",
  "chunk_index": 0,
  "track": "main"
}
```

## 5. Streaming API

### `WS /sessions`

#### Client -> Server
`session_start`
```json
{
  "type": "session_start",
  "session_id": "sess-123",
  "response_id": "resp-123",
  "character": "assistant",
  "voice_id": "voice_a",
  "tracks": ["main"]
}
```

`text_delta`
```json
{
  "type": "text_delta",
  "session_id": "sess-123",
  "seq": 1,
  "text": "こんにちは",
  "track": "main"
}
```

`session_end`
```json
{
  "type": "session_end",
  "session_id": "sess-123",
  "is_final": true
}
```

#### Server -> Client
`audio_chunk_ready`
```json
{
  "type": "audio_chunk_ready",
  "session_id": "sess-123",
  "chunk_index": 0,
  "track": "main",
  "text": "こんにちは",
  "audio_path": "cache/sess-123_000.wav",
  "audio_url": "https://<host>/audio/sess-123_000.wav",
  "pause_after": "short"
}
```

`session_completed`
```json
{ "type": "session_completed", "session_id": "sess-123" }
```

`error`
```json
{ "type": "error", "code": "SYNTHESIS_FAILED", "message": "detail" }
```

## 6. 複数本音声契約
- `audio_chunk_ready` は0回以上返却可能。
- `chunk_index` は単調増加必須。
- `track` を持つ場合、`(track, chunk_index)` の組で順序一意性を担保する。
- `session_completed` は全トラックの flush 完了後に返す。

## 7. エラー契約
- HTTP: `400`, `404`, `409`, `503`, `500`
- WS: `VOICE_NOT_FOUND`, `SESSION_NOT_FOUND`, `INVALID_SEQ`, `SYNTHESIS_FAILED`, `UNKNOWN_MESSAGE_TYPE`

## 8. API運用要件
- `audio_path` または `audio_url` のどちらか必須（推奨: 両方）
- timeout を明示し、応答不能時は fail-fast で `error` を返す

## 9. 実装例（現行採用）
- 現行例: SBV2
- 将来候補: Irodori 等
- 現行の既定待受ポート: `8765`
- デバイス設定例:
  - `RENCROW_SBV2_DEVICE=auto`（既定）
  - `RENCROW_SBV2_DEVICE=cuda`
  - `RENCROW_SBV2_DEVICE=cpu`

## 10. API-DOD チェックリスト（実行/証跡付き）

### `API-DOD-TTS-S-01`
- 判定基準: `/health/live` と `/health/ready` を提供する
- 検証コマンド例:
  - `curl -sS http://127.0.0.1:8765/health/live`
  - `curl -sS http://127.0.0.1:8765/health/ready | jq`
- 証跡:
  - [ ] 両APIレスポンスをPRに添付
  - [ ] `capabilities` 値を記録

### `API-DOD-TTS-S-02`
- 判定基準: `/synthesis` と `/synthesize` の両経路を提供する
- 検証コマンド例:
  - `curl -sS -X POST http://127.0.0.1:8765/synthesis -H 'Content-Type: application/json' -d '{"text":"test","voice_id":"female_01","track":"main"}'`
  - `curl -sS -X POST http://127.0.0.1:8765/synthesize -H 'Content-Type: application/json' -d '{"text":"test","voice_id":"female_01","session_id":"sess-1"}'`
- 証跡:
  - [ ] 両経路の成功レスポンスを添付
  - [ ] `audio_path` / `audio_url` の返却確認を記録

### `API-DOD-TTS-S-03`
- 判定基準: `WS /sessions` で `audio_chunk_ready` を0回以上返せる
- 検証コマンド例:
  - `wscat -c ws://127.0.0.1:8765/sessions`
  - `session_start` -> `session_end`（0件ケース）と `text_delta` ありケースの両方を実施
- 証跡:
  - [ ] 0件ケースの `session_completed` ログを添付
  - [ ] 1件以上ケースの `audio_chunk_ready` ログを添付

### `API-DOD-TTS-S-04`
- 判定基準: `chunk_index` 単調増加と `(track, chunk_index)` 一意性を保証する
- 検証コマンド例:
  - 複数チャンク生成を発生させ、受信イベント列を抽出して検査
- 証跡:
  - [ ] `chunk_index` 推移ログを添付
  - [ ] `(track, chunk_index)` 重複なし確認を記録

### `API-DOD-TTS-S-05`
- 判定基準: `session_completed` は全トラックflush後に返す
- 検証コマンド例:
  - 複数トラック指定の `session_start` を送信し、全チャンク出力完了後の終端イベント順を確認
- 証跡:
  - [ ] イベント時系列ログを添付
  - [ ] 早期 `session_completed` がないことを記録

### `API-DOD-TTS-S-06`
- 判定基準: HTTP/WSエラー契約（`400/404/409/503/500`, error code群）を満たす
- 検証コマンド例:
  - 不正リクエスト送信でHTTPエラーを確認
  - WSで未知メッセージを送信して `UNKNOWN_MESSAGE_TYPE` などを確認
- 証跡:
  - [ ] HTTPエラーレスポンス一覧を添付
  - [ ] WSエラーコード受信ログを添付
