# RenCrow Atlas / Backlog / Implementation Lifecycle 仕様

## 1. 目的

RenCrow Atlasは、RenCrowに存在する機能、構想、調査情報、Backlog、実装状況を一元的に可視化し、採用された機能を実装完了まで自動的に進めるための開発管理機能である。

Atlasは単なる進捗表ではない。

以下を一つにつなぐ。

```text
新しい情報
  ↓
Radar
  ↓
Candidate
  ↓
採用判断
  ↓
Backlog
  ↓
仕様
  ↓
TDD実装
  ↓
E2E
  ↓
Build
  ↓
Deploy
  ↓
Restart
  ↓
Production Verify
  ↓
Live Verified
  ↓
Done / closure
  ↓
Current Atlas
```

最終目的は、

> RenCrowに何が存在し、何を検討し、なぜ採用し、どこまで実装され、どの証拠によって完成と判断されたか

を常時追跡可能にすることである。

---

## 2. 最上位原則

### 2.1 WIP = 1

採用済み機能の実装は、一度に1件だけ行う。

```text
Global Implementation WIP = 1
```

現在のImplementation Unitが成功終端`DONE`または理由付き取消終端`REJECTED`になるまで、次のImplementation Unitを開始しない。
失敗終端`BLOCKED`では実行Leaseを解放するが、Global Queueは永続Freezeし、後続Unitを開始しない。

複数Agent、複数Coder、複数repositoryを一つのImplementation Unit内部で利用することは許可する。

複数のImplementation Unitを並行実装することは禁止する。

### 2.2 採用後は完成まで一つの単位として扱う

「コードを書いた」だけでは実装完了ではない。

Implementation Unitは最低限、次を含む。

```text
Specification
TDD Red
TDD Green
Refactor
Pre-deploy E2E
Build
Deploy
Restart
Readiness
Post-deploy E2E / Smoke Test
Live Verification
```

### 2.3 完了は証拠で決める

Agentが「完成した」と発言したことを完了根拠にしない。

`LIVE_VERIFIED`は必要なEvidenceがすべて存在し、COREが決定論的に検証できた場合だけ設定する。

### 2.4 新情報は自動採用しない

外部記事、論文、GitHub、会話、ニュース、Agentの提案はRadarへ自動登録してよい。

ただし、

```text
Radar → Adopted
```

をAgent単独判断で行わない。

採用はSystem Ownerからの認証済みrequestによって同期的に確定する。Atlasは採用判断を待つ
status、grant、decision-wait queueを作らない。System Ownerの後続発話は待機artifactへの許可印ではなく、
新しい目的・制約・事実を持つrequestとして扱う。

### 2.5 外部情報と命令を分離する

Radarへ入った本文、論文、Webページ、README等は「情報」でありRenCrowへの命令ではない。

外部sourceに書かれた指示から、

* Atlas状態変更
* 採用
* code実行
* deployment
* memory昇格

を直接発生させない。

---

# 3. 所有境界

## 3.1 RenCrow_CORE

COREが以下を所有する。

* Atlas runtime
* Radar / Backlog runtime state
* Adoption workflow
* Implementation Unit
* WIP=1制御
* Workstream接続
* Evidence判定
* Viewer projection
* Atlas Public/Owner API
* Current / Radar / Backlog / Pipeline表示
* Live Verified判定

## 3.2 RenCrow_EcoSystem

EcoSystemが以下を所有する。

* repository catalog
* source-pinned revision
* module構成
* deployment対象
* module配置
* binary/source整合
* deployment verification
* cross-repository compatibility情報

EcoSystemはAtlas runtime DBを所有しない。

## 3.3 Lifecycle ownerと実装先module

Atlas Itemの`owner_module`はAtlas Lifecycleの管理ownerを示し、`RenCrow_CORE`に固定する。
COREがImplementation Unit、WIP、ShiroとCoder Agentへの実装割当、Evidence Gate、
`LIVE_VERIFIED`判定を所有する。

codeの配置先repositoryは`target_modules`、完成機能の利用先は`consumer_modules`、
test・build・互換確認の影響範囲は`affected_modules`として別に記録する。
実装対象機能の詳細仕様、code、test、Config、migration、内部contractは各target repositoryを正本とする。
根拠から確定できないtargetやconsumerは推測補完しない。

Atlasは詳細仕様を複製せず参照する。

