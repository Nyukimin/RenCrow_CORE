# Run無しqueued Taskの滞留 (Task作成と最初のRun開始の分離)

記録日: 2026-10-11 (JST)。対象: Task owner (`internal/application/taskmanager`) と、Taskを作って直ちに実行する
全経路 (atlas、Memory Promotion、LLM provider call、TTS、playback receipt、STT、Vision、patch apply、
DCI search、IdleChat、Heartbeat worker、OPS入口)、および Background job failure の通知。

事実の根拠は 2026-10-10 の本番Task storeの読み取りコピーに対する読み取り専用の集計 (件数・種別・時刻のみ。
記録の本文は読んでいない)。本書は再発防止の実装 (本commit群) の記録。既存queuedの整理は「既存queuedの整理 (一度きりの
道具)」節の道具で行う (道具は実装済みで、本番での実行は別の工程)。配備は含まない。

## 概要

Task storeに、Runを1つも持たない `queued` のTaskが単調に増え続けた (読み取り時点で18,326件、Task総数75,166件の
約24%)。全件が Run 0、親・依存・置換先の参照なし、通知なしで、後で開始するconsumerが無いため永久に終端しない。
終端しないTaskは保持期限設計でも退避できず、hotな領域の床になる。

## 発生条件

- `Manager.Create` はTaskを `queued` で永続する公開入口で、実行を前提にしたTaskと「記録だけ」のTaskを区別しない。
- 実行する呼び出し元は、Createの後に別transactionで `StartRunWithReason` を呼ぶ。その中の `CanStart` が
  `status=running` のTask数で実行枠を判定し、満杯なら `ErrParallelLimit` を返す。拒否されてもCreateのTaskは残る。
- 実行枠が満杯になる原因は複数ある (Idle会話の長時間running、終端書き込みに失敗して `running` のまま残ったHeartbeat
  worker。後者は `20261009_213300_Heartbeat終端書き込みの期限切れによるOPS枠占有_FailureKnowledge.md`)。
  満杯のあいだ、周期的に動く各経路が拒否のたびに1件ずつqueuedを積んだ。

## 事実

- queued 18,326件の内訳 (前日の同種集計を含む主な種別): IdleChat 約1.2万、Background job failure 約2,000、
  Memory Promotion 約2,000、atlas_acquire 約1,900ほか。
- 補償 (拒否後にTaskを取り消す) の無かった経路は9つ: atlas (3操作の共通入口)、Memory Promotion、LLM provider call、
  TTS session、playback receipt、STT、Vision、patch apply、DCI search。補償のあった経路 (IdleChatのCancel、
  Heartbeat workerのFail、OPS入口のFail) も、補償自体がtransactionなので遅いstoreでは失敗し得た
  (Heartbeatでは補償の失敗で10件がqueuedのまま残った)。
- Background job failure は、失敗イベントに載せる `task_id` を得るためだけにCreateし、開始も終端もしなかった。
  Memory Promotionの拒否が `reporter.Failed` に流れ、MP TaskとBackground job failure Taskが1:1で増えた
  (同じ時間帯の件数がほぼ一致)。
- 修正前の既存対策 (`ca1d7685`) はHeartbeatの終端書き込みの予算と再試行の修正で、Create/StartRunの分離は
  直していない。その後の24時間でも新規queuedは27件 (修正前の24時間は294件) 残った。

## 結論 (Failure Knowledge)

- **Failure**: 実行枠不足で開始を拒否されたTaskが、Runを持たない `queued` のまま永久に残り、Task storeが単調に増える。
- **Problem**: Taskの作成と最初のRunの開始が別transactionで、拒否時の取り消しが呼び出し側の補償に任されていた。
  補償は9経路で欠落し、ある経路でも補償自体が失敗し得た。Background job failureは識別子を運ぶためだけにTaskを作った。
- **Cause**: (1) `Create` が「実行するTask」と「記録だけのTask」を区別せず、queuedを永続する入口だった。
  (2) 枠判定 (`CanStart`) とTask作成が別transactionで、拒否してもCreateは巻き戻らない。(3) queuedを後で拾う
  consumerが無く、残ったTaskは誰にも終端されない。(4) 識別子が欲しいだけの用途にTaskを使った。
  (5) 枠拒否を失敗として報告する経路が、さらにTaskを作る (失敗通知→Task) ことで増殖を倍加した。
- **Lesson**: Taskは実行するために作る。作成と最初のRun開始は一つのtransactionで成立させ、拒否は何も残さない。
  後から取り消す補償は、取り消し自体が失敗し得るので構造的な防止にならない。識別子や記録が欲しいだけなら
  TaskではなくEventで足りる。枠が満杯であることは失敗ではなく「後で再試行」である。
