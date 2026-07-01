# RenCrow_CORE Ver0.80 モジュール化実装仕様

作成日: 2026-07-01
対象: `picoclaw_multiLLM`
位置づけ: `05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` を実装へ移すための正本実装仕様

## RenCrow_CORE Public repo 起点化の前提

この実装仕様は、`picoclaw_multiLLM` 現ブランチを RenCrow_CORE Ver0.80 の seed / staging source として整えるための手順である。

作業は既存 repository 内で行うが、完了時点で push 済み HEAD を新規 Public repository `RenCrow_CORE` の Ver0.80 起点として使う。

そのため、本文中の `PR` は、今回の作業では次のように読み替える。

| 既存表現 | 今回の読み替え |
| --- | --- |
| PR | 作業単位 / commit / push 済み HEAD |
| PR 説明 | 作業メモ / commit message / export log |
| 1 PR / 1 feature group | 1 作業単位 / 1 feature group |

RenCrow_CORE 起点化では、既存機能を削って軽くするのではなく、未整理機能も `modules/*`、`internal/features/*`、`internal/adapter/*`、または `legacy-body` として保持する。Public repo 化のための除外は、secret、local config、cache、artifact、private-only docs など公開不能物に限る。

実装に入る直前の作業資料は `docs/02_正本仕様/07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md` を参照する。特に `cmd/picoclaw` registrar 起点追加、Viewer Chat contract 固定、既存機能非削除チェックは同資料を使う。

Public repository `RenCrow_CORE` の初期投入条件、公開範囲、root README 要件、secret / artifact / local config 除外条件は `docs/02_正本仕様/08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md` を正本とする。

## 実装方針

Ver0.80 の実装は、機能を落とさず、挙動変更と構造変更を混ぜず、段階的に進める。

実装順は常に次とする。

```text
仕様確認
  -> 現在挙動の固定
  -> contract / test
  -> feature registrar / facade
  -> legacy adapter 接続
  -> 実体移動
  -> 統合テスト / 必要なら Viewer E2E
```

最初から大規模な `git mv` を行わない。移動前に呼び出し元を contract に寄せ、移動しても import と挙動が読める状態にする。

## 新人向け作業手順

新人が 1 feature を担当する場合は、次の順で作業する。

1. `docs/01_理解/01_システム概要.md` を読む。
2. `docs/01_理解/02_キャラクター・エージェント仕様.md` を読む。
3. `docs/02_正本仕様/05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` の Feature Module Catalog で担当 feature を探す。
4. `docs/調査/20260701_170923_RenCrow_CORE_Ver0.80_現状モジュール検証.md` で現在の集中点を確認する。
5. `modules/CURRENT_MAP.md` で現在の ownership を確認する。
6. 対象 feature について、入力、出力、副作用、永続化、ログ、エラー契約を書く。
7. 既存テストを確認し、足りない contract test を先に追加する。
8. `internal/features/<feature>` の facade / registrar / ports を作る。
9. `cmd/picoclaw` から feature 内部 policy を削り、registrar 呼び出しに寄せる。
10. package-local test、影響 package test、必要なら `go test ./...` を実行する。
11. Viewer / runtime が関係する場合は、実ブラウザまたは API / log evidence で対象フローを確認する。

## 事前チェックリスト

各 feature の着手前に、作業メモ、commit message、export log、または PR 説明へ次を記載する。

| 項目 | 必須内容 |
| --- | --- |
| Feature | 対象 feature 名 |
| 現在の主ファイル | `cmd/`, `internal/`, `modules/` の対象 |
| 仕様参照 | 正本仕様と関連 refs |
| 入力 | HTTP request、event、CLI args、config、store record など |
| 出力 | HTTP response、event、log、file、store record など |
| 副作用 | file edit、command、external API、background job、DB write など |
| 永続化 | JSONL、SQLite、workspace log、memory store など |
| ログ | job_id、session_id、route、status、error kind など |
| エラー契約 | non-2xx、visible-state error、fallback 禁止など |
| 触るファイル | 変更対象 |
| 触らないファイル | 関係ない feature / archive / provider |
| 先に固定する検証 | unit、contract、API、Viewer、live runtime |

この表が埋まらない feature は、実体移動に入らず、先に調査を行う。

## Viewer / LLM 実テストにおける状態・シーケンス・ログの区別