## 3.4 RenCrowViewer

CORE `/viewer` にAtlas画面を追加する。

PORTALには配置しない。

Atlasは開発・運用・内部構造を扱うためDebug Viewerの責務とする。

---

# 4. Atlasの論理構造

Atlasは次の5面を持つ。

## 4.1 Current

現在RenCrowに存在し、closureまで完了した`DONE`機能。`LIVE_VERIFIED`はclosure処理中であり、
Currentの完成機能件数へ含めない。

表示例:

```text
UserMemory
Owner        RenCrow_CORE
Concept      ADOPTED
Delivery     DONE
Revision     d5f181a
Evidence     12
```

## 4.2 Radar

入ってきた新情報。

対象例:

* 論文
* 技術記事
* GitHub repository
* GitHub issue / commit
* ニュース
* ユーザーとの会話で出たアイデア
* Agentによる発見
* 障害から得られた改善案
* 他システムの設計
* 新しいLLM技術

Radarは「検討材料」でありBacklogではない。

## 4.3 Backlog

RenCrowへの関連性を評価済みの項目。

次を区別する。

```text
CANDIDATE
ADOPTED
DEFERRED
REJECTED
```

`ADOPTED`になった項目だけImplementation Queueへ入れる。

## 4.4 Implementation Pipeline

現在実装している1件と、その工程を表示する。

```text
L0v2 Shadow Recall

✓ Specification
✓ TDD Red
✓ TDD Green
✓ Refactor
→ E2E
- Build
- Deploy
- Restart
- Post Deploy Verify
- Live Verified
```

## 4.5 Evidence

実装完了根拠を表示する。

* spec revision
* commit
* test result
* E2E result
* build artifact hash
* EcoSystem pin
* deployment receipt
* service restart result
* readiness
* production smoke test
* trace ID

## 4.6 Revision 2の実装契約

Revision 2では、Evidence Refを受け取っただけでstageを成功にしない。COREはItemから
`item_id`、`implementation_unit_id`、`implementation_revision`、`target_delivery_state`を解決し、
次のtyped contextとclaimを一緒にowner verifierへ渡す。

```text
EvidenceVerificationRequest {
  ref
  item_id
  implementation_unit_id
  implementation_revision
  target_delivery_state
}
```

requestの`passed`は外部claimとして保存できるが、requestが持ち込んだverified値は検証前に除去する。
CORE verifierが成功した場合だけ、同じRefへCORE-owned verification resultを付加し、Pipelineでは
`evidence_refs`（claims）と`verified_evidence_refs`（CORE result）を分けて表示する。Item、Unit、revision、
stageの不一致、owner不一致、stale、hash不一致、検証不能はfail closedとする。

Production verifierのsourceは固定し、requestが任意path、URL、receipt storeを選べない。

| Evidence kind | 固定sourceと検証条件 |
| --- | --- |
| `spec` | COREへembeddedされたBackfill Specification。本文、revision、captured_at、content SHA-256を照合する。local 8件だけが本文Evidenceを通過し、external 3件はintake／metadata参照には使えるが本文Evidenceの代用にはしない |
| `execution_report` | 設定済みCORE ExecutionReport storeの`execution_report:<job_id>`。EvidenceRefの`repository=RenCrow_CORE`、`revision=<full source revision>`を要求し、成功・終了時刻と`atlas.item`、`atlas.unit`、`atlas.implementation_revision`、`atlas.stage`、`atlas.source_revision`（full 40-hex）の完全一致markerを照合する。TDD_REDは`atlas.red_observed=true`、BUILDはEvidenceRefのartifact SHA-256と`atlas.artifact.sha256` markerの一致も要求する |
| `deploy_receipt` | 固定`~/.rencrow/receipts/binary-redeployment.jsonl`のCORE receipt。component、complete/success、target revision、installed binary hashを照合する |
| `readiness` | 固定loopbackの`GET /ready`（ref=`core:/ready`）。要求revisionが現行CORE executableのfull SHAであり、build stampがcleanであることを確認する |
| `production_smoke` | 固定loopbackの`GET /viewer/atlas/items/{item_id}`（ref=`core:/viewer/atlas/items`）。Item、Unit、revision、Design Card、resolved Specificationを照合し、同じclean executable revisionを要求する |

