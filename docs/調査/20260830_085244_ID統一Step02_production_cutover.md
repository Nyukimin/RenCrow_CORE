# ID統一 Step 02 production cutover receipt

- 実施日時: 2026-08-30 08:52 JST
- Source branch: `identity/02-event`
- Source revision: `82202a3b0eb70e8c5fb988dcd4a32fbc45289e93`
- Installed binary SHA-256: `10b5d769000cc9353bebef1cab760225a8f34eba3fe331534e67fa0ac9f4be4e`
- Active config: `/home/nyukimi/.rencrow/config/core.yaml`
- Canonical Event Store: `/srv/rencrow/db/core/databases/events/event_store.db`
- Rollback root: `/srv/rencrow/db/core/backups/identity-step02-cutover/20260829T233848Z`

## Migration

Writerをruntime maskで停止し、旧binary、active config、AI Workflow SQLite、
SuperAgent SQLiteをrollback rootへ保存した。live JSONLはSQLiteより古い別期間の事実を
914件保持していたため、重複backupとして削除せず、SQLiteとは別のchecksum-bound batchで移行した。

| Source | Input | Converted | dropped run-as-parent | Canonical set SHA-256 |
| --- | ---: | ---: | ---: | --- |
| SQLite | 564 | 564 | 103 | `d5e148b706ab6a1795fc1f32092c766c07a056d008f27e8f6a6e370b9b356bfc` |
| JSONL | 914 | 914 | 16 | `d391061018bbca4f351ba40bf2b3d601b025967afe9ca6d22561c92e350d8f0f` |

除外reasonはいずれも`legacy_parent_event_id_referenced_run_id`である。apply後の同一入力再実行は
両batchとも`noop`となった。

## Legacy deletion and integrity

- `ai_workflow_event` table: zero
- `trace_event` table: zero
- live `ai_workflow_event.jsonl`: absent。rollback rootへ退避済み
- live `trace_event.jsonl`: absent。rollback rootへ退避済み
- Canonical Event count: 1,559
- component count: `ai_workflow=100`、`orchestrator=71`、`superagent=1,388`
- `PRAGMA quick_check`: `ok`
- invalid `envelope_json`: zero
- orphan dependency: zero
- 旧`ops/event_store.db`: absent

## Runtime identity and connectivity

- `rencrow.service`: active/running、PID `3787207`、`NRestarts=0`
- listener: `*:18790`、owner PID `3787207`
- process executableとinstalled binaryのSHA-256一致
- `/health/ready`: ready
- `/health`: Mio、Worker、Shiro、Kuro、Midori、Visionがok
- 正規`RenCrow_CMD -> CORE -> Mio` Chat: `CHAT` routeでMioの実応答を確認
- 正規`RenCrow_CMD -> CORE -> Mio -> Shiro` Chat: OPS routeでShiro実行、Mio最終応答を確認
- Viewer、AI Workflow、SuperAgent、Memory、Scheduler、DCI、IdleChat、Atlas、Capability API: HTTP 200
- STT owner readiness: HTTP 200
- active TTS owner `http://192.168.1.205:7870/health/ready`: HTTP 200、Irodori ready

起動時間は最終起動で90.602秒だった。内訳の主要部分は
`chatgpt_import_reconcile=65.546秒`で、Event Store migrationの失敗ではない。

## Unverified boundary

`/viewer/verification/summary`はactive configでVerification pipelineがdisabledのためHTTP 503を返した。
これはtyped fail-closed境界として確認したが、Verificationの機能E2E成功ではない。Verificationを有効化する
別のpolicy/config変更は本cutoverへ混ぜていない。また、VoiceはSTT/TTS owner readinessまでで、実音声の
CORE往復・再生は本cutoverでは実行していない。このため正本Gate 6全項目を100%通過したとは扱わない。
