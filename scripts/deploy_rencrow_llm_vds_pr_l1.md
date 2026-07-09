# RenCrow_LLM PR-L1 デプロイ手順（VDS audio session）

> **Mac 用正本仕様**: [`RenCrow_LLM/docs/VDS_audio_session_Macデプロイ仕様.md`](../../RenCrow_LLM/docs/VDS_audio_session_Macデプロイ仕様.md)  
> **索引**: [`docs/10_新仕様/76_RenCrow_LLM_VDS_Macデプロイ仕様.md`](../docs/10_新仕様/76_RenCrow_LLM_VDS_Macデプロイ仕様.md)

本ファイルは Linux（rencrow）側の切り替え手順。Mac 作業は正本を参照。

## 目的

RenCrow_LLM Chat に `/v1/chat/audio/sessions` を有効化し、rencrow `/voice-chat` から **LLM 直結**できるようにする。

## 前提

- Chat は **RenCrow_LLM** の alias proxy（`:8081`）
- rencrow `chat_base_url`: `http://192.168.1.207:8081`
- 管理 API: `http://192.168.1.207:8079`（Bearer `LLM_OPS_TOKEN`）

## 1. LLM ホストでコード反映（必須）

RenCrow_LLM を PR-L1 入りに更新:

```bash
cd ~/RenCrow/RenCrow_LLM   # 実際の checkout パスに合わせる
git pull
PYTHONPATH=src uv run python -m unittest tests.test_audio_session_contract -q
```

`audio_session_server.py` が存在することを確認:

```bash
test -f src/llm_server/audio_session_server.py && echo OK
```

## 2. Chat 再起動

```bash
cd ~/RenCrow/RenCrow_LLM
uv run mlx-restart Chat
```

または Linux から管理 API:

```bash
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"roles":["Chat"]}' -X POST http://192.168.1.207:8079/v1/control/restart
```

## 3. 確認

```bash
# 426 = WS upgrade 待ち（404 ではない）
curl -s -o /dev/null -w '%{http_code}\n' http://192.168.1.207:8081/v1/chat/audio/sessions

curl -s http://192.168.1.207:8081/health
```

WS スモーク（Python）:

```python
import json, websocket
ws = websocket.create_connection("ws://192.168.1.207:8081/v1/chat/audio/sessions", timeout=10)
ws.send(json.dumps({
    "type": "session.start",
    "utterance_id": "deploy-smoke-1",
    "sample_rate": 16000,
    "channels": 1,
    "format": "pcm16le",
    "voice_input_mode": "vds_sub",
}))
print(ws.recv())
ws.close()
```

期待: `{"type":"session.ready", ...}`

## 4. rencrow 側（Linux）

暫定プロキシ `:18091` をやめ、LLM 直結に戻す:

```bash
# ~/.rencrow/.env から削除 or コメントアウト
# RENCROW_LLM_CHAT_WS=ws://127.0.0.1:18091/v1/chat/audio/sessions

# 自動導出でよい場合は RENCROW_LLM_CHAT_WS 行ごと削除
# 明示する場合:
# RENCROW_LLM_CHAT_WS=ws://192.168.1.207:8081/v1/chat/audio/sessions

systemctl --user restart rencrow.service
```

ローカル暫定プロキシ停止:

```bash
pkill -f 'port 18091.*alias_proxy' || true
systemctl --user stop vds-llm-gateway.service 2>/dev/null || true
```

## 5. E2E 再計測

```bash
cd RenCrow_CORE
python3 scripts/vds_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --rounds 2 --with-sse --require-llm-final --require-phase1-gate \
  --write-md tmp/vds_e2e_probe_latest.md
```

## トラブルシュート

| 症状 | 対処 |
|------|------|
| `/v1/chat/audio/sessions` が 404 | PR-L1 未反映。手順 1–2 |
| `fixed port 8081 is not free` | **Mac で** 8081 を解放してから再起動（下記） |
| Chat health が 000 / connection closed | 8081 ゾンビプロセスの可能性。Mac で復旧 |
| rencrow `LLM_SESSION_UNAVAILABLE` | Chat health / WS URL 確認 |
| Tailscale 経由で 8081 不可 | LAN `192.168.1.207` を使用（現構成） |

### 緊急復旧（8081 が塞がれたとき・Mac 上で実行）

```bash
# 8081 を掴んでいるプロセスを確認・停止
lsof -nP -iTCP:8081 -sTCP:LISTEN
lsof -ti TCP:8081 | xargs kill -9

cd ~/RenCrow/RenCrow_LLM
git pull
uv run mlx-restart Chat
curl -s http://127.0.0.1:8081/health
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/v1/chat/audio/sessions
```

期待: health=200、audio sessions=426（404 ではない）