- **Invariant**:
  1. 直ちに実行するTaskは `Manager.CreateAndStartRun` で admission する。拒否 (`ErrParallelLimit`、依存未達、
     不正な入力) はTask・context・Run・通知を何も永続しない。
  2. Runを持たない `queued` Taskを永続してよいのは、許可リストのintake (orchestratorのroot／child／repair、
     operatorが明示するCLI) に限る。
  3. Background job failureはTaskを作らず、`task_id`／`run_id`を持たないEventとして発行する。
  4. 枠拒否は失敗として報告しない。再試行は周期的なschedulerの次の周期、または有界のbackoff (Memory Promotionは
     idle graceから倍々、上限5分) で行い、内部に無限の再試行ループを持たない。
- **Enforcement**: `Manager.CreateAndStartRun` (単一transaction)。`queued_creation_architecture_test.go` が、許可リスト外の
  `Create(ctx, Task, SharedRoleContext)` 呼び出しと、それを公開するinterfaceを拒否し、実体の無い許可リスト項目も拒否する。
  reporterから `owner` を削除し、型の上でTaskを作れなくした。`ErrHeartbeatAdmissionDeferred` と `capacityBackoff`。
- **Tests**: `create_and_start_test.go` (単一txの成否、枠・依存・不正入力の拒否で何も残らない)、
  `taskmanagertest.Saturated` を使った各経路の「拒否後に何も残らない」試験 (`*_capacity_test.go`)、
  `queued_creation_architecture_test.go`、`worker_capacity_deferral_test.go`、`runtime_memory_promotion_capacity_test.go`、
  `capacity_backoff_test.go`。

## 経路ごとの処置

| 経路 | 以前 | 処置 |
| --- | --- | --- |
| atlas / Memory Promotion / LLM provider / TTS / playback / STT / Vision / patch apply / DCI search | Create後に別txでStartRun。補償なし | `CreateAndStartRun` に移行 |
| IdleChat | 拒否時にCancel (補償) | `CreateAndStartRun`。再開 (既存Task) だけ `StartRunWithReason`。応答不整合の取り下げCancelだけ残した |
| Heartbeat worker | 拒否時にFail (補償) | `CreateAndStartRun`。開始後の検証失敗はRun付きでFailして終端 |
| legacy OPS入口 / OPS DCI identity | 拒否時にFail | `CreateAndStartRun`。枠拒否は従来どおり503 |
| Background job failure | Createのみ (永久queued) | Taskを作らない。Task-lessなEventを発行 |
| 枠拒否の扱い (backlog runner、workstream、Gmail／X Bookmark、tick、Memory Promotion worker) | 失敗として報告 | 失敗でなく延期。Memory Promotionは有界backoff |
| orchestratorのroot／child／repair、`rencrow tasks create` | queuedを作り、routing後にStart | 残す (許可リスト)。Taskがroutingの記録より先に必要で、失敗時はFailで終端する |

## 既存queuedの整理 (一度きりの道具)

再発防止の前に作られたRun無しqueued (読み取り時点で約1.8万件) を、owner経由で一度だけ `cancelled` にする。
周期的なreaperは作らない (YAGNI。作成側は塞いだので新規は増えない)。

```
rencrow tasks reap-queued --created-before <RFC3339> [--min-age 24h] [--batch-size 2000] [--apply] [--compact]
```

- **担い手**: `taskmanager.Manager.ReapStaleQueued` (`internal/application/taskmanager/reap_queued.go`)。
  永続化は既存の `JSONLStore.ExecuteIdempotentGlobalTaskOperation` (batchごとに、複数Taskの更新と所有者のreceiptを
  1回のJSONL batchで書く)。状態遷移は `updateStatusInTransaction` を通り、`domaintask.CanTransition` が検査する。
  JSONLを直接編集しない。cancelledは `ShouldNotify` の対象外なので通知は作られない。
- **dry-run既定**: `--apply` を付けるまで何も書かない。dry-runは読み取り専用handle (`NewIndexedJSONLReader`) で行い、
  6つのlogファイルと `index/` を変えない。`--apply` は writer として開くので、稼働中のCOREが writer lock を持つ間は
  `ErrTaskWriterBusy` で止まる (JSONの `error` は `writer_busy`)。
- **選択条件** (`classifyStaleQueued`。純粋関数で、dry-runと各batchのtx内再検証が同じ関数を呼ぶ): status=queued、
  `origin_session/thread/turn/message_id` が無い、種別がallowlistの12種のどれか、`--created-before` より前の作成
  (必須、既定値なし)、`--min-age` 以上の経過、自身の親・依存・置換先が無い、他のTaskから参照されない、Runが0件。
  条件を満たさない最初の1つが除外理由として数えられる。dry-runのあとで開始された・Runを得た・参照されたTaskは、
  tx内の再検証で外れ、cancelされない。
