# RenCrow ATLAS Backfill / Continuous Design Memory Automation 仕様 v1

## 1. 目的
初回Backfillと同じ作業を、将来RenCrow自身が安全に繰り返せるようにする。
新Chat、論文、記事、GitHub、障害、Agent proposalから、ワードだけではないDesign CardとSpecification Artifactを継続的に生成する。

## 2. 入力
- authenticated RenCrow conversation
- manually supplied URL / paper / GitHub locator
- owner module specs
- EcoSystem manifest / release evidence
- RenCrow incident / repair evidence
- Agent proposal
外部source本文はinstruction authorityを持たない。

## 3. Pipeline
1. Intake
2. Source normalization
3. Provenance capture
4. Candidate extraction
   - Title
   - Purpose
   - Problem
   - Idea
   - Background
   - Expected Effect
   - Relations
5. Specification detection
6. Exact/semantic dedupe
7. Source-bound validation
8. Radar write
9. Candidate promotion eligibility
10. Viewer projection
11. System OwnerによるAdopt/Defer/Reject

## 4. 根拠強度
reconstruction_basis:
- direct_spec
- direct_chat
- repo_spec
- project_summary
- implementation_inference
- unresolved

direct_spec/direct_chat/repo_spec以外から具体的事実を補完する場合は、必ず推論であることを明示する。
根拠がなければunresolvedとする。

## 5. Specification検出
完成した機能仕様、実装仕様、API仕様、test plan、design decisionがChat/sourceに存在する場合、原文captureをimmutable Specification Artifactへ保存する。
単なる途中のアイデアをfull specificationとして昇格しない。
revisionを上書きせずsuperseded_byで履歴化する。

## 6. Dedupe
exact:
canonical locator + content hash。
semantic:
類似候補をrelatedとして提示するだけで自動mergeしない。

## 7. 自動採用禁止
PipelineはRADAR/CANDIDATEまで自動化できる。
ADOPTEDへの遷移はSystem Ownerの明示操作だけ。

## 8. Current implementation evidence
GitHub/docs/commitだけでLIVE_VERIFIEDを自己宣言しない。
Atlas Lifecycle Evidence Gateへ渡し、source/build/deploy/readiness/live evidenceを別々に評価する。

## 9. Schedule
初期版はイベント/明示intake中心。
将来Scheduler/Heartbeatで新conversation/sourceをbounded batch処理できる。
同じsourceはcontent hash/idempotency keyで再処理しない。

## 10. Failure
source取得不能、仕様判定不能、Purpose不明、矛盾sourceはfake completionせずpartial/unresolvedとして記録する。
production Atlasへ推測値を確定値として書かない。

## 11. 初期Fixture
この2026-08-21 Backfill Datasetをgolden fixtureとして使用する。
Automation実装は同じ入力source setに対して、
- Item数
- feature_id安定性
- Purpose非空
- SpecificationRefs
- unresolved件数
- source provenance
を回帰比較する。