Viewer、通常 Chat、LLM、agent 切り替え、background job を実テストする場合は、`現在状態`、`シーケンス`、`ログ` を混同してはいけない。

特に `/viewer/send` の 200、`/viewer/jobs` の `running`、`/viewer/logs` の履歴、`/viewer/status` の agent state は、それぞれ意味が異なる。

| 区分 | 意味 | 正本 endpoint / record | 完了判定に使えるか |
| --- | --- | --- | --- |
| 受付結果 | HTTP request を Viewer が受け取ったか | `/viewer/send` response | 単独では不可 |
| 現在状態 | 今 agent / worker / coder が動いているか | `/viewer/status`, `/viewer/agents` | runtime busy / idle 判定に使う |
| シーケンス | 1 つの依頼が受付、分類、dispatch、応答、完了まで進んだか | `/viewer/jobs` の job 単位 `events`, `status`, `terminal_outcome` | 依頼単位の成否判定に使う |
| 応答結果 | LLM が実際に本文を返したか | job events の `agent.response` | 応答確認に使う |
| ログ | 過去に発生した event 履歴 | `/viewer/logs` | 追跡、原因調査、証跡に使う |

### 判定順序

実テストでは次の順に判断する。

1. `/viewer/status` と `/viewer/agents` で現在状態を見る。
2. 対象 request の識別子、合言葉、または `job_id` を決める。
3. `/viewer/send` の HTTP status で受付を確認する。
4. `/viewer/jobs` で対象 job のシーケンスを追う。
5. job が `done` / `error` / `failed` / `terminal_outcome` へ進んだかを見る。
6. `agent.response` の `from`、`to`、本文、`route`、`owner`、`job_id` を確認する。
7. 原因調査が必要な場合だけ `/viewer/logs` を見る。

`/viewer/logs` は現在状態の正本ではない。現在 agent が動いているかをログだけで判断してはいけない。

### `running` の扱い

`/viewer/jobs` に `status=running` が残っていても、ただちに「今も実行中」と報告してはいけない。

`running` を見つけた場合は、必ず次を分けて報告する。

| 観点 | 見る場所 | 判断 |
| --- | --- | --- |
| 現在実行中か | `/viewer/status`, `/viewer/agents` | agent state が `running` / `busy` か |
| シーケンス未終端か | `/viewer/jobs` | job が terminal state へ進んでいないか |
| 履歴上の失敗か | `/viewer/logs` | timeout、queue failure、viewer.error があるか |

現在状態が `offline` / `idle` で、job だけ `running` の場合は、次のように報告する。

```text
現在 agent は実行中ではない。
ただし対象 job sequence が terminal state へ進んでいない。
これは runtime 実行中ではなく、job lifecycle / jobs store の不整合として扱う。
```

### `to=mio|shiro|kuro|midori` 実テストの報告契約

Viewer 通常 Chat の recipient contract を実テストする場合は、少なくとも次を分けて報告する。

| 項目 | 必須確認 |
| --- | --- |
| request | `/viewer/send` payload の `to` と本文 |
| 受付 | HTTP status と response body |
| sequence | job `route`, `owner`, `status`, `terminal_outcome` |
| response | `agent.response` の `from`, `to`, 本文 |
| persona / character | 応答本文が `to` の persona と一致するか |
| identity | `owner` / `from` が `to` と一致する契約か、Mio 経由 persona 切替契約か |

`to=shiro|kuro|midori` が受け付けられても、job `owner` または `agent.response.from` が常に `mio` になる場合は、仕様上の意図を確認せずに正常扱いしない。

Ver0.80 の Viewer 通常 Chat では、`to=mio|shiro|kuro|midori` は route alias ではなく recipient / character selection contract として扱う。したがって、次のどちらであるかを実装と test で固定する必要がある。

| 方式 | 必須条件 |
| --- | --- |
| identity 切替 | job `owner`、`agent.response.from`、Viewer 表示上の speaker が `to` と一致する |
| Mio 経由 persona 切替 | job `owner` / `from` は `mio` のままでもよいが、sequence record に `requested_to` / `resolved_character` / `speaker` を明示する |

どちらの方式でも、黙って `mio` へ fallback して成功扱いしてはいけない。

### 短文・長文応答テストの追加ルール

短文と長文の応答確認では、各 request に一意な合言葉を入れる。

合言葉は次の確認に使う。

- request payload と job sequence の対応確認
- `message.received` と `agent.response` の対応確認
- 履歴混入、prompt 混線、古い context の混入検出

