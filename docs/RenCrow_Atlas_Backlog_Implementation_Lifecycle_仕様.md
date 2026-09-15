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

`CANDIDATE`は実装待ちではなく、技術観測、仮説、設計候補、競合案を保持する熟成領域である。
`CANDIDATE`へ入った新規ItemはMaturation / Revalidation契約を通過し、
`maturation_state=PROMOTED`がCOREによって保存されるまで採用できない。

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

## 5.2 Maturation State

Concept StateとDelivery Stateの間に、実装判断の熟成と再検証を表す
`maturation_state`を置く。これは構想の採否や実装stageを二重管理するstateではない。

```text
MATURATION
REVALIDATION
PROMOTED
MERGED
HOLD
DROPPED
```

- `RADAR` Itemは情報であり、まだMaturationの対象ではない。
- `RADAR -> CANDIDATE`と同じCORE owner操作で`MATURATION`を開始する。
- 最低熟成期間は`7 * 24h`とし、`maturation_started_at`と
  `maturation_eligible_at`をUTCで保存する。七日経過は採用ではなく、再検証可能を意味する。
- 関連資料、Source Ref、コメント、関連Item、仮説、暫定priorityは
  `MATURATION`中も追記できる。追記は履歴を消さない。
- 目的、Problem、上位設計との関係、実装方針を変える重大更新は理由を必須とし、
  新しい`MATURATION`を開始する。軽微な根拠追記で時刻をリセットしない。
- `PROMOTED`のItemだけが、認証済みowner requestの`Adopt`によって
  `ADOPTED / QUEUED`へ進める。
- `MERGED`は統合元を履歴として保持し、実在する別Itemを`merged_into`で参照する。
- `HOLD`は失敗終端ではない。`next_review_trigger`と一致する新事実を受けた新requestで
  `REVALIDATION`へ戻れる。人の返答待ちstatusにはしない。
- `DROPPED`は理由付き非採用であり、Itemと過去のrevalidation recordを削除しない。

Revalidationでは必ずNecessity、Duplication、Mergeability、Architectural Consistency、
Technology Validity、Implementation Value、Timingを評価する。決定は
`PROMOTE` / `MERGE` / `HOLD` / `DROP`の一つに収束させる。LLMは意味評価案を生成できるが、
COREがJSON contract、熟成日数、対象Item、統合先、policy、state transitionを決定論的に
検証した後だけ保存する。

各再検証は既存Backlog Itemのappend-only履歴に次を保存する。

```text
backlog_id
revalidation_date
maturation_days
decision
reason
necessity
duplication
mergeability
architectural_consistency
technology_validity
timing
related_backlogs
conflicting_specs
merged_into
technology_changes
architecture_impact
implementation_value
next_review_trigger
review_agents
forced
maturation_bypass
bypass_reason
```

Security Issue、Data Loss Risk、Production Failure、Breaking Change、明確なBug Fix、
RenCrow稼働維持だけ7日gateを迂回できる。迂回は`maturation_bypass=true`と
machine-readableな`bypass_reason`を同じrecordに必ず保存する。

System Ownerは認証済みの新requestとして強制`PROMOTE` / `HOLD` / `DROP`、
またはRevalidation再実行を指示できる。これらも人の返答待ちworkflowの解除ではなく、
理由とActorをrecordしてCOREが同期評価する新しいowner requestである。
強制`MERGE`は統合先との意味的整合性を人が直接確定する迂回経路になるため受理せず、
通常Revalidationだけが実在する統合先を選択できる。

通常RevalidationのHTTP bodyは`request_id`とevent-driven HOLD用`trigger`だけを受理する。
七観点、decision、reason、関連候補はRenCrowのRevalidation Evaluatorが生成し、COREが
schema、対象、7日gate、統合先、state transitionを検証する。callerが通常requestへ
semantic decisionを注入した場合は拒否する。Heartbeatは1日1回、古いeligible Itemから
最大1件を評価し、失敗時に同じheartbeatで無限再試行しない。

Debug ViewerのAtlas Backlog画面は判断対象、原文・Evidence、現在state、eligible時刻、
RenCrowの推奨decisionと理由、七観点、各選択肢、緊急bypass、編集可能なenrichment、
未処理数、30日metrics、実行結果receiptを同じ画面に表示する。Bearer tokenはpage memoryだけに
保持し、browser storageへ保存しない。旧`/viewer/backlog`はGET互換だけを残し、POSTは405で
Atlas owner APIへ誘導する。

## 5.3 Delivery State

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

