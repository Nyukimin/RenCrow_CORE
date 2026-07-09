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
