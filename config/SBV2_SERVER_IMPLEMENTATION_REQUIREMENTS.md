# SBV2 サーバ実装要件（テスト連携用）

このドキュメントは、RenCrow 側から SBV2 サーバを利用するための契約を整理したものです。  
サーバ側の実装担当へ、そのまま共有できます。

## 1. 前提（RenCrow 側の利用モード）

RenCrow では SBV2 を次の 2 経路で利用します。

1. **Autonomous 直呼び出し（HTTP /synthesis）**
   - `tts.sbv2.base_url` を POST 先として直接呼ぶ
2. **TTS Client Bridge（HTTP + WebSocket）**
   - `tts.http_base_url` と `tts.ws_url` を使ってセッション制御・ストリーミング再生

どちらを使うかは機能経路に依存するため、**両方の契約を満たす実装**を推奨します。

---

## 2. 必須 API 契約（Autonomous 直呼び出し）

`POST {tts.sbv2.base_url}`  
例: `http://<host>:5000/synthesis`

### リクエスト JSON

```json
{
  "text": "こんにちは",
  "voice_id": "mio",
  "emotion": "calm",
  "speed": 1.0,
  "pitch": 0.0
}
```

- `text` は必須（空文字は不可）
- `voice_id` は任意（未指定時はサーバデフォルトでも可）
- `emotion`, `speed`, `pitch` は任意（未使用なら無視して可）

### レスポンス JSON（必須）

```json
{
  "audio_path": "/tmp/sbv2.wav",
  "duration_ms": 1234,
  "voice_id": "mio"
}
```

- `audio_path` は必須（空だと RenCrow 側で失敗）
- `duration_ms` は任意（0/省略可）
- `voice_id` は任意（省略時はリクエスト値を継承）
- HTTP ステータスは **2xx**

### `audio_path` の取り扱い

- 絶対パス: そのまま利用
- 相対パス（例: `cache\\oneshot-xxx.wav`）:  
  RenCrow 側で `tts.audio_path_root` と結合して解決
- Windows 区切り（`\`）は `/` に正規化されても問題なし

---

## 3. 必須 API 契約（TTS Client Bridge）

### 3.1 Ready チェック

`GET {tts.http_base_url}/health/ready`

#### レスポンス JSON（必須）

```json
{
  "status": "ready",
  "voices": ["female_01", "male_01", "mio"]
}
```

- HTTP 200 かつ `status == "ready"` が必要
- `voices` が返る場合、要求 `voice_id` を含むこと

### 3.2 WebSocket セッション

`{tts.ws_url}`  
例: `ws://<host>:8765/sessions`

#### RenCrow -> サーバ（送信）

1. `session_start`
```json
{
  "type": "session_start",
  "session_id": "sess-123",
  "response_id": "resp-123",
  "character": "mio",
  "voice_id": "female_01",
  "speech_mode": "conversational",
  "context": {
    "event": "conversation",
    "urgency": "normal",
    "conversation_mode": "chat",
    "user_attention_required": false,
    "user_waiting_time_sec": 0
  }
}
```

2. `text_delta`（複数回）
```json
{
  "type": "text_delta",
  "session_id": "sess-123",
  "seq": 1,
  "text": "こんにちは",
  "emitted_at": "2026-02-22T14:00:00Z",
  "emotion_state": {
    "primary_emotion": "warm",
    "prosody": {
      "speed": 1.0,
      "pitch": 0.0,
      "pause": 0.1,
      "expressiveness": 0.6
    }
  }
}
```

3. `session_end`
```json
{
  "type": "session_end",
  "session_id": "sess-123",
  "is_final": true
}
```

#### サーバ -> RenCrow（受信）

1. `audio_chunk_ready`（0回以上）
```json
{
  "type": "audio_chunk_ready",
  "chunk_index": 0,
  "text": "こんにちは",
  "audio_path": "/tmp/chunk0.wav",
  "audio_url": "http://<host>:8765/audio/chunk0.wav",
  "pause_after": "short"
}
```

- `audio_path` または `audio_url` のどちらかは必須
- `chunk_index` は再生順に使うため、単調増加を推奨

2. `session_completed`
```json
{
  "type": "session_completed",
  "session_id": "sess-123"
}
```

3. `error`（失敗時）
```json
{
  "type": "error",
  "code": "synthesis_failed",
  "message": "detail message"
}
```

### 3.3 Fallback HTTP（WS未利用時）

`POST {tts.http_base_url}/synthesize`

#### リクエスト
```json
{
  "text": "こんにちは",
  "voice_id": "female_01",
  "emotion_state": {
    "primary_emotion": "calm"
  }
}
```

#### レスポンス
```json
{
  "text": "こんにちは",
  "audio_path": "/tmp/oneshot.wav",
  "audio_url": "http://<host>:8765/audio/oneshot.wav"
}
```

- 2xx を返すこと
- `audio_path` または `audio_url` のどちらかを返すこと

---

## 4. タイムアウト要件（RenCrow 側）

- Connect timeout: `tts.connect_timeout_ms`（既定 3000ms）
- Receive timeout: `tts.receive_timeout_ms`（既定 15000ms）
- Chunk gap timeout: `tts.chunk_gap_timeout_ms`（既定 3000ms）
- SBV2 直呼び出し timeout: `tts.sbv2.timeout_sec`（既定 20秒）

サーバはこの範囲で応答できること。

---

## 5. テスト観点（サーバ側で事前確認）

1. `/health/ready` が 200 + `status=ready` を返す
2. 指定 `voice_id` が `voices` に含まれる
3. `/synthesis` が `audio_path` を返す
4. WS で `session_start` -> `text_delta` -> `session_end` を受け、`audio_chunk_ready` -> `session_completed` を返す
5. 異常時に `type=error` を返す
6. 相対 `audio_path` を返した場合でも、RenCrow 側で再生可能な実ファイルを参照できる

---

## 6. RenCrow 設定で揃える項目

- `tts.enabled`
- `tts.http_base_url`
- `tts.ws_url`
- `tts.audio_path_root`
- `tts.voice_id`
- `tts.sbv2.enabled`
- `tts.sbv2.base_url`
- `tts.sbv2.voice_id`
- `tts.sbv2.timeout_sec`

以上を一致させれば、SBV2 サーバ変更後の E2E テストが可能です。
