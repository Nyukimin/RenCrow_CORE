# RenCrow Atlas / Backlog / Implementation Lifecycle 実装仕様

## 1. 実装方針

既存Backlog実装を破棄せず、Schema v2へ拡張する。

現行COREにはすでに、

```text
internal/features/backlog
internal/domain/backlog
internal/adapter/viewer/backlog_handler.go
```

が存在する。

既存の`BacklogStore`、`/viewer/backlog`互換を維持しながら、Atlas用projectionとImplementation Lifecycleを追加する。

新しい物理databaseは作成しない。

---

# 2. 実装配置

## RenCrow_CORE

```text
internal/domain/backlog/
  types.go
  state_machine.go
  validation.go

internal/application/backlog/
  service.go
  implementation_service.go
  evidence_gate.go
  lease.go

internal/features/backlog/
  README.md
  ports.go
  registrar.go
  catalog/
    atlas_catalog.json

internal/infrastructure/backlog/
  jsonl_store.go

internal/adapter/viewer/
  backlog_handler.go
  atlas_handler.go

cmd/rencrow/
  runtime_dependencies.go
```

既存`BacklogStore`のI/O実装は、契約test追加後に`internal/infrastructure/backlog`へ移す。

一度に大規模renameしない。

---

# 3. Static Atlas Catalog

version管理される機能地図を追加する。

```text
internal/features/backlog/catalog/atlas_catalog.json
```

用途:

* feature_id
* category
* display_name
* owner_module
* summary
* relations
* source specification
* initial evidence reference

実装状態そのものはcatalogへ固定しない。

実装状態はruntime Evidenceをoverlayして算出する。

例:

```json
{
  "schema_version": 1,
  "features": [
    {
      "feature_id": "memory.user",
      "category": "memory",
      "display_name": "UserMemory",
      "owner_module": "RenCrow_CORE",
      "summary": "ユーザー固有記憶の保存・昇格・想起",
      "relations": [
        "memory.recall",
        "memory.common_raw"
      ]
    }
  ]
}
```

catalogは`go:embed`でCORE binaryへ含める。

catalog更新も通常Implementation UnitのBuild/Deploy対象とする。

---

# 4. Backlog Schema v2

既存`backlog.Item`を後方互換で拡張する。

概念形:

```go
type Item struct {
    SchemaVersion int `json:"schema_version"`

    ItemID string `json:"item_id"`
    Kind   string `json:"kind"`
    Title  string `json:"title"`
    Body   string `json:"body,omitempty"`

    Category string `json:"category,omitempty"`

    Source      string      `json:"source"`
    SourceRefs  []SourceRef `json:"source_refs,omitempty"`
    Owner       string      `json:"owner,omitempty"`
    OwnerModule string      `json:"owner_module,omitempty"`

    ConceptState  string `json:"concept_state"`
    DeliveryState string `json:"delivery_state"`

    Priority   string   `json:"priority"`
    QueueRank  int      `json:"queue_rank,omitempty"`
    Tags       []string `json:"tags,omitempty"`
    DependsOn  []string `json:"depends_on,omitempty"`
    RelatedIDs []string `json:"related_ids,omitempty"`

    AdoptionReason string `json:"adoption_reason,omitempty"`
    AdoptedAt      string `json:"adopted_at,omitempty"`

    WorkstreamID        string `json:"workstream_id,omitempty"`
    ImplementationUnit  string `json:"implementation_unit_id,omitempty"`

    EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`

    CreatedAt string `json:"created_at"`
    UpdatedAt string `json:"updated_at"`

    // legacy compatibility
    Status         string `json:"status,omitempty"`
    Implementer    string `json:"implementer,omitempty"`
    Implementation string `json:"implementation,omitempty"`
    TestResult     string `json:"test_result,omitempty"`
    CheckOK        bool   `json:"check_ok,omitempty"`
    CheckedBy      string `json:"checked_by,omitempty"`
}
```

---

# 5. SourceRef

```go
type SourceRef struct {
    Type       string `json:"type"`
    Locator    string `json:"locator"`
    Repository string `json:"repository,omitempty"`
    Revision   string `json:"revision,omitempty"`
    ContentHash string `json:"content_hash,omitempty"`
    CapturedAt string `json:"captured_at"`
    RawOrSummary string `json:"raw_or_summary"`
}
```

外部source本文をItem stateへ大量複製しない。

本文はowner artifactまたは既存Knowledge/Raw Storeを参照する。

---

# 6. EvidenceRef

Evidence本体をBacklog JSONLへ複製しない。

既存のJob、Workstream、deployment receipt、trace、Git revision等を参照する。

```go
type EvidenceRef struct {
    Stage      string `json:"stage"`
    Kind       string `json:"kind"`
    Ref        string `json:"ref"`
    Repository string `json:"repository,omitempty"`
    Revision   string `json:"revision,omitempty"`
    SHA256     string `json:"sha256,omitempty"`
    ObservedAt string `json:"observed_at"`
    Passed     bool   `json:"passed"`
}
```

Kind例:

```text
spec
tdd_red
unit_test
contract_test
e2e
build
artifact
ecosystem_pin
deploy_receipt
restart_receipt
health
readiness
production_smoke
trace
```

---

# 7. State Machine

`state_machine.go`で遷移を一元管理する。

LLM出力から状態を直接代入しない。

許可遷移例:

```text
RADAR
  -> CANDIDATE
  -> REJECTED