- **種別の判定**: Taskには作成経路を示す field (kind、source、action) が無い (Actionは Run に付く)。IdleChat 3種は
  route・assignee・read_only・owner が全て同じで、Background job failure と Heartbeat worker も同じなので、title の
  定型部を正のキーにするしかない。ただし title だけでは選ばず、route、read_only、owner_id の有無、default の
  priority と interrupt_policy、assignee があること、routing/assignment event・module・workstream・goal・
  開始/終了/要約が無いことを必須の連言にする。title の定型は作成箇所のliteralの複製 (Semantic Duplication。
  一度きりの道具で、作成箇所がtitleを変えると候補が減るだけで増えない)。Background job failure の作成箇所は
  commit `16d106ed` で削除済みで、過去の版にだけ存在する。
- **理由コード**: `reaped: stale queued without run (<kind>)` を Task の `summary` に書く (種別ごとの固定文字列。
  本文は入れない)。`finished_at` は実行時刻になる。
- **冪等**: 各batchの operation_id は Task 集合から決まる (`reap-queued-` + 集合のsha256の先頭40hex)。request_hash は
  policy version・`--created-before`・`--min-age`・集合から決まる。再実行の正本は tx 内の status=queued 再検証で、
  cancel済みのTaskは候補から外れるので、途中で落ちた後・2回実行・`ErrCommitUncertain` の後の再実行でも各Taskは
  ちょうど1回だけcancelされる。receipt は batch の永続記録で、同じ集合のbatchを再生すると保存済みの結果を返す。
  自動リトライはしない (commit不確実なら終了status 1 で止まり、再実行で候補が再計算される)。
- **索引**: 道具はメモリ上のID索引を、設定の `task_store.index` に関わらず使う (使わないと SaveTask ごとに履歴全体を畳み込む
  ので batch 内が二乗になる)。索引は派生物で、道具は sidecar を書かない。`tasks verify` は古い sidecar を復元して末尾の
  log を再生し、`matches_log` を返す。次のCORE起動が同じ手順で取り込み、新しい sidecar を書く。
- **出力と終了status**: 件数だけのJSON (種別ごとの候補数、理由ごとの除外数、batchごとの operation_id と件数、apply件数、
  候補集合の sha256)。title・context・Task ID は出さない。終了status: 0=候補あり (dry-run) または処理した (apply)、
  1=失敗、2=コマンドラインの誤り、3=候補なし。
- **確認** (2026-10-10 の読み取りコピー、Task 75,166件、queued 18,326件に対して手元で実施): dry-run の候補 18,325 件
  (種別ごとの件数は上の調査と一致) と除外 1 件 (`user_origin`)。`--apply` で 10 batch、18,325 件を cancelled にし、
  通知は増えず、再実行は候補 0 件 (終了status 3)、`tasks verify` は `ok`、不一致 0。

### 本番での実行 (2026-10-12)

本番ホストのCOREを停止して、道具 (commit `6538b04a`) を一度だけ実行した。本番の `rencrow` binary と設定は変えていない
(道具は同じ commit から本番ホストでbuildした別binary)。実行前に、本番binaryでも予行コピーの `tasks verify` が `ok`・不一致0・
receipt読取可になることを確かめた (道具は本番binaryより新しい版から作られているため)。

- **時刻 (UTC)**: 08:38:17Z に停止、08:40:09Z に起動、08:43:30Z に `/health` が200。COREが停止していたのは約112秒で、
  停止から `/health` 200 までは約313秒 (うち `startup_total` が197.2秒)。停止前に、running の OPS と Heartbeat worker が
  0件であることを確かめた (Memory Promotion の常駐ジョブ1件だけ running)。
- **件数**: Task 80,447件、queued 18,349件のうち候補 18,344件を cancelled にした。種別は idlechat_conversation 6,484、
  idlechat_dialogue_episode 3,416、idlechat_word_topic 2,097、background_job_failure 2,044、memory_promotion 2,026、
  atlas_acquire_runnable 1,997、llm_provider_call 230、tts_playback_session 21、atlas_recover 14、heartbeat_worker 10、
  dci_search 4、atlas_revise 1。対象外は5件: ユーザー起点 (`user_origin`) の1件は定義上の除外、作成から24時間未満
  (`younger_than_min_age`) の4件は残す判断 (利用者判断済み。2026-10-13 02:00Z 以降は次回の候補になる)。
  `created_after_cutoff` は0 (P1配備の後にqueuedは増えていない)。