採用前に`maturation_state=PROMOTED`を必須とする。熟成期間中、未検証、`MERGED`、
`HOLD`、`DROPPED`のItemからImplementation Unitを作成しない。

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

Intakeの再送は生成済みの安定IDで既存Itemを参照し、利用者による題名・本文・状態の変更を保持する。
異なるID間の機械的な完全一致は、出典のlocator／content hashと空白・大文字小文字を正規化した題名で判定する。
同じ記事を引用する別題の仕様案は別Itemとして保持する。出典の共有だけでは同じ仕様とは判定しない。

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

---

# 16. Development Methodology v1

## 16.1 適用境界

Development Methodologyは別の開発管理systemではない。既存Atlasの
`Item -> Implementation Unit -> Workstream -> Evidence -> DONE`を、Task、Plan、Review、
Ruling、Authority Evidenceまで詳細化するCORE所有の契約である。Concept State、Delivery
State、Global Implementation Lease、Queue Freeze、Workstream、Skill Registry、Event／Traceの
正本は既存機構を維持し、同義のstate machine、ledger、policy engine、schedulerを新設しない。

Specification、Plan、Implementation Authority Token、Ruling、Evidence Receipt、Review、Development Ledgerは、
Implementation Unitの既存Workstreamへ属するtyped artifactとして保存する。static Atlas catalogへ
runtime stateやEvidenceを手書きしない。

## 16.2 CLI / LLM / Boundary分類

| 工程 | 区分 | Owner | 入力 | 出力・失敗 | Evidence |
| --- | --- | --- | --- | --- | --- |
| intake分類、state遷移、hash、token期限、authority、worktree、test、review、LIVE gate | CLI | CORE | typed requestと既存state | typed result、`blocked`／`failed` | receipt、event、ledger revision |
| 設計案、仕様本文、plan案、原因仮説、review finding | LLM | CORE Agent | bounded context | proposal。直接state変更不可 | transcript refとcontent hash |
| schema、認証、policy、保存、外部実行、deploy、restart | Boundary | CORE／対象module | 認証済みrequestとproposal | 同期実行／拒否 | owner receiptとtrace |

LLMは曖昧な意味判断または複数の妥当案からの選択にだけ使う。hash、遷移、認証、policy、保存、
shell、test、build、deployをLLMへ移さない。

## 16.3 Implementation AuthorityとNo-Human-Gate

Implementation Authority Tokenは、System Ownerが発行済みの採用requestまたは7日Maturation後のowner
revalidationをunit、spec hash、scope、issuer、期間へ束縛するprovenanceである。人の返答を待つ
status、decision queue、grant待機artifactではない。tokenがない、失効、期限切れ、scope不一致、
spec hash不一致の場合、開始requestを同期的に`blocked`へ収束させる。有効tokenを持つ同一requestで
同じ確認を繰り返さない。

## 16.4 Stateとterminal outcome

Task StateはDelivery Stateと分離し、単一transition tableで扱う。

```text
PENDING -> READY -> ASSIGNED -> RED_VERIFIED -> GREEN_VERIFIED
        -> REFACTORED -> TASK_REVIEWED -> DONE
```

どの非terminal stateからも、検証済み理由により`BLOCKED`、`FAILED`、`CANCELLED`へ遷移できる。
Task、run、unitのterminal outcomeは`ok | failed | blocked | cancelled`のいずれかとし、timeout、
context終了、Tool終了を曖昧なrunningへ残さない。

Deliveryの既存stage名は変更しない。`SPEC`はapproved specとplan、`TDD_RED`はworktree／baseline／
RED、`TDD_GREEN`はGREEN、`REFACTOR`はrefactorとTask Review、`E2E_PREDEPLOY`はBranch Reviewと
predeploy E2E、以降は既存stageへ対応する。新しい同義Delivery enumを追加しない。

## 16.5 Policy Gate

各遷移はCORE owner serviceで同じgateを通す。

* Authority: Coder execution roleはplan／patch／test proposal／findingだけを生成する。repository
  write、shell、test、build、deploy、restartはWorker authorityだけが行う。Skillはauthorityを付与しない。
* Worktree: production変更は既定branch以外の隔離worktreeとbase revisionを必要とする。
* Baseline/TDD: baseline receipt、意図した失敗を示すRED、成功したGREENを順に必要とする。
* Review: implementerと異なるreviewerによるTask Review、全diffとintegration Evidenceを読むBranch
  Reviewを必要とする。
