# RenCrow_STT Chat連携 README

Chat が RenCrow_STT を利用するための最小ガイドです。

## 提供物

- 仕様: `06_仕様_Chat向け.md`
- 実装仕様: `07_実装仕様_Chat向け.md`
- 接続依頼文: `01_依頼文_Chat本体向け.md`
- API抜粋: `02_STT_API.md`
- 起動手順: `05_起動手順_README.md`

## 正本ドキュメント

- `docs/10_SPEC.md`（外部公開契約）
- `docs/20_ARCHITECTURE.md`（内部実装仕様）
- `docs/30_ENGINE_INTERFACE.md`（エンジン実装仕様）
- `docs/50_CHAT_INTEGRATION.md`（Chat統合契約）
- `README.md`（リポジトリ全体 README）

## Chat設定の最小例

```env
RENCROW_STT_URL=ws://127.0.0.1:8090/stt
RENCROW_STT_CONNECT_TIMEOUT_MS=5000
RENCROW_STT_RECONNECT_BACKOFF_MS=2000,5000,10000
```

## 接続確認

1. STT起動: `powershell -NoProfile -ExecutionPolicy Bypass -File .\ops\start-server.ps1`
2. `GET /health` が `{"ok":true}`
3. `GET /ready` が `{"ready":true,...}`
4. Chat から `/stt` 接続して `session_info` 受信
5. 音声送信で `final` 受信