Stageの冪等単位は`implementation_unit_id + implementation_revision + target_stage`であり、
`StageRunReceipt`へrequest ID、payload hash、prepared/completed状態、結果をappendする。同一key・同一payloadの
再送は同じreceiptへ収束し、payload違いはconflictとする。receiptをItem stateより先に保存するため、途中停止後も
prepared receiptを再実行でき、過去revisionの履歴は巻き戻さない。

`BLOCKED`のQueue Freeze、replacement lease、resolution payloadはWorkstreamのdurable JSONLへ保存する。
resolutionは旧UnitのBLOCKED、revision、`supersedes_unit_id`、blocker Evidence、dependency、他Lease不在を
同じCORE decisionで検証し、owner storeの一つのlifecycle操作としてpending Freeze、replacement lease、resolved
Freezeをappendする。lease append後にprocessが停止した場合も最新Freezeはactiveのままなので、queueは再開せず、
同一payloadの再送だけが安全に完了できる。request IDだけで異なるpayloadを受理しない。

Backlog、Lease、Stage、Closure、FreezeのJSONLはappend-onlyであり、履歴行のrewrite/deleteを行わない。
Backfillは全itemを検証してから最初のrevisionをappendし、lifecycle JSONLのread/write/parse失敗、unknownまたは
empty Freeze status、未完了resolutionは実行可能へ推測せずfail closedにする。Prepared receiptは再起動後の
recovery対象であり、途中までのappendを成功完了とは扱わない。

`LIVE_VERIFIED`に到達したUnitは同じlifecycle runで自動的に`DONE` closureへ進む。ClosureReceiptは
prepared → resources completed → lease released → doneのphaseを持ち、Current projectionはcompleted closure
receiptを持つ`DONE`だけを完成機能として返す。CORE起動時の`Recover`はLIVE_VERIFIEDでclosureが欠けたUnitを
再開し、terminal Unitのlease tombstoneを冪等に処理する。

Owner intakeはSchema v2 Design Cardの`feature_id`、`problem`、`idea`、`background`、`expected_effect[]`、
`relation_refs[]`、`specification_refs[]`を任意で受け取り、値と配列を保持する。Radar/Candidateではこれらの
未解決fieldを要求せず、`purpose`以外の内容を推測・生成しない。`specification_refs`が supplied の場合だけ、
固定embedded Backfill packageの11 ID（local 8 / external 3）とmanifest・本文hashを保存前に検証し、unknownまたは
broken packageではSaveしない。

起動時はcanonical Backfill reconcileの後、Lease recoveryの前に、次の完全一致だけを一度検査する。
`atlas:atlas.lifecycle`、`implementation_unit_id=atlas-lifecycle-v1`、Schema v2、Concept `ADOPTED`、
Delivery `LIVE_VERIFIED`、`implementation_revision < 2`。一致したlegacy recordは履歴を削除・書換えせず新しい
append revisionへ移し、Design Cardと旧Evidence claimを保持したまま`implementation_revision=2`、
`invalidated_from_stage=SPEC`、`delivery_state=QUEUED`、`check_ok=false`（legacy statusは`proposal_review`）にする。
自動verifyは行わない。revision 2以上、terminal、shape不一致はno-opであり、migrationまたはRecover失敗時は
Atlas serviceを公開せず、legacy completionをCurrentへ露出しない。

---

# 5. 状態モデル

Atlasでは「構想の状態」と「実装状態」を分離する。

## 5.1 Concept State

```text
RADAR
CANDIDATE
ADOPTED
DEFERRED
REJECTED
```

### RADAR

情報を取得しただけ。

### CANDIDATE

RenCrowとの関連性が認められた状態。

### ADOPTED

RenCrowへ取り込むことが決定された状態。

### DEFERRED

価値は認めるが現在は実装しない。

### REJECTED

検討した結果、採用しない。

Rejected理由は削除しない。

---

## 5.2 Delivery State

```text
NONE
QUEUED
SPEC
TDD_RED
TDD_GREEN
REFACTOR
E2E_PREDEPLOY
BUILD
DEPLOY
RESTART
POST_DEPLOY_VERIFY
LIVE_VERIFIED
DONE

BLOCKED
REJECTED
```

`LIVE_VERIFIED`以前を完成扱いしない。

`LIVE_VERIFIED`はrequired Evidenceの実在性と内容をCOREが検証した状態であり、まだLeaseを解放しない。

