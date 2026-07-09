# RenCrow OpenAI-Compatible MLX API

## Base URLs

Chat / Worker / Heavy / Wild は別プロセス、別ポートで起動する。Chat が重い処理で詰まることを避けるため、RenCrow 側でも用途ごとに base URL を分ける。

| Use | Base URL | Config |
| --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `configs/chat-server.toml` |
| Worker | `http://127.0.0.1:8082` | `configs/worker-server.toml` |
| Heavy | `http://127.0.0.1:8083` | `configs/heavy-server.toml` |
| Wild | `http://127.0.0.1:8084` | `configs/wild-server.toml` |

LAN から呼ぶ場合は各 config の `host` を `0.0.0.0` に変更し、RenCrow 側では `http://<MacのIP>:8081` のように指定する。

## Models

RenCrow 側は用途ごとに base URL と `model` 名を切り替える。

| Use | model | Backing MLX model |
| --- | --- | --- |
| Chat | `Chat` | `/Users/yukimi/models/gemma-4-e4b-it-4bit` |
| Worker | `Worker`, `Coder1`, `Coder2`, `Coder3`, `Coder4` | `/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit` |
| Heavy | `Heavy` | `/Users/yukimi/models/Qwen3.5-122B-A10B-4bit` |
| Wild | `Wild` | `/Users/yukimi/models/Qwen3.6-35B-A3B-Abliterated-Heretic-MLX-4bit` |

現行の公開 model 名は以下。

- Chat endpoint: `Chat`
- Worker endpoint: `Worker`, `Coder1`, `Coder2`, `Coder3`, `Coder4`
- Heavy endpoint: `Heavy`
- Wild endpoint: `Wild`

`Coder1`〜`Coder4` は Worker endpoint 上の公開 model alias であり、実体 backend model は Worker と同一。
クライアントは用途に応じて `model` に `Worker` または `Coder1`〜`Coder4` を指定できる。

## Endpoints

- `POST /v1/chat/completions`
- `GET /v1/models`
- `GET /health`
- `GET /v1/health`

`GET /v1/models` はクライアントが送信に使う公開 model 名を `id` で返し、表示・確認用の実体 model 名を `backend_model` で返す。

```json
{
  "object": "list",
  "data": [
    {
      "id": "Worker",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    },
    {
      "id": "Coder1",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    },
    {
      "id": "Coder2",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    },
    {
      "id": "Coder3",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    },
    {
      "id": "Coder4",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit"
    }
  ]
}
```

RenCrow 側から `/v1/chat/completions` に指定する `model` は `id` を使う。
`backend_model` は画面表示・状態確認用で、リクエストの model 名には使わない。

## Request

OpenAI互換の chat completions request を受け付ける。

```json
{
  "model": "Chat",
  "messages": [
    { "role": "system", "content": "You are a concise assistant." },
    { "role": "user", "content": "こんにちは" }
  ],
  "temperature": 0.2,
  "max_tokens": 256,
  "stream": false
}
```

対応済みの主要 field:

- `model`
- `messages`
- `temperature`
- `max_tokens`
- `stream`
- `top_p`
- `top_k`
- `min_p`
- `think`
- `parse_reasoning`
- `include_reasoning`
- `separate_reasoning`

## Thinking / Reasoning 成形

Thinking 対応モデルでは、サーバ側が reasoning と最終回答本文を分離する。
クライアント側で `<think>...</think>` タグを解析してはいけない。

RenCrow 側はローカル OpenAI 互換 LLM に対して、OpenAI 互換 request body に以下の拡張パラメータを送る。
public OpenAI API にはこれらの拡張パラメータを送らない。

| Field | 型 | 意味 |
| --- | --- | --- |
| `think` | boolean | モデルに thinking を使わせるか |
| `parse_reasoning` | boolean | サーバ側で reasoning/content を分離するか |
| `include_reasoning` | boolean | response に reasoning を含めるか |
| `separate_reasoning` | boolean | streaming delta も reasoning/content に分けるか |

`think` が指定された場合、サーバ既定より request の値を優先する。
RenCrow 側は `think:false` と `think:true` を話者・用途ごとに request ごと必ず明示する。

ローカル OpenAI 互換 LLM に対しては、通常以下を付与する。