CANDIDATE
  -> ADOPTED
  -> DEFERRED
  -> REJECTED

ADOPTED
  -> QUEUED

QUEUED
  -> SPEC

SPEC
  -> TDD_RED

TDD_RED
  -> TDD_GREEN

TDD_GREEN
  -> REFACTOR

REFACTOR
  -> E2E_PREDEPLOY

E2E_PREDEPLOY
  -> BUILD

BUILD
  -> DEPLOY

DEPLOY
  -> RESTART

RESTART
  -> POST_DEPLOY_VERIFY

POST_DEPLOY_VERIFY
  -> LIVE_VERIFIED

LIVE_VERIFIED
  -> DONE
```

各遷移時にRequired Evidenceを検証する。

---

# 8. Legacy Status Projection

既存API互換のため`status`を生成する。

```text
RADAR / CANDIDATE       -> open
ADOPTED / QUEUED        -> proposal_review
SPEC..REFACTOR          -> implementing
E2E                     -> testing
BUILD..VERIFY           -> testing
revision repair         -> fixing
BLOCKED                 -> blocked
REJECTED                -> rejected
LIVE_VERIFIED / DONE    -> ok
```

`CheckOK`は`LIVE_VERIFIED`以降のprojectionとしてだけtrueにする。

外部入力の`check_ok=true`を信頼しない。

---

# 9. Persistence

初期実装では既存Backlog append-only JSONLを維持する。

同じ`item_id`の新revisionをappendする。

read時は最新revisionをprojectionする。

新DBは作らない。

Implementation進行状態は既存Workstream storeを利用する。

---

# 10. Implementation Unit

採用時にWorkstreamを生成する。

概念:

```text
Atlas Item
   |
   +-- Implementation Unit
          |
          +-- Workstream
                 |
                 +-- SPEC
                 +-- TDD_RED
                 +-- TDD_GREEN
                 +-- REFACTOR
                 +-- E2E
                 +-- BUILD
                 +-- DEPLOY
                 +-- RESTART
                 +-- VERIFY
