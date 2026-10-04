# 保存host・物理ディスク移動 Runbook

本書は、CORE実行hostとdurable storage hostを分離し、現行の物理ディスクをれんが後で移動するための実行手順である。受入状態と証跡の正本は [20261003_storage_host_separation_受入記録.md](調査/20261003_storage_host_separation_受入記録.md) に置き、本書に別の進捗台帳は作らない。

この手順はformat、repartition、filesystem変換、label変更、`fstab`編集を行わない。物理的なsync・unmount・取り外し・再接続はれんが実行する。

## 固定する対象

| 役割 | label | UUID | filesystem | 現在のmount |
| --- | --- | --- | --- | --- |
| CORE data | `RENCROW_DB` | `05efc373-a71e-4b3e-9a53-b57efe043c43` | ext4 / 1.8 TiB | `/srv/rencrow/db` |
| backup | `RENCROW_BACKUP` | `8067e90c-fd97-4290-bcc3-a32cdc76f034` | ext4 / 1.8 TiB | backup実行中のみ`/srv/rencrow/backup` |

`/dev/sdd1`や`/dev/sdb1`は現在hostの観測値であり、移動後の識別子に使わない。label・UUID・filesystem・容量の4点が一致しなければ、mountも起動もしない。

移動先OSが未決定の場合、ext4媒体をnative Windowsへ直接mountする前提で進めない。検証済みの構成は、ext4を扱えるstorage hostが媒体を所有し、Windowsを含むCORE hostが認証付きRPCをSSH tunnel経由で使う構成である。

## 中止条件

次のいずれかが満たされない場合、物理操作へ進まない。

1. 受入記録のA1–A10が、移動に必要な最終証跡と対応していない。
2. Linux関連test/build、Windows公式no-argument runner、別hostのactual Shiro E2E、CORE再起動後の履歴・Task/Run ID確認のいずれかが未完了である。
3. 配備対象binaryのSHA-256とsource snapshot manifestが一致しない。
4. storage RPC tokenがAgent OPS tokenと別でない、またはLinuxでprivate directory `0700`／file `0600`、Windowsでcurrent-user-only ACLになっていない。
5. 定時backupもしくはrestore-checkが実行中である。
6. user serviceだけでなくroot側を含むwriter・timer・保持fileを特定できない。
7. writer停止後の最終disk snapshotとprivate HOME cohortのいずれかがない。
8. data媒体とbackup媒体のlabel／UUIDを一意に対応させられない。

2026-10-03 19:58:07 UTCに完了した定時backupは、`Result=success`、`ExecMainStatus=0`、`inactive/dead`、snapshot `/srv/rencrow/backup/snapshots/core/recent/20261004-030243`の検証まで確認済みである。ただしHOMEのTask等は含まないため、これだけで移動を開始しない。

2026-10-03 22:08 UTCにwriter停止下の最終cohortを`/home/nyukimi/RenCrow/RenCrow_CORE/Tmp/test-runtime/_storage-e2e/cohort-20261004/db-core`へ固定した。`cold-finalization-receipt.json`はsource停止前後metadata一致・final rsync RC 0・user-visible holderなし・24 HOME roots／Task `.writer.lock`を記録し、SHA-256は`488a4d7f034dcec3f2a07803b2800512b56604e5e366c26126e6ca78f3a0a39e`である。private HOME archive `/srv/rencrow/db/migration-private/20261003-220804/private-home-cohort.tar.gz`はSHA-256 `5bdc93f2aed8eb1ca53ed1b967b09b28be9158f98cc58045281aa48aaae175a6`で、隔離restoreの全regular-file SHA／size／mtime・symbolic-link metadataが一致した。20 SQLiteのread-only `PRAGMA quick_check` receipt SHA-256は`6ee82aee34fa9e4ed3954c3424a6eff239c75e1e053a5d65c815d696946d3507`で全PASS。これらはE2E用のcoherent cohortとrollback入力の準備証拠であり、rootが所有するopen-fileの否定、物理移動、新sourceのAgent E2E、または新binary配備成功の代替ではない。

同じprivate rootに、変更していないbase `acda6571`の現在配備Linux/amd64 dynamic ELFをbaseline rollback専用入力として保存した。pathは`/srv/rencrow/db/migration-private/20261003-220804/rencrow-linux-amd64-baseline-acda6571`、size 129,974,997 bytes、SHA-256 `c1c0d366d5c666b4d027f5b96ee5f8fffe8a61ff5086de8f66630b3826cda7ca`である。`baseline-runtime-rollback-input.json`（SHA-256 `ad70c71dc23595acc0966be0c06141a431b6dcc929eeb27966ff97c45241475b`）はcopy／fsync／UUID照合を記録する。このbinaryは元の互換Linux runtimeへのrollbackにだけ使い、新版、portable release、または他OS検証品として使わない。

