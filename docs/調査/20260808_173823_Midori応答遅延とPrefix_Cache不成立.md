---
title: 調査 — Midori応答遅延とPrefix Cache不成立
date: 2026-08-08 17:38 JST
status: resolved
skill: debug-investigate
symptom: Midoriの短い応答でも最初のtokenまで約10秒以上かかる
frequency: 直近の実対話3件と制御測定で再現
inputs: Prompt Debug、RenCrow_LLM lifecycle log、Wild backend metrics、APC stats、Mac process/memory state、mlx-vlm実装
related: docs/04_アーキテクチャ概要.md#prompt-context-assembly
---

## 概要

Midoriの遅延は64Kという最大CTXそのものや出力token数ではなく、通常会話でAutomatic Prefix
Cacheが成立せず、約4.3K tokenの固定Promptを毎回prefillしていることが主因だった。
Qwen3.5系のhybrid linear/full attentionに対してmlx-vlmはblock APCではなくexact snapshotを使う。
既定checkpointが末尾16 token手前に置かれるため、Recall、時刻、履歴、User Messageを含む可変末尾に
checkpointが入り、固定System prefixが同じでもUser Messageが変わるだけでcache missになる。

Shiro Workerとの物理資源競合は追加遅延を生むが、Workerと重ならない実対話でもTTFT 11.6秒だったため
主因ではない。Prompt Contextの重複注入、Gateway queue、network、最大出力枠も主因から棄却した。

同一ModelのGGUFとllama.cpp b10327をA/B測定し、recurrent checkpointを256 tokens間隔にすると、
User Message変更時も4,440 / 4,484 tokensを再利用してTTFT 0.94秒となった。正規Wild Backendを
llama.cppへ変更し、COREからの実Midori対話でも2回目のTTFT 2.417秒を確認した。

## 実Payloadの測定

直近の実対話 `llmreq_57cacd55-d308-47bc-a137-6df51151b34b` は次のとおりだった。

| 項目 | 実測 |
| --- | ---: |
| Gateway受信Payload | 22,038 bytes |
| Target送信Payload | 20,333 bytes |
| Target message数 | 8 |
| Target先頭System | 18,698 bytes / 433 lines |
| Backend input | 4,453 tokens |
| Backend output | 34 tokens |
| queue | 0 ms |
| prompt eval | 11.578 s |
| TTFT | 11.623 s |
| decode | 1.668 s |
| Backend total | 13.291 s |

Viewerで過剰な重複が見えないという観測は正しい。Character blockは各1回、Stable RuntimeContextも
各1回で、toolsは0件だった。RecallPackはL0だけで小さい。ただし全体をtokenizeすると4,453 tokensあり、
出力34 tokensとは別にprefill対象となる。

| Prompt block | bytes |
| --- | ---: |
| `00_system.md` | 1,707 |
| `10_policy.md` | 5,398 |
| `20_scope.md` | 770 |
| `30_knowledge.md` | 8,220 |
| Agent Contract | 521 |
| Interaction Contract | 491 |
| Tool / Capability Boundary | 815 |

Character SystemPromptとStable RuntimeContextで17,922 bytesを占める。これは無駄な二重注入ではなく、
意図したSTATIC PREFIXである。削減より再利用を成立させるべき対象である。

## 制御測定

同じ18.7KB Systemを使い、`max_tokens=16`としてGateway経由で測定した。測定中はShiro Workerが
activeだったが、MidoriのGateway queueは全試行で0msだった。

| 条件 | input | prompt eval | TTFT | total | APC |
| --- | ---: | ---: | ---: | ---: | --- |
| 初回、4K級Prompt | 4,323 | 14.640 s | 14.766 s | 14.819 s | miss |
| 完全同一requestを再送 | 4,323 | 0.348 s | 0.424 s | 0.499 s | 4,307 tokens hit |
| System同一、User Messageだけ変更 | 4,326 | 13.969 s | 14.027 s | 14.083 s | miss |
| 一意な短いPrompt | 36 | 1.050 s | 1.111 s | 1.175 s | miss |

