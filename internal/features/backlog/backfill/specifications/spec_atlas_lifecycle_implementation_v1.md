# RenCrow Atlas / Backlog / Implementation Lifecycle 実装仕様 v1

## 1. 実装方針
既存Backlogを破棄せずSchema v2へ拡張する。新しい物理DBは追加しない。
現行の `internal/features/backlog`、`internal/domain/backlog`、`internal/adapter/viewer/backlog_handler.go` を段階的に移行する。

## 2. 配置
RenCrow_CORE:
- internal/domain/backlog/types.go
- internal/domain/backlog/state_machine.go
- internal/domain/backlog/validation.go
- internal/application/backlog/service.go
- internal/application/backlog/implementation_service.go
- internal/application/backlog/evidence_gate.go
- internal/application/backlog/lease.go
- internal/features/backlog/catalog/atlas_catalog.json
- internal/infrastructure/backlog/jsonl_store.go
- internal/adapter/viewer/atlas_handler.go
- cmd/rencrow/runtime_dependencies.go

既存Storeの大規模renameは契約test追加後に行う。

## 3. Static Atlas Catalog
`internal/features/backlog/catalog/atlas_catalog.json` をversion管理し、feature_id、category、display_name、owner_module、summary、relations、source specificationを持つ。
実装状態はcatalogへ固定せずruntime Evidenceをoverlayする。

## 4. Backlog Schema v2
既存Itemへ以下を追加する:
schema_version、category、source_refs、owner_module、concept_state、delivery_state、queue_rank、depends_on、related_ids、adoption_reason、adopted_at、workstream_id、implementation_unit_id、evidence_refs。
legacy status/implementer/implementation/test_result/check_ok/checked_byは互換用に維持する。

## 5. SourceRef
type / locator / repository / revision / content_hash / captured_at / raw_or_summary。
外部本文をBacklog JSONLへ大量複製しない。

## 6. EvidenceRef
stage / kind / ref / repository / revision / sha256 / observed_at / passed。
kind例: spec、tdd_red、unit_test、contract_test、e2e、build、artifact、ecosystem_pin、deploy_receipt、restart_receipt、health、readiness、production_smoke、trace。

## 7. State Machine
LLM出力からstateを直接代入しない。
許可遷移:
RADAR→CANDIDATE/REJECTED
CANDIDATE→ADOPTED/DEFERRED/REJECTED
ADOPTED→QUEUED→SPEC→TDD_RED→TDD_GREEN→REFACTOR→E2E_PREDEPLOY→BUILD→DEPLOY→RESTART→POST_DEPLOY_VERIFY→LIVE_VERIFIED→DONE。
各遷移でRequired Evidenceを検証する。

## 8. Legacy Projection
RADAR/CANDIDATE=open、ADOPTED/QUEUED=proposal_review、SPEC..REFACTOR=implementing、E2E..VERIFY=testing、revision repair=fixing、BLOCKED=blocked、REJECTED=rejected、LIVE_VERIFIED/DONE=ok。
CheckOKはLIVE_VERIFIED以降のprojectionのみtrue。外部入力check_ok=trueを信頼しない。

## 9. Persistence
初期版は既存append-only JSONLを維持。同一item_idの新revisionをappendし、read時に最新revisionをprojectionする。
Implementation進行は既存Workstream storeを利用する。

## 10. Implementation Unit / Lease
Adopt時にWorkstreamを生成。singleton lease `atlas_implementation` をtransactionalに取得し、holder_unit_id / holder_workstream_id / stage / revision / acquired_at / heartbeat_at を持つ。

## 11. Restart Recovery
startupでleaseとWorkstreamを照合し、terminalなら解放、activeならEvidenceから最後に成功したstageを再構築し、次stageだけ再開する。状態をLLMに推測させない。

