# RenCrow LLM Client API Guide

この文書は、RenCrow_LLM の OpenAI 互換 API を呼び出すクライアント向けの利用仕様である。
サーバ内部の parser 実装ではなく、クライアントが送る request、受け取る response、表示・履歴保存で守ることを定義する。

## 1. 接続先

用途ごとに base URL と `model` を切り替える。

| 用途 | Base URL | model | 主な用途 |
| --- | --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `Chat` | 通常会話、音声UI、軽い応答 |
| Worker | `http://127.0.0.1:8082` | `Worker` | 要約、整理、実務処理、通常の画像解析 |
| Heavy | `http://127.0.0.1:8083` | `Heavy` | 深い分析、レビュー、失敗原因調査 |
| Wild | `http://127.0.0.1:8084` | `Wild` | 創作、画像プロンプト、創作用解析 |

別PCから呼ぶ場合は `127.0.0.1` を Mac の LAN IP に置き換える。

例:

```text
http://192.168.1.31:8082
```

## 2. エンドポイント

クライアントが通常使う endpoint は以下。

| Method | Path | 用途 |
| --- | --- | --- |
| `GET` | `/v1/models` | 利用可能な public model 名の取得 |
| `POST` | `/v1/chat/completions` | Chat Completions |

`/v1/models` の `id` が request に指定する `model` 名である。
`backend_model` は表示・診断用であり、request の `model` には使わない。

## 3. 基本リクエスト

```json
{
  "model": "Worker",
  "messages": [
    {
      "role": "user",
      "content": "次の文章を一文で要約して: RenCrowは音声とLLMを統合します。"
    }
  ],
  "temperature": 0.2,
  "max_tokens": 512,
  "stream": false
}
```

認証は現時点では不要。
OpenAI SDK 互換クライアントで API key が必須の場合は、dummy 値を指定してよい。

## 4. ThinkingBridge オプション

Thinking 対応モデルでは、サーバ側が `<think>...</think>` を解析し、ユーザー表示用本文と reasoning を分離する。
クライアント側で `<think>` タグを解析してはいけない。

追加で指定できる field:

| Field | 型 | 既定 | 意味 |
| --- | --- | --- | --- |
| `think` | boolean | モデル設定依存 | モデルに thinking を促すか |
| `parse_reasoning` | boolean | `true` | サーバ側で reasoning/content を分離するか |
| `include_reasoning` | boolean | `false` | response に reasoning を含めるか |
| `separate_reasoning` | boolean | `true` | streaming delta も reasoning/content に分けるか |
| `chat_template_kwargs` | object | `{}` | backend chat template への補助指定 |

通常クライアントは以下を推奨する。

```json
{
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

デバッグ画面や開発者向け表示では `include_reasoning: true` を指定できる。

## 5. Non-Streaming 応答

通常表示では `choices[0].message.content` だけを使う。

`include_reasoning: false` の例:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最終回答です。",
        "parse_status": "ok",
        "parser_name": "qwen3"
      },
      "finish_reason": "stop"
    }
  ]
}
```

`include_reasoning: true` の例:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最終回答です。",
        "reasoning_content": "内部で考える",
        "thinking": "内部で考える",
        "raw_content": "<think>内部で考える</think>最終回答です。",
        "parse_status": "ok",
        "parser_name": "qwen3"
      },
      "finish_reason": "stop"
    }
  ]
}
```

`thinking` と `reasoning_content` は同じ内容として扱ってよい。
Ollama 互換表示では `thinking`、OpenAI 互換寄りの実装では `reasoning_content` を使う。

## 6. Streaming 応答

`stream: true` の場合、response は `text/event-stream` で返る。
通常クライアントは `delta.content` を表示本文として連結する。

`include_reasoning: false` の場合、reasoning delta は通常 response へ出さない。

`include_reasoning: true` の場合、delta は以下のように分かれる。

Reasoning delta:

```json
{
  "choices": [
    {
      "delta": {
        "reasoning_content": "内部で考える",
        "content": ""
      }
    }
  ]
}
```

Content delta:

```json
{
  "choices": [
    {
      "delta": {
        "reasoning_content": "",
        "content": "最終回答です。"
      }
    }
  ]
}
```

streaming は最後に以下で終わる。

```text
data: [DONE]
```

## 7. parse_status

`parse_status` は parser の状態を表す。
クライアントは表示の補助やデバッグに使える。

| status | 意味 | クライアントの扱い |
| --- | --- | --- |
| `ok` | reasoning/content を分離できた | `content` を通常表示 |
| `no_reasoning` | reasoning がなかった | `content` を通常表示 |
| `unclosed_reasoning` | reasoning が閉じなかった | `content` が空の場合は空応答として扱う |
| `disabled` | `parse_reasoning=false` | raw に近い `content` として扱う |
| `passthrough` | parser 対象外 | `content` を通常表示 |
| `parser_error` | parser 例外 | エラー表示または raw fallback |

通常 UI では `parse_status` をユーザーに見せなくてよい。

## 8. 表示と履歴保存

クライアント側の原則:

- ユーザーに通常表示するのは `content` のみ。
- `reasoning_content` / `thinking` は通常 UI に出さない。
- reasoning を表示する場合は、開発者向けの折りたたみ表示にする。
- 次ターンの `messages` に戻す assistant message は `content` のみにする。
- `raw_content` や reasoning を会話履歴へ再注入しない。
- クライアント側で `<think>` タグを解析しない。

次ターンへ戻す assistant message の例:

```json
{
  "role": "assistant",
  "content": "最終回答です。"
}
```

## 9. Tool Calling

現時点で RenCrow_LLM のクライアント最低要件に tool calling は含めない。

サーバ側は reasoning 内の JSON 風テキストを tool call として扱わないように分離する。
クライアント側で tool call を実装する場合も、対象は `content` 側だけにする。
`raw_content` から tool call を抽出してはいけない。

## 10. curl 例

通常応答:

```sh
curl -s http://127.0.0.1:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Worker",
    "messages": [
      {
        "role": "user",
        "content": "次の文章を一文で要約して: RenCrowは音声とLLMを統合します。"
      }
    ],
    "temperature": 0.2,
    "max_tokens": 512,
    "parse_reasoning": true,
    "include_reasoning": false,
    "stream": false
  }'
```

reasoning 付きデバッグ応答:

```sh
curl -s http://127.0.0.1:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Worker",
    "messages": [
      {
        "role": "user",
        "content": "短く考えてから答えて"
      }
    ],
    "parse_reasoning": true,
    "include_reasoning": true,
    "stream": false
  }'
```

streaming:

```sh
curl -N http://127.0.0.1:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Worker",
    "messages": [
      {
        "role": "user",
        "content": "短く返事して"
      }
    ],
    "parse_reasoning": true,
    "include_reasoning": false,
    "separate_reasoning": true,
    "stream": true
  }'
```

## 11. 実装チェックリスト

クライアント実装時は以下を満たす。

- `/v1/models` の `id` を request `model` に使う。
- 通常表示は `choices[0].message.content` または streaming の `delta.content` のみを使う。
- `include_reasoning` は通常 `false` にする。
- reasoning を保存・表示する場合は明示的なデバッグ用途に限定する。
- 次ターン履歴には assistant の `content` だけを戻す。
- `<think>` タグをクライアント側で parse しない。
- tool call を実装する場合は `content` だけを対象にする。