同じ64K上限、同じmodel、同じWorker active状態でも、36 tokenは約1.1秒、4.3K tokenは約14秒だった。
したがって64Kの上限確保だけが遅延を生む仮説は棄却できる。`max_tokens=16`でも4.3K入力は遅いため、
`max_tokens=2048`も今回のTTFT主因ではない。

完全同一requestだけ0.42秒になる一方、User Messageを変えると14秒へ戻った。APC statsも
`pool_used=0`、`stores=0`のまま、`exact_hits`と`exact_stores`だけが増えた。

## llama.cpp A/Bと本番反映

同一の`Qwen3.6-27B Uncensored Heretic v2`について、作者配布のQ4_K_M GGUFと
llama.cpp b10327をMacへ隔離導入した。GPT-OSS Workerは常駐させたまま、MLX測定後に
Wild Backendだけを入れ替え、同じ`:8084` proxy、65,536 context、1 slot、同一Payload、
`max_tokens=16`で比較した。

| Backend / 条件 | cold TTFT | exact TTFT | suffix変更 TTFT | suffix cached |
| --- | ---: | ---: | ---: | ---: |
| MLX 4bit / exact APC | 15.56 s | 0.49 s | 14.04 s | 0 / 4,484 |
| llama.cpp既定checkpoint | 17.64 s | 0.26 s | 2.94 s | 3,968 / 4,484 |
| llama.cpp checkpoint 256 | 17.19 s | 未再測定 | 0.94 s | 4,440 / 4,484 |

llama.cppはcold prefill自体を速くしない。一方、hybrid recurrent stateのcheckpoint間隔を
既定8,192から256へ変更すると、可変suffix直前に近いcheckpointを保持でき、再評価対象は44 tokensに
減った。これによりsuffix変更TTFTはMLX比で約93%短縮した。

正規設定へ反映後、RenCrow LLM mgmt daemonからWild roleをrestartし、次を確認した。

- process: llama.cpp b10327 / Q4_K_M GGUF
- route: `CORE -> RenCrow LLM Gateway -> RenCrow LLM Runtime -> :8084 proxy -> :18084 llama.cpp`
- context: 65,536 / 1 slot
- 初回実Midori対話: first token 15.894 s、generation 16.610 s（cold）
- 次の実Midori対話: first token 2.417 s、generation 2.947 s、CLI wall 3 s
- 2回目のllama.cpp log: prompt再評価503 tokens、queue 0 ms

初回CLI試験はLLM応答後のWorkerによるMemory抽出が同期完了するまで待ち、60秒timeoutになった。
Midori LLM自体は16.6秒で応答済みであり、Backend速度とCORE後処理時間は分けて扱う必要がある。

## Prefix Cache不成立のメカニズム

稼働modelの`config.json`は`model_type=qwen3_5`で、64 layer中、linear attention 3層とfull attention
1層を繰り返すhybrid構造である。mlx-vlm 0.6.2の`make_cache()`はlinear layerへ`ArraysCache`、
full attention layerへ`KVCache`を返す。

mlx-vlm commit `f7f43dbec3c9e8e8e911766573ac84b62ed60e7f`は、全layerがblock互換cacheでないmodelを
`apc_mode="exact"`へ分類する。exact modeは全blockを保存せず、次の2つを保存する。

1. Prompt全体のexact snapshot
2. Prompt末尾から`APC_EXACT_PREFIX_GUARD_TOKENS`を引いた位置のcheckpoint

現在のMac環境には`APC_ENABLED=1`、`APC_BLOCK_SIZE=16`、`APC_NUM_BLOCKS=2048`だけがあり、
exact guardは既定の16 tokens、exact cache entriesは既定の2件である。

実対話2件のTarget Systemは18,297 bytes、4,192 tokensまで共通だった。最新Prompt全体は4,453 tokens
なので、最初の変化位置より後ろに約261 tokensある。既定checkpointは4,437付近となり、すでに
Recall、時刻、履歴、User Messageからなる可変末尾の内部である。このためSTATIC PREFIXが同じでもhitしない。