長文で合言葉が一致しない場合は、まずパラメータ確認として扱い、次の順に切り分ける。

1. `/viewer/send` に送った payload の `to` と本文。
2. `/viewer/jobs` の対象 job に `message.received` が残っているか。
3. route decision 後の resolved recipient / character。
4. prompt / context に過去の合言葉が混入していないか。
5. `agent.response` の本文が対象 request と一致するか。

この段階では「LLM 応答失敗」と断定しない。

## 作業時の判断補足

Ver0.80 の仕様は、`05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` が構造上の正本であり、この文書が実装手順の正本である。

作業時に迷いやすい点は、未解決仕様として扱わず、feature 着手時の判断点として次のように固定する。

| 判断点 | 固定方針 |
| --- | --- |
| `modules/*` へ昇格する範囲 | 純粋な contract、DTO、event、policy、state ownership として説明できるものだけを `modules/<id>` へ置く。既存実装の束ね、DI、HTTP handler、legacy adapter、provider 実行はまず `internal/features/<id>` または `internal/adapter/*` に置く。 |
| `internal/features/*` に置く範囲 | feature facade、ports、registrar、legacy 実装の束ね、feature 単位の依存注入を置く。実装本体を重複保持せず、将来 `modules/*` へ昇格する contract 候補を README と ports で見えるようにする。 |
| Viewer 通常チャットの `to` 契約 | 新経路では `to=mio|shiro|kuro|midori` を contract test で固定する。`model_alias`、`route_prefix`、旧 route alias は legacy と明示して隔離する。`to=shiro` を Worker / OPS 実行 route と混同しない。 |
| `cmd/picoclaw` の分割順 | 一度に分割せず、Viewer、IdleChat、Ops、Voice、Web、Knowledge / Memory、Governance など feature group ごとに registrar へ寄せる。registrar は route 登録と dependency handoff に限定し、新しい巨大 `manager` を作らない。 |
| 実体移動の開始条件 | feature README、contract test、facade / ports、呼び出し元の facade / contract 依存、package-local test が揃うまで `git mv` に入らない。 |

したがって、各 feature の開始時に必要なのは「仕様を追加で作ること」ではなく、上記の判断点を対象 feature の入力、出力、副作用、永続化、ログ、エラー契約へ具体化することである。

## Phase 0: 仕様入口の固定

目的:

- Ver0.80 の構成仕様と実装仕様を正本 docs へ置く。
- 古い 5 Agent 構成、旧 docs path、旧 Coder 対応のズレを作業前に見える状態にする。

作業:

- `docs/02_正本仕様/05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` を作成する。
- `docs/02_正本仕様/06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md` を作成する。
- `docs/README.md` と `docs/00_読む順番.md` へ Ver0.80 仕様を追加する。
- `docs/refs/キャラクター仕様/` は `Aka/Coder1`, `Ao/Coder2`, `Gin/Coder3`, `Kin/Coder4` へ揃える。

完了条件:

- Ver0.80 の仕様と実装仕様が docs から辿れる。
- `docs/refs/キャラクター仕様` に旧 Coder 対応の記述が残っていない。
- 実装変更は行っていないことを報告する。

## Phase 1: modules contract の棚卸し

目的:

- 現存する `modules/*` を全て contract 対象として扱う。
- `modules/README.md` と `modules/core/manifest.go` が実体と一致するようにする。

対象:

- `modules/core`
- `modules/chat`
- `modules/worker`
- `modules/llm`
- `modules/tts`
- `modules/stt`
- `modules/voicechat`
- `modules/browseractor`
- `modules/webgather`

作業:

