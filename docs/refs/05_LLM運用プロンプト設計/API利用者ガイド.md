# RenCrow_LLM API 利用者ガイド

この文書は、RenCrow_LLM を OpenAI 互換 API として呼び出すアプリケーション向けの利用ガイドである。
サーバ起動、モデル差し替え、内部 parser、運用 daemon の詳細は扱わない。

## 基本

RenCrow_LLM は用途ごとに base URL と `model` を分ける。

| 用途 | Base URL | model | 主な用途 |
| --- | --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `Chat` | 通常会話、音声 UI、短い対話 |
| Worker | `http://127.0.0.1:8082` | `Worker` | 作業応答、要約、整理、調査結果の処理 |
| ChatWorker | `http://127.0.0.1:8082` | `ChatWorker` | IdleChat 内の短文 Worker 応答 |
| Coder | `http://127.0.0.1:8082` | `Coder1` - `Coder4` | 設計、実装相談、長文の作業会話 |
| Heavy | `http://127.0.0.1:8083` | `Heavy` | 重い分析、前提見直し、レビュー |
| Wild | `http://127.0.0.1:8084` | `Wild` | 実験、創作系 |

クライアントは `model` に公開 alias を指定する。`backend_model` や実体モデル名を request の
`model` に指定してはいけない。

## Endpoint

利用者が通常使う endpoint:

| Method | Path | 用途 |
| --- | --- | --- |
| `POST` | `/v1/chat/completions` | Chat Completions |
| `GET` | `/v1/models` | 利用可能な公開 model alias の確認 |
| `GET` | `/health` | 疎通、backend 状態、Ollama model load 状態の確認 |
| `GET` | `/v1/health` | `/health` と同等の互換 health |

## 最小リクエスト

Chat:

```sh
curl -sS http://127.0.0.1:8081/v1/chat/completions \
  -H 'Content-Type: application/json; charset=utf-8' \
  -d '{
    "model": "Chat",
    "messages": [
      {"role": "user", "content": "こんにちは。短く返事して。"}
    ],
    "max_tokens": 128,
    "stream": false
  }'
```

Worker:

```sh
curl -sS http://127.0.0.1:8082/v1/chat/completions \
  -H 'Content-Type: application/json; charset=utf-8' \
  -d '{
    "model": "Worker",
    "messages": [
      {"role": "user", "content": "次の作業ログを要約して。ログ: ..."}
    ],
    "max_tokens": 1024,
    "stream": false
  }'
```

ChatWorker:

```sh
curl -sS http://127.0.0.1:8082/v1/chat/completions \
  -H 'Content-Type: application/json; charset=utf-8' \
  -d '{
    "model": "ChatWorker",
    "messages": [
      {"role": "user", "content": "眠い。短く返事して。"}
    ],
    "max_tokens": 64,
    "stream": false
  }'
```

## Request Body

主要 field:

| Field | 型 | 必須 | 説明 |
| --- | --- | --- | --- |
| `model` | string | 必須 | 公開 alias。例: `Chat`, `Worker`, `ChatWorker` |
| `messages` | array | 必須 | OpenAI 互換の chat messages |
| `max_tokens` | integer | 任意 | 出力 token 上限。入力 context 長ではない |
| `temperature` | number | 任意 | 生成のランダム性 |
| `top_p` | number | 任意 | nucleus sampling |
| `top_k` | integer | 任意 | 上位候補制限 |
| `min_p` | number | 任意 | 低確率候補の除外 |
| `stream` | boolean | 任意 | `true` で SSE streaming |
| `parse_reasoning` | boolean | 任意 | reasoning と本文を分離する |
| `include_reasoning` | boolean | 任意 | response に reasoning を含める |
| `separate_reasoning` | boolean | 任意 | streaming delta でも reasoning と本文を分ける |
| `reasoning_mode` | string | 任意 | `nothink`, `think`, `reasoning` |

通常の UI 表示、TTS、会話履歴には `choices[0].message.content` だけを使う。
`<think>` タグをクライアント側で解析しない。

## Response Body

