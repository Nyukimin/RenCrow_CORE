# ID統一 Step 03: DCI Action / Event / Evidence分離

## 目的

`docs/architecture/identity/IDENTITY_CANONICAL.md`のStep 03に従い、DCI検索全体へ誤って使われている
`EventID`を`ActionID`へ置換し、検索中の発生済み事実をCanonical Event Storeへ記録する。
EvidenceIDは独立Entity IDとし、作成Eventを逆参照する。

## 変更前Evidence

- active DCI DB: `/srv/rencrow/db/core/databases/ops/dci.db`
  - search trace 2、step 10、Evidence 8、query term 0
  - `dci_search_trace.event_id`が検索親key
  - Evidence 8/8が`<search EventID>_ev_<seq>`
- active legacy DCI JSONL: `/home/nyukimi/.rencrow/workspace/logs/dci_search_trace.jsonl`
  - trace 8、`read_file` step 3、failed trace 5
  - active configはSQLiteでありruntime consumerではない
- Conversation L1 current:
  - `namespace=kb:dci` staging 26、すべてpending
  - 26/26がsynthetic search EventIDと旧`search_event_id` metadataを保持
  - News／Knowledge／Domain Graphへの参照は0
- Conversation archive:
  - DCI staging archive 8
- Canonical Event Store:
  - `dci.*` Event 0

SQLiteとJSONLの検索IDは重複せず、検索は合計10件。DCI DB Evidence 8件はL1 staging 26件の部分集合である。

## Canonical target

- `SearchTrace`: `TraceID`、`ActionID`、`ActorKind`、`ActorID`
- `SearchTrace.actor_attribution`: 新規runtimeは`authenticated`、根拠なくActorへ帰属できない旧履歴だけ
  migration専用の`legacy_unattributed`
- `EvidencePack`: `ActionID`
- `SearchStep`: 親`ActionID`とは別の`EventID`と`EventType=dci.file.read`
- `Evidence`: 独立`EvidenceID`と`CreatedByEventID=dci.evidence.created`
- persistence parent key: `action_id`
- Data Write idempotency: `idempotency_key`でlookupし、ActionIDへhash変換しない
- L1 staging `event_id`: Evidence作成EventID
- L1 metadata: `search_action_id`、`evidence_id`、`evidence_created_event_id`

## 過去履歴の変換境界

既存recordへCanonical IDをUUIDv5で付ける。次だけを過去Eventへ変換する。

- legacy trace started/status -> `dci.search.started` + `dci.search.completed|failed`
- legacy `read_file` step -> `dci.file.read`
- legacy Evidence／DCI staging -> `dci.evidence.created`

独立recordがない過去の`dci.search.requested`、`dci.source.selected`は生成しない。
旧`limit` step 1件はterminal payloadへ統合し、receiptへ`legacy_limit_projection`として1件記録する。
同じ旧EvidenceがDCI DBとL1に存在する場合は一つのEvidenceID／EventIDへdedupeする。

旧`actor=Worker`は実装roleであり、Shiroその他のAgentへ推定置換しない。canonical Agent catalogまたは
認証済みuser Evidenceへ一致しないものは、空のActor fieldと`legacy_unattributed`として変換し、元labelは
migration Event payloadとbounded receiptの分類にだけ保持する。通常runtimeからこの状態を書き込む経路は作らない。

予想されるdedupe後の履歴は、検索10、read Event 12、Evidence Event 26、started 10、terminal 10、
合計58 Eventである。実装後のsnapshot dry-runでこの件数を機械検査し、差があればcutoverを拒否する。

## Cutover

1. production snapshotからdry-runし、schema、件数、mapping hash、orphan、legacy keyを検査する。
2. 新DCI DB、Canonical Event Store、L1 current、archive DBと新binaryを別pathへbuildする。
3. build receipt SHA-256と各source logical hashを固定する。
4. `rencrow.service`を停止し、PID、socket、WAL／SHM不在、active logical hash一致を確認する。
5. 4 DB、旧DCI JSONL、新旧binaryを同じrollback rootへ保存する。
6. DB群とbinaryを置換する。途中失敗時は全対象をrollbackする。
7. owner process、listener、health、readiness、DB quick check、mapping set、legacy zeroを確認する。
8. 実ActorのDCI検索とData Write replayを行い、Action／Event／Evidence／L1参照をEvent Storeと照合する。

## 受入条件

- 正本に列挙された6 behavioral caseとEvent Store failureがREDからGREENになる。
- DCI runtime source、API、Viewer、client、current DBに検索全体`event_id`が残らない。
- 全新規stepが実在するCanonical EventIDを持つ。
- 全新規Evidenceが独立EvidenceIDと作成Event逆参照を持つ。
- migration mappingが再実行で同一、orphan 0、duplicate 0、対象rowの欠落 0。
- old JSONL runtime route、dual read/write、runtime alias、旧lookupが0。
- restart後も正規DCI routeとexact Action lookupが成功する。

## D1a migration dry-run / receipt contract

この節はStep 03の出力DB build、apply、cutover、rollbackを実行しない、
RenCrow_CORE所有のdeterministic dry-run境界を定義する。

### API / CLI

- Go APIは`dcimigration.DryRun(context.Context, dcimigration.Options)`だけを
  source snapshotの分類入口とする。APIは五つのsource path、manifest path、期待件数、
  composition rootから渡されたcanonical Agent-ID setを受け取る。
- CLI名は`rencrow-dci-migrate`とし、`--mode dry-run`だけを受け付ける。
  他mode、欠落した`--snapshot-dir`、または次の期待件数flagの欠落・負値は拒否する。
  `--source-dci`、`--source-dci-jsonl`、`--source-event-store`、`--source-l1`、
  `--source-archive`はsnapshot root内のsourceを指定し、
  `--manifest`は同root内の新規receiptを指定する。
- 期待件数flagは`searches`、`read-events`、`evidence-events`、`total-events`、
  `legacy-limit-steps`である。CLIはreceiptをstdoutへ出力し、blocked時は非zero exitする。

### Read-only boundary and classification

- snapshot rootと全sourceはabsolute-resolve後に同じreal root内へ限定する。
  実行時のsnapshot rootは、production treeやactive data directoryではなく、
  五つのsourceを直下へ平坦配置した専用offline cohort directoryでなければならない。
  sourceはその専用rootのdirect childであるregular non-symlink fileだけを受け付け、
  nested path、広すぎるroot（別directory群やfilesystem rootを含む）、SQLiteの`-wal`、`-shm`、
  `-journal` sidecar、active path、source置換を拒否する。SQLiteはread-only/query-only
  connectionで開き、sourceの書込み・migration・VACUUM・checkpointを行わない。
- legacy DCI SQLite v1、legacy DCI JSONL、current L1、archive L1、canonical Event Storeの
  required schema、column、type、primary key、foreign key、versionをread-onlyで検査する。
  SQLiteごとにpathに依存しないlogical SHA-256、JSONLにはfile SHA-256を記録する。
- `dci_query_terms`はこのone-shot dry-runの対象外であり、production snapshotでは件数0を
  事前条件とする。sourceに1件でも存在する場合は`unsupported_query_terms`でblockedにし、
  暗黙にdropせず、query-term conversionは別仕様で実施する。
- search universeはlegacy DCI traceとJSONL traceだけから作り、legacy search idでdedupeする。
  evidenceはlegacy DCI、current/archive DCI stagingをlegacy evidence idでdedupeし、
  canonical contentが衝突したらblockedにする。missing parent、source registryの解決不能、
  promoted staging参照、`read_file`以外のtoolはblockedにする。
- `limit` stepはEvent／SearchStepへ変換せずterminal limitationとreceiptの
  `legacy_limit_projection`へ集計する。過去に独立recordがないrequested/source.selectedは生成しない。

### Deterministic mapping and event plan

- ActionID、TraceID、EventID、EvidenceIDは`modulecore.NewMigrationID`だけで生成する。
  mapping keyは`target_type + "\\0" + source_table + "\\0" + source_field + "\\0" + source_value`で、
  source path、時刻、連番、SHA、legacy EventIDからの文字列派生を含めない。
- searchごとに同じActionID／TraceIDを使い、planned historical eventsは
  `dci.search.started`、`dci.file.read`、`dci.evidence.created`、
  `dci.search.completed|failed`だけとする。全Eventは`component_id=dci`、typed ID、
  共通TraceID／ActionID、causation chainを持ち、planned graphを検証する。
- terminalの`dci.search.completed|failed`は同じsearchのEvidence全branchを閉じたjoinで束縛する。Evidenceが1件以上なら
  決定的に最後のEvidence Eventを`CausationEventID`とし、それより前の全Evidence EventIDを重複なくsorted
  `DependencyEventIDs`へ入れる。Evidenceが0件なら最後のreadまたはstartedをcauseとしdependencyは空、1件なら追加dependencyなしとする。
  各`dci.evidence.created`のreadまたはstartedへのcausationは維持し、joinを含む全graphを検証する。
