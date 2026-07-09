# STT実装仕様（AUDIO_Server仕様）

## 1. 目的
本書は、`STT仕様.md` と `STT_API.md` を満たすための**実装可能レベル**の設計仕様を定義する。  
本仕様はベンダー非依存であり、STT Provider（Whisper / 他Provider）を差し替えても成立する。

## 2. 実装スコープ
- STT Gateway 実装（WebSocket受け口、発話状態管理、draft/final送信）
- STT Provider Adapter 実装（Provider差分吸収）
- 音声前処理実装（形式判定、しきい値、フレーム化）
- エラー処理・監視・運用設定

非スコープ:
- 認識モデルの学習/再学習
- UI実装詳細

## 3. 論理アーキテクチャ
```text
[Client]
  -> WS /ws
[STT Gateway]
  -> ProviderAdapter.infer()
[STT Provider]
```

構成要素:
- `gateway`: WS接続管理、状態遷移、draft/finalオーケストレーション
- `audio_preprocess`: 受信バイナリ解析、PCM変換、フレーム分割
- `vad_engine`: 発話開始/終了判定
- `provider_adapter`: Provider HTTP 契約吸収
- `normalizer`: Provider応答の共通化（`text` 抽出、trim）

## 4. 主要設定（必須）

| key | type | default | 説明 |
|---|---:|---:|---|
| `STT_PROVIDER_URL` | string | `http://127.0.0.1:8080/inference` | Provider 推論URL |
| `STT_TIMEOUT_MS` | int | `15000` | Provider 推論タイムアウト |
| `STT_MIN_AUDIO_BYTES` | int | `32044` | 推論対象最小バイト |
| `STT_DRAFT_INTERVAL_MS` | int | `2000` | draft推論間隔 |
| `STT_FRAME_SAMPLES` | int | `1536` | VADフレームサイズ |
| `STT_MAX_RETRY` | int | `0` | Provider再試行回数 |
| `STT_BUSY_POLICY` | enum | `drop` | busy時の挙動（`drop`/`queue_latest`） |
| `STT_ALLOW_NON_RIFF` | bool | `false` | 非RIFF入力の許容可否 |

備考:
- `STT_MIN_AUDIO_BYTES` は系統別に上書き可（例: HTTPS拡張系では `256`）。

## 5. セッション状態モデル

### 5.1 セッション状態
- `idle`: 発話待機
- `speaking`: 発話中（draft対象）
- `finalizing`: final推論中（busy）

### 5.2 セッション保持データ
- `isSpeaking: bool`
- `busy: bool`
- `speechBuffer: Float32[]`（発話区間データ）
- `pcmRemainder: Float32[]`（端数バッファ）
- `draftTimer: timer?`
- `lastDraftAt: timestamp`
- `sessionId: string`

## 6. WebSocket処理仕様

### 6.1 接続
- Endpoint: `/stt-ws`（推奨）、`/ws`（後方互換）
- 1接続につき1セッション状態を保持
- 接続時に状態を `idle` 初期化
- 接続確立時に `session_info` を送信する（後述 6.4）

### 6.2 受信メッセージ
1. binary audio
2. JSON control (`config`, `vad`, `final_pending`)

実装方針:
- JSON制御は後方互換として受理
- サポート外値は `error` 通知しつつ接続継続

### 6.3 送信メッセージ
- `session_info`
- `speech_start`
- `draft`
- `final`
- `reply_reset`
- `reply_delta`
- `error`
- `status`

### 6.4 session_info

接続確立時、および `config` 受信時（echo）に送信する。

```json
{ "type": "session_info", "session_id": "sess-xxxxxx" }
```

- `session_id` はサーバーが接続ごとに生成（形式: `sess-{tag}-{timestamp36}-{random}`）
- クライアント/サーバーのログ突合に使用する
- クライアントは受信した `session_id` を保持することを推奨
- `config` echo は接続直後のメッセージ取りこぼしに対するリカバリ

## 7. 音声処理仕様

### 7.1 受信バイナリ処理
1. RIFF判定（`STT_ALLOW_NON_RIFF=false` なら非RIFFを破棄）
2. PCM16抽出
3. Float32変換
4. `STT_FRAME_SAMPLES` 単位でフレーム化
5. VADへ投入

### 7.2 発話開始
- VAD が開始判定したら:
  - `isSpeaking=true`
  - `speech_start` 送信
  - draftタイマー開始

### 7.3 発話中（draft）
- `STT_DRAFT_INTERVAL_MS` ごとにバッファスナップショット推論
- `busy=true` の間は `STT_BUSY_POLICY` に従う
- 推論成功で `draft` 送信

### 7.4 発話終了（final）
- draftタイマー停止
- `busy=true` に設定
- 発話全体バッファを final推論
- 推論成功で `final` 送信
- 後処理で `busy=false`, `isSpeaking=false`, バッファクリア

## 8. Provider Adapter 実装仕様

### 8.1 インターフェース
```text
infer(audioBuffer, mimeType, options) -> { text: string }
```

`options`:
- `timeoutMs`
- `signal`（キャンセル制御）
- `requestId`

### 8.2 リクエスト契約（共通）
- Method: `POST`
- URL: `STT_PROVIDER_URL`
- Content-Type: `multipart/form-data`
- Required field: `file`
- Optional field: `response_format=json`

### 8.3 レスポンス正規化
- 期待: `text`
- `text` が空/欠落時は空文字で扱う
- 前後空白圧縮と trim を適用

### 8.4 差し替え要件
Provider差し替え時は次を満たす:
- 音声入力を受理できる
- 最終テキストを返せる
- timeout/失敗を分類できる

## 9. エラー処理仕様