```json
{
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

### RenCrow 側の送信方針

Mio / Chat は常時 `think:false` とする。

```json
{
  "model": "Chat",
  "think": false,
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

Shiro / IdleChat Worker は `think:false` とする。

```json
{
  "model": "Worker",
  "think": false,
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

Shiro / 通常 Worker は `think:true` とする。

```json
{
  "model": "Worker",
  "think": true,
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

その他モデルは原則として常時 `think:true` とする。

```json
{
  "think": true,
  "parse_reasoning": true,
  "include_reasoning": false,
  "separate_reasoning": true
}
```

### サーバ側必須動作

`think:false` が指定された場合、backend へ thinking 無効として確実に渡す。

- backend の `chat_template_kwargs.enable_thinking=false` 相当に反映する
- Qwen3 系でも thinking 出力を生成または通常本文へ混入させない
- サーバ既定より request の `think` を優先する
- `think:false` と `think:true` の連続呼び出しで、前回設定を持ち越さない

`parse_reasoning:true` かつ `include_reasoning:false` の場合、`choices[0].message.content` には最終回答本文のみを返す。
reasoning を返す必要がある場合は、通常本文とは別フィールドへ分離する。

```json
{
  "choices": [
    {
      "message": {
        "content": "最終回答本文のみ",
        "reasoning_content": "内部推論"
      }
    }
  ]
}
```

モデルが `<think>...</think>` を返した場合、`parse_reasoning:true` ではサーバ側で解析し、通常 `content` から除去する。

- `content` に `<think>` タグを含めない
- `include_reasoning:false` では reasoning 本文も `content` に含めない
- クライアント側で `<think>` タグを再解析しなくてよい状態にする

reasoning だけが生成され、最終本文が空になった場合、空の正常応答として扱わず、原因を追跡できる情報を残す。

- サーバログで `reasoning_only` または `empty_final_content` 相当が判別できる
- 可能なら response metadata でも判別できる
- `finish=stop` だけで正常本文ありと誤認させない

## Response

non-streaming は OpenAI互換で `choices[0].message.content` を返す。

streaming は `text/event-stream` の SSE 形式で `data: {...}` chunk を返す。

通常 UI は `choices[0].message.content` のみを表示本文として使う。
`include_reasoning:false` では、non-streaming と streaming のどちらでも reasoning を通常 `content` へ混ぜない。

`include_reasoning:false` の non-streaming 応答例:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最終回答本文のみ",
        "parse_status": "ok",
        "parser_name": "qwen3"
      },
      "finish_reason": "stop"
    }
  ]
}
```

`include_reasoning:true` の non-streaming 応答例:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最終回答本文のみ",
        "reasoning_content": "内部推論",
        "raw_content": "<think>内部推論</think>最終回答本文のみ",
        "parse_status": "ok",
        "parser_name": "qwen3"
      },
      "finish_reason": "stop"
    }
  ]
}
```

`include_reasoning:true` の streaming では、reasoning と content を分ける。

Reasoning delta:

```json
{
  "choices": [
    {
      "delta": {
        "reasoning_content": "内部推論",
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
        "content": "最終回答本文"
      }
    }
  ]
}
```

## Client Handling

RenCrow 側は以下を維持する。

- Viewer 表示、TTS、口パク、会話履歴には `content` のみを使う
- `reasoning_content` / `thinking` / `raw_content` を通常 UI や次ターン prompt へ混入させない
- streaming では `delta.reasoning_content` を表示本文として扱わず、`delta.content` のみ連結する
- reasoning が `content` に混入した場合は表示・TTS に流さず、discard または recovery する
- sanitizer / recovery は保険であり、正規経路の成形責任は LLM サーバ側に置く

## RenCrow 側の実装状況

**ステータス**: 2026-05-24 時点の実装状況。

### 実装済み

- ローカル OpenAI 互換 LLM サーバ向け provider では、ThinkingBridge field を request body に付与する。
  - `parse_reasoning:true`
  - `include_reasoning:false`
  - `separate_reasoning:true`
- `think` は provider option として渡された場合に request body へ付与する。
- IdleChat では speaker option により、Mio / Shiro も含めて `think` を request ごとに明示する。
- public OpenAI API 向けには、ThinkingBridge 固有 field を送らない。
- non-streaming 応答では `choices[0].message.content` を本文として扱い、`parse_status:no_reasoning` かつ untagged reasoning と判定できる場合は final answer 抽出または空化する。
- streaming 応答では `reasoning_content` delta を表示本文として扱わず、`content` delta だけを通常本文として扱う。
- health check request でも ThinkingBridge 安全 field を送る。
- IdleChat の speaker 別 `think` default は以下。
  - `mio`: `false`
  - `shiro`: `false`
  - その他 participant: `true`
- IdleChat では speaker の `think` 設定に応じて system prompt 先頭へ `/think` または `/no_think` を付ける。
- Shiro / IdleChat では、Worker 応答が空または内部推論 leak 由来で unusable な場合、Worker 再試行を待たず default provider recovery へ進む。
- TTS 用テキストは thinking event を読み上げ対象にしない。

### 実装ファイル

| 項目 | ファイル |
| --- | --- |
| ThinkingBridge field 付与 | `internal/infrastructure/llm/providers/openai/thinking_bridge.go` |
| OpenAI 互換 provider request / response 処理 | `internal/infrastructure/llm/providers/openai/provider.go` |
| OpenAI 互換 response parse / sanitize | `internal/infrastructure/llm/providers/openai/response_parse.go` |
| OpenAI 互換 streaming parse | `internal/infrastructure/llm/providers/openai/stream.go` |
| health check field 付与 | `internal/infrastructure/health/openai_compatible.go` |
| IdleChat speaker option / provider option 注入 | `internal/application/idlechat/orchestrator_constructor.go` |
| IdleChat `/think` / `/no_think` prompt 指示 | `internal/application/idlechat/orchestrator_prompts.go` |
| Shiro IdleChat recovery | `internal/application/idlechat/orchestrator_response_generation.go` |
| IdleChat speaker `think` config default | `internal/adapter/config/config_defaults.go` |
| TTS thinking 除外 | `internal/application/tts/text_filter.go` |