- canonical Agent labelの一致だけをauthenticated agentへ分類し、それ以外（Workerを含む）は
  `legacy_unattributed`、空ActorKind／ActorIDとする。userへの推測やWorkerからShiroへの写像は行わない。
  元labelはbounded分類Evidenceとmigration Event payload計画にのみ保持する。
- planned Event payloadは旧IDを完全に置換し、`legacy_search_id`、`legacy_evidence_id`、
  `legacy_step_no`、`legacy_final_evidence_count`、`search_event_id`のkeyとraw valueを含めない。
  step番号は`step_no`、最終件数は`evidence_count`へ移し、migration metadataとして残すのは
  `legacy_actor_label`だけとする。旧`limit` stepの除外だけはterminalの`legacy_limit_steps`／
  `limitations`へ投影する。dry-runはplanned payloadを再帰的にexact-key／raw legacy ID scanし、
  nonzeroならblocked、`planned_zero_counters.legacy_key_zero`は旧keyとraw valueの検出数を合算した測定値から生成する。

### Manifest receipt

manifestはschema version `rencrow.identity.dci-migration/v2`、mode `dry-run`、status `ready|blocked`
を持つbounded JSONとする。v1 manifestは受理せず、次のfieldを固定する。

- `expected_counts`、`source_counts`、`actual_counts`、`dedupe_counts`
- `exclusion_reason_counts`、`actor_classification_counts`、`legacy_actor_label_counts`
- `logical_hash_algorithm`（固定値`rencrow.sqlite.logical/v1`）
- `source_database_logical_sha256`（`source_dci`、`source_event_store`、`source_l1`、`source_archive`の4キー）
- `source_schema_sha256`（同じ4キー）
- `source_dci_classification_sha256`（5 source全て。JSONLも含む）
- `source_file_sha256`（`source_dci_jsonl`のみ）
- `source_non_dci_logical_sha256`（`source_event_store`、`source_l1`、`source_archive`の3キー）
- `mapping_sha256`
- `action_id_set_sha256`、`trace_id_set_sha256`、`evidence_id_set_sha256`、`event_id_set_sha256`
- `event_plan_sha256`（payload／envelope／causationを含む、検証済みplanned EventEnvelope全体のcanonical内容hash）
- `planned_zero_counters`（legacy key zero、orphan zero）と、blocked時のbounded `error_code`

SQLite logical hashはread-only/query-only接続の同一source-open windowで、`user_version`、`application_id`、
`encoding`、canonical `sqlite_schema` object、`table_xinfo` descriptor、`sqlite_sequence` allocator row、
全table（user/shadow tableを含む）の全row/valueをtyped length-prefixで処理する。row order、rowid、
挿入順、page size、VACUUM、filesystem pathはhashへ含めない。row digestはtable単位で固定32 byteだけを保持し、
最大row/cell/column bound、context cancellation、未知のSQLite value typeをfail closedで拒否する。L1のnon-DCI hash
だけは、厳密に分類済みのDCI staging primary keyとcurrent registry source_idを除外し、それ以外のtable、FTS、projectionを含める。
selectiveなDCI classification hashだけでは協調cutoverを保護できないため、v2はfull/schema/non-DCIを全て要求する。

manifestは1 MiB以内、atomic rename、permission 0600でsnapshot root内の新規pathへ一度だけ書く。
queries、snippets、commands、file paths、token values、個別Event payloadはreceiptへ含めない。
blocked receiptはnonzero exitとし、source mutationやoutput DB／別runtime artifactを伴わない。
同じ内容のroot-independent snapshotを二度dry-runした場合、mapping、全ID set hash、`event_plan_sha256`、全logical/schema/classification
hashは一致しなければならない。`event_plan_sha256`は、payload／Event graph検証後の全planned EventEnvelopeをJSON化した行を
sortedして既存のSHA-256 canonical-line hashへ渡すため、Event slice順序とsnapshot rootには依存せず、payload、envelope field、
causationの変更を検出する。ready manifestではlowercase 64桁を必須とし、欠落・不正値はfail closed、blocked manifestでは省略を許可する。
receiptの明文fieldへ個別payloadやsource pathを含めず、hashはcanonical EventEnvelope JSON linesだけから計算する。

## D1b-1 live capture / offline snapshot contract

D1b-1は、D1aのread-only dry-runへ渡すための五つのlive sourceを、専用の新規offline rootへ物理的に捕捉する。
この単位はcaptureとreceiptだけを所有し、output DBのbuild／apply、cutover、rollback、service停止、runtime writerの
制御は行わない。Go APIは`dcimigration.Capture(context.Context, dcimigration.CaptureOptions)`であり、
`CaptureOptions`には`SnapshotDir`と五つのlive source pathを明示的に渡す。

### Source role and artifact contract

役割、出力ファイル名、方式は次の五つに固定する。

| role | artifact filename | method |
| --- | --- | --- |
| `source_dci` | `source-dci` | `sqlite_backup` |
| `source_dci_jsonl` | `source-dci-jsonl` | `byte_copy` |
| `source_event_store` | `source-event-store` | `sqlite_backup` |
| `source_l1` | `source-l1` | `sqlite_backup` |
| `source_archive` | `source-archive` | `sqlite_backup` |

`SnapshotDir`は存在していない専用rootでなければならず、親、live source、解決後の実体を検査する。
live sourceはregular non-symlink fileで、五つの解決済み実体は相互に異なり、hardlinkを含むaliasと出力先aliasを拒否する。
rootは`0700`、artifactと`capture.json`は`0600`、既存root／artifact／receiptを上書きしない。
各artifactは一時名へ作成、flush後に同じroot内へatomic renameし、失敗時は`blocked` receiptだけを残せる。
`ready` receiptは五つ全てのartifactが確定している場合だけ発行する。

### SQLite and JSONL capture

SQLiteはsourceを`sql.Open`のread-only URI（`mode=ro`、`immutable`は使わない）で開き、固定した
`sql.Conn.Raw`からmodernc SQLiteの`NewBackup`を取得する。各artifactは一時destinationへ
`Step(256)`を繰り返し、各stepの前後でcontext cancellationを検査する。`Step`が成功して
`Remaining()==0`となった場合だけpage countを受理し、成功・失敗を問わず`Finish`を実行して
operation／finish errorをjoinする。Backup APIが読むSQLiteのtransaction/WAL可視状態をそのまま捕捉するため、
checkpointやsource writeは行わない。宛先のjournalをoffline側で整理した後、`-wal`、`-shm`、`-journal`
sidecarが存在しないことを確認し、artifactを`0600`でflushする。続けて宛先をread-onlyで開き、
単一行の`PRAGMA quick_check`が厳密に`ok`であることを検証してからrename、SHA-256、byte sizeを確定する。

JSONLは`maxJSONLBytes`を上限にbounded stream copyし、sourceのcopy前後のbyte hashが一致することを確認する。
copyしたartifactをflushした後、UTF-8、line bound、truncation、legacy DCI JSONL schemaを既存の
`loadLegacyJSONL`で検証する。source変更、上限超過、invalid UTF-8、line/schema不正はいずれもreadyにしない。

### Capture receipt and failure boundary

receiptはroot直下の新規`capture.json`（最大64 KiB）だけで、schemaは
`rencrow.identity.dci-capture/v1`、modeは`capture`、statusは`ready|blocked`とする。明文fieldは
UTCの`started_at`／`completed_at`、固定roleの`artifacts`、`artifact_set_sha256`、`error_code`に限定する。
各artifactはmethod、lowercase `file_sha256`、non-negative `bytes`を持ち、SQLiteだけが
`page_count`、`quick_check="ok"`、`sidecar_zero=0`を持つ。JSONLへSQLite fieldを追加しない。
`artifact_set_sha256`はroleを含むcanonical artifact recordをsortedしてhashする。receiptはatomic
temp write、file sync、rename、portableなdirectory syncで永続化する。

receiptへsource path、content、error message、token、logical/schema/classification hashを入れない。
失敗時の代表的なmachine-readable boundaryは`invalid_options`／`unsafe_path`、`context_canceled`、
`capture_source`、`capture_backup`／`capture_backup_step`／`capture_backup_finish`、
`capture_quick_check`、`capture_sidecar`、`oversized_jsonl`／`malformed_jsonl`／`source_changed`、
`capture_artifact`／`capture_sync`、`capture_receipt`である。partial artifactが残る場合は必ず
`blocked` receiptに束縛し、ready statusを返さない。capture自身はwriter停止を主張しない。

CLI `rencrow-dci-migrate --mode capture`は`--live-dci`、`--live-dci-jsonl`、`--live-event-store`、
`--live-l1`、`--live-archive`を必須入力とし、expected-count flagを要求しない。stdoutはpath-freeな
bounded JSON receiptを一つ、blocked時のstderrはreceiptの`error_code`一行だけとする。parser error、
unsupported `build`／`apply`／`cutover`、pathを含むdiagnosticはechoしない。`--mode dry-run`の既存
snapshot／expected-count contractは変更しない。

