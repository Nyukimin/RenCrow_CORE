# TTS実装仕様（AUDIO_Server仕様）

## 1. 目的
本書は TTS仕様を満たす実装方式を定義する。  
仕様本体はベンダー非依存とし、ここでは現行実装と差し替え要件を併記する。

## 2. 実装構成（抽象）
- TTS Gateway: HTTP/WS 受け口、セッション管理、順序制御
- TTS Provider Adapter: Providerごとの差分吸収
- Audio Artifact Store: 音声ファイル保存・配信
- Chunk/Track Manager: 複数本音声の順序管理

## 3. 必須実装要件

### 3.1 Readiness
- `GET /health/ready` で `status=ready` と `voices[]` を返す。
- `capabilities.multi_chunk=true` を返せること。

### 3.2 Direct
- `POST /synthesis` で `audio_path` または `audio_url` を返す。

### 3.3 Bridge Fallback
- `POST /synthesize` で単発生成を返却。

### 3.4 Bridge Streaming
- `WS /sessions` を提供。
- 受信: `session_start`, `text_delta`, `session_end`
- 送信: `audio_chunk_ready`, `session_completed`, `error`
- 複数本前提で `chunk_index` と `track` を管理する。

## 4. 複数本音声の実装
- 1セッション内で複数チャンク生成を許容。
- `chunk_index` はトラックごとに単調増加を保証。
- 再生順制御のため、`track` と `chunk_index` を返却する。
- 全チャンク生成完了後に `session_completed` を送る。

## 5. Provider差し替え契約
Provider差し替え時は以下を満たすこと。
- 入力: テキスト + 音声制御パラメータを受理
- 出力: 音声参照（`audio_path` または `audio_url`）を返却
- timeout/エラーを分類可能

## 6. エラー実装
- HTTP: `400/404/409/503/500`
- WS: `VOICE_NOT_FOUND`, `INVALID_SEQ`, `SYNTHESIS_FAILED` など
- chunk単位失敗は可能な限りセッション継続（致命時のみ終了）

## 7. 性能/タイムアウト
- 初期同時推論数は小さく開始（例: 1）
- 目標 timeout:
  - connect: 3秒
  - ready: 300ms
  - synthesis: 20秒（通常文）

## 8. 運用要件
- 監視: ready状態、error率、p95推論時間、active sessions
- ログ: request_id/session_id/voice_id/track/chunk_index/elapsed_ms/result
- HTTPS/WSS と CORS/WS upgrade を整合
- GPU運用時は `RENCROW_SBV2_DEVICE` を `cuda` に設定し、`/health/ready` の `device` / `cuda_available` で実際の割り当てを確認する
- 起動スクリプト（`start-stt-tts.ps1`）の READY 表示を運用ゲートにし、READY未達時は運用投入しない

## 9. 実装例（現行採用）
- 現行例: SBV2 Adapter
- 将来候補: Irodori Adapter
- Adapter差し替えで上位契約を維持する。

## 10. 検証コマンド例の参照
- 実装検証で使用するコマンドは `docs/STT_TTS/AUDIO_Server仕様/TTS/TTS仕様.md` の「検証コマンド例」節を一次参照とする。
- API-DOD の判定と証跡記録は `docs/STT_TTS/AUDIO_Server仕様/TTS/TTS_API.md` のAPI-DOD節および `docs/STT_TTS/API_DOD_CHECKLIST.md` を併用する。