* Root Cause: repairは再現Evidence、log／trace、call path、単一仮説、最小実験を必要とする。同じ症状への
  3回目の失敗はarchitecture assumption reviewへ移し、追加patchを拒否する。
* Conflict: reversible local ambiguityとnon-destructive design gapだけをRuling付きで継続できる。
  destructive／irreversible／security／data loss／external contract／product semantics conflictは`BLOCKED`。
* LIVE: accepted implementation、relevant tests、build artifact hash、EcoSystem pin、deploy、restart／reload、
  process／port／binary identity、readiness、production smoke／E2E、Viewer確認が揃うまで拒否する。

Agentの自然言語報告、request側の`passed=true`、`check_ok=true`、別unit／別revision／期限切れEvidenceは
gateを満たさない。credential、Authorization、cookie、password、秘密tokenはartifact、event、ledger、
Viewerへ保存しない。

## 16.6 ArtifactとLedger

全artifactは`unit_id`、`plan_id`、`spec_ref`、`spec_hash`、revision、作成時刻を照合できるbounded
JSONとする。Planはexact files、interface、Task DAG、command、expected result、review、rollbackを持つ。
Evidenceはstage、type、command、exit code、artifact ref／SHA-256、git revision、trace、valid revisionを持つ。
Reviewはimplementer、reviewer、diff、finding、verdict、Evidence refを持つ。Rulingはconflict type、decision、
rationale、impact、actorを持つ。

Development Ledgerはplanごとに一意で、Task、assignment、worktree、baseline、ruling、review、Evidence、
blocked reason、checkpoint、resume tokenを集約する。同じIDと同じpayloadの再保存はidempotent、同じIDと
異なるpayloadはconflictとして拒否する。process restart後は最新の同一unit／plan ledgerから再開し、
別planの記録を採用しない。revision変更は既存ledgerをterminalへ閉じた後、新plan、新revision、
`supersedes_plan_id`／`supersedes_revision`、新しい隔離worktree／baselineを持つ`PENDING` ledgerとして開始する。
旧revisionのEvidence、Review、Ruling、assignmentは持ち越さない。

## 16.7 Skill、Team Composer、Authority

Development stage skillは既存Skill Registryへ登録し、description、required capability／tools／knowledge、
authority requirement、input／output contract、cost、risk、evaluation、versionを宣言する。
`rencrow_development_loop`は現在stateから次のstage skillを選ぶだけで、authority付与、Evidence生成、
state上書き、LIVE判定を行わない。

Team ComposerはSkill requirementとruntime capabilityを照合してexecution role候補を返す。Agent identityや
model／providerを固定せず、World ActorとExecution Roleを混同しない。最終実行前にCOREのAuthority
Validationが認証済みactor、execution role、requested action、scopeを同期評価する。

## 16.8 API、CMD、Event、Viewer

CORE read APIはactive unit、unit detail、Task DAG／status、Implementation Authority、Ruling、Evidence list／detail、Review、worktree／
revision、deployment／readiness／LIVEを返す。mutationは認証済みCORE owner APIだけが受け、CMDとViewerは
state machineを持たない。CMDは既存`atlas` commandへread facadeを追加する。

実行側はunit／transition／skill／worktree／TDD／review／ruling／evidence／build／deploy／readiness／
production／terminalの事実を既存Event／Traceへ発行する。Viewerは既存Atlas Pipeline／Evidenceを拡張し、
poll結果から内部stateを推測しない。Viewer停止時もowner workflowは継続する。

## 16.9 Migration、rollback、recovery

既存Item、Lease、Stage／Closure Receiptを変更または自動成功させない。Development artifactがない既存Unitは
legacyとして読めるが、新methodology stage開始時にapproved spec、plan、token、worktree／baselineを新規検証する。
migrationはappend-onlyかつ冪等とし、別DBを作らない。

rollbackは新artifact／projectionの読取りを無効化して既存Atlas lifecycleへ戻せることを要求する。Unit stateや
既存Evidenceを削除・書換えない。再起動時はplan-scoped ledgerと既存Lease／Queue Freezeを照合し、不一致、
unknown status、malformed receiptはfail closedで`blocked`にする。


## 17. Gmailからの定期取り込み

### 17.1 ownerと選択規則

Gmail transport／Google OAuthはRenCrow_Toolsのnative Go `rencrow-gmail`、
件名による振り分け、意味評価、Atlas状態、receipt、定期実行はCOREが所有する。
CodexのGmail接続とは独立し、`heartbeat.gmail.account`で指定したGoogle accountを
profile APIで照合してからメールを読む。scopeは`gmail.readonly`。
送信、既読化、削除、ラベル変更は行わない。

