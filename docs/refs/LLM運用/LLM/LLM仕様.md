# LLM 仕様

この文書は、RenCrow_LLM が提供する MLX / oMLX ベースの OpenAI 互換 API 仕様をまとめる。

## 実行方式

- Python / `uv` で起動する。
- LLM 実体は `mlx-vlm` または `oMLX` を使う。
- 各ロールは backend と OpenAI model alias proxy の 2 プロセスで構成する。
- backend は `127.0.0.1:180xx` で待ち受ける。
- proxy は `0.0.0.0:808x` で待ち受け、クライアント向けの model 名を backend model に変換する。

## ロール一覧

| Role | Public URL | Backend URL | Config | Public model |
| --- | --- | --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `http://127.0.0.1:18081` | `configs/chat-server.toml` | `Chat` |
| Worker | `http://127.0.0.1:8082` | `http://127.0.0.1:18082` | `configs/worker-server.toml` | `Worker` |
| Heavy | `http://127.0.0.1:8083` | `http://127.0.0.1:18083` | `configs/heavy-server.toml` | `Heavy` |
| Wild | `http://127.0.0.1:8084` | `http://127.0.0.1:18084` | `configs/wild-server.toml` | `Wild` |

## モデル割り当て

| Role | Backend model | 主用途 |
| --- | --- | --- |
| Chat | `/Users/yukimi/models/gemma-4-e4b-it-4bit` | 会話テンポ、ルミナ人格、音声 UI、自然な対話 |
| Worker | `/Users/yukimi/models/Qwen3-VL-30B-A3B-Thinking-4bit` | 実務処理、要約、整理、RAG、通常の画像解析、UI・資料・スクショ理解 |
| Heavy | `/Users/yukimi/models/Qwen3.5-122B-A10B-4bit` | 深考察、前提の見直し、失敗原因分析、ローカル最終レビュー |
| Wild | `/Users/yukimi/models/Qwen3.5-27B-heretic-8bit` | 物語生成、画像プロンプト生成、創作用の画像解析 |

現行の主要公開 model 名は `Chat` / `Worker` / `Heavy` / `Wild`。
Worker / Heavy / Wild では、同じ backend に向く `Coder1` / `Coder2` / `Coder3` / `Coder4` alias も公開する。
`Coder` 単体 alias はクライアントへ公開しない。

## 起動

通常運用の Chat + Worker:

```sh
uv run mlx-servers
```

ロール指定:

```sh
uv run mlx-servers Chat
uv run mlx-servers Worker
uv run mlx-servers Heavy
uv run mlx-servers Wild
```

単一 backend の起動確認:

```sh
uv run mlx-server --config configs/chat-server.toml --dry-run
```

## API

Public port は OpenAI 互換の主要 endpoint を受け付ける。

| Method | Path | 用途 |
| --- | --- | --- |
| `POST` | `/v1/chat/completions` | Chat Completions |
| `GET` | `/v1/models` | public model alias 一覧 |
| `GET` | `/health` | backend health passthrough |

`/health` の正常応答は `{"status":"healthy", ...}` または proxy 側の `{"status":"ok"}` 相当を正常扱いする。

### `/v1/models` の返却仕様

クライアントには公開 model 名と実体の backend model 名を合わせて返す。

Chat の例:

```json
{
  "object": "list",
  "data": [
    {
      "id": "Chat",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/gemma-4-e4b-it-4bit"
    }
  ]
}
```

Worker の例:

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

クライアントが `/v1/chat/completions` に指定する `model` は `id` の値を使う。
実体モデル名を表示・確認したい場合は `backend_model` を使う。

## 起動管理 API

管理 API は `mlx-mgmt` が `8079` で提供する。

```sh
export LLM_OPS_TOKEN='長くランダムな秘密'
uv run mlx-mgmt Chat Worker
```

主な endpoint:

- `GET /health`
- `GET /v1/status`
- `POST /v1/control/start`
- `POST /v1/control/restart`
- `POST /v1/control/stop`

`/v1/control/start` では `{"selection":"Worker"}` / `{"selection":"Wild"}` / `{"selection":"Heavy"}` を受け付ける。
selection 指定時は Chat と選択 role を起動対象にする。

## 注意

- 起動対象は `mlx_vlm.server` に統一する。
- 旧単一サーバ設定ファイルは廃止済み。
- Worker と Heavy は別 backend model を使う。どちらも大きいため、同時起動はメモリ効率が悪い。
- Wild / Heavy は必要時に起動し、通常は Chat + Worker を常駐させる。
