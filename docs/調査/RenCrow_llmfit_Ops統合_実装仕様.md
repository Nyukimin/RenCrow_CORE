# RenCrow llmfit Ops統合 実装仕様

Version: 0.1
Status: Proposed
対象: RenCrow CORE / Observability / Viewer Ops
外部依存: llmfit

本書は実装仕様である。

機能仕様（Part A）の正本は`../02_機能仕様.md`の「LLM Ops / Hardware Capability（llmfit統合）」章とし、本書はその実装方法だけを定める。仕様と本書が衝突した場合は正本を優先し、本書を修正する。

Domain構造体の定義（3章）は本書を正本とし、`02_機能仕様.md`側には記述しない。

## 前提条件: llmfitの導入

llmfitはRust製CLI（単一binary）であり、配布元はGitHub `https://github.com/AlexsJones/llmfit`、公式サイト `https://www.llmfit.org/`、配布経路はcrates.io／Homebrew／公式install scriptである。
導入手段は`brew install llmfit`、`cargo install llmfit`、または公式install scriptとする。
llmfitはRenCrowシステムへ必ず導入し、各LLM実行ノードおよびCORE hostの標準導入手順に含める。未導入は構成不備として扱う。
ただしruntimeとしてはfail-safeであり、未導入・停止・応答不能時もCOREは停止せず、LLM Ops画面だけを`OFFLINE`／`STALE`へ縮退させる。
位置付けと標準構成の例外記録は`../04_アーキテクチャ概要.md`の「標準Go配布境界との関係」、導入確認手順は`../05_設定リファレンス.md`の「LLM Ops / llmfit 設定」を正本とする。

---

# 1. 実装方針

新しい巨大moduleを作らない。

llmfitはHardware / Model適合度を観測する外部sensorであり、RenCrowはその観測結果を取り込み、既存Observability / Viewer Opsへ投影する。

層構成:

```text
external llmfit
  -> infra adapter          (CLI / HTTP client、DTO、mapper)
  -> application service    (CapabilityService、cache、node集約)
  -> domain model           (NodeHardwareProfile、ModelFitAssessment)
  -> existing observability (Event、Read Model)
  -> viewer                 (Ops画面のHardware / Model Fit)
```

依存方向は上から下への一方向とし、domainがinfraやllmfit DTOを参照しない。

既存の機構（Event、Read Model、Config loader、HTTP client、Viewer router、Ops画面）を再利用し、同等物を新設しない。

---

# 2. 推奨配置

最終配置は既存Repository調査（15章）の後に決定する。

概念配置:

```text
internal/
  domain/llmops/        hardware.go, model_fit.go
  application/llmops/   capability_service.go
  infra/llmfit/         client.go, cli_client.go, http_client.go, dto.go, mapper.go
  adapter/viewer/       llmops_handler.go
```

既存RenCrowに同等のdomain / service / client / viewer handlerが存在する場合は、新規階層を作らず既存構造へ合わせる。

着手時点で確認済みの事実:

```text
internal/infrastructure/   が実在する（上記の infra/ はこの配下へ読み替える）
internal/features/ops/     が実在する（README.md, ports.go, registrar.go）
internal/adapter/viewer/   が実在する（*_handler.go 群）
```

したがって実配置は`internal/infrastructure/llmfit/`となる見込みであり、Viewerへの配線は既存`internal/features/ops/`のregistrar / portsを経由する候補を最初に検討する。

一度に大規模なdirectory新設をしない。

---

# 3. Domain Model

本章の構造体定義は本書が正本である。

```go
type NodeHardwareProfile struct {
    NodeID       string
    NodeName     string
    OS           string

    CPUName      string
    CPUCores     int

    TotalRAMGB     float64
    AvailableRAMGB float64

    HasGPU       bool
    GPUCount     int
    GPUs         []GPUProfile

    UnifiedMemory bool
    Backend       string

    CollectedAt time.Time
    Source      string
}

type GPUProfile struct {
    Name               string
    VRAMGB             float64
    AvailableVRAMGB    *float64
    MemoryBandwidthGBs float64
    Count              int
}

type ModelFitAssessment struct {
    NodeID    string
    ModelID   string
    Provider  string

    ParameterCount string
    ParamsB        float64
    IsMoE          bool

    FitLevel string
    Score    float64

    QualityScore float64
    SpeedScore   float64
    FitScore     float64
    ContextScore float64

    Runtime string
    RunMode string

    BestQuant *string

    ContextLength          int
    UsableContext          int
    EffectiveContextLength int

    MemoryRequiredGB  float64
    MemoryAvailableGB float64
    UtilizationPct    float64

    EstimatedTPS *float64
    MeasuredTPS  *float64

    PrefillTPS *float64
    TTFTMs     *float64

    EstimateConfidence string

    Installed    bool
    DiskSizeGB   *float64
    Capabilities []string
    License      string

    Notes []string

    CollectedAt time.Time
}
```

