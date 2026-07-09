# RenCrow_STT 仕様要点（運用・責務）

## 1. 責務境界

- RenCrow_STT の責務: 音声入力から `final` テキスト発行まで
- Chat の責務: `final` を受けた後の LLM 応答生成、会話履歴管理、TTS 接続

## 2. セッション仕様

- 接続ごとに独立セッション
- 接続直後 `session_info` を送信
- `session_id` 形式:
  - 通常: `sess-<timestamp_base36>-<random>`
  - タグあり: `sess-<tag>-<timestamp_base36>-<random>`

## 3. エラーコードと扱い

- `PROVIDER_UNAVAILABLE`: STTバックエンド未起動/未接続
- `PROVIDER_TIMEOUT`: 推論タイムアウト
- `PROVIDER_HTTP_ERROR`: バックエンド HTTP 失敗
- `PROVIDER_EXCEPTION`: 通信/実行例外
- `AUDIO_TOO_SHORT`: 最小音声長未満
- `INVALID_PAYLOAD`: 不正 JSON/不正オーディオ

## 4. 運用決定事項（確定）

- 主経路は `/stt` を使用
- 後方互換パス（`/ws`, `/stt-ws`）は段階的廃止予定
- LAN/リモート接続は WSS 推奨
- Chat 側 VAD は任意（確定トリガは STT 側 `final`）
- `RENCROW_STT_BUSY_POLICY=queue_latest` は現状未実装（実挙動は `drop` フォールバック）
- `RENCROW_STT_ENGINE=openai-api` は現状未実装（指定時は起動エラー）