## 1. 成果物と経路を固定する

1. source snapshot manifest、Linux/Windows binary SHA-256、使用configのsecretを含まないhash、storage/runtime host名、検証時刻を1つのexecution receiptへ記録する。secretの値は記録しない。
2. 製品LLM経路が `CORE -> RenCrow_LLM Gateway -> Runtime -> Backend -> Model` であることを実traceで確認する。開発に用いたcoding assistantは製品経路に入れない。
3. Windows検証時はcanonical CORE checkout内のpromptを元にする。現行配備promptのsymlink farmにあるoptional `idle_chat/mio.md`／`idle_chat/shiro.md`の破損linkを、必須prompt欠落と混同したりコピーで隠したりしない。
4. 以下のIDを移動前cohortとして記録する。既存の読み取り参照だけをbackup成功と扱わない。
   - 実Shiro conversation/session/thread ID
   - Task ID・Run ID・actor・route・status・writer generation
   - storage writer generationと有効なreceipt/fenceの最大generation

## 2. writerとtimerを停止する

現在hostで次のread-only inventoryを取り、先に記録した一覧と比較する。非対話SSHの`PATH`に`~/.local/bin`が入ると仮定せず、helperは絶対pathで扱う。

```bash
systemctl --user list-units --type=service --state=running
systemctl --user list-timers --all
systemctl --user show rencrow-storage-backup.service \
  -p ActiveState -p SubState -p Result -p ExecMainStatus -p MainPID
/home/nyukimi/.local/bin/rencrow-storage-configure inspect --json
lsblk --json -o PATH,LABEL,UUID,FSTYPE,SIZE,MOUNTPOINTS
findmnt --json --target /srv/rencrow/db
```

実際のdurable-store manifest・service unit・open-file inventoryからwriter集合を決める。現在の既知candidateはCORE、TRADE、Image、GAMES observer、movie/person-related catalog、trade learningとこれらを起動し得るtimerである。この固定一覧だけに頼らず、実行時の追加unitを含める。

1. backup serviceが`inactive/dead`であることを確認し、writerやbackupを再起動し得るtimerを停止する。
2. user serviceのwriterを停止する。停止対象を実行receiptに列挙し、停止前にactiveだったunitも別に記録する。
3. 管理者権限でroot側writerを停止する。非対話sudoがpasswordを要求した場合はこのgateを省略しない。
4. CORE portのlistenとhealth応答が消え、関係processが残っていないことを確認する。Gateway等の非writerを停止する必要がある場合は、その理由と経路への影響を記録する。
5. 次を実行し、mount process以外の保持が1件でもあれば中止する。

```bash
sudo fuser -vm -- /srv/rencrow/db
findmnt --json --target /srv/rencrow/db
```

## 3. 最終snapshotとprivate HOME cohortを固定する

writer停止後に最終snapshotを作る。既存backup binaryを直接、または別のbackupと並行して起動せず、実行時のuser unitの`ExecStart`・binary SHA-256・対象source rootを照合したうえで1回だけ起動する。backupがCORE等を戻す設計なら、完了後に再度writerを停止する。

backup完了は次のすべてで判定する。

- `ActiveState=inactive`、`SubState=dead`、`Result=success`、`ExecMainStatus=0`
- snapshotのchecksum/archive検証と隔離抽出が成功
- Conversation SQLiteのintegrity checkと必要directoryの存在確認が成功
- backup媒体がhelperによって通常状態へ戻った
- snapshot path、receipt、helper/binary SHA-256が実行receiptにある

disk snapshotと別に、少なくとも次のprivate cohortを保存する。

```text
/home/nyukimi/.rencrow/config
/home/nyukimi/.rencrow/credentials
/home/nyukimi/.rencrow/workspace/tasks
/home/nyukimi/.rencrow/workspace/control
/home/nyukimi/.rencrow/workspace/prompts
/home/nyukimi/.rencrow/workspace/logs
/home/nyukimi/.rencrow/workspace/state
/home/nyukimi/.rencrow/workspace/policies
/home/nyukimi/.rencrow/workspace/jobs
/home/nyukimi/.rencrow/workspace/tools
/home/nyukimi/.rencrow/workspace/knowledge
/home/nyukimi/.rencrow/workspace/memory
/home/nyukimi/.rencrow/workspace/viewer_uploads
/home/nyukimi/.rencrow/workspace/execution_report.jsonl
/home/nyukimi/.rencrow/workspace/orchestrator_event_log.jsonl
/home/nyukimi/.rencrow/workspace/orchestrator_event_gc.jsonl
/home/nyukimi/.rencrow/state
/home/nyukimi/.rencrow/resilience
/home/nyukimi/.rencrow/.env
/home/nyukimi/.rencrow/llm_ops.env
/home/nyukimi/.config/rencrow/tts
/home/nyukimi/.local/share/rencrow/config/durable-stores.json
/home/nyukimi/.local/share/rencrow/config/durable-stores.d
/home/nyukimi/.local/share/rencrow/prompts
```