`DONE`はCurrent反映、Workstream終端、closure receipt保存、Implementation Lease解放まで完了した成功終端である。
`LIVE_VERIFIED`から`DONE`へのclosureは追加の人判断を待たず、COREが同じlifecycle run内で冪等に実行する。

`BLOCKED`と`REJECTED`は成功を意味せず、Currentへ完成機能として掲載しない。`BLOCKED`はQueue Freezeを伴う
解決不能終端、`REJECTED`は認証済みSystem Ownerまたは決定済みpolicyによる理由付き取消終端であり、
取消closure完了後はQueueをFreezeしない。

---

# 6. 採用から実装まで

## 6.1 採用

System Ownerが認証済みrequestとしてAtlas Itemを明示的に採用し、COREは同じrequest内で
request outcomeを`applied`／`rejected`／`blocked`のいずれかへ確定する。人の追加判断を待つ中間状態は作らない。

`applied`の場合だけConcept Stateを`ADOPTED`へ変更する。採用しない決定をItemへ反映する場合は
Concept Stateを理由付き`REJECTED`へ変更する。依存利用不能、lease競合、owner scope不一致等でrequest
outcomeが`blocked`になった場合、Concept Stateへ存在しない`BLOCKED`を代入せず、Itemを採用前状態のまま保持する。

採用時に最低限以下を確定する。

* item_id
* title
* purpose
* owner module（`RenCrow_CORE`）
* target modules（根拠で確定できる場合）
* consumer modules（根拠で確定できる場合）
* affected modules
* acceptance criteria
* priority
* dependency
* source references
* adoption reason

採用後にImplementation Unitを生成する。

---

## 6.2 Queue

Implementation UnitはGlobal Queueへ入る。

選択順は決定論的にする。

基本順:

```text
dependency
→ priority
→ adopted_at
→ item_id
```

LLMがその場の判断だけで順番を変更しない。

---

## 6.3 Implementation Lease

実装開始時、Implementation UnitはGlobal Implementation Leaseを取得する。

他のUnitはLease取得中に開始できない。

CORE再起動時にはLeaseとWorkstreamを照合し、二重実行を防止する。

Leaseは「現在実行中の1 Unit」を排他する状態であり、Queue停止理由の正本にはしない。
成功終端`DONE`、失敗終端`BLOCKED`、取消終端`REJECTED`ではLeaseを冪等に解放する。

`BLOCKED`時の後続停止は、Leaseとは別のCORE-owned durable `Queue Freeze`で表す。Queue Freezeは最低限、
`freeze_id`、`blocked_unit_id`、`blocked_revision`、`reason_code`、`invalidated_from_stage`、
`evidence_refs`、`created_at`を持ち、CORE再起動後も維持する。Lease不在をQueue実行可能と解釈してはならない。

Queue dispatcherは、active Leaseなし、Queue Freezeなし、dependency成立、直前Unitが`DONE`または取消closure済み
`REJECTED`であることを同じdecision内で検証してから次UnitのLeaseを取得する。これらを別々に判定してはならない。

---

# 7. Implementation Unit標準工程

各Unitは単調増加する`implementation_revision`を持つ。stage失敗後に過去の成功recordを削除したり、
Delivery Stateを履歴上書きで巻き戻したりしない。新revisionへ`invalidated_from_stage`、root cause、
変更した前提／設計／route、引き継ぐ有効Evidenceを記録し、COREが新revisionのeffective stageを導出する。

Runnerの冪等単位は`implementation_unit_id + implementation_revision + target_stage`とする。
同一キーの再送は同じreceiptを返し、異なるpayloadはconflictとして拒否する。1 stage完了後は次のtarget stageを
新しいキーで開始できなければならず、Unit全体に一度だけ付けるstarted markerで後続stageを止めない。

## Stage 1: Specification

実装前に仕様を正本へ記述する。

最低限:

* 目的
* 対象
* 非対象
* owner
* contract
* data
* state
* error
* security
* acceptance criteria
* migration
* rollback

仕様commitが存在しない状態でTDDへ進めない。

---

## Stage 2: TDD Red

実装前にAcceptance Criteriaを検証するtestを書く。

少なくとも一つのtestが対象機能未実装を理由として失敗することを確認する。

Red Evidenceを保存する。

---

## Stage 3: TDD Green

必要最小限の実装を行い、Red testを成功させる。

Coderはproposal / patchを生成できる。

