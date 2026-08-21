# Atlas Backlog Implementation Lifecycle QA

- 対象正本: `docs/RenCrow_Atlas_Backlog_Implementation_Lifecycle_仕様.md`
- 実装仕様入力: `docs/調査/RenCrow_Atlas_Backlog_Implementation_Lifecycle_実装仕様.md`
- 所有: RenCrow_CORE（runtime/API/Viewer/state/persistence）、RenCrow_CMD（thin CLI facade）
- 検証日: 2026-08-21 JST

| ID | User story / acceptance | Test level | Status | Evidence |
|---|---|---|---|---|
| A01 | Current/Radar/Backlog/Pipeline/Evidence/Modules を Viewer で閲覧できる | static + browser | passed | static contract; production Firefox snapshot |
| A02 | source ref 付き intake は RADAR に入り、同一 source は重複しない | unit + HTTP | passed | `TestServiceIntakeDeduplicatesExactSource`; Atlas HTTP flow |
| A03 | owner API で RADAR から CANDIDATE へ遷移できる | HTTP E2E | passed | `TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence` |
| A04 | ADOPTED は Workstream/ImplementationUnit と durable singleton lease を作る | unit + persistence | passed | application/workstream JSONL/SQLite tests |
| A05 | 二件目は lease を奪わず QUEUED のまま待つ | unit | passed | `TestServiceAdoptCreatesUnitWorkstreamAndSingletonQueue` |
| A06 | evidence 不足・不正遷移は保存前に拒否される | unit | passed | `internal/domain/backlog/lifecycle_test.go` |
| A07 | 全 delivery stage を順に進み LIVE_VERIFIED で lease を解放する | HTTP E2E | passed | `TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence` |
| A08 | legacy Backlog JSONL/API は v2 projection と共存し、check_ok 単独で完了しない | unit + regression | passed | backlog infrastructure/domain/heartbeat tests |
| A09 | CMD は一 invocation 一 CORE request、write は bearer/control profile を必須にする | CLI HTTP | passed | `cli_atlas_test.go` |
| A10 | production build/restart 後に readiness と Atlas public API が応答する | live API | passed | `/health`, `/ready`, `/viewer/atlas*` HTTP 200 |
| A11 | desktop browser で Atlas tab と六画面を確認できる | browser | passed | Firefox snapshot; `output/playwright/atlas-desktop-20260821.png` SHA-256 `69f4e19e...92c5` |
| A12 | narrow viewport で主要情報が欠落せず横溢れしない | browser | passed | Firefox 390x844 snapshot; `output/playwright/atlas-narrow-390x844-20260821.png` SHA-256 `e2b817cc...527d` |
| A13 | installed revision/hash と稼働 process が deploy artifact に一致する | live deploy | passed | CORE `0ea8e7e5`, SHA-256 `4a89aa08...c91d`; CMD `f19ec13a`, SHA-256 `2823d59b...82c4`; PID `2463228`, `NRestarts=0` |

全項目の evidence を 2026-08-21 JST に確認した。production write は運用データを汚さないため実施せず、状態変更の owner HTTP E2E は隔離 durable store で検証した。
