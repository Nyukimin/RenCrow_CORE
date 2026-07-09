# STT_API（AUDIO_Server仕様）

## 1. 対象
本書は STTサーバが提供する API 契約をベンダー非依存で定義する。
- Browser <-> STT Gateway（WebSocket）
- STT Gateway <-> STT Provider（HTTP）
- STT Gateway health（HTTP）

## 2. HTTP API

### 2.1 Health
- Method: `GET`
- Path: `/health`
- Response:
```json
{ "ok": true }
```

## 3. WebSocket API

### 3.1 Endpoint
- Path: `/ws`
- 既定例: `ws://127.0.0.1:8090/ws`（`STT_PORT` で変更可能）

### 3.2 Client -> Server
- Binary audio
  - 通常運用系の推奨: `16kHz / mono / PCM16 / RIFF(WAV)`
  - 非RIFF入力は `STT_ALLOW_NON_RIFF=true` 時のみ受理（既定は拒否）
- JSON control message

`config`
```json
{ "type": "config", "mimeType": "audio/webm;codecs=opus" }
```

`vad`
```json
{ "type": "vad", "speaking": true }
```

`final_pending`
```json
{ "type": "final_pending" }
```

注記:
- 現行通常系（`server.js`）では `config` / `vad` / `final_pending` は後方互換受理が中心で、no-op となる場合がある。

### 3.3 Server -> Client
`speech_start`
```json
{ "type": "speech_start" }
```

`draft`
```json
{ "type": "draft", "text": "..." }
```

`final`
```json
{ "type": "final", "text": "..." }
```

`reply_reset`
```json
{ "type": "reply_reset" }
```

`reply_delta`
```json
{ "type": "reply_delta", "text": "..." }
```

`error`
```json
{ "type": "error", "text": "..." }
```

## 4. STT Provider HTTP 契約（抽象）
- Method: `POST`
- URL: `${STT_PROVIDER_URL}`
- Content-Type: `multipart/form-data`
- Required fields:
  - `file` (音声バイナリ)
- Optional fields:
  - `response_format=json`（Provider互換モードに応じて付与）
- Response（最小契約）:
```json
{ "text": "認識結果" }
```

## 5. エラー契約
- Provider非2xx: server内で失敗処理し、必要に応じて空文字継続
- 不正メッセージ: `error`
- timeout: 当該リクエスト失敗扱い、後続は継続

## 6. 設定契約
- `STT_PROVIDER_URL`: Provider 推論エンドポイント
- `STT_TIMEOUT_MS`: Provider 呼び出し timeout
- `STT_MIN_AUDIO_BYTES`: 最小音声サイズしきい値
- `STT_DRAFT_INTERVAL_MS`: draft 推論間隔
- `STT_FRAME_SAMPLES`: VAD フレームサイズ
- `STT_MAX_RETRY`: Provider 再試行回数
- `STT_BUSY_POLICY`: busy時の再入制御（`drop` / `queue_latest`）
- `STT_ALLOW_NON_RIFF`: 非RIFF入力許容フラグ
- `STT_PORT`: STT Gateway 待受ポート

## 7. 実装例（現行採用）
- `STT_PROVIDER_URL=http://127.0.0.1:8080/inference`
- Providerは Whisper（`POST /inference`, `file`, `response_format=json`）

## 8. API-DOD チェックリスト（実行/証跡付き）

### `API-DOD-STT-S-01`
- 判定基準: `/health` が常に `{ "ok": true }` を返す
- 検証コマンド例:
  - `curl -sS "http://127.0.0.1:${STT_PORT}/health"`（既定: `8090`）
- 証跡:
  - [ ] レスポンスJSONをPRに添付
  - [ ] 連続3回以上実行結果を記録

### `API-DOD-STT-S-02`
- 判定基準: `/ws` が `speech_start` / `draft` / `final` / `error` を返せる
- 検証コマンド例:
  - `wscat -c ws://127.0.0.1:${STT_PORT}/ws`（既定: `8090`）
  - `config` 送信後、音声投入と不正メッセージ投入で正常系/異常系イベントを確認
- 証跡:
  - [ ] 正常系イベント受信ログを添付
  - [ ] 異常系 `error` 受信ログを添付

### `API-DOD-STT-S-03`
- 判定基準: `STT_PROVIDER_URL` への multipart `file` 呼び出しを実装している
- 検証コマンド例:
  - `curl -sS -X POST "$STT_PROVIDER_URL" -F "file=@sample.wav" -F "response_format=json"`
- 証跡:
  - [ ] Provider呼び出し成功ログ（request/response）を添付
  - [ ] `text` フィールド抽出結果を記録

### `API-DOD-STT-S-04`
- 判定基準: Provider非2xx/timeout/例外を分類し、継続可能な失敗処理を行う
- 検証コマンド例:
  - `STT_PROVIDER_URL=http://127.0.0.1:9/inference` で起動して失敗注入
  - timeout注入時の再要求継続性を確認
- 証跡:
  - [ ] 非2xx/timeout/例外それぞれのログ分類結果を添付
  - [ ] 後続セッション継続結果を記録

### `API-DOD-STT-S-05`
- 判定基準: `STT_PROVIDER_URL` / `STT_TIMEOUT_MS` / `STT_MIN_AUDIO_BYTES` を設定で変更可能
- 検証コマンド例:
  - `STT_PROVIDER_URL=http://127.0.0.1:8080/inference STT_TIMEOUT_MS=1000 STT_MIN_AUDIO_BYTES=64000 STT_PORT=8091 <server起動コマンド>`
- 証跡:
  - [ ] 起動時設定読み込みログを添付
  - [ ] しきい値変更時の挙動差分を記録