- **batch**: 10 batch (2,000件 x 9 + 344件)。1 batch は 3.0〜6.3秒、batch合計 56.6秒、`--apply` 全体で62.7秒。
  `applied`=18,344、`skipped`=0。候補集合の sha256 は、稼働中のdry-run・停止後のdry-run・applyで同一。
- **停止中の照合**: 整理前に `tasks/` 全体 (約150MB、`index/seg-legacy.tsi` を含む10 file) を複製し、sha256が全て同一だった。
  再applyは候補0件 (終了status 3)。`tasks verify` は `ok`、不一致0、`sidecar=matches_log`、`sidecar_replayed_lines`=18,354
  (= 18,344 + batch 10)、receipt 10。`task_run`・`task_context`・`task_notifications`・`task_execution_fences`・
  `index/seg-legacy.tsi` は複製とbyte単位で同一 (通知は56,237件のまま)。`task_state.jsonl` は18,344行増えた。
- **起動後** (起動から約34分まで観測):
  - `[TaskStore] index loaded` は `source=sidecar replayed=18354` (復元463ms、`rebuilt=0`)、続いて `index checkpoint ... reason=open`。
    `WARN index rebuild` は無い。
  - `/health` は31回の標本 (1分間隔) で全て200・`"ok":true`。起動後の `journalctl` に `ERROR`/`panic` は0件。
  - queued は稼働中のdry-runで `queued_pool`=5 (除外分だけ)・`candidates`=0 (起動13分後と34分後の2回)。
    通知は、整理前の56,237件と先頭がbyte単位で同一で、整理に由来する通知は無い (起動後の通常動作で12件増えた)。
  - `tx summary` (window 33m20s、write_n=34): `write_exec_p50`=1ms、`write_commit_p50`=2.80s (整理前2.5s前後。stateが約10%増えた
    分の増加)、`write_commit_p95`=3.37s、`write_lock_wait_p95`=2.28s。[WARN] lock_wait の p95 は整理前の窓 (0s) と違うが、
    標本が少なく (34件)、起動直後 (08:43〜08:44Z) に13+3行が集中して書かれた時の待ちと整合する。以降の窓で増えていない。
  - RSS は、起動直後の10分が200〜350MB、以降 (25標本) は133〜284MB (平均177MB)。基準の約250MBと同程度かそれ以下。
- **残りの4件の整理** (同日): 2026-10-12 13:25Z、TaskStoreの段階2の配備でCOREを停止している間に、残していた4件 (atlas_acquire_runnable 3、memory_promotion 1) を `--min-age 9h` で cancelled にした。これで queued は、利用者のチャット由来の1件だけになった。

## 残るリスクと未実施

- [WARN] 満杯の原因である「`running` のまま残ったTask」の排除 (枠占有を生存証拠で判定) は未実装。これが無いと
  queuedは溜まらないが、仕事が進まない状態に変わる。
- [WARN] 既に残っていた約1.8万件のqueuedの整理は、道具 (上記) で2026-10-12に本番で実行した (「本番での実行」)。残るのは
  ユーザー起点の1件と24時間未満の4件。期限切れqueuedを終端する周期処理は作らない。追記専用なので、整理しても store は縮まない (cancelledの行が約8MB増える)。
- [WARN] health checkの「Run無しqueuedの滞留数」は未実装。architecture testはソース構造だけを見る。
- orchestratorのintakeの補償 (Fail) はtransactionなので、store遅延下では失敗し得る。SuperAgentのrun queueは、
  既存Taskの再開が枠で拒否されるとTaskを終端する (別論点)。
- [WARN] 本変更は配備していない。本番は旧binaryのままで、配備後に新規queuedが増えないことを実運用で確認する必要がある。
- `docs/調査/identity-remediation-ledger.json` の `background_notification_capacity` は旧契約 (Create-only) の記録で、
  履歴として残している。現行契約は `docs/architecture/identity/IDENTITY_CANONICAL.md` を参照。

## 関連ファイル

- `internal/application/taskmanager/manager.go` (`CreateAndStartRun`)
- `internal/application/taskmanager/queued_creation_architecture_test.go`
- `internal/application/taskmanager/reap_queued.go`、`reap_queued_classify_test.go`、`reap_queued_test.go`
- `cmd/rencrow/cli_tasks_reap.go`、`cli_tasks_reap_test.go`
- `internal/application/taskmanager/taskmanagertest/saturated.go`
- `internal/application/heartbeat/worker_lifecycle.go`、`internal/application/idlechat/run_identity.go`
- `cmd/rencrow/runtime_background_jobs.go`、`cmd/rencrow/runtime_memory_promotion.go`、`cmd/rencrow/capacity_backoff.go`
- `docs/architecture/identity/IDENTITY_CANONICAL.md`、`docs/02_機能仕様.md`