Gmailは一つの入力からAtlas候補とprivate Knowledgeを生成するcross-destination intakeであるため、
オーケストレーション、意味評価、準備済みreceipt、保存先の振り分けは
`internal/application/gmailintake`が所有する。Atlasのlifecycleと状態遷移は
`internal/application/backlog`が所有し、Gmail intakeは公開された`Service.List`、`Service.Intake`、
`Service.Candidate`だけを呼び出す。Gmail固有の型、validator、receipt storeをAtlas packageへ戻さない。

| 入力 | 工程 | 区分 | 出力・失敗 |
| --- | --- | --- | --- |
| 対象account、固定query、page token | 取得、MIME変換、hash検証 | CLI（Tools／CORE） | 有界JSON、取得失敗はerror |
| 件名にRenCrowとBacklogの両方を含むメール（大文字小文字不問） | 原文をIntakeしてCandidateへ進める | CLI／Boundary（CORE） | CANDIDATE、保存失敗はerror |
| 件名にAI・政治デイリーブリーフを含むメール | 全話題の抽出、根拠解釈、RenCrow価値判断、仕様化 | LLM（CORE Shiro、既存Worker route） | Atlas／Knowledgeの話題別結果、skipped／blocked |
| 抽出URLと取得本文 | メール内URL一致、public宛先、取得成功、引用一致、schema検証 | Boundary（CORE） | URL自体の不正はblocked、取得不能な話題は未検証Knowledge候補 |
| 検証済み提案／話題別Knowledge | 既存Atlas Intakeまたは既存Knowledge owner、dedupe、receipt保存 | Boundary（CORE） | 登録先ID、Task／Run／Trace、話題別status／reason |

件名にRenCrowとBacklogの両方を含むメールは直接仕様メールとして扱う。日次ブリーフ文字列と重なる場合もこの直接登録条件を優先する。
RenCrowだけ、Backlogだけを件名に含むメールは直接登録の対象外とする。本文の語句ではこの条件を満たさない。
日次ブリーフは番号付き話題を全件保持し、AI、政治、その他へ分類する。政治話題、その他の話題、採用しないAI話題は既存Knowledge ownerのprivate itemへ送る。
番号付き本文は連番1..N（N<=20）として話題抽出の入力を話題ごとに分離し、最初の1番見出しのdelimiterを同じ文書の見出し形式として使う。別delimiterの埋込み順序リストは話題本文とURLへ保持し、同じ見出し形式の欠落、重複、並べ替え、別話題への再割当、上限超過はfail closedとする。
AI抽出と価値判断は意味判断が必須でありLLMを使う。
取得・認証・状態変更・保存・再実行制御はLLMへ渡さない。
メール／取得記事は信頼しない入力であり、本文中の命令やコードを実行しない。

直接仕様メールは本文を保存するが、登録時点で採用・実装を開始しない。
AI提案は一次出典の取得と引用の照合、明示したverification_status=verified、RenCrowへの関係、仕様、受入条件を必要とする。
出典の主張確認を性能benchmarkや実機検証の成功と呼ばず、未実施の実験は仕様の受入条件として残す。
取得可能な出典URLがない、根拠が不足、または話題の出典が検証不能な場合は、その話題だけをverification_status=unverifiedのKnowledge candidateとして保持する。
一つの話題の取得失敗、評価JSON／引用照合の失敗、評価入力上限超過、またはAtlas関連性snapshotの欠落で他の話題を捨てず、その話題をverification_status=unverifiedのKnowledge candidateとして保持する。失敗URLと話題別reasonをreceiptへ残す。provider／contextなど全体依存の一時失敗はmail全体を再試行し、LLMが任意のowner、仕様path、権限、実行状態を設定することはできない。

Failure / Problem: 日次メールの話題1本文に含まれる`1) 2) 3)`の埋込みリストをトップレベルの`1. 2. ...`見出しとして誤認し、連番検査前の入力構築でメール全体をblockedにした。Causeはdelimiterだけを区別せず行頭の番号を収集していたことであり、Lessonは最初の1番見出しのdelimiterを文書形式として固定し、別delimiterを本文へ戻すこと。Invariantは話題本文とURLを削除せず全件保持し、同じ文書形式の欠落・重複・順序違反だけをfail closedにすること。Enforcementは`parseGmailDailyNumberedSections`のdelimiter別候補化と連番検査で行う。Testsは埋込み`1) 2) 3)`を含む10話題、同形式の欠落・重複・順序違反、話題別URL coverageで検証する。

