# Step05 ThreadID production cutover evidence

評価時刻: 2026-09-04T13:40:29Z
Owner: RenCrow_CORE
Source revision: `f41a9070cbaae84c6b5c5f64abb69392df33916b`

## 判定

本番の canonical route は検証済みである。cutover receipt 自体の status は契約どおり `applied_not_runtime_verified` のままだが、その後に同一 installed runtime、service PID、listener、readiness、実際の Mio Agent request、canonical Session/Thread persistence を確認した。

Step05 は Gate 7 pending であり、完全終了ではない。migration-only source が残っており、現行の format-5 core-export/restore verifier は `rencrow-thread-migrate` の `quiesce-sqlite`、`capture-external`、`verify-external` を必要とする。したがって現時点で one-shot source 全体を削除すると backup/restore contract を壊す。後続で snapshot/verify consumer と one-shot cutover source を安全に分離するか、restore consumer の完了を証拠化してから Gate 7 を再評価する。

## Production evidence

- Successful cutover receipt: `/srv/rencrow/backup/recovery/threadid-efbbd44-20260904T083500Z/cohort-r3/cutover-command-r2.json`; file SHA-256 `e88da5e3411ff7c25347bf818e2de12ccb9a80eb79f4bb41549f6443cd80edd7`; self SHA-256 `e67f0454528fbd3cdb2be6dfb3d43ae6b1394a7b1fd4e7cea7a73c96b98a433d`; rollback retained by all five `.pre-threadid-558a0eb09be2` artifacts.
- Recovery cutover/rollback CLI is retained as cohort basename `rencrow-thread-migrate-f41a907`, mode `0700`, SHA-256 `170586082b6c2a0ab8df5c46a432187f15fdbf0684c236896a5d853cf5cfd28d`, Go revision `f41a9070cbaae84c6b5c5f64abb69392df33916b` and `vcs.modified=false`; recovery no longer depends on the older installed `ac8` tool.
- Installed runtime SHA-256 `39ab6767b382e1dc338ba0df176db118b0154ff69be4777453ebe55e425fda98`; service MainPID `1352721`; `NRestarts=0`; readiness `HTTP 200`, `ready=true`; resilience timer restored.
- Active Qdrant collection `rencrow_memory_1024_s5_558a0eb09be2`: green, 2326 points. Redis DB1 size 2; DB0 size 0.
- One actual Actor job completed: job `20260904-132442-1ada3aa8`, root trace `trc_01a06c97-cc57-7d11-b17b-4d7a885b575b`, ingress message `msg_01a06c97-cc57-7cfd-9f2a-235f8d7d28a0`, session `ses_01a06b5f-2f44-7e0e-aaa8-6b5a273bf0d0`, thread `thr_01a06c98-49fd-76a9-ae06-37d7eb0c3f21`, sequence 1, kind `user_conversation`, two turns, DB/outbox `completed`.
- Receipt file SHA-256 values: send `692306db886bbc398a2492e93e1292eefafd9b073f36e509f058fabdb4d813b5`; events `e0326d374b32c28ef3791ab029dd464bef54adb5b80c568fdabcbe0f98aa19ec`; DB `22086551b691c291b359934b16fc05687d19cbf8eb548846c067e42260541617`.

The DB `trace_id`, user message ID, and Agent message IDs are Step06 identities; they are distinct from the ingress root trace/message above. No equality between those identity sets is claimed.

## Deferred and Gate 7 boundary

No full `rencrow-storage-restore-check` was run; `s5_backup_restore` and `s5_full_system` remain deferred, while `s5_static_architecture` remains included and pending Gate 7 cleanup. The external snapshot tooling currently validates legacy-source projections only; it is not a valid future canonical capture route after migration-source retirement.

## CLI / Boundary / LLM classification

- **CLI**: deterministic snapshot, receipt, build, stage, cutover, rollback, and restore verification owned by RenCrow_CORE; inputs, hashes, counts, status, and failure codes are machine-readable.
- **Boundary**: CORE policy and persistence, active config, service/systemd lifecycle, external Redis/Qdrant projections, identity receipts, and rollback retention constrain every state-changing operation and runtime route.
- **LLM**: only the normal actual Mio Agent response in the one production request; no migration, identity, persistence, or cutover decision was delegated to an LLM.

Next bounded unit: design and verify the smallest owner-preserving split that leaves format-5 snapshot/verify reproducible while making the one-shot cutover source removable; then rerun the included Gate 7 architecture check before any deletion.
