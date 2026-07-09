# TTS_API（AUDIO_Client仕様）

## 1. 対象
本書は、Chat から見た TTS API 契約をベンダー非依存で定義する。  
対象I/Fは以下。
- Chat <-> TTS Gateway（HTTP / WebSocket）

## 2. Bridge API

### 2.1 Readiness
- Method: `GET`
- Path: `/health/ready`
- Response例:
```json
{
  "status": "ready",
  "voices": ["female_01", "mio"],
  "device": "cpu"
}
```
備考:
- `capabilities` は実装により省略される場合がある。省略時は既定値で解釈する。

### 2.2 Fallback Synthesize
- Method: `POST`
- Path: `/synthesize`
- Request例:
```json
{
  "text": "こんにちは",
  "voice_id": "voice_a",
  "emotion_state": { "primary_emotion": "calm" },
  "session_id": "sess-123"
}
```
- Response例:
```json
{
  "text": "こんにちは",
  "audio_path": "cache/oneshot.wav",
  "audio_url": "https://<host>/audio/oneshot.wav",
  "chunk_index": 0,
  "track": "main"
}
```

### 2.3 Streaming WebSocket
- URL: `ws(s)://<host>/sessions`

#### Chat -> Server
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

#### Server -> Chat
`audio_chunk_ready`
```json
{
  "type": "audio_chunk_ready",
  "session_id": "sess-123",
  "chunk_index": 0,
  "track": "main",
  "text": "こんにちは",
  "audio_path": "cache/sess-123_000.wav",
  "audio_url": "https://<host>/audio/sess-123_000.wav"
}
```

`session_completed`
```json
{ "type": "session_completed", "session_id": "sess-123" }
```

`error`
```json
{ "type": "error", "code": "synthesis_failed", "message": "detail" }
```

## 3. Direct API（補助）
- Method: `POST`
- Path: `/synthesis`
- Request例:
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
- Response例:
```json
{
  "audio_path": "cache/oneshot-abc.wav",
  "audio_url": "https://<host>/audio/oneshot-abc.wav",
  "duration_ms": 1234,
  "voice_id": "voice_a",
  "track": "main"
}
```

## 4. 複数本音声契約（Client側）
- `audio_chunk_ready` は0回以上受信可能。
- `chunk_index` は単調増加。
- `track` がある場合は `(track, chunk_index)` で順序一意性を扱う。
- `session_completed` 受信までは再生キューを閉じない。

## 5. エラー契約
- HTTP: `400/404/409/503/500` を許容し、Chat はフォールバック可能にする。
- WS: `error` 受信時に該当セッションを停止し、再接続またはHTTP fallbackを実施する。

## 6. タイムアウト要件（Chat側）
- Connect timeout: 3秒目安
- Receive timeout: 15秒目安
- Synthesis timeout: 20秒目安

## 7. 実装例（現行採用）
- Chat -> TTS base URL（例）: `http://192.168.1.36:8765`
- 現行例: SBV2
- 将来候補: Irodori 等

## 8. API-DOD チェックリスト（実行/証跡付き）

### `API-DOD-TTS-C-01`
- 判定基準: `/health/ready` の `status` / `voices` / `capabilities` を解釈できる
- 検証コマンド例:
  - `curl -sS http://192.168.1.36:8765/health/ready | jq`
- 証跡:
  - [ ] レスポンスJSONをPRに添付
  - [ ] `streaming/multi_chunk/multi_track` の解釈結果を記録

### `API-DOD-TTS-C-02`
- 判定基準: `WS /sessions` で `audio_chunk_ready` を0回以上許容できる
- 検証コマンド例:
  - `wscat -c ws://192.168.1.36:8765/sessions`
  - `session_start` -> `session_end` のみ送信するケースと `text_delta` ありケースの両方を確認
- 証跡:
  - [ ] 0件完了ケースの受信ログを添付
  - [ ] 1件以上生成ケースの受信ログを添付

### `API-DOD-TTS-C-03`
- 判定基準: `(track, chunk_index)` で再生順を一意に制御できる
- 検証コマンド例:
  - 複数 `text_delta`（必要なら複数 `track`）を送信し、受信イベントの並びを記録
- 証跡:
  - [ ] 受信した `(track, chunk_index)` 一覧をPRに添付
  - [ ] ソート/再生順ロジックの結果を記録

### `API-DOD-TTS-C-04`
- 判定基準: `session_completed` 受信まで再生キューを閉じない
- 検証コマンド例:
  - 長文の `text_delta` を複数回送信し、最終完了イベントまでキュー状態を監視
- 証跡:
  - [ ] キュー状態遷移ログ（open -> completed）を添付
  - [ ] 早期クローズが発生しないことを記録

### `API-DOD-TTS-C-05`
- 判定基準: WS失敗時に `POST /synthesize` へフォールバックできる
- 検証コマンド例:
  - WS切断状態で `curl -sS -X POST http://192.168.1.36:8765/synthesize -H 'Content-Type: application/json' -d '{"text":"fallback test","voice_id":"female_01","session_id":"sess-fallback"}'`
- 証跡:
  - [ ] HTTPフォールバック成功レスポンスを添付
  - [ ] フォールバック発火条件を記録
