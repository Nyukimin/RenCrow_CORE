# RenCrow_STT API契約（Chat向け）

本書は Chat 実装者向けの API 抜粋です。正本は `docs/10_SPEC.md` です。

## エンドポイント

- `GET /health` : 稼働確認
- `GET /ready` : エンジン ready 判定
- `WS /stt` : 主経路
- `WS /ws` : 互換経路（非推奨）
- `WS /stt-ws` : 互換経路（非推奨）

## HTTPレスポンス例

### `GET /health`

```json
{ "ok": true }
```

### `GET /ready`

```json
{
  "ready": true,
  "engine": "whispercpp",
  "engine_detail": { "detail": "status 200" }
}
```

未 ready の場合も HTTP ステータスは 200 で、以下の形を返します。

```json
{
  "ready": false,
  "engine": "whispercpp",
  "engine_detail": { "detail": "connect ECONNREFUSED ..." }
}
```

## WSイベント（Server -> Client）

- `session_info` : 接続直後に送信
- `speech_start` : 発話開始検出
- `draft` : 中間結果（設定有効時のみ）
- `final` : 発話確定テキスト
- `status` : 処理状態
- `error` : 契約違反/推論失敗

## WS制御（Client -> Server）

- `{"type":"config","mimeType":"audio/wav"}`
- `{"type":"vad","speaking":true}`（現状 no-op）
- `{"type":"final_pending"}`（即時 finalize 要求）

## 重要制約

- Chat が利用する確定結果は `final` のみ
- `reply_*` イベントは存在しない
- 音声フォーマットは WAV PCM16 16kHz mono を推奨
- 非 RIFF 音声を送る場合は `RENCROW_STT_ALLOW_NON_RIFF=true` かつ `RENCROW_STT_DRAFT_ENABLED=true` が必要