`APC_BLOCK_SIZE`と`APC_NUM_BLOCKS`を設定しただけでは、exact modeのQwen3.5でblock prefix再利用には
ならない。healthの`apc_enabled=true`と完全同一Payloadの再送だけを受入条件にした検証が不十分だった。

## Mac物理資源の測定

制御測定中、Shiro Workerは5分超のtranslation requestを実行していた。Gateway上のresource groupは
MidoriとShiroで分離されていたが、同じMacのMetal GPUとUnified Memoryを共有する。

| process | RSS |
| --- | ---: |
| GPT-OSS-120B `llama-server` | 68,973,712 KB |
| Wild Qwen3.5 27B MLX | 13,203,344 KB |

Macはmemory free 33%、compressor使用は約20GBだった。Worker active中の未cache 4.3K Promptは
TTFT 14.0-14.8秒だったのに対し、Worker終了直後の過去実測は9.16-11.62秒だったため、物理競合は
2-5秒程度を追加する可能性がある。ただし完全cache hitはWorker active中でも0.42秒であり、
cache missによるprefillが主因、物理競合は二次要因と判定する。

## 仮説別判定

### 仮説1: 入力Promptが想定より大きい

- **判定**: 確認。ただし無駄な重複ではない。
- **証拠**: 実入力4,453 tokens、出力34 tokens。STATIC PREFIXが大半を占める。
- **反証**: 64K近くは使用しておらず、Viewer上の各blockは正規構造で各1回だった。

### 仮説2: Prefix Cacheが通常会話で成立していない

- **判定**: 確認。主原因。
- **証拠**: 完全同一requestは4,307 tokens hit、User Message変更だけでhit 0へ戻る。
  Qwen3.5 hybrid cacheはexact mode、guard既定16 tokens、block poolは未使用だった。
- **反証**: APC自体が完全に停止しているわけではなく、完全同一requestでは0.42秒まで短縮した。

### 仮説3: Shiro Workerとの物理競合

- **判定**: 確認。二次要因。
- **証拠**: Worker active中の未cache prefillは14秒台、Worker終了直後の実測は9-11秒台。
  両modelの合計RSSは約82GBでUnified Memoryを共有する。
- **反証**: queueは0msで、Workerと重ならない実対話にも11.6秒のTTFTがある。

## 棄却した原因

- 64K最大CTX: 36-token Promptは同じ上限で1.1秒だった。
- 2,048 output枠: `max_tokens=16`でも未cache 4.3K Promptは14秒だった。
- Gateway queue: 全試行0ms。
- Gateway/network: Gateway接続は約0.2msで、Gateway TTFTとBackend TTFTが一致した。
- SystemPrompt二重注入: 4 blockは各1回、Stable RuntimeContext各1回、tools 0件だった。
- model cold startだけ: model warm後もUser Message変更でTTFT 14.0秒を再現した。

## 修正案

### 1. 適用済み: llama.cpp recurrent checkpointを固定prefix近傍に置く

正規Wild Backendをllama.cpp b10327へ変更し、`--checkpoint-min-step 256`、
`--ctx-checkpoints 32`、host prompt cache 8,192 MiBを設定した。これによりPrompt Builderや
Payload内容を削らず、可変suffixの再評価だけへ抑えた。

### 2. MLXへ戻す場合: STATIC PREFIX境界をexact APC checkpointにする

Qwen3.5 exact modeへ、Prompt末尾からの固定guardではなく、Character SystemPromptとStable
RuntimeContextが終わるtoken位置をcheckpointとして渡す。Prompt Builderの型付きmetadataと
STATIC PREFIX hashを利用し、target wire変換後も境界を失わないcontractをRenCrow_LLMとbackendに追加する。

受入条件は、User MessageとVariable RuntimeContextが変わる2 requestで次を満たすこととする。

- 2回目の`matched_tokens`が少なくとも4,000 tokens
- 2回目のTTFTが2秒以内
- 完全同一requestのexact hitではなく、suffix変更時のprefix hitである
- Character、Stable RuntimeContext、Recall、User Messageの内容を削除しない