1. `modules/README.md` に現存 module を全て列挙する。
2. README がない module には README を追加する。
3. `modules/core/manifest.go` の descriptors に不足 module を追加するか、未公開 module として明示する。
4. `modules/dependency_rules_test.go` を現存 module 全体に拡張する。
5. `modules/*` が `cmd/*` と `internal/*` を import していないことを確認する。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/...
```

完了条件:

- 全 `modules/*` に README がある。
- dependency test が現存 module 全体を対象にする。
- `modules/README.md` と `find modules -maxdepth 1 -type d` の差が説明できる。

## Phase 2: Feature inventory と registrar 雛形

目的:

- `cmd/picoclaw` と `internal/adapter/viewer` に散った feature を棚卸しし、feature registrar へ寄せる準備をする。

作業:

1. `internal/features/` を作成する。
2. 最初は実装を移動せず、以下の feature に空でない `README.md` と `ports.go` / `registrar.go` の雛形を置く。
   - `core`
   - `agent`
   - `chat`
   - `worker`
   - `idlechat`
   - `viewer`
   - `llm`
   - `tts`
   - `stt`
   - `voice`
   - `avatar`
   - `backlog`
   - `heartbeat`
   - `scheduler`
   - `workstream`
   - `revenue`
   - `repair`
   - `web`
   - `source`
   - `knowledge`
   - `memory`
   - `reports`
   - `security`
   - `sandbox`
   - `governance`
   - `superagent`
   - `aiworkflow`
   - `distributed`
   - `channels`
   - `ops`
3. 各 README に、入力、出力、副作用、永続化、ログ、エラー契約、現在の主ファイルを書く。
4. `cmd/picoclaw` へ feature 固有 policy を追加しないため、既存 route / dependency を feature 別に分類する。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/adapter/viewer ./modules/...
```

完了条件:

- feature 一覧に抜けがない。
- 新しい `internal/features/*` は facade / registrar の入口だけで、実装本体を重複保持しない。
- `cmd/picoclaw` の責務が composition root として説明できる。

## Phase 3: Viewer Chat contract の固定

目的:

- Viewer 通常チャットの `to=mio|shiro|kuro|midori` 契約を先にテストで固定する。
- `model_alias` / `route_prefix` は legacy 互換として隔離する。

対象:

- `modules/chat`
- `internal/adapter/viewer/handler_send.go`
- `internal/application/orchestrator`
- `cmd/picoclaw` の Viewer send route
- Viewer JS send payload

作業:

1. `modules/chat` に recipient contract を追加する。
2. `to` の許可値、既定値、明示 command 優先順をテストする。
3. Viewer send payload の test で `model_alias` / `route_prefix` を新経路に含めないことを確認する。
4. Orchestrator は `to` を Chat 相手として解決し、route と混同しない。
5. legacy alias を受ける必要がある場合は、明示的に `legacy` package / function 名を付ける。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/chat ./internal/adapter/viewer ./internal/application/orchestrator ./cmd/picoclaw
```

Viewer を触る場合は、最低 1 セッションで送信、応答、Timeline event、error 表示を確認する。

完了条件:

- `to=mio|shiro|kuro|midori` の contract test がある。
- `to=shiro` と `OPS` 実行 route が混同されない。
- 対象 runtime 不可時に Mio へ黙って fallback しない。

## Phase 4: `cmd/picoclaw` registrar 分割

目的:

- `cmd/picoclaw/routes.go` と `runtime_dependencies.go` に集まった feature 配線を、feature registrar 呼び出しへ分ける。

作業順:

1. `viewer` base route と module route を分ける。
2. `idlechat` route / background start を `internal/features/idlechat` へ寄せる。
3. `backlog` / `heartbeat` / `scheduler` / `workstream` / `revenue` を Ops feature group として分ける。
4. `voice` / `stt` / `tts` / `voicechat` を音声 feature group として分ける。
5. `web` / `browseractor` / `webgather` / `browsertrace` を Web feature group として分ける。
6. `knowledge` / `memory` / `source` を知識 feature group として分ける。
7. `governance` / `sandbox` / `security` / `reports` / `superagent` / `aiworkflow` を運用 feature group として分ける。

現ブランチの Ver0.80 seed では、Phase 4 の HTTP route registrar handoff は完了状態として扱う。`cmd/picoclaw` は feature group ごとの wrapper を残すが、実 route 登録は `internal/features/*/registrar.go` が所有する。`security` と `distributed` は直接 HTTP route を持たないため、README で所有境界を記録する。

ルール:

- 1 作業単位 / 1 feature group にする。
- `cmd/picoclaw` から削った policy を別の巨大 `manager` へ移さない。
- registrar は route 登録と dependency handoff だけを行う。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...
```

