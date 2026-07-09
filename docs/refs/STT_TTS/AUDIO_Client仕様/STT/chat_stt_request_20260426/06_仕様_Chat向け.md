# RenCrow_STT 仕様（Chat向け）

本書は Chat 実装担当向けの仕様抜粋です。  
正本は `docs/10_SPEC.md` を参照してください。

## 1. エンドポイント

- `GET /health`
- `GET /ready`
- `WS /stt`（主経路）
- `WS /ws`（後方互換）
- `WS /stt-ws`（後方互換）

## 2. HTTP契約

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

未 ready 時も HTTP 200 で以下の形:

```json
{
  "ready": false,
  "engine": "whispercpp",
  "engine_detail": { "detail": "..." }
}
```

## 3. WSイベント（Server -> Client）

- `session_info`
- `speech_start`
- `draft`（`RENCROW_STT_DRAFT_ENABLED=true` 時のみ）
- `final`
- `status`
- `error`

## 4. WS制御（Client -> Server）

- `config`（`mimeType` 通知）
- `vad`（現状 no-op）
- `final_pending`（即時 finalize 要求）

## 5. 重要制約

- Chat が使う確定テキストは `final` のみ
- `reply_reset` / `reply_delta` は送出されない
- 非 RIFF 音声は `RENCROW_STT_ALLOW_NON_RIFF=true` かつ `RENCROW_STT_DRAFT_ENABLED=true` の場合のみ受理
