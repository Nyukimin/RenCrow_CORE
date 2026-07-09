# クライアント用 LLM 起動・再起動仕様

この文書は、クライアントから RenCrow_LLM の LLM プロセスを起動・再起動・停止するための仕様をまとめる。

## 管理 API

LLM の起動・停止・再起動は管理 API から行う。

| 項目 | 値 |
| --- | --- |
| Base URL | `http://127.0.0.1:8079` |
| 認証 | Bearer token |
| Token env | `LLM_OPS_TOKEN` |

認証ヘッダー:

```http
Authorization: Bearer <LLM_OPS_TOKEN>
```

LAN から使う場合は `127.0.0.1` を Mac の IP に置き換える。

## 生存確認

```sh
curl -s http://127.0.0.1:8079/health
```

レスポンス例:

```json
{
  "status": "ok",
  "daemon": "llm-mgmt"
}
```

## 状態確認

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  http://127.0.0.1:8079/v1/status
```

用途:

- 各 LLM が起動しているか確認する。
- `health_ok` を見る。
- `halted` 状態を見る。

## 起動

LLM が落ちている場合、クライアントは `start` を呼ぶ。

```http
POST /v1/control/start
```

### Chat + Worker を起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Worker"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

### Chat + Wild を起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Wild"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

### Chat + Heavy を起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Heavy"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

### start の挙動

- `selection` は `Worker` / `Wild` / `Heavy`。
- サーバ側で常に `Chat + selection` を起動対象にする。
- すでに起動済みで health OK のロールは再起動しない。
- 落ちているロールだけ起動する。
- `stop` 済みの `halted` 状態も解除する。
- 起動後、health OK まで待つ。

レスポンス例:

```json
{
  "started": ["Chat", "Wild"],
  "already_running": [],
  "roles": ["Chat", "Wild"],
  "ok_all": true,
  "details": {
    "Chat": "{\"status\":\"ok\"}",
    "Wild": "{\"status\":\"ok\"}"
  }
}
```

## 再起動

既存プロセスを止めて、同じ固定ポートで起動し直す。

```http
POST /v1/control/restart
```

### Chat + Worker を再起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"roles":["Chat","Worker"]}' \
  -X POST http://127.0.0.1:8079/v1/control/restart
```

### Chat + Wild を再起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"roles":["Chat","Wild"]}' \
  -X POST http://127.0.0.1:8079/v1/control/restart
```

### Chat + Heavy を再起動

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"roles":["Chat","Heavy"]}' \
  -X POST http://127.0.0.1:8079/v1/control/restart
```

### restart の挙動

- 指定ロールを停止する。
- 同じ固定ポートで起動し直す。
- `halted` 状態を解除する。
- 起動後、health OK まで待つ。
- health が通らなければ `503` を返す。

レスポンス例:

```json
{
  "restarted": ["Chat", "Worker"],
  "ok_all": true
}
```

## 停止

```http
POST /v1/control/stop
```

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"roles":["Chat","Worker"]}' \
  -X POST http://127.0.0.1:8079/v1/control/stop
```

### stop の挙動

- 指定ロールを停止する。
- `halted` 扱いにする。
- watchdog による自動復旧を止める。
- 再開するには `start` または `restart` を呼ぶ。

レスポンス例:

```json
{
  "stopped": ["Chat", "Worker"],
  "halted": true
}
```

## クライアント推奨フロー

1. `GET /health` で管理 API が生きているか確認する。
2. `GET /v1/status` で対象 LLM の状態を確認する。
3. 落ちているだけなら `/v1/control/start` を呼ぶ。
4. 挙動がおかしい場合は `/v1/control/restart` を呼ぶ。
5. 明示停止したい場合だけ `/v1/control/stop` を呼ぶ。

## role 一覧

| Role | API port | 用途 |
| --- | ---: | --- |
| Chat | 8081 | 通常会話 |
| Worker | 8082 | 実務処理 |
| Coder | 8082 | Worker 内 alias。起動対象 role としては指定しない |
| Heavy | 8083 | 深い分析 |
| Wild | 8084 | 創作 |