### 17.2 定期実行、認証、再起動

`heartbeat.enabled`と`local_agent_ops.enabled`、設定済みowner user_idが有効な場合にだけ、
`heartbeat.gmail.enabled`を有効化できる。既定は無効、周期30分、1page20件、全体timeout20分。
CORE Heartbeatはownerが発行するTask／RunとShiroのagent_orchestrator scopeを束縛し、
指定ownerのuser data scopeで実行する。LLMはこのAgent処理に使う実装機構である。
実行中の重複起動を抑止し、停止時は子process／LLM／取得をcancelしてTask終端まで待つ。

Google desktop OAuth client JSONとtoken JSONは別々の絶対pathで指定する。
`rencrow-gmail authorize --account owner@gmail.com --credentials-file <絶対path> --token-file <絶対path>`
が表示するURLをブラウザで開き、Google同意画面を操作する。認証にはPKCE／state／loopback callbackを使う。
secretはリポジトリ、引数値、メール本文、共有logへ出力しない。JSON内容をChatへ貼り付けない。
設定例は`config/config.yaml.example`、Tools側の手順は
[collector README](../../RenCrow_Tools/tools/mail/gmail/README.md)を参照する。

COREのreceipt正本は`<workspace>/logs/gmail-intake/`。
account／message IDをキーに、原文、判定理由、準備済み登録要求、登録先ID、Agent／Task／Run／Traceを保存する。
再開時は最新のTask／Run／Traceを記録し、初回のidentityはorigin欄に保持する。
秘密ファイルとreceiptはowner権限で保存し、symlinkや不正形式を拒否する。
Unixは秘密ファイル0600・receipt root0700、Windowsは現在userとOS管理主体（SYSTEM／Administrators）だけに許可するDACLを使う。
Toolsの認証秘密とCOREのreceiptは独立した配布・障害境界を持つため、OS権限adapterを各owner内に置く。
receiptの意味・identity・hashの検証はCORE applicationの一箇所を正本とし、storageはそれを呼ぶ。
直接仕様の準備済み要求は、件名と本文が保存済みメール原文に一致することを再開時にも検証する。
completeには準備済み要求と登録先IDが必要であり、空の完了記録で未登録メールを処理済みにしない。
AI提案の判定理由は準備時から完了後まで保持し、Viewerから参照できるようにする。
意味評価結果とAtlas／Knowledgeの準備済み要求を外部書込みより先に保存し、部分書込み後は同じ要求から再開する。
Knowledgeのレビュー済みitemはverification_status=verified、取得hash、capture時刻、原文に一致する引用を持ち、未検証itemはcandidateと明示する。
長い取得本文はXの既存外部リンク要約契約（`docs/02_機能仕様.md`）と同じCORE owner summarizerへ渡す。取得本文全体のhash／capture時刻と原文一致引用を保持し、本文上限超過は取得不能として明示する。旧48KiBの集約切捨ては行わない。
現行PolicyRevisionは`gmail-daily-routing-v4`とする。complete、準備済み要求または登録先IDを持つreceipt、現行policyのskipped／blockedは再評価しない。ただし出力のない日次／直接仕様のskipped／blocked receiptで、既存のlegacy値（空、`gmail-daily-routing-v1`、`gmail-daily-routing-v2`）または`gmail-daily-routing-v3`のものは、定期実行一回につき最新100件から最大1件だけ現行PolicyRevisionで再評価できる。未知または将来のrevisionは再評価しない。再評価は保存済み原文を同じ`processMessage`経路で処理し、成功／失敗ともpage cursorを保存しない。provider、context、writerなどの一時依存失敗はterminal receiptを作らずpage cursorを進めない。
privateなNews Knowledgeは既存Knowledge ownerのDB、immutableなimport manifestとは独立して検証するsource receipt overlay、owner-filteredなSQL search projectionだけへ保存する。generic stagingやglobal registryへprivate入力の複製を作らず、同じownerのreview／replayでは既存itemの編集を保持したままreceiptとprojectionを再検証する。
全messageが終端になってから次pageへ進み、末尾で先頭へ戻る。古いcursorによる取得失敗は
次回の先頭走査へ戻すが、既存receiptとAtlasのdedupeを維持する。

### 17.3 Viewerと受入証拠