### 3. MLX暫定対応: exact guardを512 tokensへ拡張して実測する

現状の可変末尾は約261 tokensなので、`APC_EXACT_PREFIX_GUARD_TOKENS=512`なら現在の会話では
checkpointが共通prefix内に入る。これはすぐ検証できるが、履歴がさらに増えた場合は再び可変部へ
checkpointが入るため恒久解ではない。変更する場合はrestart後、異なるUser Messageでhitを確認する。

### 4. 物理競合を抑える

対話中Midoriのprefill区間ではShiro background Workerを新規開始しない、または一時的に物理優先度を
下げる。論理resource group分離だけではMetal/Unified Memoryを分離できない。ただしcache修正より優先しない。

### 5. 観測契約を強化する

`/health`と`/metrics`へ`apc_mode`、exact guard、exact cache entries、cached prompt tokensを公開する。
`apc_enabled=true`だけでPrefix Cache成功と扱わない。

## Phase 3 バイアス防止再点検

- [x] 確証バイアス: Prompt長だけで結論せず、短いPrompt、完全同一、suffix変更の3条件を比較した。
- [x] 反証: 64K、max output、queue、network、cold start、重複注入を個別に棄却した。
- [x] ライフサイクル: Gateway queue→active→completed、APC store→lookup→stats、Worker overlapを追跡した。
- [x] 頻度制約: 直近実対話と3つの制御条件で再現性を確認した。
- [x] 既存知見: `20260808_135025_Midori_Wild64K無応答.md`のmessage形式エラーは修正済みで、
  今回の正常HTTP 200かつ遅い症状とは区別した。

## 調査上の副作用

token count endpointへalias `Wild64K`を直接渡した最初の診断requestがbackendのmodel cacheを一度
clearした。直後に正しいmodel pathで再loadし、`/health`、通常completion、warm後のsuffix変更試験を
再確認した。warm後も14.0秒を再現したため、根本原因判定はcold reloadだけに依存しない。

llama.cpp A/B開始時にもMLX Backendへalias `Wild64K`を直送し、同じ誤loadでBackendを一度終了させた。
Qwen profileの既定restartで直ちに復旧し、それ以後のMLX測定はaliasを物理model pathへ変換する
`:8084` proxy経由へ限定した。正規経路復旧後の3条件で基準値を取り直している。

## 関連ソース・実行設定

- `~/.rencrow/logs/llm_prompt_debug.jsonl` - Gateway受信/Target送信Payload
- `~/.rencrow/config/llm.json` - `midori -> midori_wild -> Wild64K`
- `../RenCrow_LLM/gateway/internal/httpapi/system_messages.go` - Wild向けsingle-leading System正規化
- Mac `.venv/lib/python3.12/site-packages/mlx_vlm/apc.py:3737` - model別APC mode判定
- Mac `.venv/lib/python3.12/site-packages/mlx_vlm/apc.py:2782` - exact guard既定16 tokens
- Mac `.venv/lib/python3.12/site-packages/mlx_vlm/generate/ar.py:2223` - exact checkpoint位置
- Mac `.venv/lib/python3.12/site-packages/mlx_vlm/models/qwen3_5/language.py:2614` - hybrid cache構成
- Mac `run/Wild.backend.log`、`GET :18084/metrics`、`GET :18084/v1/cache/stats` - live metrics
- `../RenCrow_Qwen36_27B/config/qwen36.env` - llama.cpp / GGUF / checkpoint正本
- `../RenCrow_Qwen36_27B/scripts/llama-server.sh` - Backend tuning wrapper
- `../RenCrow_Qwen36_27B/scripts/health.sh` - llama.cpp Model / context / metrics readiness

## 教訓

- `apc_enabled=true`は「通常会話の固定prefixが再利用される」という意味ではない。
- 完全同一Payloadの再送はexact response/cacheだけを検証し、suffix可変の対話cacheを検証しない。
- 最大CTX、入力token、出力token、prefill、decode、queueを分けて測定する。
- Qwen3.5 hybrid modelではblock数の設定よりexact checkpoint境界が重要である。