side effect、適用、test実行はWorker境界を通す。

---

## Stage 4: Refactor

重複、責務越境、不要なfallback、概念的不整合を確認する。

Refactor後に全対象testを再実行する。

---

## Stage 5: Pre-deploy E2E

実際の公開contractを通したE2Eを行う。

mockや内部function直接呼出しだけをE2Eと呼ばない。

外部backendがoptionalの場合も、設定された正規routeを検証する。

---

## Stage 6: Build

affected moduleをすべて再ビルドする。

Build Evidence:

* repository
* revision
* dirty=false
* artifact
* SHA-256
* build result

を保存する。

---

## Stage 7: Deploy

新artifactをdeployment対象へ配置する。

cross-repository変更の場合はEcoSystem pinも更新する。

配置済みbinaryとsource revisionの一致を検証する。

---

## Stage 8: Restart

変更対象serviceを再起動する。

原則としてdeployment前に稼働していたserviceだけを再起動する。

停止していたoneshotやserviceを勝手に起動しない。

---

## Stage 9: Post-deploy Verify

最低限:

```text
process alive
health
readiness
expected revision
expected artifact hash
target API smoke test
```

を確認する。

必要な機能ではproduction E2Eも実施する。

---

## Stage 10: Live Verified

すべての必須Evidenceについて、参照先のowner、revision、hash、result、observed_atをCOREが検証できた場合のみ、

```text
Delivery State = LIVE_VERIFIED
```

へ遷移する。request payloadの`passed=true`、Agentの完了発言、文字列だけのrefは検証結果ではない。
この時点ではImplementation Leaseを解放しない。

---

## Stage 11: Done

COREは`LIVE_VERIFIED`到達後、同じlifecycle run内で次を冪等に実行する。

```text
closure receipt prepared
Workstream / Goal / Artifact終端更新
Implementation Lease解放
Delivery State = DONE
closure receipt completed
Current projectionへDONEを反映
```

複数storeを一つのtransactionにできない場合は、closure receiptのphaseを正本として順序を固定し、
再起動時に未完phaseだけを再実行する。Queue dispatcherは`DONE`とcompleted closure receiptの両方を要求する。
lease解放失敗を無視して`DONE`を保存してはならない。

その後、次のADOPTED itemを開始可能にする。

---

# 8. 失敗時の扱い

各Stage失敗時はroot cause、reason code、失敗Evidence、無効になった最も早いstageを固定する。

```text
failure
 ↓
root cause / invalidated_from_stage
 ↓
implementation_revision + 1
 ↓
新revisionのeffective stageを導出
```

例:

```text
E2E failure
→ implementation revision
→ TDD_GREEN
→ E2E
```

```text
Deployment failure
→ deployment/build revision
→ BUILD
```

同じ失敗原因を無条件に繰り返さない。revisionごとに変更した前提、分解、route、Tool、設計を記録し、
policyで定めた有限回のrevisionでも成立しない場合は理由とEvidence付き`BLOCKED`で閉じる。

`BLOCKED`は待機状態ではなく終端結果である。

Active Unitが`BLOCKED`になった場合、COREは失敗終端recordとQueue Freezeを保存してから実行Leaseを解放する。
Implementation Queueは再起動後も停止する。

後続項目を黙って飛ばさない。

`BLOCKED` Unitそのものを再開、上書き、状態巻戻ししてはならない。`BLOCKED`到達前のstage失敗は同じUnitの
新しい`implementation_revision`で再試行できるが、`BLOCKED`到達後の再試行には必ず置換Unitを作り、旧Unitを`supersedes_unit_id`、
`blocker_resolution_refs`で参照する。Queue Freeze解除は、認証済みSystem Ownerからの新しいrequest内で、
置換revision、blocker解消Evidence、dependency成立をCOREが同期検証できた場合だけ行う。
このrequestは停止中artifactに判を付けるものではなく、新しい事実と実行目的を持つ独立requestである。

解除requestが`rejected`／`blocked`ならFreezeを維持する。単なるCORE再起動、Lease不在、priority変更、
Agentの自然言語報告ではFreezeを解除しない。

解除のowner APIは次に固定する。

```text
POST /v1/atlas/queue-freezes/{freeze_id}/resolve
```

