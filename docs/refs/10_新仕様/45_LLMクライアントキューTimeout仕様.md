# LLM クライアントキュー / Timeout 仕様

## 目的

RenCrow から Chat / Worker / ChatWorker / Heavy / Wild へ推論を投げるとき、サーバ側 queue に任せるだけでは timeout 原因を切り分けできない。

この仕様は、RenCrow クライアント側で queue wait と generate 実行を分けて制御し、IdleChat 本文生成など UX 上の締切がある処理で timeout を診断可能にするための契約である。

## 問題

LLM サーバ側だけで queue される場合、RenCrow から見る timeout には次が混ざる。

```text
client queue wait
+ HTTP connect / request write
+ server queue wait
+ model generation
+ response read
```

この状態では、10 秒 timeout が発生しても次を区別できない。

```text
server queue wait 8s + generation 3s = timeout
generation 11s = timeout
client queue wait 9s + generation 2s = timeout
```

IdleChat の Mio / Chat は短い体感応答が必要であり、queue wait と generation elapsed を同じ timeout に混ぜてはいけない。

## 対象

対象は RenCrow 側から呼び出す LLM provider 全般である。

| 用途 | alias | 備考 |
| --- | --- | --- |
| Chat | `Chat` | Mio / 通常対話 / IdleChat Mio |
| Worker | `Worker` | Shiro / Worker Core |
| IdleChat Shiro | `ChatWorker` | Worker サーバ上の会話専用 alias |
| Heavy | `Heavy` | 高負荷推論 |
| Wild | `Wild` | 補助推論 |

## 基本方針

RenCrow 側は client-side queue を持つ。

- alias または endpoint/model 単位で同時実行数を制御する。
- queue wait timeout と generation timeout を別の計測値として扱う。
- queue wait が上限を超えた場合、HTTP request を送らず `phase=queue` の timeout として失敗させる。
- generation timeout は queue 獲得後、実際に provider へ渡す context にだけ適用する。
- server-side queue は最後の保険として許容するが、RenCrow 側の主要な流量制御として扱わない。
- fallback は成功扱いしない。timeout / busy / queue full はログと Viewer 診断で区別する。

## Timeout 分離

LLM 呼び出しは次の phase を持つ。

| phase | 内容 | timeout の意味 |
| --- | --- | --- |
| `queue` | RenCrow 側 semaphore / queue の獲得待ち | 実行枠を取れない待ち時間 |
| `generate` | provider へ HTTP request を送り、応答完了まで待つ | 実推論 + server-side wait + response read |
| `total` | queue 開始から response 完了まで | UX 上の総締切 |

`total` は観測値として必ず記録する。制御上は `queue_timeout` と `generation_timeout` を優先する。

## 推奨既定値

初期値は live 運用で調整可能にする。

| alias | queue_timeout | generation_timeout | total soft limit | 備考 |
| --- | ---: | ---: | ---: | --- |
| `Chat` | 1s | 10s | 12s | Mio / Chat 体感優先 |
| `ChatWorker` | 2s | 45s | 50s | IdleChat Shiro / topic generation |
| `Worker` | 5s | 120s | 130s | 通常 Worker |
| `Heavy` | 5s | 30s | 40s | 高負荷だが無制限にしない |
| `Wild` | 2s | 15s | 20s | 補助用途 |

`local_llm.timeout_sec` は Worker 系の長い generation timeout の既定に使ってよい。ただし `Chat` の体感 timeout と queue timeout を `local_llm.timeout_sec` だけで決めてはいけない。

## Queue policy

queue policy は用途別に選べるようにする。

| policy | 意味 | 用途 |
| --- | --- | --- |
| `wait` | queue_timeout まで待つ | 通常 |
| `reject` | 枠がなければ即失敗 | UI 即時応答が必要な診断 |
| `latest` | 古い pending を supersede し、最新だけ残す | STT など連続入力向け。LLM 本文生成では原則使わない |

IdleChat 本文生成は `wait` を基本とする。ただし `queue_timeout` を超えた場合は、その発話を生成失敗として扱い、無制限に後続発話を詰まらせない。

## ログ契約

LLM client queue は、最低限次のイベントをログに残す。