### D2 ownership and acceptance

D2（service manager／cutover owner）はCORE writerを停止し、PID、socket、WAL／SHM不在、停止状態を
独立receiptで確認した後、active sourceのlogical hashを再計算してD1a receiptと再照合する。D1b-1の
`capture.json`はwriter停止、active logical hash一致、build／apply成功を自己証明しない。D2の確認なしに
capture結果だけでcutoverへ進めない。

受入試験は、WALへcommit済みでcheckpointされていないrowのBackup可視性、五つのexact role／filename、
root／artifact／receipt permissions、quick_check、sidecar zero、receipt size／path-free境界、
existing root／missing source／symlink／hardlink／destination alias拒否、context cancellation、
Step／Finish／receipt write fault injection、partial never ready、JSONL UTF-8／line／schema／truncation／
source mutation、CLI success／blocked path-free outputを含む。dry-runのnormal／blocked／root-independent
receipt回帰も同じ実装単位で確認する。

#### D1a Failure Knowledge: selective source hashでは協調cutoverを束縛できない

- **Failure:** legacy DCIの抽出結果だけをlogical source hashとしてreceiptへ記録し、L1の非DCI row、schema、allocator、
  Event Storeの全tableをcutover対象と同じsource証拠として束縛していなかった。
- **Problem:** DCI classificationが一致していても、同じcutoverで置換する別tableやschemaが改変され得る。page layout依存の
  byte hashを使うと、正当なSQLite Backup／VACUUM後のsourceを同一logical sourceとして再照合できない。
- **Cause:** 「移行対象の意味行」と「一括置換するdatabase全体」を一つのhashへ混同し、path／page／row順に依存する読み取りを
  source identityとして扱った。
- **Lesson:** classification evidenceは対象行の説明であり、cutover証拠そのものではない。v2では同じread-only windowで
  schema、full database、厳密な除外規則を適用したnon-DCI databaseを、独立したalgorithm-bound hashとして発行する。
- **Invariant:** ready receiptはv2と固定algorithmを持ち、5 source classification、4 source full/schema、3 source non-DCI、
  JSONL file hashのexact key setを全てlowercase SHA-256で満たす。v1はapply-eligibleにならない。
- **Enforcement:** `table_xinfo`、typed length-prefix、row digest memory bound、unknown type拒否、L1 classified primary-key-only
  exclusion、manifest exact-map validation、atomic 0600 receiptで強制する。logical hashはDB pageを直接読まず、source pathを
  hashへ混ぜない。
- **Tests:** insertion order／page size／VACUUM不変、content／schema／allocator／duplicate row変化、L1 DCI／non-DCI mutation、
  shadow／projection inclusion、cell／row／context bound、v1 rejection、root-independent receipt、raw-content-free bounded
  ready／blocked receiptを検査する。

#### D1a Failure Knowledge: production payload aliasではrollbackを証明できない

- **Failure:** rollbackや再検索用に旧検索／Evidence IDまたは旧JSON keyをplanned Event payloadへ残し、payload aliasを
  zeroの根拠として扱った。
- **Problem:** 新旧IDの併記がproductionへ流れ、Gate 7の旧key／旧lookup zeroとAction／Event／Evidenceの分離を同時に
  証明できない。aliasは新旧stateの混在を招き、再実行時の監査境界も曖昧になる。
- **Cause:** rollback根拠をpayloadに埋め込み、同じrollback rootに保存したsource／binary、migration-only UUIDv5 rule、
  mapping hashの再照合を行わなかった。また、planned payloadを走査せず`legacy_key_zero=0`を固定した。
- **Lesson:** rollbackは4 DBと新旧binaryを同じrollback rootへ保存したchecksum-bound artifactから行う。旧recordのCanonical
  IDは固定UUIDv5 ruleで再計算し、mapping hashを再照合する。rollbackの根拠をproduction payload aliasへ保存しない。
- **Invariant:** planned payloadの旧key／raw legacy IDはzeroで、許可される旧語彙は`legacy_actor_label`とterminalの
  `legacy_limit_steps`／`limitations`だけである。ready manifestは完全なplanned EventEnvelope内容を束縛する
  `event_plan_sha256`も持つ。
- **Enforcement:** exact-key／raw valueの再帰validatorをbuild planへ組み込み、nonzeroをfail closedにする。manifestの
  `planned_zero_counters.legacy_key_zero`は旧keyとraw valueの検出数を合算したscan結果を反映する。payload／graph検証後に
  canonical EventEnvelope全体をsorted JSON linesとしてhashし、readyの欠落・不正な`event_plan_sha256`を拒否する。
- **Tests:** 全planned Eventの旧key／raw value zero、許可metadata保持、actor classificationとmapping／ID set hashの
  決定性、root／slice order非依存、payload／causation変更検出、ready欠落・不正値拒否、blocked省略許可、禁止payload注入拒否、
  rollback root／UUIDv5／mapping hash再照合境界を検査する。

#### D1a Failure Knowledge: terminal EventがEvidence branchを閉じない

- **Failure:** terminalのcauseを最後のEvidenceだけへ設定し、同じsearchの他の`dci.evidence.created` branchを
  `DependencyEventIDs`へ含めなかった。
- **Problem:** terminalから全Evidenceが到達可能であることを証明できず、並列Evidenceを含む検索完了／失敗のclosed graphにならない。
- **Cause:** Evidenceを単一の線形`lastEvent`として扱い、terminalをjoinとして構築しなかった。
- **Lesson:** 各Evidenceのreadまたはstartedへのcausationは維持し、terminalだけを全branchのjoinにする。最後の決定的なEvidenceをcause、
  それ以前をsorted dependenciesとしてEvent graph参照で束縛する。
- **Invariant:** 複数Evidenceではterminalのcauseとdependenciesの和集合が各Evidence EventIDを重複なく一度ずつ含み、causeをdependencyへ
  重複させない。zeroは最後のread／started cause、oneは追加dependencyなしとする。
- **Enforcement:** planned Event graphを検証し、terminalのcausation／dependenciesを含む完全なEnvelope setを`event_plan_sha256`へ束縛する。
- **Tests:** 2 read／3 Evidenceのclosed join、zero／one Evidence、sorted dependency、cause重複なし、graph validation、root／order-independent
  plan hashを検査する。

検証対象はsuccess/dedupe、missing parent、conflicting evidence、unexpected tool、promoted staging、
malformed/oversized JSONL、unknown schema、Event collision、期待件数不一致、blocked receipt、
source mutationなし、manifest raw-content非漏洩、および10 search／12 read／26 evidence／58 total／
1 limitのsynthetic countである。

## D1b-2 offline DCI snapshot foundation

D1b-2は、D1a dry-runが分類したsource snapshotと一つの決定的なmigration planを、後続のoffline buildへ
引き渡すための基盤である。この単位はplanとcanonical DCI snapshot writerだけを準備し、build、Event Store append、
apply、cutover、rollbackは実行しない。

### Invariants

- plannerは一回の呼び出しでactual counts、planned EventEnvelope、mapping、event-plan hashを生成し、同じplanへ
  searchごとのActionID／TraceID／started／terminal EventIDとactor attribution／kind／ID、read stepごとのEventID、
  EvidenceごとのEvidenceID／created EventIDを保持する。dry-run manifestはこのplanの射影であり、同じsource snapshotの
  再計画は決定的に一致する。
- legacy IDを含むtyped mapはmigration内部の引き渡し専用で、receipt、production payload、runtime lookupへ公開しない。
  ID生成は`modulecore.NewMigrationID`をこのplannerの中だけで行い、後段でIDを再計算しない。
- DCI SQLiteのhistorical writeは`CreateMigrationSnapshot`をownerとし、fresh targetへcanonical schema v2を作る。
  runtime `SaveSearchResult`とは分離し、migrationだけが`ValidateStoredSearchResult`を受け入れる。これにより
  `legacy_unattributed`の履歴保存は可能だが、runtime writerへlegacy attributionを許可しない。
- migration recordのEvidenceIDとcreated_at mapは完全一致し、timestampはnonzeroでUTCへ変換可能でなければならない。
  元のEvidence created_atはtrace終了時刻へ置換せず、そのrecord固有の値を保持する。ActionID、TraceID、step EventID、
  EvidenceID、Evidence created EventIDは全record横断で重複を拒否する。
- 入力全体をtarget作成前に検証し、targetは既存file／symlinkを上書きしない。投入は一transactionで行い、quick_check、
  foreign_key_check、close、syncを通過したときだけ成功とする。作成後の失敗ではtargetとSQLite sidecarを残さない。

### Failure Knowledge

- **Failure:** runtime writerへmigration専用のhistorical writeを混ぜ、通常の`ValidateSearchResult`とtrace.EndedAtによる
  Evidence timestampを変更しようとした。
- **Problem:** legacy historyを保存するためにruntimeのactor attribution境界やtimestamp semanticsが緩み、将来の通常検索へ
  `legacy_unattributed`を注入できる。
