# STT Server側 現状サマリ

## 1. この文書の位置づけ

`AUDIO_Server仕様/STT` の複数ドキュメントを、**Server実装観点で最短把握**できるように統合した要約。
詳細は各原本（`STT仕様.md`, `STT_API.md`, `STT実装仕様.md`, `STT_ノイズ抑制・誤り訂正仕様.md`）を参照する。

---

## 2. Server側の責務（固定）

- STT Gateway と STT Provider の連携を提供する
- 発話状態に応じて `speech_start` / `draft` / `final` を返す
- 障害時は fail-safe（継続可能な失敗処理）を優先する
- Provider依存を Adapter 層で吸収し、ベンダー非依存契約を維持する

---

## 3. API契約（Server側）

## 3.1 HTTP

- `GET /health` -> `{ "ok": true }`

## 3.2 WebSocket

- Endpoint: `/ws`
- Client -> Server:
  - binary audio
  - `config` / `vad` / `final_pending`
- Server -> Client:
  - `speech_start`
  - `draft`
  - `final`
  - `reply_reset`
  - `reply_delta`
  - `error`

## 3.3 Provider HTTP（抽象契約）

- `POST ${STT_PROVIDER_URL}`
- `multipart/form-data`
- 必須: `file`
- 任意: `response_format=json`
- 期待レスポンス最小契約: `{ "text": "..." }`

---

## 4. 実装仕様の要点

## 4.1 論理構成

```text
[Client]
  -> WS /ws
[STT Gateway]
  -> ProviderAdapter.infer()
[STT Provider]
```

## 4.2 必須設定（初期値）

- `STT_PROVIDER_URL = http://127.0.0.1:8080/inference`
- `STT_TIMEOUT_MS = 15000`
- `STT_MIN_AUDIO_BYTES = 32044`
- `STT_DRAFT_INTERVAL_MS = 2000`
- `STT_FRAME_SAMPLES = 1536`
- `STT_MAX_RETRY = 0`
- `STT_BUSY_POLICY = drop`
- `STT_ALLOW_NON_RIFF = false`

## 4.3 状態遷移（1セッション）

- `idle` -> `speaking` -> `finalizing` -> `idle`
- 発話開始で `speech_start`
- 発話中に定期 `draft`
- 発話終了で `final`
- busy時挙動は `STT_BUSY_POLICY`（初期推奨は `drop`）

---

## 5. エラー/障害時ポリシー

- `INVALID_MESSAGE`: `error` 送信、接続継続
- `INVALID_AUDIO`: warn ログ、チャンク破棄
- `AUDIO_TOO_SHORT`: 推論スキップ（空文字）
- `PROVIDER_TIMEOUT` / `PROVIDER_HTTP_ERROR` / `PROVIDER_EXCEPTION`:
  - 当該リクエスト失敗
  - セッション全体は継続

原則: **停止より継続**、致命エラー時のみ切断。

---

## 6. ノイズ抑制・誤り訂正（Server側）

- ノイズ抑制方式は現時点で固定未完了（段階導入方針）
- 誤り訂正は `raw_text` と `normalized_text` を分離管理
- 判定フラグ:
  - `OK`
  - `RECONSTRUCT`
  - `CONFIRM`
- 危険語かつ確信不足は `CONFIRM` 優先
- 補正失敗時は fail-open（継続）

---

## 7. 運用・監視の最低要件

- 起動順序: Provider -> Gateway
- 追跡ログ必須項目:
  - `request_id`, `session_id`, `phase`, `elapsed_ms`, `input_bytes`, `result`, `error_code`
- 推奨メトリクス:
  - draft/final latency
  - provider error rate
  - busy drop count
  - audio too short count

---

## 8. 実装前提（現行採用例）

- Gateway: `voice-bridge`（`webui/voice-bridge/server.js`）
- Provider: Whisper (`/inference`)
- ただし仕様自体は Provider 非依存で、契約準拠なら差し替え可能

---

## 8.1 voice-bridge 詳細

### ファイル構成

| ファイル | 役割 |
|---|---|
| `webui/voice-bridge/server.js` | メイン実装（HTTP/WS、VAD、Whisper連携） |
| `webui/voice-bridge/server-https.js` | HTTPS版（同一ロジック） |
| `webui/voice-bridge/stt-gateway-contract.js` | 共有ロジック（WS_PATHS、VAD設定、制御メッセージ処理） |
| `webui/voice-bridge/public/` | UI（テスト用ブラウザクライアント） |

### エンドポイント

- `/stt-ws`（推奨）
- `/ws`（後方互換）

両パスとも同一ハンドラが処理する。

### 起動

```bash
cd webui/voice-bridge
npm start
# または HTTPS版
node webui/voice-bridge/server-https.js
```

### 主要環境変数

| 変数 | 既定値 | 説明 |
|---|---|---|
| `STT_PORT` | `8090` | 待受ポート |
| `STT_PROVIDER_URL` | `http://127.0.0.1:8080/inference` | Whisper エンドポイント |
| `STT_DRAFT_ENABLED` | `false` | draft推論の有効化 |
| `STT_SILENCE_END_MS` | `850` | 沈黙判定時間（ms） |
| `STT_FINALIZE_HOLD_MS` | `240` | final遅延保留時間（ms） |
| `STT_MIN_SPEECH_MS` | `180` | 最小発話時間（ms） |
| `STT_MIN_AUDIO_BYTES` | `32044` | 推論対象最小バイト（約1秒） |
| `STT_BUSY_POLICY` | `drop` | busy時挙動（`drop`/`queue_latest`） |
| `STT_MAX_RETRY` | `1` | Provider再試行回数 |

### session_info メッセージ

接続確立時および `config` 受信時に、サーバーからクライアントへ送信する。

```json
{ "type": "session_info", "session_id": "sess-xxxxxx" }
```

- `session_id` はセッションごとにサーバーが生成
- クライアント/サーバーのログ突合に使用する
- クライアントは受信した `session_id` を保持・表示することを推奨

### VAD（サーバーサイド）

- ライブラリ: `@ricky0123/vad-node`（Silero VAD）
- フレームサイズ: `STT_FRAME_SAMPLES`（既定 1536 samples = 96ms @ 16kHz）
- ノイズ除去: `@jitsi/rnnoise-wasm`（クライアント側 AudioWorklet と連携）
- クライアントは生PCMを送信するだけでよい。発話判定はすべてサーバーが行う

---

## 9. 実装・検証時の参照順

1. `STT_API.md`（契約）
2. `STT実装仕様.md`（設計）
3. `STT_ノイズ抑制・誤り訂正仕様.md`（品質機能）
4. `docs/STT_TTS/API_DOD_CHECKLIST.md`（証跡集約）

---

## 10. 再起動時の最重要ポイント

- 起動順序は `Provider -> Gateway` を厳守する
- 8080 既存占有がある場合は、旧 `whisper-server.exe` の残留を疑う
- 起動後は `CommandLine` を実測し、想定引数（`-l ja --convert --split-on-word` など）を確認する
- known-good WAV と `samples/jfk.wav` で `/inference` を即時確認し、失敗時は運用投入しない
- 詳細チェックリストは `STT実装仕様.md` の「16. 再起動時注意事項（運用チェックリスト）」を参照

