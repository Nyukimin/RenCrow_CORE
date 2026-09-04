# Step05 ThreadID production cutover evidence

評価時刻: 2026-09-04T15:30:54Z
Owner: RenCrow_CORE
Source revision: `f41a9070cbaae84c6b5c5f64abb69392df33916b`
Gate 7 deployed revision: `6388137bf5cec0bcf8be5809a58b79459dc677c5`

## 判定

本番の canonical route は、以下に記録した f41 cutover時点の production evidence として検証済みである。cutover receipt 自体の status は契約どおり `applied_not_runtime_verified` のままだが、その後に同一 installed runtime、service PID、listener、readiness、実際の Mio Agent request、canonical Session/Thread persistence を確認した。

Gate 7のsource/predeploy検証は完了し、`6388137bf5cec0bcf8be5809a58b79459dc677c5`をpush・deployした。migration-only sourceと8つのlegacy adapterを削除し、Makefileとstorage scriptsのThreadID migration dependencyをゼロにし、current format-4 CORE+Redis+Qdrant cohortとv2/v3/v4 restore-checkだけを残した。architecture guard、storage contract、対象package tests、およびbuild-profile-resolved 255-package vet/buildは通過し、`/srv/rencrow/backup`のread-only inventoryではformat-5 manifestを検出しなかった。

Gate 7の配備後検証も完了した。installed artifact、service PID、listener、readiness、timer、旧ac8 CLIのrecoverable retirement、frozen f41 recovery保持、および正規CORE/Mio routeの実Actor E2Eを同一証拠鎖で確認した。これによりStep05 ThreadIDとGate 7のsource/deploy implementation unitは完了とする。ただし、これはIdentity program全体またはfull-systemの完了を意味しない。production core-export、full restore-check、`s5_backup_restore`、`s5_full_system`、full-system verificationは未実施であり、別のdeferred boundaryとして残る。

## Production evidence

以下のreceipt、hash、process、storage、Actor factsはf41 cutover時点のproduction evidenceとして保持する。Gate 7 source retirement後の配備証拠とは扱わない。

- Successful cutover receipt: `/srv/rencrow/backup/recovery/threadid-efbbd44-20260904T083500Z/cohort-r3/cutover-command-r2.json`; file SHA-256 `e88da5e3411ff7c25347bf818e2de12ccb9a80eb79f4bb41549f6443cd80edd7`; self SHA-256 `e67f0454528fbd3cdb2be6dfb3d43ae6b1394a7b1fd4e7cea7a73c96b98a433d`; rollback retained by all five `.pre-threadid-558a0eb09be2` artifacts.
- Recovery cutover/rollback CLI is retained as cohort basename `rencrow-thread-migrate-f41a907`, mode `0700`, SHA-256 `170586082b6c2a0ab8df5c46a432187f15fdbf0684c236896a5d853cf5cfd28d`, Go revision `f41a9070cbaae84c6b5c5f64abb69392df33916b` and `vcs.modified=false`; recovery no longer depends on the older installed `ac8` tool.
- Installed runtime SHA-256 `39ab6767b382e1dc338ba0df176db118b0154ff69be4777453ebe55e425fda98`; service MainPID `1352721`; `NRestarts=0`; readiness `HTTP 200`, `ready=true`; resilience timer restored.
- Active Qdrant collection `rencrow_memory_1024_s5_558a0eb09be2`: green, 2326 points. Redis DB1 size 2; DB0 size 0.
- One actual Actor job completed: job `20260904-132442-1ada3aa8`, root trace `trc_01a06c97-cc57-7d11-b17b-4d7a885b575b`, ingress message `msg_01a06c97-cc57-7cfd-9f2a-235f8d7d28a0`, session `ses_01a06b5f-2f44-7e0e-aaa8-6b5a273bf0d0`, thread `thr_01a06c98-49fd-76a9-ae06-37d7eb0c3f21`, sequence 1, kind `user_conversation`, two turns, DB/outbox `completed`.
- Receipt file SHA-256 values: send `692306db886bbc398a2492e93e1292eefafd9b073f36e509f058fabdb4d813b5`; events `e0326d374b32c28ef3791ab029dd464bef54adb5b80c568fdabcbe0f98aa19ec`; DB `22086551b691c291b359934b16fc05687d19cbf8eb548846c067e42260541617`.

The DB `trace_id`, user message ID, and Agent message IDs are Step06 identities; they are distinct from the ingress root trace/message above. No equality between those identity sets is claimed.

## Gate 7 deployment and E2E evidence