広範囲に触れた場合:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./...
GOCACHE=/tmp/picoclaw-gocache go vet ./...
```

完了条件:

- `cmd/picoclaw` は feature registrar を呼ぶだけに近づく。
- feature 固有 handler / store / policy の所在が README で追える。
- route 登録漏れが module / feature test で検出される。

## Phase 5: IdleChat feature 化

目的:

- IdleChat を通常 Chat、TTS、Viewer、LLM provider から疎結合にする。

作業:

1. `modules/idlechat` を追加するか、まず `internal/features/idlechat` で ports を定義する。
2. `SessionPort`, `TopicPort`, `SpeakerLLMPort`, `TTSPort`, `ViewerEventPort`, `StopPort` を分ける。
3. `internal/application/idlechat` の orchestration を facade 越しに呼ぶ。
4. TTS trigger と表示本文を同じ値として扱わない。
5. `IdleChat` 中は外部検索禁止、Heavy / Wild 稼働中の割り込み禁止を contract に入れる。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/idlechat ./modules/chat ./modules/tts ./cmd/picoclaw
```

Viewer / 音声を触る場合は、最低 1 セッションで timeline、raw/view/audio trigger、終了状態を確認する。

完了条件:

- IdleChat の入力、出力、副作用、永続化、ログ、エラー契約が README にある。
- fallback / invalid response を成功扱いしない。
- 通常 Chat STT 入力と IdleChat が混ざらない。

## Phase 6: Ops loop feature 化

目的:

- Backlog、Heartbeat、Scheduler、Workstream、Revenue を、Viewer 表示ではなく運用 loop として整理する。

作業:

1. `internal/features/backlog` は backlog item と intake runner を所有する。
2. `internal/features/heartbeat` は heartbeat due run と draft report 起動を所有する。
3. `internal/features/scheduler` は in-app scheduler の due job と run log を所有する。
4. `internal/features/workstream` は goal、artifact、steering、vault update を所有する。
5. `internal/features/revenue` は daily routine、draft、human decision gate を所有する。
6. Viewer handler は visible state と操作 API に限定する。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/heartbeat ./internal/application/scheduler ./internal/application/revenue ./cmd/picoclaw ./internal/adapter/viewer
```

完了条件:

- Backlog と Heartbeat と Workstream の owner が分かれる。
- Heartbeat は Workstream / Revenue を起動できるが、それらの state owner にならない。
- Viewer は status API failure を stale 成功として表示しない。

## Phase 7: Web / Knowledge / Memory feature 化

目的:

- 外部情報の discovery、source read、browser evidence、Source Registry、Knowledge、Memory を混同しない。

作業:

1. `modules/browseractor` と `modules/webgather` に README と依存テストを追加する。
2. `internal/features/web` は BrowserActor / WebGather / Webwright / BrowserTrace の入口を束ねる。
3. `internal/features/source` は Source Registry と source fetcher を所有する。
4. `internal/features/knowledge` は import / wiki index / vocabulary / glossary を所有する。
5. `internal/features/memory` は observed / candidate / validated / promoted の state transition を所有する。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/browseractor ./modules/webgather ./internal/application/webgather ./internal/application/sourcefetcher ./internal/application/knowledge ./internal/application/knowledgememory
```

完了条件:

- search result だけで Knowledge / Memory へ昇格しない。
- Source Registry の review 境界が残る。
- Memory prompt injection と保存 state が分かれる。

## Phase 8: Voice / Audio / Avatar feature 化

目的:

- STT、TTS、VoiceChat、AudioRouter、VTuber / Live2D を音声領域として見通せるが、入力、出力、会話経路、表示を混同しない。

作業:

1. `modules/voicechat` に README と manifest / dependency test を追加する。
2. `internal/features/voice` は VoiceChat / VDS / AudioRouter の route と ports を束ねる。
3. STT provider state と Viewer microphone state を分ける。
4. TTS provider state と playback ACK / active audio owner を分ける。
5. Avatar は emotion / lipsync trigger を扱い、表示本文を所有しない。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/stt ./modules/tts ./modules/voicechat ./cmd/picoclaw ./internal/infrastructure/stt ./internal/infrastructure/tts
```

ブラウザを触る場合は、mic capture、transcript final、TTS playback、ACK、lipsync trigger を分けて確認する。

完了条件:

- STT/TTS/VoiceChat/AudioRouter/VDS の責務が README に分かれている。
- 音声 chunk を本文表示の唯一根拠にしない。
- provider timeout と Viewer 表示不具合を混同しない。

## Phase 9: 実体移動

目的:

- contract と registrar が安定した feature だけ、実体ファイルを移動する。

移動条件:

- feature README がある。
- contract test がある。
- facade / ports がある。
- 呼び出し元が内部実装ではなく facade / contract に依存している。
- package-local test が通る。

移動ルール:

- 1 回の差分で 1 feature だけ移動する。
- `git mv` を使い、削除と再作成に見せない。
- import 修正と挙動変更を混ぜない。
- 移動後、古い package に互換 wrapper を残すか、呼び出し元を全て facade に寄せる。
- archive 文書や旧 docs を編集しない。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./...
GOCACHE=/tmp/picoclaw-gocache go vet ./...
```

