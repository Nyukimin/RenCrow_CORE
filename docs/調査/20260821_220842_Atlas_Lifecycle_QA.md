# Atlas Backlog Implementation Lifecycle QA

- 対象正本: `docs/RenCrow_Atlas_Backlog_Implementation_Lifecycle_仕様.md`
- 実装仕様入力: `docs/調査/RenCrow_Atlas_Backlog_Implementation_Lifecycle_実装仕様.md`
- 所有: RenCrow_CORE（runtime/API/Viewer/state/persistence）、RenCrow_CMD（thin CLI facade）
- 検証日: 2026-08-21 JST

| ID | User story / acceptance | Test level | Status | Evidence |
|---|---|---|---|---|
| A01 | Current/Radar/Backlog/Pipeline/Evidence/Modules を Viewer で閲覧できる | static + browser | pending | `viewer_static_contract_test.go`; production browser |
| A02 | source ref 付き intake は RADAR に入り、同一 source は重複しない | unit + HTTP | passed | `TestServiceIntakeDeduplicatesExactSource`; Atlas HTTP flow |
| A03 | owner API で RADAR から CANDIDATE へ遷移できる | HTTP E2E | passed | `TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence` |
| A04 | ADOPTED は Workstream/ImplementationUnit と durable singleton lease を作る | unit + persistence | passed | application/workstream JSONL/SQLite tests |
| A05 | 二件目は lease を奪わず QUEUED のまま待つ | unit | passed | `TestServiceAdoptCreatesUnitWorkstreamAndSingletonQueue` |
| A06 | evidence 不足・不正遷移は保存前に拒否される | unit | passed | `internal/domain/backlog/lifecycle_test.go` |
| A07 | 全 delivery stage を順に進み LIVE_VERIFIED で lease を解放する | HTTP E2E | passed | `TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence` |
| A08 | legacy Backlog JSONL/API は v2 projection と共存し、check_ok 単独で完了しない | unit + regression | passed | backlog infrastructure/domain/heartbeat tests |
| A09 | CMD は一 invocation 一 CORE request、write は bearer/control profile を必須にする | CLI HTTP | passed | `cli_atlas_test.go` |
| A10 | production build/restart 後に readiness と Atlas public API が応答する | live API | pending | deploy receipt |
| A11 | desktop browser で Atlas tab と六画面を確認できる | browser | pending | Playwright snapshot/screenshot |
| A12 | narrow viewport で主要情報が欠落せず横溢れしない | browser | pending | Playwright narrow screenshot |
| A13 | installed revision/hash と稼働 process が deploy artifact に一致する | live deploy | pending | version/hash/systemd evidence |

「passed」は記載 evidence が現時点で成功した項目だけを示す。production/browser の未確認項目が残る間は機能全体を完了扱いしない。