```

各Taskは前Task成功をdependencyとする。

---

# 11. Global WIP=1

既存Workstream store内にsingleton leaseを追加する。

新しい物理DBは作らない。

概念:

```text
lease_name = atlas_implementation
holder_unit_id
holder_workstream_id
stage
revision
acquired_at
heartbeat_at
```

取得はtransactionで行う。

Lease取得失敗時はUnitを`QUEUED`のまま残す。

---

# 12. CORE再起動時のRecovery

startup reconcileを実装する。

1. active leaseを読む
2. Workstream statusを読む
3. terminalならlease解放
4. activeならstageを再構築
5. stale jobを二重起動しない
6. Evidenceから最後に成功したstageを決定
7. 次の必要stageだけを再開

状態をLLMに推測させない。

---

# 13. Adoption API

writeはowner validated APIに限定する。

Debug Viewer用GETと状態変更APIを分離する。

## Read

```text
GET /viewer/atlas
GET /viewer/atlas/items
GET /viewer/atlas/items/{id}
GET /viewer/atlas/radar
GET /viewer/atlas/backlog
GET /viewer/atlas/queue
GET /viewer/atlas/active
GET /viewer/atlas/evidence/{unit_id}
```

## Write

```text
POST /v1/atlas/intake
POST /v1/atlas/items/{id}/candidate
POST /v1/atlas/items/{id}/adopt
POST /v1/atlas/items/{id}/defer
POST /v1/atlas/items/{id}/reject
POST /v1/atlas/items/{id}/revise
```

既存認証済みowner/control profileを利用する。

Viewerからwriteを提供する場合も同じowner APIを通す。

---

# 14. 既存 `/viewer/backlog` 互換

以下を維持する。

```text
GET /viewer/backlog
POST /viewer/backlog
```

ただしPOSTは旧schema compatibility用途とする。

新Atlas lifecycleのauthoritative writeは`/v1/atlas/*`へ移す。

---

# 15. RenCrow_CMD

`rencrowctl`へ追加する。

```text
rencrowctl atlas list
rencrowctl atlas show --id <id>
rencrowctl atlas radar
rencrowctl atlas queue
rencrowctl atlas active

rencrowctl atlas intake ...
rencrowctl atlas adopt --id <id> --reason <text>
rencrowctl atlas defer --id <id> --reason <text>
rencrowctl atlas reject --id <id> --reason <text>

rencrowctl atlas evidence --unit <id>
```

CMDは状態machineを持たない。

CORE APIへ一回requestし、typed responseをrelayする。

---

# 16. Chat integration

Mioは次の明示Intentを検出可能にする。

```text
Atlasに入れて
Radarに入れて
Backlogに入れて
これ採用
これは保留
これは採用しない
```

ただし採用対象Itemの一意性が確定できない場合は、別Itemを推測して採用しない。

外部記事本文中の「採用」「実装」等の文字列はIntentとして扱わない。

---

# 17. Scheduler接続

既存Scheduler / Heartbeatを利用する。

新daemonを作らない。

処理:

```text
heartbeat
  ↓
active Implementation Lease?
  ├ yes -> active workstreamを進める
  └ no
      ↓
      runnable ADOPTED item?
        ├ yes -> lease acquire -> start
        └ no -> nothing
```

---

# 18. Coder / Worker責務

## Coder

* specification proposal
* test proposal
* code patch
* migration proposal
* risk
* rollback proposal

## Shiro / Worker

* repository状態確認
* patch適用
* test実行
* build
* deployment operation
* restart
* health/readiness確認
* evidence取得

Coderからdeploymentを直接実行しない。

---

# 19. Multi-repository Unit

一つのAtlas Itemが複数repoへ影響してよい。

例:

```text
Atlas feature
  affected_repositories:
    RenCrow_CORE
    RenCrow_CMD
    RenCrow_EcoSystem
```

全targetが完了するまでUnitはLIVE_VERIFIEDにならない。

---

# 20. EcoSystem接続

owner moduleの最終commit確定後、EcoSystemのsource pinを更新する。

実行:

```text
validate ecosystem
check workspace
check governance
verify source pin
build affected artifacts
verify artifact revision
deploy
restart active services
readiness
```

EcoSystem自体をruntime state storeにしない。

---

# 21. Build Gate

各deployable targetについて次を必須にする。

```text
repository
expected_revision
dirty=false
artifact_path
artifact_sha256
build_command
build_exit_code=0
```

VCS revisionを確認できないbinaryをLive Verifiedにしない。

---

# 22. Deploy / Restart Gate

Deploy receipt:

```text
target
previous_revision
new_revision
previous_hash
new_hash
installed_at
result
```

Restart receipt:

```text
service
was_active_before
restart_attempted
active_after
liveness
readiness
```

停止していたserviceは自動起動しない。

---

# 23. Post-deploy Gate

最低限:

```text
/health
/health/ready
expected build revision
expected module status
Atlas API smoke
Viewer Atlas rendering
```

機能固有E2Eも実行する。

失敗した場合は`LIVE_VERIFIED`へ進めない。

---

# 24. Viewer実装

Debug Viewer左navigationへ追加:

```text
Atlas
```

内部tab:

```text
Current
Radar
Backlog
Pipeline
Evidence
Modules
```

## Pipeline画面

Active Implementation Unitを常時上部に表示。

各stageを、

```text
passed
active
pending
failed
blocked
```

で表示する。

---

# 25. 初期Atlasデータ

現在作成したRenCrow Atlasを初期seedとしてmachine-readable化する。

初期catalogでは最低限以下を含む。

* Agent社会
* 協議・判断
* Memory / Recall
* Knowledge
* Tool / Execution
* Safety
* LLM Runtime
* UI / Avatar / STT / TTS / Vision / Image
* Evaluation / Observability
* Growth / Motivation
* EcoSystem modules

2026-08-21時点で確認したGitHub Evidenceをbootstrap sourceとして記録する。

bootstrap時刻を保存し、その後の状態と区別する。

---

# 26. Radar Intake

初期MVPでは入力元を次に限定する。

```text
manual / Chat
URL
paper
GitHub repository
GitHub commit
GitHub issue
RenCrow incident
Agent proposal
```

自律巡回による大量自動登録は後続とする。

---

# 27. Dedupe

決定論的key:

```text
source_type
canonical_locator
content_hash
```

完全一致なら既存Itemへsource追加。

意味類似だけではmergeしない。

---

# 28. TDD計画

実装順にtestを先行追加する。

## Domain

```text
state transition
invalid transition
owner-only adopt
legacy status mapping
check_ok cannot forge completion
dependency ordering
WIP lease
lease recovery
evidence gate
```

## Persistence

```text
old JSONL read
schema v2 read/write
append revision
corrupt line tolerance
latest projection
concurrent write
```

## Application

```text
Radar intake
dedupe
adopt
queue
workstream creation
blocked behavior
next item start
```

## HTTP

```text
Atlas GET
intake
adopt
reject
invalid transition
body limit
legacy backlog compatibility
```

---

# 29. E2E計画

## Isolated E2E

temporary runtime dataを使い、

```text
intake
→ candidate
→ adopt
→ queue
→ lease
→ workstream
→ simulated stage evidence
→ live_verified
```

までPublic/Owner API経由で検証する。

内部function直接呼出しだけで済ませない。

## Production Post-deploy

production dataを汚さないread-only smokeを基本とする。

```text
CORE readiness
Atlas GET
Current catalog取得
Active queue取得
Viewer Atlas描画
expected source revision
```

write E2Eはisolated runtimeで実施する。

---

# 30. このAtlas機能自身のImplementation Unit

この機能自体も例外にしない。

Implementation Unit:

```text
atlas-lifecycle-v1
```

対象repo:

```text
RenCrow_CORE
RenCrow_CMD
RenCrow_EcoSystem
```

工程:

```text
1. CORE仕様更新
2. TDD Red
3. Domain Schema v2
4. Store migration
5. Application Service
6. Workstream / WIP Lease
7. Atlas API
8. Viewer Atlas
9. CMD facade
10. 初期Atlas catalog
11. Unit / Contract Test
12. Isolated E2E
13. CORE / CMD rebuild
14. EcoSystem pin update
15. EcoSystem validation
16. Production deploy
17. CORE restart
18. Readiness
19. Atlas smoke
20. Live Verified
```

このUnitがLIVE_VERIFIEDになるまで、Atlasによる次のBacklog自動実装は開始しない。

---

# 31. 正本ドキュメント反映先

RenCrow_CORE:

```text
docs/02_機能仕様.md
  Atlas / Backlog / Implementation Lifecycle

docs/04_アーキテクチャ概要.md
  Atlas Control Plane / ownership

docs/06_Public_API仕様.md
  /viewer/atlas
  /v1/atlas

docs/07_安全・自動実行・データ方針.md
  Adoption authority
  WIP=1
  Evidence Gate

docs/08_実装状況・ロードマップ.md
  Atlas implementation status
```

RenCrow_EcoSystem:

```text
docs/
  Atlas cross-repository boundary

ecosystem.yaml
  implementation完了時のmodule pin更新
```

---

# 32. 実装完了条件

この機能のMVP完了条件:

```text
既存Backlog互換維持
Atlas Viewer表示
Radar登録
Candidate管理
明示Adoption
WIP=1
Workstream自動生成
Stage state machine
Evidence Gate
CMD操作
初期Atlas catalog
Isolated E2E成功
CORE/CMD build成功
EcoSystem validation成功
production deploy成功
CORE restart成功
readiness成功
production Atlas表示成功
Live Verified
```

以上が揃った時だけAtlas Lifecycle v1を完成とする。