Viewer / runtime が関係する場合は、対象 route の API response と最低 1 つの実フローを確認する。

## Phase 10: RenCrow_CORE Public repo 起点化

目的:

- push 済み HEAD を、新規 Public repository `RenCrow_CORE` の Ver0.80 起点として使える状態にする。
- 既存機能を削らず、公開不能物だけを除外する。
- Ver0.80 の module tree、feature catalog、実行手順、未移行領域を初期 README から辿れるようにする。

作業:

1. `picoclaw_multiLLM` 現ブランチで Ver0.80 の構成変更、検証、commit、push を完了する。
2. `docs/02_正本仕様/05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` とこの文書が HEAD と矛盾していないことを確認する。
3. `modules/README.md`、`modules/CURRENT_MAP.md`、`internal/features/README.md` が現状の module / feature 一覧を説明していることを確認する。
4. `docs/02_正本仕様/08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md` に従い、root `README.md`、公開範囲、license / attribution、未移行領域の説明を確認する。
5. 公開除外候補を洗い出す。
   - secret / token / API key
   - local config
   - runtime cache
   - generated artifact
   - private-only docs
   - user-specific logs
6. `RenCrow_CORE` 初期 README に次を置く。
   - Ver0.80 の目的
   - module tree
   - Feature Module Catalog
   - build / test / run の最小手順
   - 既存機能を削らず `legacy-body` として保持している領域
   - 未移行 feature と次の移行順
7. 公開 repository に投入する前に、secret scan、large artifact check、license / attribution check を行う。
8. 新規 Public repository `RenCrow_CORE` に Ver0.80 初期状態として投入する。
9. 投入後、clone した公開 repo で最低限の module contract test と build/test 手順の再現性を確認する。

検証:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/...
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...
git diff --check
```

公開 repo 投入前に追加で確認する。

```bash
git status --short
git log -1 --oneline
```

完了条件:

- `picoclaw_multiLLM` の push 済み HEAD が `RenCrow_CORE` Ver0.80 の起点として説明できる。
- Feature Module Catalog にある既存機能が削除されていない。
- 公開不能物が `RenCrow_CORE` に入らない。
- `RenCrow_CORE` の初期 README から module tree、実装仕様、未移行領域、代表テストへ辿れる。

## 新規 feature 追加時のテンプレート

新しい feature は次の最小構成で追加する。

```text
modules/<feature>/
  README.md
  contracts.go
  *_test.go

internal/features/<feature>/
  README.md
  ports.go
  registrar.go
  facade.go
```

`modules/<feature>/README.md` には次を書く。

```text
# <feature>

## Owner

## Inputs

## Outputs

## Side Effects

## Persistence

## Logs

## Error Contract

## Dependencies

## Tests
```

## 代表テストコマンド

| 範囲 | コマンド |
| --- | --- |
| module contract | `GOCACHE=/tmp/picoclaw-gocache go test ./modules/...` |
| composition root | `GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw` |
| Viewer adapter | `GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/viewer` |
| Chat / Orchestrator | `GOCACHE=/tmp/picoclaw-gocache go test ./modules/chat ./internal/application/orchestrator` |
| IdleChat | `GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/idlechat ./modules/chat ./modules/tts` |
| Worker | `GOCACHE=/tmp/picoclaw-gocache go test ./modules/worker ./internal/application/service` |
| Audio | `GOCACHE=/tmp/picoclaw-gocache go test ./modules/stt ./modules/tts ./modules/voicechat` |
| 全体 | `GOCACHE=/tmp/picoclaw-gocache go test ./...` |
| vet | `GOCACHE=/tmp/picoclaw-gocache go vet ./...` |

## 完了報告に含めること

各段階の完了報告には次を含める。

- 対象 feature
- 変更したファイル
- 変更した責務
- 実行したテスト
- 実ブラウザ / live runtime 確認の有無
- 機能を落としていない根拠
- 残る legacy 依存
- 次に移動できるファイル
