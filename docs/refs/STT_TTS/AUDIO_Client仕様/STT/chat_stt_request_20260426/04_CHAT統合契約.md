# Chat統合契約（実装責務）

## Chat側の実装責務

1. STT WS クライアントを実装する
2. 接続時に `session_info` を受信し、セッション追跡を開始する
3. `final` を受けたら既存 Router/Worker へ user 発話として流す
4. `error` code ごとに再接続または通知を行う

## 受け入れ条件（DoD）

- [ ] `/stt` への接続が確立できる
- [ ] 音声送信で `final` を受信できる
- [ ] `PROVIDER_UNAVAILABLE` 時に再接続リトライが動作する
- [ ] `reply_reset` / `reply_delta` 非依存で会話が継続できる
- [ ] `final` 受信後、既存 LLM/TTS フローが動作する

## 推奨設定

- `RENCROW_STT_URL=ws://127.0.0.1:8090/stt`
- `RENCROW_STT_CONNECT_TIMEOUT_MS=5000`
- `RENCROW_STT_RECONNECT_BACKOFF_MS=2000,5000,10000`

## 注意

- 仕様の最終判断は `docs/10_SPEC.md` を優先
- 実装境界の最終判断は `docs/50_CHAT_INTEGRATION.md` を優先
