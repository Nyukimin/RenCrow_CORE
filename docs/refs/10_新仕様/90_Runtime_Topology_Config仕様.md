# Runtime Topology Config 仕様

## 目的

`~/.rencrow/config.yaml` を、RenCrow の実行時モジュール構成を指し示す設計図として扱う。

この仕様でいう設計図とは、次を一箇所で読める設定である。

- どのモジュールがどの PC / host にいるか。
- そのモジュールを操作するための代表 endpoint は何か。
- モジュールの実体 repo / root path はどこか。
- `rencrow` から見た公開 API と、モジュール内部から見た backend API を混同しないこと。

個別モジュール固有の内部設定、たとえば model alias、parser、prompt、model path、queue policy までは `config.yaml` に集約しない。
`config.yaml` は配置と接続の正本、各 module repo の設定はその module の内部挙動の正本とする。

## 基本方針

通常の配置指定は IP / host だけで足りるようにする。

port は module kind ごとの既定値を持つ。
既定値と違う場合だけ module ごとに上書きする。

```yaml
runtime_topology:
  version: 1

  modules:
    rencrow:
      host: 127.0.0.1

    RenCraw_LLM:
      host: 127.0.0.1
      backend_host: 192.168.1.34

    RenCrow_TTS:
      host: 192.168.1.205
```

この例では、URL は resolver が次のように導出する。

| 参照 | 導出 URL |
| --- | --- |
| `module:rencrow.endpoints.http` | `http://127.0.0.1:18790` |
| `module:RenCraw_LLM.endpoints.mgmt` | `http://127.0.0.1:8079` |
| `module:RenCraw_LLM.endpoints.chat` | `http://127.0.0.1:8081` |
| `module:RenCraw_LLM.endpoints.worker` | `http://127.0.0.1:8082` |
| `module:RenCraw_LLM.backends.chat` | `http://192.168.1.34:18081` |
| `module:RenCraw_LLM.backends.worker` | `http://192.168.1.34:11434` |
| `module:RenCrow_TTS.endpoints.http` | `http://192.168.1.205:7870` |

## Module ID

`runtime_topology.modules` の key を module id と呼ぶ。

module id は設定・UI・診断で使う論理名であり、directory 名と一致しなくてよい。
LLM module の論理名は `RenCraw_LLM` とする。

実体 checkout が既存の `/home/nyukimi/RenCrow/RenCrow_LLM` にある場合でも、module id は `RenCraw_LLM` のままでよい。
必要なら `root` に実 path を書く。

```yaml
runtime_topology:
  modules:
    RenCraw_LLM:
      host: 127.0.0.1
      root: /home/nyukimi/RenCrow/RenCrow_LLM
```

## Port Catalog

既定 port は resolver 側に持つ。
`config.yaml` に明示する場合は、次の形を標準とする。

```yaml
runtime_topology:
  default_ports:
    rencrow:
      http: 18790

    RenCraw_LLM:
      mgmt: 8079
      chat: 8081
      worker: 8082
      heavy: 8083
      wild: 8084
      backend_chat: 18081
      backend_worker: 11434
      backend_heavy: 18083
      backend_wild: 18084

    RenCrow_STT:
      http: 8766

    RenCrow_TTS:
      http: 7870
```

module ごとに port を変える場合は `ports` で上書きする。

```yaml
runtime_topology:
  modules:
    RenCraw_LLM:
      host: 127.0.0.1
      backend_host: 192.168.1.34
      ports:
        worker: 18082
        backend_worker: 11435
```

## Public Endpoint と Backend Endpoint

`RenCraw_LLM` では public endpoint と backend endpoint を分ける。

```text
rencrow
  -> RenCraw_LLM public endpoint
    -> LLM backend endpoint
```

例:

```text
rencrow
  -> http://127.0.0.1:8081        # RenCraw_LLM Chat proxy
    -> http://192.168.1.34:18081  # Chat backend

rencrow
  -> http://127.0.0.1:8082        # RenCraw_LLM Worker proxy
    -> http://192.168.1.34:11434  # Worker backend
```

`local_llm.*` は `rencrow -> RenCraw_LLM` の public endpoint を指す。
`RenCraw_LLM` の `backend_base` は `RenCraw_LLM -> LLM backend` を指す。
この二つを同じ値にしない。

## 推奨 config 例

```yaml
runtime_topology:
  version: 1
  active_profile: home_linux

  profiles:
    home_linux:
      modules:
        rencrow:
          kind: core_viewer_server
          host: 127.0.0.1
          root: /home/nyukimi/RenCrow/RenCrow_CORE
          service: rencrow.service

        RenCraw_LLM:
          kind: llm_role_server
          host: 127.0.0.1
          backend_host: 192.168.1.34
          root: /home/nyukimi/RenCrow/RenCrow_LLM
          roles:
            chat:
              enabled: true
            worker:
              enabled: true
            heavy:
              enabled: false
            wild:
              enabled: false

        RenCrow_STT:
          kind: stt_server
          host: 127.0.0.1
          root: /home/nyukimi/RenCrow/RenCrow_STT

        RenCrow_TTS:
          kind: tts_server
          host: 192.168.1.205
```

