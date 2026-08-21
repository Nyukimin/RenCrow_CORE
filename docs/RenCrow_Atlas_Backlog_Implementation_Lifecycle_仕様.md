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

現在のImplementation Unitが`LIVE_VERIFIED`または明示的な終端状態になるまで、次のImplementation Unitを開始しない。

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

## 3.3 各owner module

実装対象機能の詳細仕様、code、test、Config、migration、内部contractは各owner repositoryを正本とする。

Atlasは詳細仕様を複製せず参照する。

## 3.4 RenCrowViewer

CORE `/viewer` にAtlas画面を追加する。

PORTALには配置しない。

Atlasは開発・運用・内部構造を扱うためDebug Viewerの責務とする。

---

# 4. Atlasの論理構造

Atlasは次の5面を持つ。

## 4.1 Current

現在RenCrowに存在する機能。

表示例:

```text
UserMemory
Owner        RenCrow_CORE
Concept      ADOPTED
Delivery     LIVE_VERIFIED
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

`DONE`はAtlasへの最終反映まで完了した状態。

---

# 6. 採用から実装まで

## 6.1 採用

System Ownerが認証済みrequestとしてAtlas Itemを明示的に採用し、COREは同じrequest内で
`ADOPTED`または理由付き`REJECTED`／`BLOCKED`を確定する。人の追加判断を待つ中間状態は作らない。

採用時に最低限以下を確定する。

* item_id
* title
* purpose
* owner module
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

---

# 7. Implementation Unit標準工程

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

すべての必須Evidenceが成立した場合のみ、

```text
Delivery State = LIVE_VERIFIED
```

へ遷移する。

---

## Stage 11: Done

Atlas Currentへ状態を反映し、Implementation Leaseを解放する。

その後、次のADOPTED itemを開始可能にする。

---

# 8. 失敗時の扱い

各Stage失敗時は原因を固定する。

```text
failure
 ↓
root cause
 ↓
revision
 ↓
失敗によって無効になった最も早いStageへ戻る
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

有限回のrevisionでも成立しない場合は`BLOCKED`で閉じる。

`BLOCKED`は待機状態ではなく終端結果である。

既定ではActive UnitがBLOCKEDになった場合、Implementation Queueを停止する。

後続項目を黙って飛ばさない。

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
```

一つでも欠ける場合はDONEにしない。
