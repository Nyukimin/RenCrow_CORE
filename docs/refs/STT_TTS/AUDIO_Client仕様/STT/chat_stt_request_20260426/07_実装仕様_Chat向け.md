# RenCrow_STT 実装仕様（Chat向け）

本書は Chat 側が把握すべき実装仕様の要点です。  
正本は `docs/20_ARCHITECTURE.md` と `docs/30_ENGINE_INTERFACE.md` です。

## 1. 構成

- Gateway 層: `src/gateway/`
- Engine Adapter 層: `src/engines/`
- Engine 層（外部）: whisper.cpp / faster-whisper

## 2. Chatから見た動作境界

- RenCrow_STT の責務: 音声入力 -> `final` 発行
- Chat の責務: `final` を受けた後の LLM/TTS 連携

## 3. 実装上の制約（現状）

- `RENCROW_STT_BUSY_POLICY=queue_latest` は未実装（`drop` にフォールバック）
- `RENCROW_STT_ENGINE=openai-api` は未実装（指定時は起動エラー）

## 4. セッション仕様

- 1 WS 接続 = 1 セッション
- 接続直後に `session_info` を返す
- セッション ID は `sess-<timestamp_base36>-<random>` 形式（タグ指定時は `sess-<tag>-...`）

## 5. エラーコード（Chat側で処理推奨）

- `PROVIDER_UNAVAILABLE`
- `PROVIDER_TIMEOUT`
- `PROVIDER_HTTP_ERROR`
- `PROVIDER_EXCEPTION`
- `AUDIO_TOO_SHORT`
- `INVALID_PAYLOAD`
