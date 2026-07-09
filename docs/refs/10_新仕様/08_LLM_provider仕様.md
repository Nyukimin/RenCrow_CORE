# LLM provider 仕様

## 目的

LLM provider は Chat / Worker / Heavy / Wild / Coder の推論実装を差し替える境界である。

Application / Domain は provider 固有の HTTP request、stream、response parse、thinking bridge、model ready 判定へ直接依存しない。

## provider の役割

| 用途 | 代表 role | 位置づけ |
| --- | --- | --- |
| Chat | Mio | ユーザー対話、ルーティング判断、最終応答 |
| Worker | Shiro | 実行判断、tool calling、summary、ops |
| Heavy | 重い推論 | 必要時の高負荷推論 |
| Wild | 補助推論 | 用途別補助 |
| Coder | AO / Aka / Kin / Gin | plan / patch / proposal 生成 |

## 主な実装箇所

| 領域 | 主担当 |
| --- | --- |
| provider interface | `internal/domain/llm` |
| provider factory | `internal/infrastructure/llm/factory` |
| runtime provider assembly | `cmd/rencrow/llm_runtime_factory.go`, `cmd/rencrow/runtime_llm_providers.go` |
| middleware / raw log | `internal/infrastructure/llm/middleware` |
| OpenAI provider | `internal/infrastructure/llm/providers/openai/*.go` |
| Ollama provider | `internal/infrastructure/llm/providers/ollama/*.go` |
| Claude provider | `internal/infrastructure/llm/providers/claude/provider.go` |
| DeepSeek provider | `internal/infrastructure/llm/providers/deepseek/provider.go` |
| Gemini provider | `internal/infrastructure/llm/providers/gemini/provider.go` |
| Viewer LLM Ops | `internal/adapter/viewer/llm_ops_handler.go` |

## runtime config

repo example と live runtime config を混同しない。

- repo example は設定例である。
- live runtime config は `~/.rencrow/config.yaml` と実起動状態で決まる。
- Viewer runtime config は表示用の投影である。
- provider health は inference endpoint と management API を分けて見る。

local OpenAI-compatible provider は Chat / Worker / Heavy / Wild の主経路になり得る。`local_llm.*` では role 別 base URL、model alias、timeout、warmup、global concurrency、model concurrency を扱う。

`Chat` / `Worker` / `Wild` alias は runtime provider assembly で解決する。repo example の model 名と live runtime の model 名が違う場合は、live config と `/viewer/runtime-config` を優先して確認する。

timeout は queue wait と generation elapsed を分けて設計する。server-side queue に任せるだけでは timeout 原因を切り分けできないため、RenCrow 側の client queue、queue timeout、generation timeout、phase 別ログ契約は `45_LLMクライアントキューTimeout仕様.md` を正とする。

OpenAI-compatible embedding は conversation summarizer、profile extractor、Recall / KB の補助に使う。Embedding provider と Chat/Worker provider を混同しない。

## raw log / thinking bridge

raw log は provider 応答の観測用である。Viewer 表示本文や会話注入本文とは別に扱う。

OpenAI / OpenAI互換 provider の thinking bridge や reasoning sanitize は、内部推論漏れを表示本文へ混ぜないための境界である。

raw log は `chat_raw.log`、`worker_raw.log`、`IdleChat_raw.log` など用途別に分ける。空 `content`、invalid response、finish reason、token usage は fallback ではなく原因切り分けの証拠として扱う。

## fallback

provider fallback は正常系ではない。

接続失敗、空応答、invalid response、timeout、model not ready は成功ではない。fallback に落ちたことをログと検証結果で区別する。

## 検証

主な確認:

```bash
GOCACHE=/tmp/rencrow-gocache go test ./internal/infrastructure/llm/factory ./internal/infrastructure/llm/middleware ./internal/infrastructure/llm/providers/...
GOCACHE=/tmp/rencrow-gocache go test ./cmd/rencrow
```

live 確認:

```bash
curl -fsS http://127.0.0.1:18790/health
curl -fsS http://127.0.0.1:18790/viewer/llm-ops/status
```

確認対象:

- Chat / Worker endpoint が reachable である。
- provider timeout / model / base URL が live config と一致する。
- raw log と Viewer 表示が混ざっていない。
- fallback が成功扱いされていない。
