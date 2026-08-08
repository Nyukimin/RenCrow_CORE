---
title: 調査 — MLX Wild64K alias直送によるbackend停止
date: 2026-08-08 18:30 JST
status: resolved
skill: debug-investigate
symptom: Wild64Kを指定した診断requestの後にMLX backendが停止し、role proxyから接続拒否になった
frequency: 2026-08-08の診断中に同じ誤経路で2回発生。正規proxy経由では再現しない
inputs: Wild role log、稼働process観測、mlx-vlm 0.6.2実装、RenCrow alias proxy実装、復旧後health/completion
related: docs/調査/20260808_173823_Midori応答遅延とPrefix_Cache不成立.md
---

## 概要

Midori遅延調査中、論理model alias `Wild64K`をRenCrow LLM Runtimeのalias proxyを通さず、
MLX物理backendへ直接送った。mlx-vlm 0.6.2はrequestの`model`を物理model pathとして扱い、
現在の正常modelをunloadしてから`Wild64K`のloadを試みるため、load失敗後にbackendが利用不能になった。

本件で確認できた名前付き例外はProxy側の`ConnectionRefusedError`であり、mlx-vlmに
`CrashError`または`ChashError`という例外型が発生した証拠はない。本書では「MLX crash」を、
process停止を含むbackend利用不能という運用上の事象名として扱う。

## 発生経路

正常な経路では、論理aliasは物理model pathへ変換される。

```text
診断request
  -> RenCrow LLM Runtime / alias proxy :8084
     model: Wild64K
       -> model: <configured MLX model path>
          -> MLX backend :18084
```

事故時はalias proxyを迂回した。

```text
診断request
  -> MLX backend :18084
     model: Wild64K
       -> mlx-vlmがWild64Kを物理model identifierとして扱う
          -> 稼働modelをunload
          -> Wild64Kのloadに失敗
          -> backend利用不能
```

## 調査経緯

### 仮説1: 論理aliasの物理backend直送がmodel切替を起動した

- **根拠**: 事故直前の診断は`model=Wild64K`を`:18084`へ直接送っていた。通常の
  `:8084`経路ではlogに`original_model='Wild64K'`から構成済みmodel pathへの変換が記録される。
- **検証結果**: 確認。
- **証拠**:
  - mlx-vlm 0.6.2のOpenAI completionとAnthropic token countは、requestの`model`をそのまま
    `get_cached_model()`へ渡す。
  - `get_cached_model()`はcache keyが異なると既存modelを先に`unload_model_sync()`し、その後に
    新modelのloadを開始する。
  - `unload_model_sync()`はResponseGenerator停止、APC clear、model cache破棄、GC、Metal cache
    clearまで実行する。
  - 復旧後の正規proxy requestでは`Wild64K`が構成済みmodel pathへ変換され、HTTP 200になった。
- **反証**:
  - `Wild64K`が物理model pathとして登録されていれば同じ文字列でもloadできる可能性がある。
    今回のMLX構成では登録されておらず、alias変換はRenCrow proxyの責務だった。
- **チェックリスト結果**:
  - [x] 確証バイアス: 正規proxy経由と直送経路を比較し、文字列だけでなく変換境界を確認した。
  - [x] 頻度制約: 誤った直送時に2回発生し、通常会話の正規経路では発生していない。
  - [x] ライフサイクル: cache判定、旧model unload、新model load、失敗、restart、readyまで追跡した。
  - [x] 既存知見: Prefix Cache不成立の調査で記録済みの診断副作用と一致する。

### 仮説2: Unified Memory不足またはWorker競合がMLXを停止させた

- **根拠**: 事故時のMacではWildと大型WorkerがUnified Memoryを共有し、memory compressorも使用していた。
- **検証結果**: 棄却。
- **証拠**:
  - 物理競合は通常requestのTTFTを悪化させたが、正規proxy経由のcompletionは成功していた。
  - 利用不能化はalias直送直後に発生し、mlx-vlm実装上も同じ入力でmodel unloadが必ず起動する。
  - 保存された証拠にはOSのOOM killまたはMetal allocation failureがない。
- **反証**:
  - 元のbackend終了traceが保存されていないため、memory pressureが終了挙動を増幅した可能性までは
    完全には否定できない。ただし事故を開始した条件ではない。
- **チェックリスト結果**:
  - [x] 確証バイアス: 高いRSSだけでOOMと判断せず、OS kill証拠の有無を確認した。
  - [x] 頻度制約: Worker競合中の正常requestが存在し、競合だけでは毎回停止しない。
  - [x] ライフサイクル: Worker active状態とWild request成功、誤request後の停止を分離した。
  - [x] 既存知見: 既存調査の「物理競合は遅延の二次要因」という判定と矛盾しない。

### 仮説3: RenCrow alias proxyが誤ったmodel名をbackendへ送った

- **根拠**: Proxy logの外形だけを見ると、`Wild64K` requestの直後にbackend接続拒否が発生している。
- **検証結果**: 棄却。
- **証拠**:
  - 正規proxy logは`original_model='Wild64K'`を構成済みMLX model pathへ変換している。
  - 事故を起こしたrequestはproxyではなく物理backendへ直送していた。
  - restart後、同じaliasをproxy経由で送ると正常にcompletionできた。
- **反証**:
  - Proxyの汎用passthrough endpointにはalias書換えを行わない経路があり、診断ツールがendpoint契約を
    誤る余地は残る。したがってProxy全体が無関係なのではなく、境界の強制が不足していた。
