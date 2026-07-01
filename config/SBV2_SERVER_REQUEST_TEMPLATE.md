# SBV2 サーバ実装依頼（RenCrow Live 接続用）

RenCrow Live で SBV2 音声連携を確認したいため、以下の API 契約に対応をお願いします。

## 1. 接続情報

- HTTP Base URL: `http://<host>:<port>`
- WebSocket URL: `ws://<host>:<port>/sessions`
- （任意）SBV2 直呼び出し URL: `http://<host>:5000/synthesis`

## 2. 必須エンドポイント

### GET `/health/ready`

- HTTP 200 を返す
- 例:

```json
{
  "status": "ready",
  "voices": ["female_01", "male_01", "mio"]
}
```

### POST `/synthesize`（Bridge fallback 用）

- リクエスト例:

```json
{
  "text": "こんにちは",
  "voice_id": "female_01",
  "emotion_state": {
    "primary_emotion": "calm"
  }
}
```

- レスポンス例（`audio_path` or `audio_url` のどちらか必須）:

```json
{
  "text": "こんにちは",
  "audio_path": "/tmp/oneshot.wav",
  "audio_url": "http://<host>:<port>/audio/oneshot.wav"
}
```

### （任意）POST `/synthesis`（SBV2 直呼び出し）

- リクエスト例:

```json
{
  "text": "こんにちは",
  "voice_id": "mio",
  "emotion": "calm",
  "speed": 1.0,
  "pitch": 0.0
}
```

- レスポンス例（`audio_path` 必須）:

```json
{
  "audio_path": "/tmp/sbv2.wav",
  "duration_ms": 1234,
  "voice_id": "mio"
}
```

## 3. WebSocket 契約（`/sessions`）

### クライアント -> サーバ

- `session_start`
- `text_delta`（複数回）
- `session_end`

### サーバ -> クライアント

- `audio_chunk_ready`（0回以上）
  - `audio_path` or `audio_url` のどちらか必須
  - `chunk_index` は単調増加推奨
- `session_completed`
- `error`（異常時）

## 4. 実装上の注意

- `voice_id` 指定時は `voices` に含まれること
- HTTP は 2xx を返すこと（異常時は 4xx/5xx + エラーボディ）
- 相対 `audio_path` でも可（実在ファイル参照が前提）
- パス区切りは `\` / `/` いずれでも解決可能にすること

## 5. 事前テスト項目（お願い）

1. `/health/ready` が `status=ready` を返す
2. `/synthesize` で `audio_path` または `audio_url` が返る
3. WS で `session_start -> text_delta -> session_end` を受けて `audio_chunk_ready -> session_completed` を返せる
4. 異常時に `type=error` を返せる

---

詳細版仕様は `config/SBV2_SERVER_IMPLEMENTATION_REQUIREMENTS.md` を参照してください。