### テスト状況

以下の観点はテスト済み。

- ローカル OpenAI 互換 provider が `parse_reasoning:true` / `include_reasoning:false` / `separate_reasoning:true` を送る。
- provider option の `think:false` が request body に入る。
- public OpenAI API 向けには ThinkingBridge field を送らない。
- streaming で `reasoning_content` を通常本文へ混ぜない。
- untagged reasoning 風 content を sanitize する。
- health check が ThinkingBridge 安全 field を送る。
- IdleChat の Mio / Shiro が `think:false` を provider option として渡し、system prompt に `/no_think` を付ける。
- IdleChat のその他 participant は default で `think:true` になる。
- Shiro / IdleChat の reasoning leak または空応答で default provider recovery へ進む。

関連テスト:

- `internal/infrastructure/llm/providers/openai/provider_test.go`
- `internal/infrastructure/health/openai_compatible_test.go`
- `internal/application/idlechat/dialogue_prompt_test.go`
- `internal/adapter/config/config_test.go`
- `internal/application/tts/text_filter_test.go`

### 未保証・サーバ側依存

以下は RenCrow 側だけでは保証できない。

- backend が `think:false` を本当に thinking 無効化へ反映したこと。
- Qwen3 系モデルが thinking を生成しないこと。
- `<think>...</think>` や untagged reasoning が `content` に混入しないこと。
- reasoning only / empty final content の原因がサーバ metadata で判別できること。
- `think:false` と `think:true` を連続で切り替えたときに、LLM サーバ側で前回設定が残らないこと。

RenCrow 側の sanitizer / recovery は保険であり、OpenAI 互換 API の正規応答はサーバ側で最終本文と reasoning が分離済みであることを前提にする。

## Auth

現状は認証なし。RenCrow 側の API key は空文字、または任意の dummy 値でよい。

## Limits

- Chat サーバ既定の `max_tokens`: `16384`
- Worker サーバ既定の `max_tokens`: `8192`
- Heavy サーバ既定の `max_tokens`: `4096`
- Wild サーバ既定の `max_tokens`: `2048`
- request ごとの `max_tokens`: 指定可能
- Chat tokenizer config の実用上限はサーバ設定を正とする
- Wild tokenizer config の `model_max_length`: `262144`
- Worker は初回呼び出し時にモデルを取得するため、取得後に config を確認する

## Operational Notes

- 各用途は別プロセスで常駐させる。
- Chat プロセスは `Chat` 以外の model 名を拒否する。
- Worker プロセスは `Worker` / `Coder1` / `Coder2` / `Coder3` / `Coder4` を受け付け、それ以外の model 名を拒否する。
- Heavy プロセスは `Heavy` 以外の model 名を拒否する。
- Wild プロセスは `Wild` 以外の model 名を拒否する。
- 各プロセス内は安定性優先で単一リクエスト処理にしている。
- RenCrow 側の同時リクエストは用途ごとに 1 が安全。
- 初回リクエストはモデルロードとダウンロードで遅い。常駐後は同じモデルがメモリ上に残る。
- warmup は起動後に各 base URL へ `max_tokens: 1` の短い request を送る。

## Start Commands

```sh
uv run mlx-servers Chat
uv run mlx-servers Worker
uv run mlx-servers Heavy
uv run mlx-servers Wild
```

通常運用の Chat + Worker をまとめて起動する場合:

```sh
uv run mlx-servers
```

## Health / Restart Commands

用途ごとに health check:

```sh
uv run mlx-health Chat
uv run mlx-health Worker
uv run mlx-health Heavy
uv run mlx-health Wild
```

全用途まとめて health check:

```sh
uv run mlx-health
```

5分に1回の継続 health check:

```sh
uv run mlx-health-watch
```

用途ごとに再起動:

```sh
uv run mlx-restart Chat
uv run mlx-restart Worker
uv run mlx-restart Heavy
uv run mlx-restart Wild
```

全用途まとめて再起動:

```sh
uv run mlx-restart
```

ログは `run/Chat.log`、`run/Worker.log`、`run/Heavy.log`、`run/Wild.log` に出る。

ポート番号は固定。再起動時に別ポートへずらさない。既存の該当ポートの listener を停止し、同じポートで起動し直す。

health check 間隔は 300秒。health request timeout は 10秒。再起動時の起動待ち timeout は 600秒。

## Tool Calling

専用プロセス構成では未対応。Chat が詰まらないことと OpenAI互換 chat completions を優先する。

RenCrow 連携の最低要件には tool calling を含めない方針。