- **チェックリスト結果**:
  - [x] 確証バイアス: 接続拒否をProxy起因と決めず、requestの実送信先とpayload変換を確認した。
  - [x] 頻度制約: 正規completion経路ではalias変換が毎回成功する。
  - [x] ライフサイクル: Proxy processの生存と物理backend processの停止を別々に確認した。
  - [x] 既存知見: CORE、Gateway、Runtime、Backendを分離する正本architectureと一致する。

## 根本原因

- **直接原因**: 診断処理が正規のalias proxyを迂回し、論理alias `Wild64K`をMLX物理backendの
  `model`へ指定した。
- **技術的原因**: mlx-vlm 0.6.2は未一致model requestを受けると、新modelの妥当性確認より先に
  現在のmodelをunloadする。新model loadが失敗した時点で、直前まで利用可能だったmodelは戻らない。
- **設計上の寄与要因**:
  - 物理backendがrequestごとの任意model切替を受け付け、起動時modelへ固定されていなかった。
  - 診断ツールに「aliasはrole proxy、物理pathはbackend」という型付き境界がなかった。
  - Proxy自身のhealthと背後backendのreadinessを混同しやすかった。
  - backend logが切替時に保持されず、終了直前のMLX tracebackまたはsignalが失われた。
- **影響範囲**:
  - 事故からrestart完了までMidoriのWild roleが利用不能になった。
  - MLXのmodel cacheとAPCは破棄されたが、model file、会話、Memoryなどの永続データ損失はない。
  - Workerなど別backendは停止していない。
  - 現在の本番Wildはllama.cppへ移行済みだが、MLX fallbackへ戻した場合は同じ誤直送に注意が必要である。

## 復旧

1. 直送診断を停止した。
2. Qwen runtime profileの正規restartでMLX backendとalias proxyを再起動した。
3. backend `/health`、proxy `/health`、正規completionを個別に確認した。
4. MLX A/B測定は以後すべて`:8084` proxy経由に限定した。
5. 最終的にWild roleをllama.cppへ切り替え、同じrole proxy契約を維持した。

## 修正案

1. **適用済み**: 診断・A/B測定でもrole aliasを使う場合は必ずRenCrow LLM Runtimeのrole proxyを通す。
2. backendへ直接送る必要がある診断は、Backend inventoryが返す物理model identifierだけを使用する。
3. 診断request schemaを`logical_alias`と`physical_model_id`へ分離し、同じ`model`文字列型で兼用しない。
4. MLX fallbackではbackendをloopbackへ限定し、任意model切替を外部から到達不能にする。
5. 固定model運用時は、起動modelと異なるrequestをunload前にHTTP 400で拒否するguardを追加する。
6. readinessはProxy process、Backend listener、loaded model、実completionを別項目で公開する。
7. restart前後でbackend logをrotateし、終了traceとsignalを保持する。
8. regression testへ「異なるUser Messageのprefix cache測定」と「alias直送拒否」を追加する。

## 関連ソースファイル

- `mlx_vlm/server/openai.py:1307` - OpenAI requestの`model`を`get_cached_model()`へ渡す。
- `mlx_vlm/server/anthropic.py:1045` - token countでもrequestの`model`を同じloaderへ渡す。
- `mlx_vlm/server/app.py:423-434` - cache不一致時に既存modelを先にunloadする。
- `mlx_vlm/server/app.py:555-581` - ResponseGenerator、APC、model cache、Metal cacheを破棄する。
- incident時のMac compatibility checkout `340c5c3`にある
  `src/llm_server/chat_adapter.py:38-90` - 論理aliasの検証と物理modelへの変換。
- 同checkoutの`src/llm_server/http_handler.py:195-216` - passthroughが背後backendへraw bodyを転送する。
- `RenCrow_Qwen36_27B/scripts/health.sh` - BackendとProxyを分けたreadiness確認。
- `docs/調査/20260808_173823_Midori応答遅延とPrefix_Cache不成立.md` - 発端となった遅延調査とA/B結果。

## Phase 3 バイアス防止再点検

- [x] 確証バイアス: alias直送以外にmemory pressureとProxy変換不良を検証し、支持証拠のない原因を棄却した。
- [x] 反証: 元のMLX終了traceが失われているため、最終process終了理由は断定していない。
- [x] ライフサイクル: 正常model、誤request、unload、load失敗、接続拒否、restart、readyを追跡した。
- [x] 頻度制約: 誤直送2回と正規proxy経由の正常requestを区別した。
- [x] 既存知見: Prefix Cache報告およびLLM責務境界と矛盾しない。

## 証拠の限界

- 保存されたProxy logには、物理backend停止後の`ConnectionRefusedError: [Errno 61]`が残っている。
- 稼働観測ではMLX processが終了状態になったことを確認した。
- 一方、MLX backend logはllama.cpp切替時に置き換わり、終了直前のtraceback、exit code、signalは
  保持されていない。このため「load failureの後にprocessが終了した最終理由」は保留とする。
- 新たな停止を起こす再現実験は、復旧済み本番へ不要な影響を与えるため実施していない。

## 教訓（将来の調査への知見）

- logical aliasとphysical model identifierは同じ値として扱わない。
- Backendへ直接到達できることと、そのrequestが正規contractであることは別である。
- model loaderは、新modelの妥当性確認前に正常modelを破棄してはならない。
- health HTTP 200だけで、背後modelのreadinessやAgent E2E成功を代用しない。
- 診断中の副作用も障害として記録し、復旧証拠と証拠欠落を隠さない。