AtlasのBacklog画面にGmail処理結果を表示する。
`GET /viewer/atlas/gmail`は既存owner bearer／client profile／direct-local認証を必須とし、
直近20件の原文、現在状態、理由、登録先、Task／Runを返す。内部の再開用要求は返さず、cacheを禁止する。
登録されたAtlas候補の仕様／原文／出典は既存Atlas itemに保存され、既存候補の判断GUIから参照・採用・保留・却下する。政治、その他、採用しないAIのKnowledge itemとcandidateは既存の認証済みKnowledge review surfaceから原文URL、検証status／reason、引用、hash、capture時刻を参照する。
Gmail取込専用の採用routeや返答待ちworkflowは追加しない。

受入は、対象Google accountの実メール→正規collector→CORE Shiro Task／Run→
話題別の既存Atlas候補またはprivate Knowledge item→Viewer／Knowledge surfaceでの原文・根拠・receipt参照→再起動後の重複なしまでを必要とする。
synthetic mail、HTTP fixture、mock LLMのtestはsource契約検査であり実Gmail／実Agent E2Eを代替しない。
OAuth未設定、未配備、実メール未実施はそのまま未確認として記録する。

Failure Knowledge: メールに含まれるURLをhostname文字列だけで検査するとDNS rebindingで
private networkへ到達し得る。CORE web-gatherのpublic HTTP transportでDNS解決結果を検査し、
検査済みIPへ接続を固定する。redirectも同じ境界を通し、privateなIPが混在する回答は拒否する。
`public_transport_test.go`とGmail evaluatorの引用／URL検査でこのInvariantを検証する。
private Newsをgeneric staging／global registryへ流すと、owner filter前のsource projectionからprivate本文が漏れる。原因はprivate入力へ汎用stagingを適用することにある。private入力はKnowledge owner DBとowner-filtered SQL projectionだけを正本経路とし、writerと同一ownerのViewer review／replay検査で強制する。

Failure: AtlasとKnowledgeを同じGmail入力から扱う処理をAtlas packageへ置くと、保存先の責務と型のownerが混在する。
Problem: cross-destination intakeの変更がAtlas lifecycleと同期し、legacy Gmail型がAtlas正本へ戻る余地を作る。
Cause: Gmailの取得・評価・receipt・保存先振り分けをAtlas lifecycleの一部として扱うこと。
Lesson: 入力を束ねるownerと、各保存先のlifecycle ownerを分離し、既存公開APIだけで接続する。
Invariant: Gmail固有の型と保存制御は`internal/application/gmailintake`／`internal/infrastructure/gmailintake`に限定し、Atlas状態は`internal/application/backlog`だけが変更する。
Enforcement: package移動後のimport graph、公開`Service` API呼出し、旧`backlog/gmail_*.go`不在を検査する。
Tests: `internal/application/gmailintake`、`internal/infrastructure/gmailintake`の既存テストと、Atlas JSONL回帰、Step18 legacy-field gateで検証する。

Failure: 長い取得本文の要約結果と取得証拠のhash表現が異なると、正しい外部記事でも日次メール全体がblockedになった。
Problem: COREの`ExternalBodySummary.BodySHA256`は64文字hex、web-gatherの`ContentHash`は`sha256:`接頭辞付きであり、片側だけを正規化して比較していた。
Cause: LLM要約の意味結果と、取得本文のhash／引用／URL契約を同じ文字列表現だと仮定し、境界で両方を検証しなかった。
Lesson: LLMは要約と意味判断だけを返し、CORE Boundaryが両hashを既存validatorで正規化してからmalformed／不一致を拒否する。保存する取得証拠hashの表現は変更しない。
Invariant: 同一UTF-8本文のbare／`sha256:`付きhashは同値として受け入れ、形式不正または本文不一致は証拠不適合としてfail closedする。provider応答、hash、引用、状態変更、receipt保存をLLMの自己申告で完了扱いにしない。
Enforcement: `summarizeGmailEvidence`が`summary.BodySHA256`と`evidence.ContentHash`の双方へ`normalizedSHA256`を適用し、比較後にのみ要約本文を評価する。`gmailPolicyRevision`更新時は、出力のない旧terminal receiptを一回一件だけ同じCORE処理経路で再評価し、cursorを保持する。
Tests: `internal/application/gmailintake`の長文`SummarizeBody`統合テスト、bare／接頭辞付きhash同値、malformed／不一致拒否、旧blocked／skippedの最大1件再評価、prepared／complete除外、再評価失敗時のreceipt／cursor保持で検証する。fake providerはLLM契約のfixtureであり、実LLM実行の証拠とはしない。