- Source `6388137bf5cec0bcf8be5809a58b79459dc677c5` was pushed and deployed. The runtime/build/install SHA-256 is `aca12d4efcb13f586a2ad953b79f332da183bd9a5378f8ebce9a01ecc0e3b52f`; `go version -m` reported the exact revision and `vcs.modified=false`.
- Installed storage backup script SHA-256 is `1965c840b3bd677c4ee549de4eae23f74b261cc15acbebbe5975f3ffc2b0efdc`, restore-check script SHA-256 is `3a428d0a8fbad30adabce1b6c8c3d850f342a22985f2e3cb47dd671808a84e41`, and migration packager SHA-256 is `be806379512394033d02a9a78d97ac1092db2590f2807630bbacce9afa194152`.
- Only `rencrow.service` was restarted: old MainPID `1352721` is absent and new MainPID `1523484` is `active/running` with `NRestarts=0`; startup total was `179872ms`. Readiness returned HTTP 200 with `ready=true`, and `*:18790` was owned by PID `1523484`. `rencrow-storage-backup.timer` remained `active/waiting`.
- The installed `rencrow-thread-migrate` path is absent. The retired ac8 artifact is recoverable at `/srv/rencrow/backup/recovery/threadid-efbbd44-20260904T083500Z/cohort-r3/retired-installed-ac8/rencrow-thread-migrate-ac8`, mode `0700`, SHA-256 `367e82259a85f837dcb6419845dc5c98468c79824bca86cb99a6995d6c9154cd`. The frozen f41 recovery artifact remains mode `0700`, SHA-256 `170586082b6c2a0ab8df5c46a432187f15fdbf0684c236896a5d853cf5cfd28d`, unchanged.
- The bounded real route exited 0 with `recipient=mio`, `route=ANALYZE`, and an actual Mio->Kuro->Mio response. Job `20260904-152755-b9b79993`, ingress trace `trc_01a06d08-9b1c-7019-86cb-82e76d2f9d92`, and session `ses_01a06b5f-2f44-7e0e-aaa8-6b5a273bf0d0` completed. Canonical thread `thr_01a06d08-ea98-7f16-ae34-7935c2c94eb9` is sequence 2, kind `user_conversation`; prior thread `thr_01a06c98-49fd-76a9-ae06-37d7eb0c3f21` is closed at sequence 1.
- The turn receipt is `completed`; outbox `redis_projection` and `thread_followers` are `completed` with one attempt and no error. Redis DB1/DB0 are `2`/`0`; session `last_thread` equals the new thread, sequence, and kind. Qdrant is `green` with `2327` points.
- Owner-only evidence is retained under `/srv/rencrow/backup/recovery/threadid-efbbd44-20260904T083500Z/cohort-r3/gate7-6388137`; chat stdout SHA-256 `0cbc6b32a349e7dfb054c1b7652f35485f93c66687cc7bcfd300b795b9b700b8`, service receipt `a42248311cb546d41c553fcd5302b9668c6093c88a0ad1dedada901b5e548778`, health `65102295e1bef493fbed8ffff7ca3603dad45339b994588b4923ae22d85faa1b`, and integrity `bb2ede04c0ed3fd8ed372827e99b0158e4dc3637a0fd2298ca6279f429968136`.

### Restart side effect

The restart stopped the previously active IdleChat Forecast session `ses_01a06cfd-b6c1-787a-868f-9ddcbdf55a55`, which logged `dialogue_generation_failed`. This is a disclosed operational restart side effect, not an Identity failure or a broad health-pass claim.

## Deferred and Gate 7 boundary

No production `rencrow-storage-restore-check` or `core-export` was run; `s5_backup_restore` and `s5_full_system` remain deferred. The `s5_static_architecture` source/predeploy and deployed-runtime portions passed, including removal of the former migration source, zero ThreadID migration dependency, format-5 rejection, and the post-restart canonical route evidence above. The retained recovery cohort is a frozen artifact, not an ongoing installed owner, and its historical external snapshot route is not a future canonical capture route. Overall Identity and full-system verification remain open.

## CLI / Boundary / LLM classification

- **CLI**: current deterministic `core-export` creates the format-4 CORE+Redis+Qdrant cohort, `restore-check` accepts v2/v3/v4 and rejects v5, and `migrationpackage` publishes the existing package contract; inputs, hashes, counts, status, and failure codes are machine-readable. The retained one-shot recovery binary is a frozen cohort artifact, not an ongoing CLI owner. Gate 7 deployment and the bounded `rencrowctl chat` invocation also produced deterministic exit status and receipts.
- **Boundary**: CORE policy and persistence, active config, service/systemd lifecycle, external Redis/Qdrant projections, identity receipts, and rollback retention constrain every state-changing operation and runtime route. The deployed artifact, service readiness, listener ownership, timer, retired ac8 path, frozen f41 artifact, and post-restart real request are verified; production recovery remains explicitly deferred.
- **LLM**: the bounded E2E used the actual Mio->Kuro->Mio response path; no migration, identity, persistence, or cutover decision was delegated to an LLM.

Next bounded unit: continue the remaining Identity program work and, as a separate recovery operation, schedule the deferred production format-4 `core-export`/restore-check and full-system checks. Do not treat those deferred checks as part of the completed Gate 7 source/deploy unit, and do not replace or remove the frozen recovery cohort artifact.