| 区分 | 条件 | 挙動 |
|---|---|---|
| `INVALID_MESSAGE` | JSON不正/未知type | `error` 送信、接続継続 |
| `INVALID_AUDIO` | 非対応形式/破損 | warnログ、チャンク破棄 |
| `AUDIO_TOO_SHORT` | `len < STT_MIN_AUDIO_BYTES` | 推論スキップ（空文字） |
| `PROVIDER_TIMEOUT` | timeout超過 | warnログ、当該推論失敗 |
| `PROVIDER_HTTP_ERROR` | 非2xx | warnログ、当該推論失敗 |
| `PROVIDER_EXCEPTION` | 例外 | warnログ、当該推論失敗 |

原則:
- fail-safe（継続）
- セッション全体停止はしない
- 致命エラー時のみ接続切断

## 10. 再入制御（busy）

### 10.1 `drop`
- busy中のdraft要求は破棄
- final処理優先

### 10.2 `queue_latest`
- busy中は最新1件のみ保持
- busy解除後に最新のみ再処理

初期実装は `drop` を推奨。

## 11. ログ/監視仕様

必須ログ項目:
- `request_id`
- `session_id`
- `provider`
- `phase`（draft/final）
- `elapsed_ms`
- `input_bytes`
- `result`（success/fail/skipped）
- `error_code`（失敗時）

推奨メトリクス:
- draft latency p50/p95
- final latency p50/p95
- provider error rate
- busy drop count
- audio too short count

## 12. セキュリティ/耐障害
- 入力サイズ上限を設ける（DoS緩和）
- multipart処理でファイル名を信用しない
- タイムアウトを必ず設定
- 外部Provider接続は許可先ホストを制限可能にする

## 13. 受け入れ基準（実装完了条件）

### 13.1 正常系
- 発話開始で `speech_start` が返る
- 発話中に `draft` が返る
- 発話終了で `final` が返る

### 13.2 異常系
- 不正JSONで `error` が返る
- Provider停止時にセッション継続できる
- timeout発生時に後続発話が処理される

### 13.3 性能系（目標）
- `final` 推論 p95 が `STT_TIMEOUT_MS` 未満
- busyドロップ率が運用閾値以下

## 14. 実装例（現行）

### voice-bridge（Gateway実装）

| ファイル | 役割 |
|---|---|
| `webui/voice-bridge/server.js` | メイン実装 |
| `webui/voice-bridge/server-https.js` | HTTPS版 |
| `webui/voice-bridge/stt-gateway-contract.js` | 共有ロジック（WS_PATHS、VAD設定、制御メッセージ処理） |

- 待受ポート: `STT_PORT`（既定 `8090`）
- エンドポイント: `/stt-ws`（推奨）、`/ws`（後方互換）
- VAD: `@ricky0123/vad-node`（Silero VAD、サーバーサイド）
- ノイズ除去: `@jitsi/rnnoise-wasm`（クライアント AudioWorklet 連携）
- 起動: `cd webui/voice-bridge && npm start`

### Provider（Whisper）

- エンドポイント: `STT_PROVIDER_URL`（既定 `http://127.0.0.1:8080/inference`）
- 起動補助: `ops/audioio/start-whisper.ps1`

本仕様は上記実装例に依存しない。Provider を I/F互換の別STTへ差し替えても成立する。

## 15. 検証コマンド例の参照
- 実装検証で使用するコマンドは `docs/STT_TTS/AUDIO_Server仕様/STT/STT_ノイズ抑制・誤り訂正仕様.md` の「検証コマンド例」節を一次参照とする。
- API-DOD の判定と証跡記録は `docs/STT_TTS/AUDIO_Server仕様/STT/STT_API.md` のAPI-DOD節および `docs/STT_TTS/API_DOD_CHECKLIST.md` を併用する。

## 16. 再起動時注意事項（運用チェックリスト）

STT サーバを長期運用する前提で、再起動時は以下を必ず実施する。

1. 既存プロセスの残留確認
   - 8080 を占有する旧 `whisper-server.exe` を停止してから起動する
   - 「Port 8080 is already listening. Skip start.」が出る場合は、想定外プロセス混在の可能性を疑う

2. 起動引数の実測確認
   - 起動後にプロセス `CommandLine` を取得し、想定引数と一致することを確認する
   - 既定の確認ポイント:
     - `--host 0.0.0.0`
     - `--port 8080`
     - `-m <model-path>`
     - `-l ja`
     - `--convert`
     - `--split-on-word`
   - 想定外引数が付与されている場合（例: 過去起動系由来のオプション）は再起動手順を見直す

3. 起動ログの健全性確認
   - `logs/audioio/start-whisper.log` に ERROR がないこと
   - 既知エラー例（例: `$extraWhisperArgs` 未定義）を検知した場合は、その起動を採用しない

4. 既知良好WAVで即時疎通確認
   - `mono / 16kHz / PCM16 / RIFF` の固定ファイル（known-good）で `/inference` を試験する
   - 最低2本で確認する:
     - 運用側 known-good WAV
     - `audioio/whisper.cpp/samples/jfk.wav`（ベースライン）
   - いずれかが `{"error":"FFmpeg conversion failed."}` になる場合、再起動完了とみなさない

5. Whisperログの失敗シグネチャ確認
   - `whisper.stdout.log` で `Received request` / `Successfully loaded` を確認
   - `whisper.stderr.log` で以下が連発する場合は異常として扱う:
     - `failed to decode`
     - `failed to encode`
     - `client disconnected, aborted processing`

6. Provider 正常化後に Gateway を起動
   - 起動順序は常に `Provider -> Gateway`
   - Provider 未確定状態で `voice-bridge` を先行起動しない

参考:
- `docs/STT_TTS/AUDIO_Server仕様/STT/STT_既知良好WAV_分離テスト結果_2026-04-10.md`
