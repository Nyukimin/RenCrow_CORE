# ID統一 Step 02 root Trace修復 receipt

- 実施日時: 2026-08-30 10:06 UTC
- Source branch: `identity/02-event`
- Source revision: `d69c0437e9885658f01b0ee9beb002ea6c416bef`
- Installed binary SHA-256: `807e40c590ced77bcf118a50d9533220d846667adfbd9230acc4287724772030`
- Previous binary backup: `/home/nyukimi/.local/bin/rencrow.before-d69c043`
- Active config: `/home/nyukimi/.rencrow/config/core.yaml`
- Canonical Event Store: `/srv/rencrow/db/core/databases/events/event_store.db`

## Failureと修正境界

旧runtimeはOrchestrator Eventの`TraceID`へ`JobID`を渡していた。Canonical Event adapterは
非canonical値をEventごとに別の`TraceID`へ置換したため、同じ外部TriggerのEventが分断された。
Viewer ingressでCanonical `TraceID`を一度だけ生成し、Message／Distributed Orchestrator、
SuperAgent、AI Workflowへ保持して伝播する実装へ修正した。`TraceID == JobID`を拒否するtestを追加した。

## Source verification

- `go test ./internal/application/orchestrator ./internal/adapter/viewer ./cmd/rencrow ./internal/infrastructure/persistence/eventmigration ./internal/infrastructure/persistence/eventstore ./modules/core -count=1`: passed
- `go vet ./...`: passed
- `go build ./cmd/rencrow ./cmd/rencrow-event-migrate`: passed
- `git diff --check`: passed
- 全package走査では、旧TTS fixtureが`TraceID == ResponseID`を要求した2件だけがREDとなった。fixtureをcanonical Trace契約へ更新後、owner package `cmd/rencrow`全体がpassed。その他のpackageは同じsource treeでpassedした。

## Deploy and restart

- user service: `rencrow.service`
- PID: `105012`
- `NRestarts=0`
- listener: `*:18790`、owner PID `105012`
- process executableとinstalled binaryのSHA-256一致
- `/health/ready`: HTTP 200、`ready=true`
- `startup_total=152577ms`
- `chatgpt_import_reconcile=121887ms`

## Production E2E

### Viewer -> Mio -> SuperAgent

- JobID: `20260830-100446-2d02b55a`
- TraceID: `trc_01a05220-f2ee-7bdf-9df9-e5106c0e2fa9`
- Viewer受付、MessageOrchestrator開始／完了、Mio実応答が同じTraceID
- Event Store: 32 events、2 components、distinct Trace 1、wrong Trace 0

### Viewer -> Mio -> Heavy -> AI Workflow -> SuperAgent

- JobID: `20260830-100545-b63c94b8`
- TraceID: `trc_01a05221-db98-7239-9f3b-7d0d9745dc39`
- 正規`ANALYZE` routeでHeavy実応答まで完了
- Event Store: 23 events、3 components、distinct Trace 1、wrong Trace 0
- components: `orchestrator`、`superagent`、`ai_workflow`

配備後のEvent Storeは`PRAGMA quick_check=ok`、dependency orphan=0だった。

## Append-only historical boundary

修正前のJob `20260830-094130-f66c4c10`は、同一JobのEventごとに別Traceを持つことを
読み取り専用queryで確認した。Canonical Event Storeはappend-onlyであり、この修復では既存Eventを
更新、削除、再生成していない。既存分断Eventを正式migrationで再識別するか、immutable incident
evidenceとして保持するかは、checksum-bound snapshot、決定的mapping、rollbackを持つ別Gateで決定する。
この未決定境界があるため、Step 02全体を完了とは扱わない。

また、Voice全工程を同一Traceへ接続する終端はStep 17であり、本receiptは実音声再生成功を主張しない。