- **Cause:** historical inputのvalidation、timestamp map、fresh snapshot lifecycleをDCI ownerの別操作として分離しなかった。
- **Lesson:** runtime SaveSearchResultはauthenticated resultだけを従来どおり保存し、migrationはstored validationと明示的な
  evidence timestamp mapを使う一方向のoffline writerとする。
- **Invariant:** migration-specific historical writeはowner-ownedでruntime pathから到達不能であり、legacy attributionを
  runtimeへgrantしない。入力全体のvalidation、global identity uniqueness、transaction、quick／foreign-key check、cleanupを
  API境界で強制する。
- **Enforcement:** `CreateMigrationSnapshot`はtarget作成前に全recordを検証し、canonical SQLiteStoreの共通insert helperで
  evidence timestampだけをmigration mapから選択する。runtime pathは`ValidateSearchResult`とtrace.EndedAtを使い続ける。
- **Tests:** authenticated／legacy_unattributed混在のv2 readback、original created_at、legacy runtime rejection、invalid stored
  result、idempotency／timestamp mismatch、global duplicate identity/event、existing／symlink／canceled target、permission、
  sidecar zeroを検査する。

- **Failure:** dry-runとbuildがそれぞれIDを生成し、mappingとplanned EventEnvelopeが別計算になった。
- **Problem:** receipt hashと後続DB／Event StoreのIDが一致しない再実行や、legacy IDの取りこぼしを検知できない。
- **Cause:** actual counts、events、mapping、hash、identity mapsを同じplannerの結果として保持しなかった。
- **Lesson:** source snapshotからcanonical IDsを生成するのは一つのdeterministic planだけに限定し、dry-runと後続offline buildは
  そのplanを共有する。
- **Invariant:** 一つのsource snapshotには一つのplanが束縛され、manifestのcounts／mapping hash／event-plan hashとplanの値は同一である。
  legacy ID mapは内部だけに留まり、ID再計算やreceiptへの露出を行わない。
- **Enforcement:** `classificationReport`がsource snapshotとplanを保持し、`buildEventPlan`は単一plannerへの互換射影とする。
  typed mapのsearch／read／Evidence網羅性と再計画のdeep equalityをテストする。
- **Tests:** dry-run manifestとretained planの一致、全legacy search/read/evidenceの一度だけのmap coverage、同一snapshotの反復
  planning determinism、raw legacy IDを含まないplanned payloadを検査する。

## D1b-2a explicit UTF-8 normalization

D1b-2aは、D1b-2のcanonical DCI／planned Event生成前に、dedupe済みのEvidence snippetだけを決定的にUTF-8正規化する。
固定algorithmは`rencrow.utf8.invalid-byte-replacement/v1`であり、production snapshotで確認する対象はunique Evidence value
1件、invalid byte 2件である。この単位はcanonical in-memory valueとbounded manifest／plan countsだけを更新し、productionへ
適用されたことは主張しない。

### Invariants and preservation boundary

- `utf8.DecodeRuneInString`がRuneError／size 1を返す各invalid byteを一つのU+FFFDへ置換し、valid encoded U+FFFDはそのまま保持する。
  Evidence IDをsortedした順に一度だけ処理し、normalized value数とinvalid byte数をplan／manifestのexpected／actual countsへ束縛する。
- normalizationはraw source loading、merge、dedupe、source hash、`validateMergedSnapshot`の後、`planMigration`の前に行う。
  `report.Snapshot`とplanned `dci.evidence.created` payloadは同じnormalized textを参照する。
- L1／archiveのraw bytes、`raw_text`、`raw_hash`、legacy DCI／JSONL source、query、command、heading、path、その他source fieldは
  書き換えない。source／classification hashはoriginal raw sourceに基づく既存境界を保つ。
- manifestの`text_normalization_algorithm`はready／blockedとも固定値を要求し、receiptはcountsとalgorithmだけを公開して
  offending text、raw bytes、legacy ID、pathを含めない。dry-run CLIはnormalized value／invalid byteのexpected flagsを要求するが、
  capture modeは独立している。

### Failure Knowledge

- **Failure:** invalid UTF-8をraw sourceへ書き戻す、またはEvidence以外のtextへ広げ、source hashとcanonical derived valueを同時に
  変更しようとした。
- **Problem:** raw L1／archive証拠とraw_hashの保全、再実行可能なsource hash、canonical DCI／Event textの明示的な正規化を
  同時に証明できなくなる。
- **Cause:** source snapshotとcanonical derived snapshotのmutation boundaryを分けず、normalizationをloaderまたはhash計算へ
  混ぜた。また、invalid sequence数とinvalid byte数を同じcountとして扱った。
- **Lesson:** raw sourceはread-only evidenceとして保持し、dedupe済みcanonical Evidence copyへだけ一つのalgorithmを適用する。
  invalid byteごとに一つのreplacementを数え、planが観測値を保持してmanifestへ射影する。
- **Invariant:** production fixtureのunique normalized valueは1、invalid byteは2であり、snapshot Evidenceとplanned Event payloadは
  同一normalized string、source／classification hashとL1／archive raw bytes／raw_hashは不変である。
- **Enforcement:** sorted Evidence-ID iteration、`DecodeRuneInString`、private normalization counts、plan actual／manifest expected／actual
  fields、exact algorithm validation、path／content-free JSON receipt、CLI dry-run flag gateで強制する。
- **Tests:** one invalid `0xC3 0x28`、two invalid trailing bytes、valid literal U+FFFD、raw source／raw_hash preservation、snapshot／Event
  payload equality、ready／expected mismatch blocked、missing／wrong algorithm、path／raw invalid content-free receiptを検査する。

## D1b-2b retained historical materialization and offline L1 projection

D1b-2bは、D1b-2のretained source snapshotと一つのmigration planから、後続のbuildが利用できるhistorical DCI recordと
current／archive L1のcanonical identity projectionを作る基盤である。この単位はfresh DCI owner snapshotへのrecord materializationと
captured L1 cloneのprojectionだけを準備し、4 DBのbuild orchestration、Event Store append、apply、cutover、rollback、productionへの
適用は行わない。production evidenceはunique value 1件、invalid byte 2件というD1b-2aのcanonical normalizationを持つ一方、L1／archiveの
raw bytesと`raw_hash`は元のまま保持する。

### Invariants and preservation boundary

- current／archiveの`l1SourceData`は`sourceSnapshot`のmigration-only private fieldsとして別々に保持する。classified staging refは旧primary key、旧event、source、厳密なlowercase `raw_hash`、raw_text bytesのSHA-256、raw metadata、legacy search／evidence、origin tableを保持し、classified rowはbyte-level SHA-256(raw_text)==raw_hashでなければblockedとなる。source loaderはraw_textを正規化・書換えしない。
- materializerはretained planのsearch／step／evidence mapを一度だけ消費し、`NewMigrationID`、actor再分類、event sliceからの推論を行わない。legacy attribution、trace／action／step／evidence ID、trace times、normalized Evidence snippet、historical Evidence `created_at` mapをそのままowner recordへ渡し、stored-form validationを通す。
- projectionはcaptured sourceをSQLite Backup APIでfresh targetへ複製し、selected originのclassified DCI stagingだけの`id`、`event_id`、`meta_json`をcanonical値へ更新する。`raw_text`と`raw_hash`、その他全columnは保持する。current registryはmetadataだけを更新し、archive registryを更新しない。`search_event_id`だけを除去し、unrelated metadataは保つ。
- 新staging IDはowner formula `kb:dci:<created-event-id>:<raw_hash first 12>`、新event IDはEvidence created EventIDとし、preflightでID／event collisionとold tuple（id／event／source／raw_hash／meta_json）の不一致を拒否する。projected canonical row、old row zero、promoted reference zero、schema／non-DCI logical hash equality、raw hash equality、quick_check、foreign_key_check、sidecar zero、syncを成功条件とする。
- projection targetは既存file／symlink／sidecarを上書きせず、target作成後の失敗ではtargetとSQLite sidecarを除去する。evidenceはcounts、hashes、health／zero countersだけを持ち、path、raw content、legacy／canonical IDを公開しない。runtimeのL1 writerで再保存してappend／正規化／timestamp変更を起こす経路は使わない。

### Failure Knowledge

