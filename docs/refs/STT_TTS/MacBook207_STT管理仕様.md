# MacBook207 STT 管理仕様

## 目的

MacBook207 で稼働する STT サーバを RenCrow から監視・再起動するためのクライアント実装契約を定義する。

## STT Endpoint

```text
HTTP base: http://192.168.1.207:8766
WebSocket: ws://192.168.1.207:8766/stt
```

## Health Check

```text
GET http://192.168.1.207:8766/health
```

正常条件:

```json
{
  "ok": true,
  "status": "ready",
  "ready": {
    "model_loaded": true,
    "tmp_writable": true,
    "audio_decode_available": true
  },
  "provider": {
    "helper_running": true,
    "model_loaded": true
  }
}
```

RenCrow クライアント側の最低正常条件:

- `ok == true`
- `status == "ready"`
- `ready.model_loaded == true`

HTTP status が 2xx でも、上記を満たさない場合は STT ready と扱わない。

## STT Restart

STT サーバに不備がある場合、RenCrow から再起動指示できる。

```text
POST http://192.168.1.207:8766/admin/restart
```

成功時:

```json
{
  "ok": true,
  "status": "restarting",
  "service": "stt-gateway"
}
```

HTTP status:

```text
202 Accepted
```

RenCrow 側 proxy endpoint:

```text
POST /viewer/stt/admin/restart
```

RenCrow は設定済み `stt_base_url` に対して `/admin/restart` を送信し、その後 `/health` をポーリングする。

## Restart 後の扱い

`/admin/restart` 実行後、STT は launchd により自動復帰する。

RenCrow クライアント側の待機手順:

1. `POST /admin/restart`
2. 1〜2 秒待つ
3. `GET /health` をポーリング
4. `ok=true` かつ `ready.model_loaded=true` になるまで待つ
5. 最大待機目安は 20〜30 秒

## 注意

Mac 起動時および STT 再起動時に、サーバ側で WhisperKit を warmup する。

そのため、`ready.model_loaded=true` になった後の初回 STT は高速である。

通常時の目安:

- 短い音声: 約 0.6 秒
- 20 秒前後の音声: 約 0.9〜1.0 秒

再起動直後の warmup 中は `/health` が失敗するか、`model_loaded=false` になる可能性がある。

その間は STT 送信せず、`ready.model_loaded=true` を待つ。
