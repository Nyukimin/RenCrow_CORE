# LLM 切り替え仕様

RenCrow_LLM は OpenAI 互換 API として動作する。
クライアント側は、用途に応じて **Base URL** と **model 名** を切り替えて利用する。

## 切り替え単位

LLM は用途ごとに別プロセス・別ポートで起動する。

| 用途 | Base URL | model | backend_model | 主な用途 |
| --- | --- | --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `Chat` | `/Users/yukimi/models/gemma-4-e4b-it-4bit` | 通常会話、音声 UI、軽い応答 |
| Worker | `http://127.0.0.1:8082` | `Worker` | `/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit` | 要約、整理、RAG、実務処理 |
| Heavy | `http://127.0.0.1:8083` | `Heavy` | `/Users/yukimi/models/Qwen3.5-122B-A10B-4bit` | 深い分析、失敗原因調査、最終レビュー |
| Wild | `http://127.0.0.1:8084` | `Wild` | `/Users/yukimi/models/Qwen3.6-35B-A3B-Abliterated-Heretic-MLX-4bit` | 創作、画像プロンプト、雰囲気・構図分析 |

## 基本ルール

- Chat / Worker / Heavy / Wild は別ポートで呼び分ける。
- 各プロセスは許可された `model` 名だけを受け付ける。
- 例えば Chat 用 endpoint に `model: "Worker"` を送ると拒否される。
- 現行の公開 model 名は `Chat` / `Worker` / `Heavy` / `Wild` の 4 つ。
- `Coder` は現行 proxy では公開しない。

## リクエスト例

Chat:

```json
{
  "model": "Chat",
  "messages": [
    { "role": "user", "content": "こんにちは" }
  ],
  "max_tokens": 512
}
```

Worker:

```json
{
  "model": "Worker",
  "messages": [
    { "role": "user", "content": "この文章を要約して" }
  ],
  "max_tokens": 1024
}
```

## 切り替え判断の目安

| 入力内容 | 推奨 |
| --- | --- |
| 雑談、短い返答、音声対話 | Chat |
| 要約、整理、調査結果の整形 | Worker |
| コード修正方針、影響範囲、テスト観点 | Worker |
| 原因が複雑、前提から見直す必要がある | Heavy |
| 創作、物語、画像生成プロンプト | Wild |

## エラー仕様

許可されていない model 名を指定すると `404` が返る。

```json
{
  "error": {
    "message": "Unknown model alias: Worker. Allowed model(s): Chat",
    "type": "invalid_request_error",
    "param": "model",
    "code": "model_not_found"
  }
}
```

## 推奨クライアント設定

クライアント側では、用途ごとに以下の設定を持つ。

```json
{
  "chat": {
    "base_url": "http://127.0.0.1:8081",
    "model": "Chat"
  },
  "worker": {
    "base_url": "http://127.0.0.1:8082",
    "model": "Worker"
  },
  "heavy": {
    "base_url": "http://127.0.0.1:8083",
    "model": "Heavy"
  },
  "wild": {
    "base_url": "http://127.0.0.1:8084",
    "model": "Wild"
  }
}
```

## モデル名の取得

クライアントは各 OpenAI 互換 endpoint の `GET /v1/models` で、送信に使う公開 model 名と、表示用の実体 model 名を取得できる。

```sh
curl http://127.0.0.1:8082/v1/models
```

Worker のレスポンス例:

```json
{
  "object": "list",
  "data": [
    {
      "id": "Worker",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    }
  ]
}
```

- `/v1/chat/completions` に指定する `model` は `id` を使う。
- 画面表示や状態確認には `backend_model` を使う。
- `backend_model` は実体モデルのパスであり、クライアントからのリクエスト model 名としては使わない。

## 注意事項

- 通常運用では Chat と Worker を常駐させる。
- Heavy / Wild は必要時に起動する。
- API 自体は現状認証なし。
- LAN から使う場合は `127.0.0.1` を Mac の IP に置き換える。
- streaming は OpenAI 互換の SSE 形式で利用できる。

## LLM 起動 API

LLM 本体が落ちている場合、クライアントは管理 API から起動を要求できる。
管理 API は OpenAI 互換 API とは別ポートで動作する。

| 用途 | URL |
| --- | --- |
| 管理 API | `http://127.0.0.1:8079` |

管理 API は Bearer token が必須。

```http
Authorization: Bearer <LLM_OPS_TOKEN>
```

### 起動する組み合わせ

クライアントは `selection` で Worker / Wild / Heavy のいずれかを指定する。
サーバ側は常に Chat と選択ロールを起動対象にする。

| selection | 起動対象 |
| --- | --- |
| `Worker` | Chat + Worker |
| `Wild` | Chat + Wild |
| `Heavy` | Chat + Heavy |

例: Chat + Worker を起動する。

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Worker"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

例: Chat + Wild を起動する。

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Wild"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

例: Chat + Heavy を起動する。

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"selection":"Heavy"}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

### 明示 roles 指定

`roles` を指定すると、指定したロールだけを起動対象にする。

```sh
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"roles":["Chat","Worker"]}' \
  -X POST http://127.0.0.1:8079/v1/control/start
```

### start の挙動

- すでに health OK のロールは再起動しない。
- 落ちているロールだけを固定ポートで起動する。
- `stop` 済みで `halted` 扱いのロールも、`start` により `halted` が解除される。
- 起動後は health OK になるまで待つ。

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