cohortは、operatorが用意した暗号化済みまたは同等のprivate安全性を持つ絶対pathに、symlink、ACL、xattr、数値UID/GIDを保持して作る。secretをstdout、Git、source snapshot、public archiveへ入れない。復元テストは隔離directoryに展開し、実HOMEを上書きしない。`workspace/recovery-*`、`tasks.pre-step10`、`workspace/tmp`、旧backup／legacy tree、model cache、runtime log以外のdownload済みarchiveはこのactive cohortに混ぜない。一方、`workspace/logs`はpolicy decision・scheduler・acklog・workflow・tool mediation等のactive durable stateを含むため、一律にruntime logとして除外しない。

Task cohortの`.writer.lock`は一律除外しない。世代counter、receipt、active/released fence、Runの最大generationを同じcohortで保存し、復元時にその最大値より小さいwriterをadmitしない。

## 4. 取り外し（れんの操作）

1. 最終snapshotとprivate cohortの隔離restoreが成功し、writer/timerが再び停止していることを確認する。
2. `findmnt`で対象がUUID `05efc373-a71e-4b3e-9a53-b57efe043c43`の`/srv/rencrow/db`だけであることを解決する。
3. 管理者権限の`fuser`に保持がないことを再確認する。
4. 次をれんが実行する。対象解決が一致しなければ実行しない。

```bash
sync
sudo umount -- /srv/rencrow/db
findmnt --target /srv/rencrow/db
lsblk -o PATH,LABEL,UUID,FSTYPE,SIZE,MOUNTPOINTS
```

5. `findmnt`に対象がないことを確認してから電源／USBの安全な取り外し手順に従う。backup媒体とdata媒体をlabel・UUIDで物理的に区別する。

## 5. 移動先storage hostへ接続する

1. まずread-only inventoryでlabel・UUID・ext4・容量を確認する。`fsck`、format、label書換え、partition操作は行わない。
2. Linux storage hostでは、operatorが現行のmount policyと実際のUUIDを照合してmountする。本作業で`rencrow-storage-configure apply`を無条件に呼ばない。このcommandはLinux用で、両媒体を要求し、`fstab`を変更し得る。
3. mount後、次を確認する。
   - sourceとmountpointのUUID一致
   - 期待するtop-level directoryとstore数
   - SQLite integrity check、JSONL/WAL/receipt/fenceの存在
   - storage-host実行userのUID/GIDとdirectory/file permission
   - 空のdirectoryを新規data rootとして開いていないこと
4. 以前のUID/GIDと異なる場合は、一括`chown -R`を先に行わず、manifestでownerとmodeを比較する。変更対象を確定したうえで、れんが明示的に適用する。

## 6. config・Persona・credential・HOME stateを配置する

- storage hostとCORE hostで別のconfigを作り、secretをsource archiveやexecution receiptへ入れない。
- storage hostの`storage.host.mode` は`local`、listenerはloopback `127.0.0.1:18820`、token fileは絶対pathとする。
- CORE hostの`storage.host.mode` は`remote`、endpointはCORE host側SSH forwardのloopback endpointとする。通信断・認証失敗時にlocalへfallbackさせない。
- CORE host側workspaceのPersona/control/character promptはnative絶対pathへ復元する。WindowsではLinux絶対pathを残さない。
- `RENCROW_CONFIG`には各hostのnative絶対pathを指定する。既存Windows `~/.rencrow/config.yaml`は旧構成のため、検証済みconfigの元にしない。
- HOME cohort復元後にTask/Run/context/notification、JSONL batch WAL、receipt/fence、`.writer.lock`を対応させ、復元前後のID・count・最大generationを照合する。

## 7. storage hostから順に起動する

1. storage hostで、検証済みbinary/source/config hashとtoken permissionを確認する。同じstoreを開く旧COREや別storage-host processがないことを確認する。
2. storage hostで次の同値commandをservice managerから起動する。config pathやbinary pathは実execution receiptの値に置き換える。