requestは`request_id`、`expected_freeze_revision`、`replacement_unit_id`、`supersedes_unit_id`、
`blocker_resolution_refs`を必須とする。replacement Unitは事前に認証済みAdoptionを完了した`ADOPTED / QUEUED`
でなければならず、Freeze中のAdoptionはUnitとQueue recordを作成できるがLeaseを取得しない。
COREは旧Unitが`BLOCKED`、supersedes関係が完全一致、blocker Evidenceがowner verifier合格、dependency成立、
他Leaseなしを同じdecision内で検証する。成功時はFreeze解除receiptとreplacement UnitのLease取得を一つの
冪等operationとして確定する。同じ`request_id`／同じpayloadは同じreceiptを返し、payload違いはconflictとする。

---

# 9. Radarへの情報登録

Radar Itemは最低限以下を持つ。

```text
item_id
title
source_type
source_locator
source_hash
captured_at
captured_by
summary
relation_tags
related_feature_ids
relevance
novelty
expected_impact
raw_or_summary
provenance
```

Source Type例:

```text
conversation
article
paper
repository
commit
issue
news
incident
agent_proposal
manual_idea
```

Source本文とRenCrow側の評価を分離する。

---

# 10. 重複排除

次を利用する。

1. canonical source locator
2. content hash
3. normalized title
4. related feature
5. semantic similarity

完全一致は新Itemを作らず既存ItemへSourceRefを追加する。

意味的に近いだけの場合は自動mergeせず、related itemとして記録する。

---

# 11. Viewer仕様

左navigationへ`Atlas`を追加する。

Atlas内に次のtabを持つ。

```text
Current
Radar
Backlog
Pipeline
Evidence
Modules
```

## Current

機能カテゴリ別表示。

## Radar

取得日時順。

## Backlog

Concept State、Priority、Ownerでfilter。

## Pipeline

Active Unitを最上部へ表示。

Global WIP=1を明示する。

## Evidence

stageごとのEvidenceをtimeline表示する。

## Modules

EcoSystem由来のmodule revision、runtime health、last verified情報を表示する。

---

# 12. Atlasの現在状態判定

実装状態は文章から推測しない。

優先順位:

```text
Live Evidence
> Deployment Receipt
> E2E Evidence
> Build Evidence
> Source Implementation Evidence
> Specification
> Backlog
> Radar
```

「docsに実装済みと書いてある」だけで`LIVE_VERIFIED`へ上げない。

Evidence Refは証拠そのものではなく、ownerが管理する証拠へのaddressである。COREはEvidence kindごとの
allow-list verifierを使い、最低限次を検証する。

```text
spec        -> canonical document revision / content hash
test / E2E  -> command、exit status、対象revision、result receipt
build       -> clean source revision、artifact SHA-256、build receipt
deploy      -> EcoSystem pin、deployment receipt、installed artifact
restart     -> 対象service、before / after state、restart receipt
readiness   -> expected revisionでのreadiness response
smoke       -> production route、result、trace / observed_at
```

検証不能、owner不一致、hash不一致、stale、失敗resultは`passed`へ導出しない。外部入力の`passed`はclaimとして
保持できるが、runtime stage statusはCORE verifierの結果からだけ導出する。

---

# 13. 既存Backlogとの互換

既存の

```text
idea
unimplemented
proposal_review
open
implementing
testing
fixing
blocked
rejected
ok
```

は互換projectionとして維持する。

新しいConcept State / Delivery Stateを正本とする。

既存`check_ok=true`単独では`LIVE_VERIFIED`を意味しない。

---

# 14. 非対象

初期実装では以下を行わない。

* GitHub Web APIの常時polling
* 外部情報からの自動採用
* LLMによる無制限priority変更
* 複数Implementation Unitの並行実装
* 新しい物理DBの追加
* PORTALへのAtlas搭載
* module内部仕様のAtlasへの全文複製
* Atlasによるraw SQL実行
* Atlasによるpermission bypass

---

# 15. Definition of Done

Atlas ItemがDONEになる条件は以下すべて。

```text
仕様正本あり
TDD Red Evidenceあり
TDD Green
Refactor後test成功
Pre-deploy E2E成功
Build成功
artifact hash確認
EcoSystem整合
Deploy成功
Restart成功
Readiness成功
Post-deploy verification成功
Live Evidence保存
Atlas Current更新
Implementation Lease解放
closure receipt完了
Queue Freezeなし
```

一つでも欠ける場合はDONEにしない。