```text
llm.queue.wait.start provider=<name> alias=<alias> queue_policy=<policy>
llm.queue.wait.done provider=<name> alias=<alias> waited_ms=<n>
llm.queue.timeout provider=<name> alias=<alias> waited_ms=<n> timeout_ms=<n>
llm.generate.start provider=<name> alias=<alias> timeout_ms=<n> max_tokens=<n>
llm.generate.done provider=<name> alias=<alias> elapsed_ms=<n> finish=<reason> tokens=<n>
llm.generate.timeout provider=<name> alias=<alias> elapsed_ms=<n> timeout_ms=<n>
llm.generate.error provider=<name> alias=<alias> phase=<queue|generate> error=<summary>
```

ログには `session_id`、`job_id`、`route`、IdleChat の場合は `speaker`、`turn`、`topic_session_id` を可能な限り含める。

## IdleChat への適用

IdleChat 本文生成では、Mio と Shiro を分けて見る。

| speaker | alias | 要件 |
| --- | --- | --- |
| Mio | `Chat` | queue wait と generate elapsed を必ず分けて記録する。10 秒 timeout だけで失敗理由を判断しない。 |
| Shiro | `ChatWorker` | Worker 側 alias を使い、Chat の短い timeout に巻き込まない。 |

IdleChat のお題生成、本文生成、quality review、summary は同じ LLM provider 経由でも用途が異なるため、log label を分ける。

例:

```text
idlechat.topic.generate
idlechat.dialogue.generate
idlechat.dialogue.retry
idlechat.quality.review
idlechat.summary.generate
```

## 成功 / 失敗判定

成功条件:

- queue wait が timeout していない。
- generation が timeout していない。
- provider response が空ではない。
- finish reason が用途上許容される。
- sanitize 後の本文が invalid ではない。

失敗条件:

- `phase=queue` timeout
- `phase=generate` timeout
- HTTP / provider error
- empty content
- invalid response
- reasoning / prompt leak により unusable

失敗時の fallback や retry は回復経路であり、元の LLM 呼び出し成功として扱わない。

## Viewer / 診断表示

Viewer Ops / IdleChat 診断では、少なくとも直近 LLM 呼び出しについて次を見られるようにする。

- alias
- provider name
- phase
- queue_wait_ms
- generation_elapsed_ms
- total_elapsed_ms
- timeout_ms
- max_tokens
- finish_reason
- error summary

`/health` は provider 到達性の確認であり、queue timeout や実 generation latency の代替ではない。

## 実装境界

責務分担は次の通り。

| 層 | 責務 |
| --- | --- |
| `modules/llm` | alias 別 timeout / queue policy の計画値、既定値、validation |
| `internal/infrastructure/llm/middleware` | queue acquire、queue wait 計測、phase 分離ログ |
| `internal/infrastructure/llm/providers/*` | HTTP 実行。client-side queue policy は持たない |
| `cmd/rencrow` | live config から provider assembly へ値を渡す |
| `internal/application/idlechat` | speaker / turn / session の文脈を log label に渡す |
| `internal/adapter/viewer` | 診断表示。制御ロジックは持たない |

## テスト観点

最低限の local test:

- queue が詰まっているとき、`queue_timeout` で HTTP provider を呼ばずに失敗する。
- queue 待ち後に generation timeout が別 context で適用される。
- `phase=queue` と `phase=generate` の error が区別される。
- `model_concurrency=1` で同一 alias が並列実行されない。
- `global_concurrency` と alias 別 concurrency の両方が効く。
- IdleChat Mio の失敗ログに `speaker=mio`、`phase`、`waited_ms`、`elapsed_ms` が残る。

live 確認:

```bash
curl -fsS http://127.0.0.1:18790/health
curl -fsS http://127.0.0.1:18790/viewer/idlechat/status
curl -fsS 'http://127.0.0.1:18790/viewer/idlechat/logs?limit=50'
```

確認では、health OK だけで完了扱いにしない。実際の IdleChat セッションで、Mio / Shiro の本文生成が queue wait と generation elapsed に分離して記録されることを見る。

## 非目標

- LLM サーバ側 queue の実装を置き換えない。
- server-side batching を禁止しない。
- provider 固有の GPU / MLX scheduler 詳細を RenCrow の Application 層へ漏らさない。
- queue timeout を fallback 成功で隠さない。