## 値の意味

`Source`は観測元を示し、Phase 1では`llmfit`固定とする。

`Backend`はllmfitが報告する推論backend（例: `metal`、`cuda`、`vulkan`、`cpu`）をそのまま文字列で保持する。RenCrow側で列挙型へ固定しない。

`FitLevel`はllmfitの適合度区分（例: `perfect`、`good`、`marginal`、`too_tight`、`unsupported`）を文字列で保持する。

`EstimateConfidence`の想定値:

```text
measured_local      同一Nodeでの実測値に基づく
measured_community  同等構成の共有実測値に基づく
calibrated          実測で補正された推定値
estimated           純粋な推定値
unsupported         推定不能
```

## Pointer fieldの扱い

`BestQuant`、`EstimatedTPS`、`MeasuredTPS`、`PrefillTPS`、`TTFTMs`、`DiskSizeGB`、`GPUProfile.AvailableVRAMGB`はpointerとする。

`GPUProfile.AvailableVRAMGB`のnilはllmfitがそのGPU entryの空きVRAMを返さない（不明）ことを表し、`&0`（実測ゼロ）と区別する。Viewerはnilのとき空きVRAMの数値を出さない。

llmfitが値を返さない場合はnilを保持する。ゼロ値（`0`、`""`）と未取得を混同しない。

Viewerはnilを「未取得」として表示し、`0 tok/s`と表示しない。

Domain packageはllmfitのJSON field名やDTOをimportしない。

---

# 4. Adapter Interface

```go
type CapabilityProvider interface {
    Health(ctx context.Context) error
    System(ctx context.Context) (*NodeHardwareProfile, error)
    TopModels(ctx context.Context, query ModelFitQuery) ([]ModelFitAssessment, error)
    SearchModels(ctx context.Context, query ModelFitQuery) ([]ModelFitAssessment, error)
}
```

`ModelFitQuery`は概念的に以下を持つ。

```text
Limit       int
MinFit      string   (例: marginal)
UseCase     string   (例: coding)
Runtime     string   (例: llama.cpp)
MaxContext  int
Sort        string   (例: score)
Keyword     string   (SearchModelsのみ)
```

このinterfaceにより、以下を交換可能にする。

```text
CLIProvider   同一host上のllmfit executableを呼ぶ
HTTPProvider  remote nodeのllmfit HTTP serverを呼ぶ
MockProvider  test用。固定JSON / 固定errorを返す
```

CapabilityServiceはProviderの種類を知らず、interfaceだけに依存する。

---

# 5. CLI Provider

同一hostでは最小構成として採用可能。

```text
System:
  exec.CommandContext(ctx, "llmfit", "--json", "system")

Model Fit:
  exec.CommandContext(ctx, "llmfit", "recommend", "--json", "--limit", "20")
```

注意事項:

```text
timeout必須                  ctxにdeadlineを付け、無期限に待たない
stdoutのみJSONとして読む     stderrはJSON parseの対象にしない
stderrをUIへ直接流さない     logへ記録し、Viewerには要約errorだけ返す
shell経由で実行しない        sh -c / cmd /c を使わない
引数を無検証で渡さない       ユーザー入力（use_case等）は許可値へ正規化してから渡す
executable未検出を明示する   exec.LookPath失敗をProvider errorとして返す
```

`ModelFitQuery`からCLI引数への変換はmapperが担い、許可されたoption名と値だけを組み立てる。

---

# 6. HTTP Provider

remote nodeでは推奨。

```text
GET http://node:8787/health
GET http://node:8787/api/v1/system
GET http://node:8787/api/v1/models/top
```

Query例:

```text
limit=20
min_fit=marginal
use_case=coding
sort=score
```

## DTOとmapper

llmfit DTOをそのままDomainへ露出しない。

```text
llmfit JSON -> dto (infra) -> mapper -> ModelFitAssessment / NodeHardwareProfile
```

変換は一方向とし、DomainからDTOへ戻す経路を作らない。

llmfit側のfield追加・改名はdtoとmapperの修正だけで吸収し、Domain・Service・Viewerへ波及させない。

## 失敗の扱い