`profiles` は任意である。
PC や配置を頻繁に切り替える場合は `profiles` を使い、単一構成だけなら `runtime_topology.modules` 直下に置いてよい。

## 既存 config への投影

既存 runtime は `local_llm`、`rencrow.llm`、`llm_ops`、`webwright_fetch`、`coder1..4` などの URL を読む。
`runtime_topology` はそれらの上位正本として扱い、resolver が必要な値を投影する。

例:

```yaml
local_llm:
  enabled: true
  provider: local_openai
  base_url: ${module:RenCraw_LLM.endpoints.chat}
  chat_base_url: ${module:RenCraw_LLM.endpoints.chat}
  worker_base_url: ${module:RenCraw_LLM.endpoints.worker}
  heavy_base_url: ""
  wild_base_url: ""

rencrow:
  llm:
    enabled: true
    base_url: ${module:RenCraw_LLM.endpoints.mgmt}

llm_ops:
  enabled: true
  base_url: ${module:RenCraw_LLM.endpoints.mgmt}

webwright_fetch:
  responses_endpoint: ${module:RenCraw_LLM.endpoints.worker}/v1/responses
```

未実装段階では、上記の `${module:...}` をそのまま runtime に渡してはいけない。
resolver または生成ツールが具体 URL に展開してから既存 runtime へ渡す。

## RenCraw_LLM への投影

`RenCraw_LLM/configs/*.toml` の `backend_base` を `config.yaml` へ完全移植する必要はない。
既存 TOML は module 内部設定として残し、backend 接続先だけ topology から注入する。

推奨する投影先は既存の環境変数である。

| Role | env | topology 参照 |
| --- | --- | --- |
| Chat | `RENCROW_CHAT_BACKEND_BASE` | `module:RenCraw_LLM.backends.chat` |
| Worker | `RENCROW_WORKER_BACKEND_BASE` | `module:RenCraw_LLM.backends.worker` |
| Heavy | `RENCROW_HEAVY_BACKEND_BASE` | `module:RenCraw_LLM.backends.heavy` |
| Wild | `RENCROW_WILD_BACKEND_BASE` | `module:RenCraw_LLM.backends.wild` |

これにより、`RenCraw_LLM/configs/*.toml` は fallback と role 内部設定を保持しつつ、実運用の接続先は `~/.rencrow/config.yaml` で決められる。

## 解決優先順位

target runtime では次の優先順位で値を決める。

1. 明示的な emergency override 環境変数。
2. `runtime_topology` から導出した値。
3. 既存 config section の明示 URL。
4. module repo 内の TOML fallback。

通常運用では 2 を正本とする。
1 は一時復旧用であり、診断表示では override 中であることを必ず出す。

## Empty Endpoint

起動していない role は空文字で表現できる。

```yaml
local_llm:
  heavy_base_url: ""
  wild_base_url: ""
```

空 endpoint は「この runtime では常時利用しない」という意味であり、model 名が存在しないという意味ではない。
`RenCraw_LLM` の管理 API から Heavy / Wild を起動できる構成では、role は定義済みだが inactive として扱う。

Health check は空 endpoint を常時 down として扱わない。
ただし、明示 URL があるのに接続できない場合は down として扱う。

## Secret

`runtime_topology` に secret を置かない。

管理 token、API key、credential は `token_env` / `api_key_env` のような env 参照にする。

```yaml
rencrow:
  llm:
    token_env: LLM_OPS_TOKEN
```

## 診断で表示するもの

Viewer / CLI の診断では、次を表示できるようにする。

- active profile。
- module id。
- host / backend_host。
- 導出された public endpoint。
- 導出された backend endpoint。
- root / service が設定されている場合はその値。
- topology 値と legacy 明示 URL が衝突している場合の warning。
- emergency override が効いている場合の warning。

## 移行手順

1. `~/.rencrow/config.yaml` に `runtime_topology` を追加する。既存 URL 設定はまだ残す。
2. topology doctor / resolver を追加し、導出 URL と既存 URL の差分を warning として出す。
3. `rencrow` の `local_llm` / `rencrow.llm` / `llm_ops` は topology から導出できるようにする。
4. `RenCraw_LLM` 起動 wrapper は topology から backend env を export する。
5. runtime 診断で衝突が消えたら、固定 IP の重複記述を減らす。

## 非目標

- すべての module 固有設定を `~/.rencrow/config.yaml` に吸い上げること。
- module repo の内部 config を廃止すること。
- secret を一箇所に集約して平文保存すること。
- `RenCraw_LLM` の public endpoint と backend endpoint を同じ概念にすること。