- **Failure:** runtime APIでlegacy recordを再保存し、L1 rowをruntime writerの式で作り直そうとした。
- **Problem:** runtime validation／timestamp semanticsにmigration例外が混ざり、raw_text／raw_hashが変わる、L1 eventが追加される、またはlegacy attributionが通常runtimeへ漏れる。
- **Cause:** historical owner recordとcaptured L1 sourceのexact projectionを同じruntime write pathへ押し込み、raw sourceとcanonical derived stateの境界を持たなかった。
- **Lesson:** DCI ownerのmigration record APIはstored-formと元Evidence created_atを受け、L1はclone内の限定UPDATEでowner formulaとnon-DCI boundaryを検証する。runtime SaveSearchResultやL1 runtime writerを再利用しない。
- **Invariant:** migration materializationはretained planと完全一致し、L1 projectionはclassified DCI rowだけを変更する。raw L1／archive bytes、raw_hash、non-DCI row／logical hash、source schema hashは不変で、legacy ID／search_event_idはprojected metadata／payloadへ残らない。
- **Enforcement:** loaderのraw_hash gate、private source retention、plan map exact coverage、stored validation、fresh Backup target、preflight collision、bound old-tuple UPDATE／RowsAffected==1、transaction、post-commit row／metadata／hash／integrity checks、failure cleanupで強制する。
- **Tests:** current＋archiveのinvalid raw UTF-8 preservation、canonical ID／event／metadata、registry projection、unrelated metadata、non-DCI preservation、no-row、wrong raw_hash dry-run block、missing／extra plan map、collision、wrong tuple／RowsAffected、promoted／canceled／existing／symlink target、permissions、sidecar zero、source bytes unchangedを検査する。productionへ適用済みとは主張しない。

## D1b-2c offline four-store build and bounded receipt

### Purpose and contract

D1b-2c は、ready な capture receipt と dry-run manifest を同じ captured snapshot
へ再束縛し、prepare が保持した一つの `migrationPlan` から DCI、Event Store、current
L1、archive L1 の四つの offline SQLite output を生成する。公開 API は
`Build(ctx, BuildOptions)`、receipt は `rencrow.identity.dci-build/v1` の
`build.json` だけである。この工程は production apply／cutover／rollback、service
runtime の変更を行わず、それらの成功を主張しない。

### Invariants

- snapshot root、receipt、manifest、固定五 artifact は canonical regular non-symlink
  input であり、receipt／manifest は bounded strict JSON として一つの value、
  unknown field、trailing token、schema／mode／status を検証する。receipt bytes／
  manifest bytes、capture artifact file hash／bytes／set hash を前後で再計算する。
- `classifySnapshot` と planner は prepare 内で一度だけ呼び、manifest の
  `ExpectedCounts` と supplied `AgentIDs` をその呼び出しへ渡す。recomputed manifest
  は supplied ready manifest と exact semantic equality でなければならず、Build は
  DryRun や identity／actor の再分類を呼び出さない。
- Build root は fresh canonical directory とし、固定名の `target-dci.db`、
  `target-event-store.db`、`target-l1.db`、`target-archive.db` を owner helper で
  一回だけ materialize する。root は 0700、DB と build receipt は 0600、SQLite
  sidecar は zero、ready root の entry set はこの五つだけである。
- captured Event Store の source schema／non-DCI hash は入力世代を束縛するが、output
  schema と同一であることは要求しない。captured clone は current Event Store owner の
  `NewSQLiteStore` だけで canonicalize し、DCI append 前の schema／full logical hash を
  output baseline とする。append 後の schema は baseline と同一、planned DCI row を除外した
  non-DCI hash は baseline full hash と同一でなければならない。source の既存 envelope／
  dependency は別の exact row comparison で不変を証明する。
- receipt は capture／dry-run hash、artifact set、manifest の exact projection、
  source hash maps、mapping／ID-set／event-plan hash、counts、planned zero counters、
  四 output の file hash／bytes／role-specific schema／logical／non-DCI hash、owner
  bounded evidence、quick／foreign-key／sidecar counters、aggregate output set hash
  を束縛する。path、query、snippet、command、payload、secret、個別 canonical／legacy
  ID は含めない。
- 途中失敗は output と sidecar を削除する。safe に作成済みの root へ blocked
  receipt を durable に書ける場合だけ `build.json` を残し、blocked receipt 自体を
  書けない場合は empty root とする。どちらの場合も ready は返さず、source と全
  prepared input は前後不変である。

### Failure Knowledge

- **Failure:** Build が別の DryRun／planner を実行し、同じ source から別の mapping や
  canonical ID を作った。
- **Problem:** manifest の event-plan／mapping hash と四 DB output の owner evidence
  が同じ migration graph を証明しない。
- **Cause:** retained private plan を公開 receipt／output 境界まで運ばず、caller の
  flags と default path を再解釈した。
- **Lesson:** prepare が source、manifest、capture hashes、snapshot、plan を一つへ
  固定し、Build はその private input と owner API だけを使う。
- **Invariant:** ready receipt は一つの prepared input、四つの measured owner
  evidence、exact output file／set hash、zero health counters を同時に束縛し、どれか
  一つの drift／mismatch／unsafe path でも fail closed である。
- **Enforcement:** bounded decoder、canonical path／alias guard、single prepare／
  materialize calls、owner evidence projection、atomic 0600 write、root／parent sync、
  exact root／input recheck、blocked cleanup と generic error code で強制する。
- **Tests:** API の ready／blocked／context、malformed／oversized／tampered input、
  source drift、non-fresh／symlink root、各 output 後の cleanup、receipt writer failure、
  owner evidence／hash／zero counter tamper、repeat-build determinism、CLI build flag
  isolation、stdout 一 JSON／stderr bounded code／path-free receipt を検査する。

- **Failure:** previous-generation Event Store source と current owner が canonicalize した
  output の schema hash を無条件に同一比較し、owner が追加する必須 trace indexまで
  `target_hash_mismatch`として拒否した。
- **Problem:** source保全checkが正規schema migrationをschema driftと同一視し、readyな
  production captureからoffline buildを作れない。一方で単にschema比較を外すと、owner外の
  schema変更やnon-DCI driftを見逃す。
- **Cause:** captured input boundaryとcurrent owner output boundaryの二世代を分けず、
  source schema／non-DCI hashをoutputの期待値へ直接流用した。
- **Lesson:** source hashは入力の不変性へ使い、outputはowner migration直後・DCI append前の
  canonical baselineへ束縛する。既存rowのexact比較はschema-aware hashとは別に維持する。
- **Invariant:** output schemaはowner canonical baselineと同一で、planned DCI rowを除いた
  output non-DCI hashはbaseline full hashと同一である。captured source bytesと既存 Event
  Store rowは不変であり、owner以外のschema routeは存在しない。
- **Enforcement:** captured clone、`NewSQLiteStore`、close、read-only logical baseline、owner
  reopen／append、post-append schema／non-DCI hash比較、exact envelope／dependency comparison、
  failure cleanupを一つのhelper chainで強制する。
- **Tests:** current trace indexを持たないprevious-generation sourceがowner canonicalizationで
  indexを一つだけ取得して成功する経路、source bytes不変、post-append arbitrary schema drift、
  non-DCI event drift、target cleanupを検査する。

## D2d-2b service-manager receipt boundary

D2d-2b は、既存の private `executeServiceCutover` を service lifecycle の唯一の
owner として再利用し、その reached phase を bounded な
`rencrow.identity.dci-service-cutover/v2` receipt へ durable に記録する単位である。
service receipt は D2c の file-swap subreceipt、service manager の停止／再開証拠、
old/new runtime checksum chain だけを結合し、production apply、配備後 readiness、
実 Actor Trace／Data Write、restart 後 lookup の成功は主張しない。

### Contract and invariants

- `ServiceReceipt` は全 service command より前に fresh、canonical、regular な
  same-parent target として解決する。build root／四 output／active 五 source／旧新
  runtime／rollback root／D2c receipt と path containment、symlink、hardlink、alias
  を拒否し、入力は一切変更しない。
- receipt status は `applied`、`blocked`、`rolled_back`、`rollback_failed` に限定し、
  strict one-value JSON、unknown／trailing token 拒否、64 KiB bound、temporary file
  fsync、fresh-only atomic publication、parent sync、0600、exact inode readback を
  要求する。既存 final や racing substitution は上書きせず保持する。
- D2c durable receipt は file operation の `applied` subreceipt であり、その物理
  `cutover_subreceipt_sha256` と `cutover_subreceipt_status=applied` は service rollback
  後も不変である。D2c が pre-mutation に receipt を発行しない場合だけ subreceipt は
  空であり、`cutover_terminal_status` が D2c の in-memory terminal を別に示す。
- `initial_state=running` は `initial_running`、`initial_state=maintenance_stopped` は
  enabled／unmasked、inactive、PID-zero、listener-zero、fixed ExecStart／config／旧 runtime
  hash を束縛する `initial_maintenance_stopped` の片方だけを要求する。
  `stopped_before_prepare`、`stopped_before_apply`、`final_running` を owner projection とし、
  実 PID、listener、command、config path、query、payload、secret、個別 ID、raw error は
  receipt／error に出さない。`applied` は initial proof＋二つの stopped proof＋D2c applied＋
  new running、`rolled_back` は initial proof と D2c rollback＋old running、
  `rollback_failed` は完全復旧を claim しない。service receipt write/readback failure も新
  cohort failure として detached stop、D2c rollback、old running proof を要求する。
- この単位の TDD は fake manager と isolated temporary fixture だけを使う。production
  service command、runtime restart、DB、CLI、post-deploy E2E は実行しない。

