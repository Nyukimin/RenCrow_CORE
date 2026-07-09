# RenCrow_STT 起動・疎通確認手順

## 1. STTサーバ起動

```powershell
cd D:\GenerativeAI\RenCrow_STT
powershell -NoProfile -ExecutionPolicy Bypass -File .\ops\start-server.ps1
```

## 2. HTTP確認

```powershell
curl.exe -sS http://127.0.0.1:8090/health
curl.exe -sS http://127.0.0.1:8090/ready
```

期待:

- `{"ok":true}`
- `{"ready":true,...}`

## 3. Chat接続先

- 開発時: `ws://127.0.0.1:8090/stt`
- LAN/リモート: `wss://<host>:<port>/stt`（TLS終端あり）

## 4. WS確認（任意）

WebSocket クライアントから `/stt` へ接続し、接続直後に `session_info` が返ることを確認する。

## 5. トラブル時

- `GET /ready` が `ready:false` の場合は Whisper バックエンドを先に起動
- `error.code=INVALID_PAYLOAD` は音声フォーマットを再確認（WAV PCM16 16kHz mono）