```bash
RENCROW_CONFIG=/absolute/storage-host/core.yaml \
  /absolute/path/rencrow storage-host serve
```

3. `127.0.0.1:18820`だけでlistenし、未認証requestが拒否され、認証済みhandshake/contractが期待するoperation集合・mutating flag・writer generationを返すことを確認する。tokenはcommand lineやlogへ出さない。
4. SSH tunnelを起動し、CORE host側のloopback endpointからauthenticated readinessを確認する。storage host側listenerをLANへ直接公開しない。
5. 上記が成功してからCOREを起動する。COREの起動は約4分かかる実績があるため、20秒だけでrollback判定しない。ただし、明示的なauth/contract/schema拒否は待機で解消しないのでfail closedとする。

## 8. 起動後の受入確認

health/readinessだけで終了しない。正規の認証・policy・owner・runtime・LLM経路で次を順番に確認する。RuleBased/Dummy・直接backend・別model・偽serverは代替にならない。

1. 移動前の16 owner public GETとverification summaryの各behavior/countを取得し、source/data cohortの差を考慮してbaselineと比較する。
2. public knowledge scopeと、認証済み直接loopback CMDのprivate user scopeを別々確認する。
3. public GETを持たない`durable_store_workflow`は、実runtime Toolがowner contractを使ったreceiptで確認する。
4. 移動前のShiro history/session/threadとTask/Run IDを同一IDで取得する。writer generationがrollbackしていないことを確認する。
5. actual Shiroへ新規メッセージを送り、正規Gateway経由のmodel/route/actor trace、conversation/session/thread、Task ID、Run ID、storage owner receiptを取得する。
6. CORE processだけを正常停止・再起動し、storage hostを保持する。新規と既存の履歴・ID・owner・Task/Runが同じ正規経路で復元されることを確認する。
7. storage hostを再起動し、writer generation更新後に旧generationが拒否され、未確定operationがowner receiptから回復し、外部副作用の二重実行がないことを確認する。
8. 各hostのbinary SHA-256、source manifest hash、config hash、process ID、listen address、readiness時刻、上記ID/trace/receiptをexecution receiptに記録する。

Windows CORE検証はnative processで行い、official `scripts/test-local.ps1`をargumentなしで実行する。WSLやLinux `.test.exe`をWindows受入の代替にしない。
隔離Windows profileでは、cold configでtrueの`heartbeat.enabled`、XBookmarks、Gmail、`run_on_start`に由来するbackground collectorだけを明示的に停止し、foregroundの全17 store／Raw／Verification／actual Shiro経路を検証する。production flagsは変更せず、この隔離実行からbackground collector自体のWindows E2E成功を主張しない。

## 9. rollback

1. 新COREと新storage hostの順に停止し、旧新のdurable writerが同時に起動しないことをprocess・port・open-fileで確認する。
2. 新側でwriteが1件も始まっていない場合は、固定済みsnapshot／HOME cohort／config／binaryの組を旧hostへ戻す。
3. 新側でwriteが始まった場合は、古いsnapshotへ無言で巻き戻さない。新側の最終consistent cohortとreceipt/generationを保全し、どのcohortを正とするかを決めてから復旧する。
4. 媒体を旧hostへ戻す場合も、UUID・mountpoint・UID/GID・permission・config/binary hashを再照合する。
5. storage host readiness→SSH tunnel→COREの順で起動し、「8. 起動後の受入確認」を同じように実行する。
6. rollback完了後だけ、停止前にactiveだったtimer/serviceのうち必要なものを戻す。終了状態を受入記録とexecution receiptに対応させる。

## execution receiptの必須項目

- 実行者、host、OS、UTC/JST時刻
- source snapshot manifest SHA-256、base commit、tracked/accepted-untracked file hash
- Linux/Windows/storage-host binary SHA-256とconfig hash（secret値なし）
- data/backup label・UUID・filesystem・容量・mountpoint
- 停止前activeだったservice/timerと、実際に停止したwriter/timer
- root/user open-file確認結果
- 最終snapshot path・hash・restore-check・SQLite integrityの結果
- private HOME cohort path・archive hash・permission・隔離restore結果
- storage RPC contract/generation、SSH tunnel、auth拒否とreadiness
- pre/post migrationのconversation/session/thread、Task/Run、actor/route/status/generation
- actual ShiroのGateway/backend/model trace、storage owner receipt、CORE/storage restart後の再取得
- rollbackが必要になった場合の正cohortと切替時刻