### Failure Knowledge

- **Failure:** D2c applied receipt を service rollback の terminal status で上書きし、
  service readiness を file-swap success として記録した。
- **Problem:** file owner と service manager の failure domain が混ざり、D2c が何を
  証明したか、どこから rollback すべきかを durable に判定できなかった。
- **Cause:** 二つの owner の terminal state を一つの mutable status に圧縮し、receipt
  publication failure を単なる記録失敗として扱った。
- **Lesson:** D2c file receipt は immutable historical `applied` subreceipt として
  hash/status を保持する。外側 service receipt は lifecycle terminal と reached phase
  だけを持ち、publication failure は必ず data rollback へ入る。
- **Invariant:** checksum chain と owner phase evidence が揃わない `applied` は返さず、
  unknown final／symlink／hardlink は削除・上書きしない。失敗時は bounded code のみを
  返し、service receipt が durable でなければ success を装わない。
- **Enforcement:** private result instrumentation、strict validator、same-parent
  owner-only writer、inode／hash／mode binding、detached recovery、D2c hash cross-check、
  generic error boundary で強制する。
- **Tests:** applied／blocked／readiness rollback、D2c hash retention、first／persistent
  receipt writer failure、cancellation、fresh／alias／symlink／hardlink、strict malformed
  JSON／oversize、path／query／payload／secret／ID non-leak、既存 manager order を検査する。

## D2d-2c production cutover owner CLI

D2d-2b の service lifecycle／receipt 実装は private であり、現状の non-test caller は 0 件、
`rencrow-dci-migrate` は `cutover` を明示拒否している。このまま private test API や手動
`systemctl`／file swap で production を変更せず、既存 migration CLI に一つだけ owner operation
を追加する。

### Classification and contract

- `CLI`: `rencrow-dci-migrate --mode cutover` が build cohort、active 五 source、runtime、
  active config、fresh rollback／receipt target を受け、path-free `ServiceCutoverReceipt`、
  bounded stderr code、exit status を返す。
- `Boundary`: public `dcimigration.Cutover` は exact option mapping だけを行い、既存 private
  `executeServiceCutoverWithReceipt` と fixed manager が service command、checksum、path、
  stop/start、rollback、publication を所有する。
- `LLM`: 0。全入力、判定、state transition、receipt は決定的である。

cutover mode は `--build-dir`、`--build-receipt`、`--expected-build-receipt-sha256`、
`--installed-runtime`、`--staged-runtime`、`--expected-installed-runtime-sha256`、
`--expected-staged-runtime-sha256`、`--active-dci`、`--active-dci-jsonl`、
`--active-event-store`、`--active-l1`、`--active-archive`、`--active-config`、
`--rollback-dir`、`--cutover-receipt`、`--service-receipt` の exact set を要求する。
`--initial-service-stopped` は cutover 専用 optional flag であり、指定時は canonical service の
maintenance-stopped proof を mutation 前に要求する。
unit、port、readiness、service command、shell、query は入力にしない。他 mode の flag、unknown、
positional、missing／empty は parse/form error として stdout なし、fixed stderr、exit 2 で
mutation 前に拒否する。exact flag set を受理した invocation は stdout 一 JSON、stderr bounded
code とし、exit 0 は durable applied だけである。invalid hash／path と platform unavailable は
bounded in-memory blocked receipt を返して nonzero とする。

Linux は fixed `rencrow.service` systemd manager を使う。Windows／macOS は同じ public API と
CLI を build 可能にし、canonical manager 未実装の間は mutation 前に
`service_manager_unavailable` として fail closed にする。public facade は private lifecycle
type／manager／applied state を公開せず、既存 owner を一回だけ呼ぶ。

TDD は option mapping、single-call、flag isolation、invalid input pre-mutation、status別 exit、
one-JSON／bounded stderr、path／secret non-leak、fixed Linux owner、非 Linux compile／unavailable
を fake manager／temp fixture で検査する。production service、DB、runtime は unit test で
変更しない。cutover applied 後も D2e-3 pre/restart/post、logs、durable state、final chain が
未実行なら Step03 complete としない。

### Failure Knowledge

- **Failure:** private cutover を test packageまたは手動操作から起動し、owner CLI receipt を
  持たず production を変更した。
- **Problem:** input hash、operator intent、service generation、rollback terminal、exit status を
  再現可能な一経路へ結合できない。
- **Cause:** private safety boundaryと運用 entrypoint 不在を同一視した。
- **Lesson:** logic は private のまま、exact options と bounded receipt の public facade だけを
  migration CLI に接続する。
- **Invariant:** `rencrow-dci-migrate --mode cutover` -> `dcimigration.Cutover` -> private
  D2d-2b owner だけが production cutover route である。
- **Enforcement:** exact flag matrix、platform factory、private type boundary、architecture test、
  cross-compile、production receipt chain で強制する。

#### Failure Knowledge: startup write で frozen cohort が必ず drift する cutover

- **Failure:** stopped snapshot／build 後に old service を再起動して running preflight を通し、
  同じ frozen source hash で cutover しようとした。
- **Problem:** startup Event が Event Store を更新するため、running proof 後の active prepare は
  必ず `active_source_drift_pre_cutover` となり、timing retry では解消しない。
- **Cause:** service owner contract が running start だけを許し、認証済み maintenance stop を
  mutation 前 evidence として引き継げなかった。
- **Lesson:** quiescent cohort は enabled／unmasked の canonical service を停止した状態から、
  owner が identity と停止を証明し、runtime mask を取得したまま apply と new start まで進める。
- **Invariant:** maintenance flag 指定時は fixed unit／ExecStart／config／old runtime hash／
  inactive／PID-zero／listener-zero が揃わなければ mutation せず blocked。mask 後の失敗は
  old runtime running proof まで復旧する。
- **Enforcement:** `VerifyMaintenanceStopped`、v2 initial-state receipt、cutover-only flag、既存
  D2d-2b lifecycle／D2c／recovery の再利用で強制する。
- **Tests:** success order、proof failure no-mutation、recovery、systemd identity/state rejection、
  receipt exclusivity、flag isolation、running-mode regression、cross-compile を検査する。

#### Failure Knowledge: config base unit が runtime mask を無効化する

- **Failure:** `~/.config/systemd/user/rencrow.service`をbase unitとして配置したまま
  `systemctl --user mask --runtime --now`を成功扱いした。
- **Problem:** systemctlは`$XDG_RUNTIME_DIR/systemd/user/rencrow.service -> /dev/null`を作るが、
  user managerはより優先度の高いconfig base unitを読み続ける。command exit 0でも
  `LoadState=loaded`、`UnitFileState=enabled`、`is-enabled=enabled`となり、別経路から再起動できる。
- **Cause:** user unitのbase sourceとoperator overrideを同じconfig directoryへ置き、
  systemd user load pathとruntime maskの優先順位を検証していなかった。
- **Lesson:** 配布base unitはXDG data path、operator override／drop-inはXDG config pathへ分離する。
  mask commandの成功ではなく、load state、unit-file state、is-enabled、PID、listenerを検査する。
- **Invariant:** production `rencrow.service`の`FragmentPath`は
  `~/.local/share/systemd/user/rencrow.service`、config pathにはbase unitを置かない。
  runtime mask中は`LoadState=masked`、`UnitFileState=masked-runtime`、
  `is-enabled=masked-runtime`、inactive、PID-zero、listener-zeroを全て満たす。
- **Enforcement:** installerのexact legacy comparison、data-path install、fail-closed recovery、
  installer／docs contract test、既存cutover stopped proof、配備後live mask testで強制する。
- **Tests:** operator unit拒否、exact legacy除去、data/config ownership、drop-in allowlist、
  DCI systemd regression、live runtime mask／unmask／readinessを検査する。

## D2e-1 owner post-deploy identity evidence

