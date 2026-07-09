# STT_API（AUDIO_Client仕様）

## 1. 対象
本書は、Chat から見た STT API 契約をベンダー非依存で定義する。  
対象I/Fは以下。
- Browser <-> Chat（WebSocket）
- Chat -> STT Provider（HTTP）

## 2. Browser <-> Chat WebSocket

### 2.1 接続
- Path: `/ws`
- Protocol: `ws` / `wss`

### 2.2 Client -> Chat
- Binary audio
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

### 2.3 Chat -> Client
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

## 3. Chat -> STT Provider HTTP 契約（抽象）

### 3.1 Endpoint
- Method: `POST`
- URL: `${STT_PROVIDER_URL}`

### 3.2 Request
- `Content-Type: multipart/form-data`
- Required fields:
  - `file` (音声バイナリ)
- Optional fields:
  - `response_format=json`

### 3.3 Response（最小契約）
```json
{ "text": "認識結果" }
```

### 3.4 Error handling
- HTTP非2xx: Chat 側で警告ログ化し、継続可能な失敗として扱う。
- 例外/timeout: fail-safe（空文字またはエラー通知）にフォールバック。

## 4. タイムアウト/再試行
- `STT_TIMEOUT_MS` を設定（例: 15000）
- 再試行回数は小さく固定（例: 0〜1）

## 5. 実装差分注記
- Providerごとに path/field 差分があり得るため、Adapter層で吸収する。
- Client側契約（`draft`/`final`/`error`）は Provider 変更時も維持する。

## 6. 実装例（現行採用）
- Chat base URL（例）: `https://192.168.1.36:18790`
- `STT_PROVIDER_URL=http://192.168.1.36:8080/inference`
- Providerは Whisper（`file` + `response_format=json` + `text`）
- Provider health 応答例: `{"status":"ok"}`

## 7. API-DOD チェックリスト（実行/証跡付き）

### `API-DOD-STT-C-01`
- 判定基準: `/ws` で `speech_start` / `draft` / `final` / `error` を受信できる
- 検証コマンド例:
  - `wscat -c wss://192.168.1.36:18790/ws --no-check`
  - （ローカルGateway直結で検証する場合）`wscat -c ws://127.0.0.1:8787/ws`
  - 接続後に `{"type":"config","mimeType":"audio/webm;codecs=opus"}` を送信し、音声投入時のイベント受信を確認
- 証跡:
  - [ ] 受信ログ抜粋をPRに添付
  - [ ] 実施日時/実施者を `API_DOD_CHECKLIST.md` に記録

### `API-DOD-STT-C-02`
- 判定基準: `file` を含む multipart で Provider 呼び出しができる
- 検証コマンド例:
  - `curl -sS -X POST "http://192.168.1.36:8080/inference" -F "file=@/tmp/stt_test.wav" -F "response_format=json"`
- 証跡:
  - [ ] レスポンス本文（`text` を含む）をPRに添付
  - [ ] 使用した `STT_PROVIDER_URL` を記録

### `API-DOD-STT-C-03`
- 判定基準: Provider失敗時に fail-safe（空文字または `error` 通知）へフォールバックする
- 検証コマンド例:
  - `STT_PROVIDER_URL=http://127.0.0.1:9/inference` でChatを起動し、`wss://192.168.1.36:18790/ws` 経由で最終化を発火して挙動確認
- 証跡:
  - [ ] fail-safe動作ログ（空文字継続または `error` 通知）を添付
  - [ ] 通常系への復帰確認を記録

### `API-DOD-STT-C-04`
- 判定基準: `STT_TIMEOUT_MS` と再試行回数が設定で制御できる
- 検証コマンド例:
  - `STT_TIMEOUT_MS=1000 STT_RETRY_COUNT=1 <chat起動コマンド>`
  - 遅延Providerに対して timeout/retry が設定どおり発生するか確認
- 証跡:
  - [ ] 設定値と実測ログをPRに添付
  - [ ] timeout発生時の継続性を記録