```text
connection refused   Node offline として扱う
HTTP timeout         Node offline として扱う
HTTP 4xx             Provider設定またはquery不正。errorを返し、cacheを更新しない
HTTP 5xx             llmfit側障害。errorを返し、直前のcacheを保持する
invalid JSON         errorを返し、直前のcacheを保持する
partial response     必須fieldが欠けた要素だけ除外し、除外件数をNotes / logへ残す
```

既存CORE HTTP client（timeout、retry、logging）があればそれを使い、新規HTTP clientを作らない。

---

# 7. Node Registry

接続先をcodeへ埋め込まない。

設定例:

```yaml
llm_capability:
  llmfit:
    enabled: true
    system_ttl: 5m
    models_ttl: 15m
    nodes:
      - id: node-a
        mode: http
        endpoint: http://192.168.x.x:8787
      - id: node-b
        mode: http
        endpoint: http://192.168.x.x:8787
      - id: local
        mode: cli
```

`mode`は`http`または`cli`。`cli`ではendpointを持たない。

IP、名称、実際の設定場所は既存RenCrowのNode / Backend Registryに合わせる。

既存Registryがある場合は新設せず、既存Node定義へ`llmfit`項目を追加する形とする。上記YAMLは概念例であり、section名は既存Config loaderの命名に従う。

secret（認証token等）が必要になった場合はsource / docs / logへ置かず、既存secret管理に従う。

---

# 8. Cache

Viewer requestごとにllmfitへ問い合わせない。

```text
Viewer -> CapabilityService -> Memory Cache -> (miss / expired) -> llmfit Provider
```

cache key例:

```text
system:{node_id}
models:{node_id}:{use_case}:{runtime}:{max_context}
```

TTL:

```text
system  5分
models  15分
```

Manual Refresh（9章の`POST refresh`）時のみ強制更新可能とする。

Provider失敗時は直前のcacheを保持し、`stale`として返す。cacheが無い場合は`offline`として返す。

既存COREにmemory cache機構があればそれを使い、新規cache実装を作らない。

---

# 9. Viewer API

既存Viewer Routerは`{id}` path patternを使わず、`/viewer/agent/detail?agent_id=`のようにquery parameterで対象を指定する。本APIも同じ流儀で実装済み（`internal/adapter/viewer/llmops_handler.go`、route登録は`internal/features/llmops/registrar.go`）。

```text
GET  /viewer/llm-ops/nodes                                   全Nodeの概要（Node Card用）
GET  /viewer/llm-ops/node?node_id={node_id}                  1Nodeの詳細（Hardware Profile）
GET  /viewer/llm-ops/node/models?node_id={node_id}           1NodeのModel Fit一覧
GET  /viewer/llm-ops/model/matrix?model_id={model_id}        1Modelの全Node適合表
POST /viewer/llm-ops/refresh                                 cache強制更新
```

`node/models`の任意query: `limit`（1..200、省略時は`llm_capability.llmfit.top_limit`）、`use_case`、`runtime`、`min_fit`、`max_context`。値は4章の許可値へ検証し、許可値以外は400を返してProviderへ渡さない。

`POST refresh`は再取得のみを行う。llmfitの設定変更、model load、Benchmark実行を行わない。

`POST refresh`はbodyで対象を絞れる。

```json
{
  "node_id": "node-a"
}
```

`node_id`省略時は全Nodeを対象とする。応答は`{"generated_at","refreshed":[...],"failed":{node_id: reason}}`。

応答には各Nodeの`status`（`online` / `offline` / `stale` / `disabled`）と`collected_at`（RFC3339またはnull）を必ず含める。`disabled`は`llm_capability.llmfit.enabled: false`のときの値で、Providerを呼ばない。

error形式は既存Viewer APIと同じ（`http.Error`によるplain text）。`node_id`／`model_id`未指定は400、Node不明は404、method不一致は405、service未配線は503。一覧はnil sliceを`[]`へ正規化して返す。response field名の正本は`../06_Public_API仕様.md`の「LLM Ops Viewer API」。

---

# 10. Observability Event

新しいEvent Systemを作らない。既存Eventへ必要な情報を追加する。

追加候補（概念）:

```text
capability_refresh          refresh成功
capability_refresh_failed   refresh失敗
benchmark_observed          実測値の取り込み（Phase 2）
```

実装前に既存Event kindで表現可能か確認し、可能なら新kindを追加しない。

payload例:

```json
{
  "node_id": "node-a",
  "source": "llmfit",
  "model_count": 20,
  "duration_ms": 82,
  "status": "ok"
}
```

全modelの巨大JSONをEvent Logへ保存しない。