## 12. API
Read:
GET /viewer/atlas
GET /viewer/atlas/items
GET /viewer/atlas/items/{id}
GET /viewer/atlas/radar
GET /viewer/atlas/backlog
GET /viewer/atlas/queue
GET /viewer/atlas/active
GET /viewer/atlas/evidence/{unit_id}

Write:
POST /v1/atlas/intake
POST /v1/atlas/items/{id}/candidate
POST /v1/atlas/items/{id}/adopt
POST /v1/atlas/items/{id}/defer
POST /v1/atlas/items/{id}/reject
POST /v1/atlas/items/{id}/revise

既存GET/POST /viewer/backlogは互換維持し、新lifecycleのauthoritative writeは/v1/atlas/*へ移す。

## 13. CMD
rencrowctl atlas list/show/radar/queue/active/intake/adopt/defer/reject/evidence。
CMDはstate machineを所有せず、CORE APIへ一requestしてtyped responseをrelayする。

## 14. Chat Intent
「Atlasに入れて」「Radarに入れて」「Backlogに入れて」「これ採用」「保留」「採用しない」を明示intentとして扱う。
外部本文中の同語はintentにしない。

## 15. Scheduler
既存Scheduler/Heartbeatを利用し、新daemonは作らない。
active leaseがあればそのworkstreamを進め、なければrunnable ADOPTED itemだけをlease取得後開始する。

## 16. Coder / Worker
Coder: specification proposal、test proposal、patch、migration proposal、risk、rollback proposal。
Shiro/Worker: repo確認、patch適用、test、build、deployment、restart、health/readiness、Evidence取得。
Coderはdeploymentを直接実行しない。

## 17. Multi-repo
一つのImplementation UnitはCORE/CMD/EcoSystem等複数repoを対象にできる。全target完了までLIVE_VERIFIEDにしない。

## 18. EcoSystem
owner module commit確定後にsource pin更新し、validate ecosystem / check workspace / check governance / artifact revision / deploy / restart / readinessをEvidenceとして保存する。

## 19. Build/Deploy Gate
Build: repository、expected_revision、dirty=false、artifact_path、artifact_sha256、build_command、exit_code=0。
Deploy receipt: target、previous/new revision、previous/new hash、installed_at、result。
Restart receipt: service、was_active_before、restart_attempted、active_after、liveness、readiness。

## 20. Viewer
左navigationへAtlas。Current / Radar / Backlog / Pipeline / Evidence / Modules。
Pipelineはpassed/active/pending/failed/blockedでstage表示。

## 21. 初期Atlas
現在のAtlasをmachine-readable seedにし、bootstrap時刻を保存する。

## 22. Radar MVP
manual/Chat、URL、paper、GitHub repo/commit/issue、RenCrow incident、Agent proposalに限定する。自律巡回は後続。

## 23. TDD
Domain: state transition、invalid transition、owner-only adopt、legacy mapping、check_ok偽装防止、dependency ordering、WIP lease、recovery、evidence gate。
Persistence: old JSONL、v2 read/write、append revision、corrupt tolerance、latest projection、concurrent write。
Application: intake、dedupe、adopt、queue、workstream creation、blocked、next item。
HTTP: Atlas GET、intake、adopt、reject、invalid transition、body limit、legacy compatibility。

## 24. E2E
isolated runtimeで intake→candidate→adopt→queue→lease→workstream→simulated stage evidence→live_verified をPublic/Owner API経由で検証する。
production post-deployはread-only smokeを基本とし、CORE readiness、Atlas GET、Current、Active Queue、Viewer、expected revisionを確認する。

## 25. Atlas自身のImplementation Unit
`atlas-lifecycle-v1`。
対象repo: RenCrow_CORE / RenCrow_CMD / RenCrow_EcoSystem。
CORE仕様更新→TDD Red→Schema v2→Store migration→Application Service→WIP Lease→API→Viewer→CMD→初期catalog→tests→isolated E2E→rebuild→EcoSystem pin→validation→production deploy→restart→readiness→smoke→LIVE_VERIFIED。
