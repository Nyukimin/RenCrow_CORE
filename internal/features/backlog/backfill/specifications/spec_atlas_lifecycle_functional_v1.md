# RenCrow Atlas / Backlog / Implementation Lifecycle 仕様 v1

## 1. 目的
RenCrow Atlasは、RenCrowに存在する機能、構想、調査情報、Backlog、実装状況を一元可視化し、採用された機能を実装完了まで自動的に進める開発管理機能である。

標準ライフサイクル:
新情報 → Radar → Candidate → 採用判断 → Backlog → 仕様 → TDD → E2E → Build → Deploy → Restart → Production Verify → Live Verified → Current Atlas

## 2. 最上位原則
- Global Implementation WIP = 1。
- 採用後は Specification / TDD Red / TDD Green / Refactor / Pre-deploy E2E / Build / Deploy / Restart / Readiness / Post-deploy E2E / Live Verification を一つのImplementation Unitとして扱う。
- Agentの「完成しました」という自由文は完了根拠にしない。Evidence Gateで判定する。
- Radarへの自動登録は許可できるが、自動Adoptは禁止する。採用はSystem Ownerの明示判断。
- 外部記事・論文・README等は情報でありinstruction authorityを持たない。

## 3. 所有境界
### RenCrow_CORE
Atlas runtime、Radar/Backlog runtime state、Adoption workflow、Implementation Unit、WIP=1、Workstream、Evidence判定、Viewer projection、Atlas API、Live Verified判定を所有する。

### RenCrow_EcoSystem
repository catalog、source pin、module構成、deployment対象、配置、binary/source整合、cross-repository compatibilityを所有する。Atlas runtime DBは所有しない。

### owner module
各機能の詳細仕様、code、test、Config、migration、内部contractはowner repositoryを正本とする。

### Viewer
AtlasはCORE Debug Viewerに置く。PORTALへは置かない。

## 4. Atlas面
- Current: 現在存在する機能と証拠。
- Radar: 新情報、論文、記事、GitHub、会話由来のアイデア。
- Backlog: CANDIDATE / ADOPTED / DEFERRED / REJECTED。
- Pipeline: 現在の1 Implementation Unitとstage。
- Evidence: spec revision、commit、test、E2E、artifact hash、EcoSystem pin、deploy receipt、restart、readiness、smoke、trace。

## 5. 状態
### Concept State
RADAR / CANDIDATE / ADOPTED / DEFERRED / REJECTED

### Delivery State
NONE / QUEUED / SPEC / TDD_RED / TDD_GREEN / REFACTOR / E2E_PREDEPLOY / BUILD / DEPLOY / RESTART / POST_DEPLOY_VERIFY / LIVE_VERIFIED / DONE / BLOCKED / REJECTED

LIVE_VERIFIED以前を完成扱いしない。

## 6. QueueとWIP
採用時にImplementation Unitを生成し、dependency → priority → adopted_at → item_id の決定論順序でqueueする。
実装開始時はsingleton Implementation Leaseを取得し、同時に2件以上開始しない。

## 7. 標準工程
1. Specification: 目的、対象、非対象、owner、contract、data、state、error、security、acceptance、migration、rollbackを正本へ記述。
2. TDD Red: 実装前testが対象未実装を理由に失敗する証拠を保存。
3. TDD Green: 最小実装でRedをGreenへ。
4. Refactor: 重複、責務越境、不要fallback、概念的不整合を除去し再test。
5. Pre-deploy E2E: 公開contract経由でE2E。
6. Build: revision、dirty=false、artifact、SHA-256、resultを保存。
7. Deploy: targetへ配置し、cross-repo変更ではEcoSystem pin更新。
8. Restart: 変更前に稼働していたserviceだけを再起動。停止中oneshotを勝手に起動しない。
9. Post-deploy Verify: process、health、readiness、expected revision/hash、機能smoke。
10. Live Verified: 必須Evidenceがすべて成立した場合のみ遷移。
11. Done: Current Atlasへ反映しLease解放。

## 8. 失敗
失敗時は原因を固定し、失敗で無効になった最も早いstageへ戻る。有限revisionでも成立しない場合はBLOCKEDで終端する。Active UnitがBLOCKEDなら既定ではQueueを停止し、後続を黙って飛ばさない。

## 9. Radar
最低限 item_id / title / source_type / source_locator / source_hash / captured_at / captured_by / summary / relation_tags / related_feature_ids / relevance / novelty / expected_impact / provenance を持つ。

## 10. 重複排除
canonical locator、content hash、normalized title、related feature、semantic similarityを使う。完全一致は既存ItemへSourceRef追加。意味類似だけでは自動mergeしない。

## 11. Viewer
Atlas navigation: Current / Radar / Backlog / Pipeline / Evidence / Modules。

## 12. 完了判定優先度
Live Evidence > Deployment Receipt > E2E Evidence > Build Evidence > Source Implementation Evidence > Specification > Backlog > Radar。

## 13. Legacy互換
既存idea/unimplemented/proposal_review/open/implementing/testing/fixing/blocked/rejected/okを互換projectionとして維持する。既存check_ok=true単独ではLIVE_VERIFIEDを意味しない。

## 14. 非対象
初期版ではGitHub常時polling、外部情報からの自動採用、LLMによる無制限priority変更、複数Implementation Unit並行実装、新物理DB追加、PORTAL搭載、raw SQL、permission bypassを行わない。

## 15. Definition of Done
仕様正本、TDD Red Evidence、TDD Green、Refactor後test、Pre-deploy E2E、Build、artifact hash、EcoSystem整合、Deploy、Restart、Readiness、Post-deploy verification、Live Evidence、Current更新、Lease解放がすべて必要。