Eventは何が起きたかを残し、実data（Hardware Profile、Model Fit一覧）はRead Model側に保持する。

---

# 11. UI構成

既存ViewerのOps / Observability画面へHardwareとModel Fitを追加する。

新しい独立appを作らない。

構成案:

```text
案A  Ops配下
     Agents / Tasks / Models / Hardware / Model Fit

案B  LLM Ops配下
     Runtime / Hardware / Models / Bench
```

既存画面構成（`js/tabs/ops.js`、`js/tabs/to-be-ops.js`、`css/tabs/ops.css`）を調査し、既存tab構造へ合わせて選ぶ。

---

# 12. UI表示優先順位

初期実装の範囲:

## Node Card

```text
Name
Online / Offline / Stale
GPU
VRAM
RAM
Backend
Last Updated
```

## Model Fit Table

```text
Model
Node
Fit
Quant
Runtime
Usable Context
Estimated TPS
Measured TPS
Confidence
```

## Detail

```text
4 Scores        Quality / Speed / Fit / Context
Memory          Required / Available / Utilization
Context         Context Length / Usable / Effective
Estimate Basis  EstimateConfidence
Notes
```

chartやgraphはPhase 1では不要。

nil値は「未取得」と表示し、数値`0`と区別する。

---

# 13. テスト

## Unit

```text
llmfit JSON -> DTO
DTO -> Domain変換
null handling
best_quant = null
measured_tps = null
multi GPU
unified memory
unsupported estimate
```

## Adapter

```text
llmfit executable not found
command timeout
invalid JSON
HTTP timeout
HTTP 400
HTTP 500
connection refused
partial response
```

## Application

```text
cache hit
cache expire
manual refresh
1 node failure
multiple node partial failure
```

## Viewer

```text
online node
offline node
stale node
estimated only
measured available
model not runnable
```

Adapter testはMockProviderと固定JSON fixtureで行い、実llmfitへの接続を必要としない。

実llmfit・実Nodeを通すE2Eは本番同等の設定・認証・routeで別途実施し、unit / adapter testの成功で代用しない。

---

# 14. Phase分割

```text
Phase 1  最小導入
  llmfit Adapter
  Hardware Profile
  Top Model Fit
  Cache
  Ops Node Card
  Model Fit Table
  Manual Refresh
  自動制御はしない

Phase 2  実測統合
  llmfit bench結果の取り込み
  RenCrow側bench結果の取り込み
  Estimate vs Actual
  履歴

Phase 3  配置判断支援
  Recommended Node / Runtime / Quant
  Expected Context
  Expected TPS
  自動配置はしない

Phase 4  Scheduler連携
  Task requirements -> Capability Matrix -> Node candidate -> Scheduler
  別仕様とする
```

各Phaseは独立したImplementation Unitとし、前Phaseの完了条件を満たしてから着手する。

---

# 15. 実装前調査

実装開始前に確認する項目:

```text
現在のViewer route
現在のLLM Ops画面
Node / Backend Registry
Event Schema
Observability Read Model
Cache機構
Config loader
既存HTTP Client
既存CLI executor
Viewer frontend構造
```

新規実装より既存機構の再利用を優先する。

着手時点で確認済みの事実: 現COREには`internal/features/ops/`（`README.md` / `ports.go` / `registrar.go`）とViewer assetsの`js/tabs/ops.js`、`js/tabs/to-be-ops.js`、`css/tabs/ops.css`が存在する。infra層の実directory名は`internal/infrastructure/`である。

---

# 16. 実装完了条件

Phase 1完了時:

```text
複数Node（Mac、RX6800系Node、RTX系Nodeなど）の状態が同一Ops画面に表示可能
任意のmodelについて以下を画面だけで判断可能
  どのNodeで
  どのRuntimeで
  どのQuantで
  どれだけContextが使え
  およそ何tok/sで
  その数字をどの程度信用できるか（EstimateConfidence）
Manual Refreshが再取得のみを行う
Node offline / stale が表示上区別される
13章のUnit / Adapter / Application / Viewer testが成功
```

以上が揃った時だけllmfit Ops統合 Phase 1を完成とする。

---

# 17. 設計思想

本機能はRenCrow自身がHardware適合度計算を抱えるための機能ではない。

```text
llmfit   = Capability Sensor（観測・推定を行う）
RenCrow  = Capabilityを理解して利用するSystem（観測結果を表示・判断・配置に使う）
```

この責務境界を維持する。

適合度計算式、量子化推奨、context推定をRenCrow側へ再実装しない。llmfitが返せない値はnilとして扱い、RenCrow側で補完計算しない。