non-streaming response は OpenAI 互換形式で返る。

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最終回答本文"
      },
      "finish_reason": "stop"
    }
  ]
}
```

表示本文:

```text
choices[0].message.content
```

`content` が `null`、空文字、空白のみの場合は正常な本文として扱わない。
`parse_status:"empty_final_content"` や `error_code:"EMPTY_FINAL_CONTENT"` がある場合は、クライアント側でフォールバック表示せず、エラーとして扱う。

## Worker Alias の使い分け

Worker endpoint は Ollama `rencrow-gpt-oss-120b:64k` を使う。
`Worker` と `ChatWorker` は同じ Ollama model runner を共有し、切り替え時に再ロードしない。

| model | reasoning | GPT-OSS level | max_tokens cap | logical context budget |
| --- | --- | --- | ---: | ---: |
| `ChatWorker` | 返さない | `low` | 64 | 16384 |
| `Worker` | 返す | `high` | 4096 | 65536 |
| `Coder1` - `Coder4` | request に従う | request に従う | なし | 65536 |

使い分け:

- 短い会話応答は `ChatWorker` を使う。
- 作業結果の説明、要約、整理は `Worker` を使う。
- 長い設計、実装相談、patch 検討は `Coder1` - `Coder4` を使う。

`max_tokens` は出力上限であり、入力 context を増やす設定ではない。
`ChatWorker` の context 1/4 制限は proxy 側の有効入力 budget であり、Ollama の `num_ctx` を 16384 に切り替えるものではない。

## Reasoning の扱い

reasoning を UI や TTS に混ぜたくない場合は、以下を付ける。

```json
{
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

`reasoning_mode` の意味:

| reasoning_mode | 用途 | response reasoning |
| --- | --- | --- |
| `nothink` | 短文、会話テンポ優先 | 出さない |
| `think` | 中間 | 出さない |
| `reasoning` | 作業根拠や検討を見たい場合 | 出す |

Ollama GPT-OSS では `think:false` は完全な thinking 無効ではない。
RenCrow_LLM では `nothink` を GPT-OSS `low` に変換し、response から reasoning 系 field を除外する。

## Streaming

`stream:true` の場合、OpenAI 互換の SSE を返す。

```sh
curl -N http://127.0.0.1:8082/v1/chat/completions \
  -H 'Content-Type: application/json; charset=utf-8' \
  -d '{
    "model": "Worker",
    "messages": [
      {"role": "user", "content": "短く進捗を整理して。"}
    ],
    "stream": true,
    "max_tokens": 512
  }'
```

UI 表示では `delta.content` を連結する。
`separate_reasoning:true` を使う場合、reasoning 用 delta と content 用 delta を混同しない。

## Models

利用可能な model alias は `/v1/models` で確認する。

```sh
curl -sS http://127.0.0.1:8082/v1/models
```

例:

```json
{
  "object": "list",
  "data": [
    {
      "id": "Worker",
      "object": "model",
      "owned_by": "local",
      "backend_model": "rencrow-gpt-oss-120b:64k"
    },
    {
      "id": "ChatWorker",
      "object": "model",
      "owned_by": "local",
      "backend_model": "rencrow-gpt-oss-120b:64k"
    }
  ]
}
```

request の `model` には `id` を使う。`backend_model` は表示、診断、状態確認用である。

## Health

role ごとに `/health` を確認する。

```sh
curl -sS http://127.0.0.1:8081/health
curl -sS http://127.0.0.1:8082/health
curl -sS http://127.0.0.1:8083/health
curl -sS http://127.0.0.1:8084/health
```

Worker が Ollama backend の場合、health は Ollama `/api/ps` 相当の情報を使い、
`rencrow-gpt-oss-120b:64k` または base `gpt-oss:120b` がロード済みかを確認する。

## UTF-8

RenCrow_LLM は UTF-8 を前提にする。

- request body は UTF-8 JSON で送る。
- `Content-Type` は `application/json; charset=utf-8` を推奨する。
- response の日本語は UTF-8 として扱う。
- クライアント側で Shift_JIS などへ暗黙変換しない。

## Error

未知の `model` を指定すると `404` が返る。

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

クライアント側では次を区別する。

- HTTP error: 接続、model alias、backend 起動状態の問題。
- `content` empty: reasoning only や生成失敗の可能性。正常本文として表示しない。
- streaming 中断: 部分表示を確定扱いせず、再試行またはエラー表示する。

## 推奨クライアント設定

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
  "chat_worker": {
    "base_url": "http://127.0.0.1:8082",
    "model": "ChatWorker"
  },
  "coder": {
    "base_url": "http://127.0.0.1:8082",
    "model": "Coder1"
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
