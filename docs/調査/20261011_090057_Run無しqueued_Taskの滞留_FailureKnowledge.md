# Run無しqueued Taskの滞留 (Task作成と最初のRun開始の分離)

記録日: 2026-10-11 (JST)。対象: Task owner (`internal/application/taskmanager`) と、Taskを作って直ちに実行する
全経路 (atlas、Memory Promotion、LLM provider call、TTS、playback receipt、STT、Vision、patch apply、
DCI search、IdleChat、Heartbeat worker、OPS入口)、および Background job failure の通知。

事実の根拠は 2026-10-10 の本番Task storeの読み取りコピーに対する読み取り専用の集計 (件数・種別・時刻のみ。
記録の本文は読んでいない)。本書は再発防止の実装 (本commit群) の記録で、既存queuedの整理や配備は含まない。

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

## 残るリスクと未実施

- [WARN] 満杯の原因である「`running` のまま残ったTask」の排除 (枠占有を生存証拠で判定) は未実装。これが無いと
  queuedは溜まらないが、仕事が進まない状態に変わる。
- [WARN] 既に残っている約1.8万件のqueuedの整理 (owner側のバッチ操作) と、期限切れqueuedを終端する周期処理は未実施。
  本変更は新規の発生を止めるだけで、既存の件数は減らない。
- [WARN] health checkの「Run無しqueuedの滞留数」は未実装。architecture testはソース構造だけを見る。
- orchestratorのintakeの補償 (Fail) はtransactionなので、store遅延下では失敗し得る。SuperAgentのrun queueは、
  既存Taskの再開が枠で拒否されるとTaskを終端する (別論点)。
- [WARN] 本変更は配備していない。本番は旧binaryのままで、配備後に新規queuedが増えないことを実運用で確認する必要がある。
- `docs/調査/identity-remediation-ledger.json` の `background_notification_capacity` は旧契約 (Create-only) の記録で、
  履歴として残している。現行契約は `docs/architecture/identity/IDENTITY_CANONICAL.md` を参照。

## 関連ファイル

- `internal/application/taskmanager/manager.go` (`CreateAndStartRun`)
- `internal/application/taskmanager/queued_creation_architecture_test.go`
- `internal/application/taskmanager/taskmanagertest/saturated.go`
- `internal/application/heartbeat/worker_lifecycle.go`、`internal/application/idlechat/run_identity.go`
- `cmd/rencrow/runtime_background_jobs.go`、`cmd/rencrow/runtime_memory_promotion.go`、`cmd/rencrow/capacity_backoff.go`
- `docs/architecture/identity/IDENTITY_CANONICAL.md`、`docs/02_機能仕様.md`