D2e-1 は [IDENTITY_CANONICAL.md の D2e-1 owner post-deploy identity evidence](../architecture/identity/IDENTITY_CANONICAL.md#d2e-1-owner-post-deploy-identity-evidence)
に定義された、owner 正本間の read-only identity subevidence implementation unit である。
この単位は一つの post-deploy DCI Action を、Action／Trace／Event／Evidence と current／
archive L1 の exact projection まで束縛するが、service、build、runtime、production の
成功を主張しない。

### Owner indexes and readers

- DCI owner: `internal/infrastructure/persistence/dci/identity_evidence.go` の
  `NewIdentityEvidenceVerifier` と `VerifyAction(ctx, ActionID)`。検索正本は
  `FindSearchResultByActionID`、canonical validators は `ValidateStoredSearchResult` と
  `ValidateActor`。
- Canonical Event Store owner: `internal/infrastructure/persistence/eventstore/store.go` の
  `ListByTraceID(ctx, TraceID, 256)`。固定 256 bound、chronological exact list、Event graph と
  Action／Trace／actor/component binding を verifier が再確認する。
- L1 current owner: `internal/infrastructure/persistence/conversation/l1sqlite/` の
  `FindStagingItemByNamespaceEventID`。L1 archive owner:
  `internal/infrastructure/persistence/conversation/archivesqlite/archive_sqlite_l1_archive.go`
  の同名 reader。各 Evidence の CreatedByEventID を `kb:dci` namespace で lookup し、
  `search_result`、raw hash、required meta、全 projection equality を検査する。
- 公開結果は `rencrow.dci.identity-evidence/v1` の path/content-free bounded fields のみで、
  status、counts、actor、Action／Trace、deterministic `event_graph_sha256` を含む。失敗は
  stable bounded code とし、query、path、snippet、URL、payload、meta、raw error、DB path を
  公開しない。

### Test scope and boundary

`internal/infrastructure/persistence/dci/identity_evidence_test.go` は SQLite や service command
を使わず fake readers で、happy／deterministic hash／non-leak、missing／legacy／failed／zero
evidence、wrong Action／Trace／actor／component、missing／extra／unknown／duplicate／overbound
Event、bad graph／step／chain／evidence／terminal／payload、current／archive missing／mismatch／
hash／meta／reader error、receipt validator tamper を検査する。これは owner-read／owner-verify
subevidence の unit test であり、D2d-2b service receipt、D2c build／artifact checksum、Data
Write idempotency、restart、readiness、実 Actor の正規 runtime route、production apply／deploy の
証拠ではない。

## D2e-2 actual Shiro deterministic post-deploy route acceptance

D2e-2 は [IDENTITY_CANONICAL.md の D2e-2 actual Shiro deterministic post-deploy route acceptance](../architecture/identity/IDENTITY_CANONICAL.md#d2e-2-actual-shiro-deterministic-post-deploy-route-acceptance)
に固定した、D2e-1 owner evidence と既存 `/v1/agent/ops` の実 Shiro route を結合する
bounded な acceptance subevidence implementation unit である。新 endpoint、direct DB、
generic tool dispatcher、自然言語 `RouteOPS`、LLM acceptance は追加しない。

### Existing route and deterministic operation

- route は既存の認証済み local-only `POST /v1/agent/ops` 一つだけを使う。既存の
  client/profile、Bearer、`X-Request-ID`、local-only、body size bound、strict one-value
  JSON、unknown field／trailing token 拒否を保持する。固定 branch は
  `{ "operation": "dci_identity_acceptance", "query": "..." }`、legacy branch は
  `{ "message": "..." }` とし、互いに exclusive とする。空値、unknown field、両方、どちら
  でもない shape は bounded error とし、message／LLM へ fallback しない。`tool`、`args`、
  任意 operation、DB／path 指定は受け付けない。
- 固定 branch は設定済みの実 CORE-managed Shiro を actor とし、既存の
  `Shiro.ExecuteTool` -> `ToolRunner.ExecuteV2` を、`agent=shiro`、`role=worker`、
  `purpose=ops`、`access=internal` の一つの認証済み scope で使う。自然言語 `Execute`／
  `RouteOPS` は呼ばず、LLM は 0 回である。同じ scope と request の trusted idempotency key
  を保持し、tool call は次の三つをこの順序で一回ずつだけ実行する。

  1. `data.write` / `dci/search` に query を渡す。
  2. 同一 query／request で同じ `data.write` / `dci/search` を繰り返す。
  3. 一回目の write receipt の `audit_ref`（ActionID）を使い、`data.recall` /
     `dci/identity_evidence` を `limit=1` で呼ぶ。

- 二つの write receipt と recall projection は strict decode／validation する。owner／route、
  Shiro actor／role／purpose／internal scope、成功 schema／policy／validation、ActionID の
  一致、D2e-1 `passed` が一つでも満たされなければ fail closed とする。二回目 write は
  `idempotent_replay=true` 固定である。最初の replay は fresh では `false`、reuse では
  `true` を許すが、acceptance runner は fresh pre-restart で `false`、同一
  `X-Request-ID` と query の post-restart で `true`、post-restart の二回とも `true` を要求
  する。ActionID、TraceID、Event graph、event／step／evidence／current projection／archive
  projection counts は restart 前後で完全一致する。

### Bounded response, errors, and test boundary

- 固定 branch の success schema は `rencrow.agent-ops.dci-identity-acceptance/v1` とし、
  `schema_version`、`status`、`request_id`、`agent_id`、`role`、`operation`、`action_id`、
  `trace_id`、`first_write_replay`、`second_write_replay`、`event_count`、`step_count`、
  `evidence_count`、`current_projection_count`、`archive_projection_count`、
  `event_graph_sha256` のみを返す。status は D2e-1 `passed` と結合した `passed` に固定し、
  `job_id` と `output` は返さない。legacy message response は既存 six-field shape のまま
  とする。
- error は既存 one-field bounded envelope だけで、malformed／mixed、auth／scope、Shiro／
  ToolRunner unavailable、tool、receipt／identity tamper、schema／policy／validation failure
  の詳細を一つの bounded code に閉じる。query、path、snippet、URL、payload、meta、tool
  output、DB path、secret、raw error、arbitrary ID を漏らさず、失敗 branch を message／LLM／
  direct DB へ切り替えない。
- 実装／unit test の範囲は strict dispatch、mutual exclusion、exact three-call order、
  同一 Shiro internal scope、typed/narrow test double 経由の ExecuteTool -> ToolRunner、
  write replay／ActionID binding、D2e-1 projection mapping、success allowlist、malformed／
  unavailable／tamper non-leak までである。test double は seam の証明だけに使い、実 Shiro
  actor、production identity、deploy、post-deploy E2E の証拠とはしない。
- production acceptance は artifact／active config checksum と owner、service readiness
  を確認し、fresh pre-restart request（first `false`, second `true`）を保存する。正規
  service を restart し owner／readiness と旧 generation の消失を確認した後、同じ
  `X-Request-ID` と query の post-restart request（first `true`, second `true`）を保存する。
  service／build receipt と二つの response を final receipt chain へ結合し、route、Action／
  Trace／Event graph／counts、logs、durable state、利用主体からの到達を照合できるまで
  production/live/restart verified または Step 03 complete と報告しない。現時点ではこの
  acceptance sequence は deferred である。

### Failure Knowledge

- **Failure:** 自然言語 LLM 応答、direct DB、fake actor、または D2e-1 verifier 内部呼出しだけを
  post-deploy route acceptance と称した。
- **Problem:** 実 authenticated Shiro の owner route／policy、write idempotency、restart 後の
  同一 Action／Trace／Event graph を証明できず、別 actor／別 projection を受入結果と誤認する。
- **Cause:** message／LLM route と deterministic branch を区別せず、route 前後を test double／
  DB projection で置換し、fresh と replay の意味を一つの成功値へ圧縮した。
- **Lesson:** 既存 endpoint の strict tagged branch を拡張し、実 Shiro の既存
  `ExecuteTool` -> `ToolRunner` を一つの authenticated internal scope で三回だけ呼ぶ。
  fresh pre-restart と同じ request の post-restart を別 phase で検証し、D2e-1 と bounded chain
  を結合する。
- **Invariant:** LLM／direct DB／generic tool なし、exact order／scope／owner／actor／role／
  purpose、matching ActionID、second replay、D2e-1 passed、restart 前後の Action／Trace／graph／
  counts equality、および success field allowlist を同時に満たさない結果は受理しない。
- **Enforcement:** strict JSON allowlist、mutual exclusion、single scope、fixed dispatch、typed
  receipt validators、trusted request-id idempotency、D2e-1 schema/status/count/hash validator、
  one-field error boundary、raw suppression、fresh/restart replay assertion で強制する。
- **Tests:** legacy compatibility、operation／message mixed／unknown／trailing／oversize、exact
  order／scope／no-LLM、write mismatch／replay false／true、Action／Trace／projection tamper、
  graph/count mismatch、unavailable／tool error、success/error non-leak を typed/narrow double
  で検査する。production deploy、service restart、artifact／readiness、実 Shiro route、user
  E2E、final receipt chain は後続の実運用 acceptance まで deferred である。

## D2e-3 fixed pre/post-restart verifier checks

D2e-3 は [IDENTITY_CANONICAL.md の D2e-3 fixed pre/post-restart verifier checks](../architecture/identity/IDENTITY_CANONICAL.md#d2e-3-fixed-prepost-restart-verifier-checks)
に固定した、D2e-2 の実 Shiro route acceptance を restart 前後の二つの実行へ束ねる
`RenCrow_CORE` 所有の operational verifier implementation unit である。check は次の二つだけで、
phase flag、generic command、任意 query flag、第三の互換 check は追加しない。

| check_id | command_id | 役割 |
| --- | --- | --- |
| `core_dci_identity_pre_restart` | `core-dci-identity-pre-restart` | restart 前の fresh な実 Shiro request と pre evidence |
| `core_dci_identity_post_restart` | `core-dci-identity-post-restart` | 明示された pre evidence と restart 後の同一 request の照合 |

### Common route and bounded fixture

- owner は既存 `cmd/rencrow-core-verify` とし、認証済み local-only `POST /v1/agent/ops`、active
  config の client/profile、Bearer、`X-Request-ID`、body bound、strict JSON を再利用する。実際に
  設定された CORE-managed Shiro が D2e-2 の `Shiro.ExecuteTool` -> `ToolRunner.ExecuteV2` ->
  `data.write`／`data.recall` を通り、LLM、RouteOPS、direct DB、generic tool、別 endpoint、別
  actor、alternate route は使わない。
- request body は D2e-2 と同じ `{ "operation": "dci_identity_acceptance", "query": "<owner-fixed fixture>" }`
  のみである。fixture は一つの owner-fixed 非秘密値で、その値の正本は
  `cmd/rencrow-core-verify` の source に置く。manifest は `owner_fixed_fixture` の acquisition
  contract だけを宣言し、独立編集可能な query 値を持たない。caller の任意 query、operation、DB、
  path、tool、args を拒否し、evidence には query を保存せず fixture の lowercase SHA-256 だけを残す。
- response は D2e-2 の allowlist field だけを strict に受け入れ、query、body、Bearer、path、
  tool output、payload、meta、secret、raw error、unknown field、または unrelated／arbitrary ID を
  receipt／evidence に出さない。ID は allowlist にある `request_id`、`agent_id`、`action_id`、
  `trace_id` のうち、この chain の binding に必要な canonical 値だけを許可する。

### Pre and post contract

`core_dci_identity_pre_restart` は canonical systemd service owner の current generation、固定
listener、readiness を観測し、fresh request ID（または canonical 規則を満たす caller ID）で既存
route を呼ぶ。三つの D2e-2 tool call と response validation を通し、最初の write が
`false`、二回目が `true` のときだけ `passed` とする。pre evidence は D2e-2 allowlist facts（その
うち `request_id`、`agent_id`、`action_id`、`trace_id` はこの chain に必要な canonical 値だけ）、
`phase=pre_restart`、`observed_at`、固定 fixture の lowercase SHA-256、bounded な非秘密
`service_main_pid`、`service_generation_sha256`、`artifact_sha256`、`config_sha256`、listener／readiness
の bounded boolean、および response facts の canonical hash だけを owner-only regular file として
0600（mode 非対応 platform は owner-only ACL）で発行する。artifact／config hash は pre／post の
同じ artifact／active config を束ねるためだけの値であり、deploy／catalog 検証は既存
`core_deploy_identity_chain` の owner evidence とする。standard check receipt は通常の
`evidence_ref` で evidence publication 後に物理 evidence を参照する。chain は `observed_at`、post が
記録する物理 pre evidence SHA-256、および通常の `evidence_ref` で構成し、evidence 作成時に最終
receipt を循環参照しない。
詳細な失敗は返さず、assertion failure は `failed`、service／auth／route／publication prerequisite
unavailable は `blocked` として nonzero exit にする。

`core_dci_identity_post_restart` は明示された一つの pre evidence file のみを受け入れる。canonical
owner の regular non-symlink、owner-only 0600、bounded single JSON、strict schema／check ID／command
ID／phase／`status=passed`、固定 freshness、fixture hash、pre request ID、D2e-2 facts／hash が一致
しない file は拒否する。`service_main_pid`、`service_generation_sha256`、`artifact_sha256`、
`config_sha256` も strict に一致しなければ拒否する。caller-supplied query、body、token、service／
shell command は許可しない。
post は canonical service manager の current generation、listener、readiness を観測し、pre と異なる
generation かつ、pre evidence の `service_main_pid` に対応する `/proc/<pid>` が存在せず旧 generation
が残っていないことを確認してから、同じ request ID と固定 fixture で同じ route を呼ぶ。raw prior PID
は旧 generation の不在確認にだけ使い、post receipt／evidence へ再出力しない。pre／post の
`artifact_sha256` と `config_sha256` は同じ値でなければならず、これは deploy／catalog 成功の主張
ではない。
最初と二回目の write はともに `true`、ActionID、TraceID、event graph hash、event／step／evidence／
current projection／archive projection counts は pre と完全一致しなければならない。post evidence は
pre と同じ allowlist facts（chain に必要な canonical allowlist ID だけ）に `phase=post_restart`、
`observed_at`、post generation の非秘密 `service_generation_sha256`、一致した `artifact_sha256` と
`config_sha256`、listener／readiness／`old-generation-absent` の bounded boolean、fixture／response
facts hash、入力 pre evidence の物理 SHA-256 だけを加え、query／body／token／path／output／secret、
allowlist 外の unrelated／arbitrary ID は漏らさない。standard check receipt は通常の `evidence_ref`
で各 evidence を参照する。

### Ownership and frozen sequence

verifier は observation／validation 専用で、restart、install、deploy、Git、任意 shell、任意 request
body／query、artifact publication、DB migration、alternate route を実行しない。restart は canonical
service manager、source／artifact／publication／full lifecycle は既存の
`core_deploy_identity_chain` と `core_runtime_identity_lifecycle_security`、readiness は既存の
readiness owner が持つ。D2e-3 は service／readiness observation を generation binding に再利用する
だけで、artifact／deploy 判定を複製しない。

順序は `deploy identity + runtime identity/lifecycle + readiness -> pre -> canonical service-manager
restart -> runtime identity/lifecycle + readiness/old-generation-absent -> post -> logs/durable-state
review -> final Step03 receipt chain` に凍結する。pre／post、実 Shiro route、generation、Action／Trace／
Event graph／projection counts、logs、durable state が揃うまで D2e-3、D2e-2、または Step 03 を
production/live/restart verified／complete と報告しない。

2026-09-02 の production 実行では、source／artifact／active config／service owner と readiness を照合し、
実 Shiro の固定 route で fresh pre（first=false, second=true）を取得した。canonical service-manager
restart 後は旧 generation 不在を確認し、同じ request／Action／Trace／Event graph／counts の post
（first=true, second=true）を取得した。DCI／Event Store／current L1／archive L1 の durable state、
SQLite quick check／foreign key、restart 後 warning log も照合済みである。pre／post evidence は owner-only
0600 で保存され、artifact と active config の SHA-256 は前後で一致した。

D2e-4 で、これらを service／build receipt、logs、durable-state review と機械的に束ねる専用の
`core-dci-identity-final` を `rencrow-core-verify` と owner manifestへ追加した。pre／post、service-cutover、
cutover、deploy JSONLをowner-onlyの明示入力としてstrictに検証し、現在のservice／listener／readinessと
post以降のwarning/error journalを自己収集する。成功evidenceは固定hash、canonical chain ID／counts、
booleanだけを公開し、path、PID、query、credential、raw logを出さない。source-built verifierによる
production read-only pre-deploy acceptanceは2026-09-02に`passed`したが、pushed/pinned artifactのdeployと
配備済みverifierからのfresh final receiptが揃うまで、Step 03 全体は一部達成として扱う。

初回配備後のfresh preでは、provider候補の全件content rankと実行file再読取が10秒budgetを消費し、
Evidence 2件または6件を得た後もterminalが`context deadline exceeded`となる回帰を実DBで再現した。
provider／registry rankが存在する場合に限り、metadataで先に並べた`MaxFilesRead`件だけをcontent rankする。
metadataのないfilesystem fallbackは従来の全候補rankを維持する。bounded reader testと既存fallback／provider
suiteをGREENにし、再配備後のfresh pre／restart／post／finalで運用終端を再確認する。

### Failure Knowledge

- **Failure:** pre／post を phase flag／generic command にし、verifier 内 restart、pre response の未保存、または post の新規 request を許した。
- **Problem:** service generation 切替、同じ request の idempotency、実 Shiro の Action／Trace／Event graph、artifact／lifecycle の owner 証拠を結合できない。
- **Cause:** D2e-2 route、service manager、artifact/deploy verifier を一つの owner と扱い、fresh／replay と pre evidence の freshness／hash を省略した。
- **Lesson:** 二つの fixed check だけで pre の false／true と post の true／true、異なる generation／旧不在、同一 identity facts を bounded hash chain へ結合する。
- **Invariant:** passed は fixed fixture、strict auth/body/response、実 Shiro canonical route、pre／post replay、generation、old absent、Action／Trace／graph／counts equality、non-leak evidence を全て満たし、verifier は restart／deploy を実行しない。
- **Enforcement:** manifest allowlist、strict header／response、canonical allowlist ID の限定、owner-only 0600 evidence、fixed freshness、fixture／response／artifact／config／prior evidence hash、generation／readiness observation、prior PID の `/proc` absence と old-generation-absent、fixed status／exit code、bounded error で強制する。
- **Tests:** manifest／fixture acquisition、body／auth、pre replay、post evidence schema／0600／freshness／hash、same request／fixture、different generation／old absent、identity／count equality、allowlist、non-leak、status／exit code を fake service／typed route seam で検査する。実 restart／deploy／Git／shell／production Shiro は unit test で実行しない。
