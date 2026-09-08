# RenCrow 全体ID統一 正本地図

**Document ID:** `RC-IDENTITY-001`

**Version:** `2.0`

**Date:** `2026-08-27`

**Status:** `CANONICAL / IMPLEMENTATION READY`

**Owner:** `RenCrow CORE`

**Supersedes:** `RenCrow_IDENTITY_CANONICAL_MAP_v1.md`

**Target repository path:** `docs/architecture/identity/IDENTITY_CANONICAL.md`

**Implementation baseline:** `Nyukimin/RenCrow_CORE main@5d856613f3d10952e7a23d0cff9c4145de63da9d`

**Step 00 evidence:** [`docs/調査/20260829_221132_ID統一Step00_baseline.md`](../../調査/20260829_221132_ID統一Step00_baseline.md)

**Step 04 evidence:** [`docs/調査/20260903_033211_ID統一Step04_SessionID_production_cutover.md`](../../調査/20260903_033211_ID統一Step04_SessionID_production_cutover.md)
**Step 05 evidence:** [`docs/調査/20260904_ID統一Step05_ThreadID_production_cutover.md`](../../調査/20260904_ID統一Step05_ThreadID_production_cutover.md)

**Current implementation evidence:** [要求・Gate・対策の実施台帳](../../調査/identity-remediation-ledger.json)。
本文中の過去follow-upは履歴として読み、現在の未達判定はこの台帳のsource／historical／runtime境界で確認する。
台帳は進捗と証拠の投影であり、本仕様の要求や完了条件を上書きしない。

---

## 0. 最重要方針

RenCrowのID統一は、旧IDの上へCanonical IDを重ねる作業ではない。

**旧ID、旧名称、旧JSON key、旧DB column、旧生成器を、正しいIDへ順番に置き換え、最後に完全に削除する。**

最終状態に、次を残してはならない。

- Runtime互換層
- Identity alias table
- Dual read
- Dual write
- 旧IDと新IDの併記
- 同じ意味を持つ複数のID名
- 同じID名による複数の意味
- 旧Schemaを読むCompatibility Adapter
- 旧ID生成器
- 旧IDを前提とするViewer、Log、Query

移行中に必要な変換は、各置換工程のMigration内だけで実施する。工程完了時には、変換コード、旧column、旧field、旧typeを削除する。

各工程は小さく区切る。ただし、工程の完了単位では必ず一つの意味が完全に置換されていなければならない。

---

## 1. 正本の対象

本書は、次の唯一の正本である。

1. IDの名称
2. IDの意味
3. IDを生成するOwner
4. IDの寿命
5. IDを新しくする境界
6. ID同士の参照関係
7. 旧IDからの置換先
8. 置換順序
9. 疎通試験
10. 工程完了条件

IDに関するコード、DB、API、Log、Viewer、Graph、仕様が本書と食い違う場合、本書を優先し、実装側を修正する。

---

## 2. 設計原則

### 2.1 一つの意味には一つの名前

同じ「実行する仕事」を`Task`、`Job`、`SubagentTask`など複数の名前で表さない。

最終的に、実行可能な仕事はすべて`Task`と呼び、`TaskID`で識別する。

### 2.2 一つの名前には一つの意味

`RequestID`を、Transport Request、内部Command、Queue要求、解決要求で兼用しない。

`RequestID`は、Provider、API、Transportへ送った一回の通信要求だけを意味する。

### 2.3 IDと状態、順序、Tokenを分ける

次はIDではない。

- `generation`
- `revision`
- `turn_index`
- `event_seq`
- `chunk_index`
- `attempt_count`
- `lease_token`
- `idempotency_key`
- `payload_hash`
- `content_hash`

これらをIDへ改名したり、EventIDへ統合したりしない。

### 2.4 EventIDは事実だけを指す

EventIDは、Run、Action、Evidence、Artifactを識別しない。

一つのRunやActionから複数のEventが発生する。

### 2.5 GraphはProjection

GraphはCanonical IDとEventから構築する。

Graphを正本にはしない。現在状態の正本は各Domain Store、発生済み事実の正本はEvent Storeとする。

### 2.6 旧IDを保存するための仕組みをRuntimeに残さない

旧IDから新IDへの対応はMigrationで一度だけ使用する。

Migration後のRuntimeは、新IDだけを読み書きする。

---

## 3. 最終Canonical ID地図

```text
RenCrow Identity
│
├─ 因果
│  ├─ TraceID
│  ├─ EventID
│  ├─ CausationEventID
│  └─ DependencyEventIDs[]
│
├─ 会話
│  ├─ SessionID
│  ├─ ThreadID
│  ├─ TurnID
│  ├─ MessageID
│  └─ UtteranceID
│
├─ 仕事
│  ├─ WorkstreamID
│  ├─ GoalID
│  ├─ TaskID
│  ├─ ParentTaskID
│  └─ DependencyTaskIDs[]
│
├─ 実行
│  ├─ RunID
│  ├─ ActionID
│  └─ AttemptID
│
├─ 通信
│  ├─ RequestID
│  └─ ResponseID
│
├─ 永続Entity
│  ├─ ArtifactID
│  ├─ EvidenceID
│  ├─ MemoryID
│  ├─ RelationID
│  ├─ ScheduleID
│  ├─ QueueItemID
│  ├─ CheckpointID
│  └─ ReceiptID
│
└─ Registry Identity
   ├─ UserID
   ├─ AgentID
   ├─ ComponentID
   ├─ SkillID
   ├─ ModelID
   ├─ ProviderID
   ├─ BackendID
   ├─ NodeID
   └─ Domain固有Entity ID
```

---

## 4. 三つの主要構造

### 4.1 会話構造

```text
SessionID
└─ ThreadID
   └─ TurnID
      ├─ MessageID: input
      ├─ MessageID: response
      └─ UtteranceID[]
```

#### Session

一定期間の対話活動を表す。

日付、Channel、外部Chatの値をSessionIDへ埋め込まない。

#### Thread

同じ文脈で続く会話を表す。

UserとAgentの会話、Agent同士の協議、IdleChatの話題を同じ`Thread` modelで表し、`ThreadKind`で区別する。

```text
ThreadKind
├─ user_conversation
├─ agent_discussion
├─ idlechat
├─ document
└─ system
```

`DiscussionID`は廃止する。

#### Turn

一つの入力を受け、RenCrowが安定状態へ戻るまでの一回の相互作用を表す。

一つのTurnは、必ず一つのRoot Taskを作る。

```text
TurnID
└─ RootTaskID
```

#### Message

一つの論理メッセージを表す。

Messageを再生成した場合は、新しいMessageIDを作る。

#### Utterance

一つの音声発話を表す。

一つのMessageを複数Utteranceへ分割してよい。

音声ChunkはIDを持たず、`UtteranceID + ChunkIndex`で指す。

---

### 4.2 仕事と実行

```text
WorkstreamID
└─ GoalID
   └─ TaskID
      ├─ ParentTaskID
      ├─ DependencyTaskIDs[]
      └─ RunID
         └─ ActionID
            └─ AttemptID
               ├─ RequestID
               └─ ResponseID
```

#### Workstream

長期間継続するProject、活動領域を表す。

#### Goal

Workstream内の達成目標を表す。

#### Task

実行可能な一つの仕事を表す。

Taskは次を持てる。

- Owner
- Assignee
- Status
- Priority
- ParentTaskID
- DependencyTaskIDs
- OriginTurnID
- WorkstreamID
- GoalID

`Job`という概念は廃止する。

#### Run

Taskの一回の実行を表す。

次の場合に新しいRunIDを作る。

- 初回実行
- Process再起動後の再開
- Lease再取得
- 実行Agent変更
- Checkpointからの再開
- 明示的再実行

RunIDを階層化しない。

子作業が必要なら、子Taskを作る。

`ParentRunID`は廃止する。

#### Action

Run内の一つの論理操作を表す。

例:

- LLM生成
- DCI検索
- Tool実行
- STT
- TTS
- Playback
- File write
- Patch apply
- External send
- Verification
- Memory promotion

同じActionをRetryしてもActionIDは維持する。

#### Attempt

Actionの一回の物理試行を表す。

Retryごとに新しいAttemptIDを作る。

#### Request / Response

Provider、API、Transportへの一回の通信を表す。

一つのAttemptが複数Requestを行う場合、Requestごとに新しいRequestIDを作る。

---

### 4.3 因果Event Graph

```text
TraceID
├─ EventID
│  ├─ CausationEventID
│  └─ DependencyEventIDs[]
├─ EventID
└─ EventID
```

#### Trace

一つの外部または内部Triggerから、処理が安定状態へ戻るまでの因果範囲を表す。

Trigger例:

- User input
- Schedule fire
- Webhook
- Manual command
- Background wake
- Task resume

後日に同じTaskを再開する場合、TaskIDは維持し、TraceIDとRunIDは新しくする。

#### Event

一つの発生済み事実を表す。

例:

```text
conversation.message.received
routing.selected
task.created
task.assigned
run.started
action.requested
attempt.started
provider.request.sent
provider.response.received
artifact.created
verification.completed
memory.promoted
run.completed
```

#### CausationEventID

そのEventを直接発生させた一つのEventを表す。

`ParentEventID`は廃止し、`CausationEventID`へ統一する。

#### DependencyEventIDs

並列処理の合流など、Event成立に必要だった複数のEventを表す。

時刻順の直前Eventを因果関係として記録してはならない。

---

## 5. Canonical ID Registry

| ID | Prefix | 唯一の意味 |
|---|---|---|
| TraceID | `trc_` | 一つの因果処理 |
| EventID | `evt_` | 一つの発生済み事実 |
| SessionID | `ses_` | 一定期間の対話Session |
| ThreadID | `thr_` | 一つの会話文脈 |
| TurnID | `turn_` | 一回の相互作用 |
| MessageID | `msg_` | 一つの論理Message |
| UtteranceID | `utt_` | 一つの音声発話 |
| WorkstreamID | `ws_` | 長期Project |
| GoalID | `gol_` | 達成目標 |
| TaskID | `tsk_` | 実行可能な一つの仕事 |
| RunID | `run_` | Taskの一回の実行 |
| ActionID | `act_` | 一つの論理操作 |
| AttemptID | `att_` | Actionの一回の試行 |
| RequestID | `req_` | 一回の通信要求 |
| ResponseID | `rsp_` | 一回の通信応答 |
| ArtifactID | `art_` | 永続成果物 |
| EvidenceID | `evd_` | 保存された根拠 |
| MemoryID | `mem_` | 永続記憶 |
| RelationID | `rel_` | 永続Relation |
| ScheduleID | `sch_` | Schedule定義 |
| QueueItemID | `qit_` | Queue内の一項目 |
| CheckpointID | `ckp_` | 再開可能なCheckpoint |
| ReceiptID | `rcp_` | Idempotent処理の完了証跡 |

新規IDはUUIDv7で生成する。

UUIDv7生成が失敗した場合はfail closedとし、UUIDv4、時刻文字列、乱数文字列、連番へfallbackしない。`New*ID`は既存のno-error API契約を維持し、OS entropy取得失敗をpanicとして呼出元へ明示する。

Migrationで既存Recordへ新IDを付与する場合は、Field pathを含むUUIDv5で決定的に生成する。

`RenCrowMigrationNamespace`は次のUUIDへ固定する。

```text
6570d821-e63e-592d-a51f-8cf4b43cdba5
```

この値はDNS namespace UUIDに`rencrow.identity.migration.v1`を適用したUUIDv5である。Runtimeで再導出せず、上記UUIDを正本定数として使う。

```text
UUIDv5(
  RenCrowMigrationNamespace,
  target_type + "\0" +
  source_table + "\0" +
  source_field + "\0" +
  source_value
)
```

同じLegacy文字列が、Trace、Task、Turnで兼用されていても、target_typeが異なるため別のCanonical IDになる。

---

## 6. Core以外のID命名規則

Domain固有Entityには、Domainを明示した名前を使う。

許可例:

- `BacklogItemID`
- `ImplementationUnitID`
- `APICandidateID`
- `ClaimID`
- `OpportunityID`
- `ProductID`
- `DeliveryID`
- `SkillID`

禁止例:

- `ID`
- `ItemID`
- `ResultID`
- `RecordID`
- `EntryID`
- `ObjectID`
- `DataID`

Cross-module contractへGeneric IDを出してはならない。

Report、Draft、ContextPack、Image、Patch、Specification、Transcriptは、すべてArtifactとして扱い、`ArtifactID`と`ArtifactKind`を使う。

```text
ArtifactKind
├─ report
├─ draft
├─ context_pack
├─ image
├─ patch
├─ specification
├─ transcript
├─ diff
└─ document
```

---

## 7. 廃止する同義語と置換先

| 廃止する名前 | Canonical置換 | 理由 |
|---|---|---|
| JobID | TaskID | 実行可能な仕事をTaskへ統一 |
| Job | Task | TaskとJobの別名同義を廃止 |
| JobManager | TaskManager | Owner名も統一 |
| DiscussionID | ThreadID | 協議をThreadKindで表す |
| ParentEventID | CausationEventID | 親子ではなく直接原因 |
| CauseEventIDs | DependencyEventIDs | 複数前提を一名に統一 |
| ParentRunID | 子Task + CausationEventID | Run階層を廃止 |
| TraceRunID | RunID | TraceとRunの合成名を廃止 |
| GenerationID | RunID | 生成処理の実行ID |
| SubagentID | TaskID または AgentID | TaskとActorを分離 |
| DecisionID | EventID | 判断はEventとして識別 |
| AssignmentID | EventID | 割当はEventとして識別 |
| ChangeID | EventID | 変更事実はEvent |
| ApplyID | ActionID | Applyは論理操作 |
| SubmitID | ActionID | Submitは論理操作 |
| ReportID | ArtifactID | ReportはArtifact |
| DraftID | ArtifactID | DraftはArtifact |
| ContextPackID | ArtifactID | ContextPackはArtifact |
| 内部ImageID | ArtifactID | 生成画像はArtifact |
| QueueID | QueueItemID | Queue自体と項目を区別 |
| Scheduler JobID | ScheduleID | Schedule定義はTaskではない |
| HeartbeatID | ScheduleID | Heartbeat定義はSchedule |
| ChatID | ChannelAddress | 内部ThreadIDとの衝突回避 |
| ProducerID | ComponentID | Event emitterをComponentへ統一 |
| ToolCallID | ProviderToolCallID | Provider外部IDであることを明示 |
| 内部RequestID | ActionID | RequestIDを通信要求だけに限定 |
| ResolutionRequestID | ActionID | 解決操作をActionとして扱う |

---

## 8. 現行型の具体的な最終置換

### 8.1 現在の`task.Task`

現在の`task.Task`はUser message、Channel、ChatID、Attachment、Routeを持つ入力Value Objectであり、Canonical Taskではない。

最終的に次へ改名する。

```text
task.Task
└─ conversation.TurnInput
```

Field置換:

```text
jobID       → rootTaskID
channel     → channelAddress.channelType
chatID      → channelAddress.externalConversationID
userMessage → messageText
```

Canonical `task.Task`は、現在の`internal/domain/job.Job`を置き換えるDurable Aggregateとする。

### 8.2 現在の`internal/domain/job.Job`

```text
internal/domain/job.Job
└─ internal/domain/task.Task
```

```text
internal/application/jobmanager
└─ internal/application/taskmanager
```

JSON、DB、API、Logの`job_id`は、すべて`task_id`へ置き換える。

`ParentConversationID`は廃止し、次へ分解する。

```text
OriginSessionID
OriginThreadID
OriginTurnID
OriginMessageID
```

### 8.3 Conversation

```text
TurnID = TraceID = Root Job identity
```

という兼用を廃止する。

最終Field:

```text
SessionID
ThreadID
ThreadSeq
TurnID
TraceID
RootTaskID
UserMessageID
AgentMessageID
```

現在の数値`ThreadID`は`ThreadSeq`へ改名し、新しい`ThreadID`をUUIDで持つ。

### 8.4 Orchestrator

最終Event:

```text
EventID
TraceID
CausationEventID
DependencyEventIDs
EventSeq
SessionID
ThreadID
TurnID
TaskID
RunID
MessageID
```

`Seq`は`EventSeq`へ改名する。

`job_id`は`task_id`へ置換する。

Routing判断とAgent割当は、専用DecisionIDやAssignmentIDを作らず、EventIDで指す。

### 8.5 SuperAgent

```text
AgentRun.RunID
```

は維持する。

```text
ParentRunID
```

は廃止し、子Taskを作る。

```text
SubagentTask.SubagentID
```

は`TaskID`へ置換する。

```text
TraceEvent.ParentEventID
```

は`CausationEventID`へ置換する。

### 8.6 AIWorkflow

`WorkflowEvent`はCanonical Event Envelopeへ統合する。

`ProjectMemoryIndex.ID`は`MemoryID`へ置換する。

`ContextPackID`は`ArtifactID`へ置換する。

### 8.7 DCI

現在の検索全体`EventID`は、意味上EventではなくActionである。

```text
旧 EventID
└─ ActionID
```

検索内の各事実へ、新しいEventIDを付ける。

```text
dci.search.requested
dci.search.started
dci.source.selected
dci.file.read
dci.evidence.created
dci.search.completed
dci.search.failed
```

EvidenceIDをEventIDから文字列派生させない。

### 8.8 Execution / ToolLoop

ToolLoopはTask Runとして`RunID`を持つ。

Tool呼出しは`ActionID`、Retryは`AttemptID`、Provider callは`RequestID / ResponseID`で表す。

Providerが返したTool Call IDは`ProviderToolCallID`として保存する。

### 8.9 Scheduler

```text
scheduler.Job.JobID
└─ scheduler.Schedule.ScheduleID
```

Schedule発火時に、次を新しく作る。

```text
TraceID
TaskID
RunID
EventID
```

Schedule定義と実行Taskを同じIDで表さない。

### 8.10 Memory / Verification

Memory:

```text
MemoryID
CreatedByEventID
UpdatedByEventID
EvidenceEventIDs[]
```

Verification ReportはArtifactである。

```text
VerificationReport.ID
└─ ArtifactID
```

ClaimIDとEvidenceIDは維持する。

Profile Promotion:

```text
ProfilePromotionJob
└─ ProfilePromotionTask
   ├─ TaskID
   ├─ RunID
   └─ EvidenceEventID
```

### 8.11 Voice / IdleChat

維持する制御値:

```text
generation
turn_index
chunk_index
prefetch_token
lease_token
```

置換するID:

```text
ChatID       → ChannelAddress
GenerationID → RunID
TTS request  → ActionID / AttemptID / RequestID
TTS response → ResponseID / UtteranceID
STT request  → ActionID / AttemptID / RequestID
STT response → ResponseID / MessageID
```

IdleChatのAgent協議は`ThreadID + ThreadKind=idlechat`で表す。

---

## 9. Canonical Event Envelope

```go
type EventEnvelope struct {
    SchemaVersion string `json:"schema_version"`

    EventID            EventID   `json:"event_id"`
    TraceID            TraceID   `json:"trace_id"`
    CausationEventID   EventID   `json:"causation_event_id,omitempty"`
    DependencyEventIDs []EventID `json:"dependency_event_ids,omitempty"`

    EventType  string    `json:"event_type"`
    ComponentID string   `json:"component_id"`
    OccurredAt time.Time `json:"occurred_at"`

    SessionID SessionID `json:"session_id,omitempty"`
    ThreadID  ThreadID  `json:"thread_id,omitempty"`
    TurnID    TurnID    `json:"turn_id,omitempty"`

    WorkstreamID WorkstreamID `json:"workstream_id,omitempty"`
    GoalID       GoalID       `json:"goal_id,omitempty"`
    TaskID       TaskID       `json:"task_id,omitempty"`
    RunID        RunID        `json:"run_id,omitempty"`
    ActionID     ActionID     `json:"action_id,omitempty"`
    AttemptID    AttemptID    `json:"attempt_id,omitempty"`

    MessageID   MessageID   `json:"message_id,omitempty"`
    UtteranceID UtteranceID `json:"utterance_id,omitempty"`
    RequestID   RequestID   `json:"request_id,omitempty"`
    ResponseID  ResponseID  `json:"response_id,omitempty"`

    ActorKind string `json:"actor_kind,omitempty"`
    ActorID   string `json:"actor_id,omitempty"`

    ArtifactID   ArtifactID   `json:"artifact_id,omitempty"`
    EvidenceID   EvidenceID   `json:"evidence_id,omitempty"`
    MemoryID     MemoryID     `json:"memory_id,omitempty"`
    RelationID   RelationID   `json:"relation_id,omitempty"`
    ScheduleID   ScheduleID   `json:"schedule_id,omitempty"`
    QueueItemID  QueueItemID  `json:"queue_item_id,omitempty"`
    CheckpointID CheckpointID `json:"checkpoint_id,omitempty"`
    ReceiptID    ReceiptID    `json:"receipt_id,omitempty"`

    Payload map[string]any `json:"payload,omitempty"`
}
```

必須Field:

```text
SchemaVersion
EventID
TraceID
EventType
ComponentID
OccurredAt
```

LLM出力や外部Payloadから、EventID、TraceID、ActorIDを採用してはならない。

---

## 10. 置換方式

### 10.1 Runtime互換層を作らない

本番Runtimeで、旧IDと新IDを変換しながら動かさない。

各工程は次の順で行う。

```text
1. Writer停止
2. Snapshot取得
3. Migration dry-run
4. SchemaとDataを一括変換
5. 新Binaryへ置換
6. Unit / Integration / E2E
7. Runtime疎通
8. 旧column、旧field、旧type、旧生成器を削除
9. 全件整合性検査
10. Writer再開
```

Rollbackは、旧Runtime互換層ではなく、Snapshotと旧Binaryで行う。

### 10.2 Migration mappingは一時物

旧IDから新IDへのMappingは、Migration Process内だけで使用する。

許可:

- Transaction内Temporary Table
- Migration script内Memory map
- Release ArtifactとしてのMigration manifest

禁止:

- Runtime alias table
- Runtime fallback lookup
- 新旧両方の永続column
- Viewer用alias
- Event query用alias

### 10.3 過去Eventを捏造しない

既存Entityへ新しいCanonical IDを付けることは許可する。

存在しなかった過去Eventを、Migrationで生成してはならない。

旧Logに根拠がある場合だけ、Canonical Eventへ変換する。

---

## 11. 変更と疎通の固定サイクル

各工程は次の七Gateを通過する。

```text
Gate 1: Compile
Gate 2: Unit
Gate 3: Migration
Gate 4: Referential Integrity
Gate 5: Integration
Gate 6: Runtime Connectivity
Gate 7: Legacy Deletion
```

### Gate 1: Compile

- 旧typeを消した状態でBuild成功
- 型変換による暗黙string代入なし
- Import cycleなし

### Gate 2: Unit

- ID生成
- Prefix
- UUID validation
- Zero value rejection
- Equality
- JSON marshal
- DB scan/value
- Context propagation

### Gate 3: Migration

- Production Snapshot copyでdry-run成功
- Row count一致
- NULL増加なし
- Duplicateなし
- Migration再実行結果が同一

### Gate 4: Referential Integrity

- 全Foreign key解決
- Orphan zero
- TraceのEvent欠損zero
- TaskのRun欠損zero
- ActionのAttempt欠損zero
- Requestのterminal result欠損zero

### Gate 5: Integration

- API Schema
- Store
- Queue
- Outbox
- Event Store
- Viewer projection
- OTel export

### Gate 6: Runtime Connectivity

- Text chat
- Mio → Shiro
- Shiro → Coder
- Tool
- DCI
- Verification
- Memory
- Scheduler
- Voice
- IdleChat
- Atlas
- Viewer

### Gate 7: Legacy Deletion

工程対象Scopeで次がzeroになること。

- 旧型名
- 旧field名
- 旧JSON key
- 旧DB column
- 旧生成器
- 旧test fixture
- 旧query
- 旧Viewer field
- Runtime変換コード

Gate 7を通過しない工程は未完了とする。

---

## 12. 細分化した置換順序

### Step 00: Baseline固定

変更しない。現行を測定する。

成果:

- Baseline commit
- DB schema dump
- API schema snapshot
- Golden trace
- Golden Viewer payload
- Golden voice log
- ID inventory
- 旧語彙一覧

疎通:

- `go test ./...`
- Text chat
- Code route
- DCI
- Memory
- Scheduler
- Voice
- IdleChat
- Atlas

---

### Step 01: Canonical ID package

置換:

- Coreへ最終ID型とUUIDv7 generatorを追加
- Generic generatorを一か所へ集約
- Prefix validatorを追加

このStepでは既存IDをまだ置換しない。

Test:

- 全ID type test
- JSON / SQL round trip
- 100万件生成のduplicate zero
- Prefixとtypeの不一致拒否

完了条件:

- 新ID型が本書と完全一致
- 新たな独自ID generator追加をCIで禁止

---

### Step 02: Eventの意味を統一

置換:

```text
core.Event
SuperAgent.TraceEvent
AIWorkflow.WorkflowEvent
```

をCanonical Event Envelopeへ統一する。

```text
ParentEventID → CausationEventID
CauseEventIDs → DependencyEventIDs
```

Test:

- Root Event
- Child Event
- Parallel Event
- Join Event
- Cycle rejection
- Missing causation rejection

完了条件:

- 対象Scopeに`ParentEventID`が残っていない
- EventIDをRunIDとして使う箇所がない

#### Step 02配備契約

- 発生済み事実の唯一の永続正本は`storage.databases.event_store`が指す
  SQLite Canonical Event Storeとする。
- ownerがEventを発行する正規経路は、Canonical Event Storeへの同期appendを先に完了し、
  成功したEventだけをViewer／monitorへ投影する。正本appendを非同期queueへ委ねたり、
  投影後のappend失敗をlogだけで終端したりしてはならない。
- request処理中に一件でもCanonical Event appendが失敗した場合、そのrequest contextをcancelし、
  利用主体へ成功応答を返さない。既に成功したEventはappend-onlyのまま保持し、失敗Eventを
  投影または成功扱いしない。同期callerを持たないbackground producerは失敗をowner jobの
  failureとして返すか、少なくともerrorを明示して成功通知を発行しない。
- AI WorkflowとSuperAgentのDomain Storeは現在状態だけを所有し、Eventの独立table、
  JSONL writer、dual writeを残さない。
- Event Storeはappend-onlyとし、同一EventIDの再保存、存在しないCausation、
  存在しないDependency、Trace跨ぎ参照をtransaction内で拒否する。
- owner固有の`status`、`agent`、`repo`、`worktree_id`、`command_name`、
  `skill_name`、`summary`はCanonical fieldを増やさず`Payload`に保存する。
- ComponentIDは少なくとも`ai_workflow`、`superagent`、`orchestrator`を区別する。
- 旧RecordのEventIDは旧primary keyからEventIDへ、RunIDとWorkstreamIDは各自の
  旧fieldから対応するCanonical IDへUUIDv5で変換する。
- 旧RecordにRunIDがある場合はそのRunIDからTraceIDを決定的に導出し、ない場合は
  旧EventIDから独立TraceIDを導出する。これは過去Eventの追加ではなく、既存事実の識別子変換である。
- 旧`parent_event_id`が同一migration set内のEventを指す場合だけ
  `CausationEventID`へ変換する。RunIDを指す既知の誤用は偽Eventを生成せず、
  migration manifestに件数と理由を記録してCanonical Eventから除去する。
  それ以外の未解決参照はfail closedとする。
- 旧Event tableとJSONLの削除は、production snapshot dry-run、件数・checksum・参照整合性、
  新binaryのRuntime疎通を確認した同じcutoverで行う。

#### Step 02 Failure Knowledge: JobIDをTraceIDとして流用したEvent分断

- **Failure:** Orchestrator Eventの`TraceID`へ旧`JobID`を渡し、Canonical Event adapterが不正形式をEventごとに別Traceへ置換した。
- **Problem:** 同じ外部Triggerから生じたOrchestrator、SuperAgent、AI WorkflowのEventを、一つのTraceとして追跡できなかった。
- **Cause:** ingressでTraceを一度だけ所有せず、実行識別子を因果識別子として兼用した。adapterの形式補正が関係の欠落を隠した。
- **Lesson:** canonical prefixへの変換成功は因果整合性の証明ではない。Trace owner、伝播、永続化を一つのE2E契約として検査する。
- **Invariant:** ingress ownerが一つのCanonical `TraceID`を生成し、同一Trigger内のowner Eventへその値を保持して渡す。`TraceID == JobID`を禁止する。
- **Enforcement:** malformed ingress Traceはowner境界で置換し、Event adapter内ではJobIDからTraceを推測しない。production Event Storeはappend-onlyのままとする。
- **Queue Trigger:** `run_queue.claimed`を発生させる一回のclaim attemptは一つの内部Triggerである。Run Queue SchedulerがそのclaimのCanonical `TraceID`を一度だけ生成し、claimed／completed／failed Eventと、同じclaimから呼ぶ`ProcessMessageRequest`へ明示的に渡す。ProcessorはTraceをJobID、RunID、QueueIDから再生成・推測してはならない。lease失効後の再claimは新しい内部Triggerなので新しいTraceIDを持つ。
- **Voice Input Trigger:** `ProcessVoiceDirect`が音声入力TriggerのCanonical `TraceID`を一度だけ生成し、`voiceinput.Publisher`へ明示的に渡す。PublisherはTrace欠落／不正形式をEvent・session log発行前に拒否し、`JobID`からTraceを生成・推測しない。`ModeLLM`は非空のuser transcriptを必須とし、transcript欠落はPublisherより前にfail closedするため、Event・session log・成功finalを発行しない。user／assistant session log、Event、受付responseは同じTraceを保持する。
- **Tests:** Viewer受付、Message/Distributed Orchestrator、SuperAgent、AI Workflow、Voice Inputで、正規Trace、JobIDとの非同一、同一request内のTrace一致を検査する。Run Queueはclaim Eventと`ProcessMessageRequest`が同じTraceを持ち、再claimが別Traceになることを検査する。配備後は受付receiptとEvent Storeを照合する。

#### Step 02 Failure Knowledge: 非同期Event記録による偽成功

- **Failure:** Orchestrator EventをViewerへ先に配信し、Canonical Event Store appendを非同期queueへ渡していた。
- **Problem:** append失敗が呼出元へ戻らず、利用者には成功結果が見えても発生済み事実の正本が欠落し得た。
- **Cause:** 低遅延が必要なSSE配信と、欠落禁止の正本記録を同じ`EventListener` lifecycleへ混在させ、失敗契約をvoid callbackにしていた。
- **Lesson:** 記録と配信の分離は、正本記録をbest-effort化することではない。同期append成功が投影と成功応答の前提である。
- **Invariant:** 一つのowner Event発行は`canonical append -> projection`の一方向だけを通り、append失敗はrequest error／cancelへ伝播する。別writer、dual write、非同期canonical queueを作らない。
- **Enforcement:** Event発行境界をerror-returningにし、request Trace単位で最初の失敗を保持する。最初の受付Event失敗時はLLM／Tool／外部処理へ進まず、途中失敗時も最終成功応答を拒否する。
- **Tests:** appendが成功したEventだけが一度だけ投影されること、最初／途中／最終append失敗で成功応答が返らないこと、失敗EventがViewerへ出ないことをMessage／Distributed経路で検査する。

#### Step 02 Failure Knowledge: TTS callbackによるTrace分断

- **Failure:** 一つのAgent応答から非同期に返る`metrics.latency`、`tts.audio_chunk`、`tts.session_completed`が、callbackごとに新しい`TraceID`を生成した。
- **Problem:** 本文のOrchestrator Eventは同じTraceに保存されても、音声生成・chunk・完了だけが別Traceになり、利用者に見える応答を一つの因果処理として追跡できなかった。
- **Cause:** TTS session開始契約が親`TraceID`を受け取らず、callback adapterが`ResponseID`／`JobID`だけを持つ状態でEventを生成した。
- **Lesson:** 非同期callbackは新しいingressではない。開始時に確定した親Traceをsession stateへ明示的に保持し、全callbackへ渡す必要がある。
- **Invariant:** `ProcessMessageRequest`から開始するTTS sessionは、そのrequestのCanonical `TraceID`を`TTSSessionStart`へ明示的に渡し、全chunk／metric／completion Eventで同じ値を保持する。`ResponseID`、`JobID`、`SessionID`からTraceを推測しない。
- **Enforcement:** TTS bridgeは有効な開始Traceをそのままsessionへ保持する。独立TTS sessionに親Traceがない場合だけ開始境界で一度Canonical Traceを生成し、session終了まで再生成しない。callback adapterは保持されたTraceを`NewEventWithTraceID`へ渡す。
- **Tests:** Message／Distributed lifecycleがrequest Traceを開始契約へ渡すこと、指定Traceと開始境界で生成したTraceの両方が全chunk／completionで一致すること、配備後の実Actor応答で本文・TTSを含む全Eventが一つのTraceになることを検査する。

#### Step 02 Failure Knowledge: IdleChat timelineによるTrace分断

- **Failure:** IdleChatのtopic、message、summaryをViewer Eventへ変換するたびに新しい`TraceID`を生成し、TTS／prefetch開始契約にも親Traceを渡さなかった。
- **Problem:** 一回のIdleChat runがEvent数と同数のTraceへ分裂し、Mio／Shiroの会話、要約、音声を一つのowner Triggerとして追跡できなかった。同じstory episodeを複数回再生した履歴では、同一`SessionID`の別playbackを区別するrootも失われた。
- **Cause:** IdleChat Orchestratorがrun開始時の因果identityを所有せず、timeline adapterとTTS adapterを独立ingressとして扱った。
- **Lesson:** `SessionID`は会話対象または再利用可能なepisode identityであり、Trigger identityではない。非同期timeline／prefetchも新しいingressではなく、owner runのTraceを明示的に継承する。
- **Invariant:** 一回のIdleChat runは開始境界で一つのCanonical `TraceID`を生成し、同じrunのtopic、message、summary、TTS、prefetchへ保持する。別runは同じ`SessionID`を再利用しても別Traceを持つ。`TraceID`を`SessionID`、`MessageID`、turn番号から生成・推測しない。
- **Enforcement:** IdleChat Orchestratorはrun開始時にTraceを生成し、session bind後だけtimeline／prefetch Eventを受理する。active owner Traceとの不一致、session不一致、run終了後の遅延Eventはfail closedで拒否する。runtime adapterは受け取ったTraceを`NewEventWithTraceID`と`TTSSessionStart`へ渡し、新しく生成しない。
- **Tests:** `TestIdleChatSessionBindsTimelineAndPrefetchToOneCanonicalTrace`で同一runのtopic／message／prefetch一致、別runのTrace分離、run終了後の遅延Event拒否を検査する。runtime adapterと通常TTS／prefetch TTSの試験で同じTraceがEvent／開始契約へ伝播することを検査し、配備後は実Mio／Shiro IdleChat runの全timeline EventをEvent Storeで照合する。

#### Step 02 Failure Knowledge: Viewer失敗callbackによるTrace分断

- **Failure:** Viewerがrequestを受理した後の非同期処理失敗で、`viewer.error`だけが受付時の`TraceID`を継承せず新しいTraceを生成した。
- **Problem:** 受付Eventと失敗Eventが別Traceになり、一回の利用者requestが失敗まで追跡できず、修復済みproduction Event Storeへ新しい分断Eventを追加した。
- **Cause:** error callbackを新しいingressとして扱い、callbackへ既に渡されていたCanonical `TraceID`をEvent生成時に使用しなかった。
- **Lesson:** 非同期成功・失敗callbackはいずれも新しいTriggerではない。受付境界で確定したTraceを終端Eventまで保持する必要がある。
- **Invariant:** Viewer受付後に同じrequestから発生する`viewer.error`は、受付responseのCanonical `TraceID`をenvelopeとpayloadの両方へ保持し、`JobID`とは分離する。
- **Enforcement:** Viewer error callbackは受付`SendRequest.TraceID`を`NewEventWithTraceID`へ明示的に渡し、callback内でTraceを生成・推測しない。
- **Tests:** `TestViewerAsyncErrorEventKeepsAcceptedIngressTrace`で受付responseと永続化された`viewer.error`のJobID、payload TraceID、envelope TraceIDを照合する。配備後は正規失敗経路とEvent Storeを照合し、repair dry-runが新しい修復対象を検出しないことを確認する。

#### Step 02 offline Trace repair契約

修正前runtimeがappendした分断Eventはactive Event Store内で更新しない。SQLite backup APIで取得した
read-only production snapshotを入力とし、別pathへ全Eventを再構築するone-shot owner CLIだけを使用する。

- 同じJobの候補はfield名だけで推測せず、ownerとEvent typeを含む正規契約から決定する。
  全ownerの`job_id`、`ai_workflow`の`heavy_worker.*`にある`task_reference`、
  `superagent`の`lead_agent.*`／`subagent.*`／`run_queue.*`にある
  `run_reference=run_lead_<job_id>`だけをJob候補として集約する。
  `superagent.subagent.*`の`task_reference=sub_<agent>_<id>`はsubagent task identityであり、
  親Job候補として扱わない。同じfield名でもowner／Event typeが契約外ならJobを推測しない。
- Job候補groupは`verified`、`repairable`、`unresolved`へ全件分類する。既にTraceが一つのgroupは
  `verified`、owner契約からTriggerまたはsession同一性を立証できるgroupは`repairable`、
  それ以外は理由付き`unresolved`とし、未解決Eventを変更、削除、別groupへ混入させない。
- target Traceはroot Eventが既に持つCanonical `TraceID`とし、新しい過去Eventや新しいTraceを生成しない。
  通常requestは`component_id=orchestrator,event_type=message.received`、Queue Triggerは
  `component_id=superagent,event_type=run_queue.claimed`、background failureは
  `component_id=orchestrator,event_type=background_job.failed`をrootとする。
- 同一JobIDが複数Triggerで再利用された場合はJob単位で統合しない。EventID順のowner rootでsegment化し、
  `run_queue.claimed`の後に同じJobの`message.received`が一件だけ続く場合は同じQueue Triggerへ含める。
  新しいowner rootより前のsegmentに`agent.response`、`viewer.error`、`verification.report`、
  `run_queue.completed`、`run_queue.failed`のいずれかの終端証拠がない場合は、時刻の近さだけで
  分割せずJob全体を`unresolved`にする。
- 証拠で立証した各segmentのroot TraceIDが既にsegment内の全Eventへ保持されている場合、
  複数Traceを持つJob groupでも`verified`として扱う。`repair_job_count`と`repairable_job_count`は
  実際にTraceIDを変更するgroupだけ、`repair_event_count`は変更したEventだけを数え、`repair_segment_count`と
  `repair_evidence_counts`も少なくとも一件の変更があるsegmentだけを数える。変更のないsegmentも
  境界参照の検証対象から外さず、TraceID、EventID、Payload、依存関係は変更しない。
- 独立TTS sessionは、group内の全Eventが`component_id=orchestrator`で、Event typeが
  `metrics.latency`の`audio_chunk_ready`、`tts.audio_chunk`、`tts.session_completed`だけ、
  `session_id`と`response_id`が各一値、completionが一件の場合に限り同一sessionと立証する。
  この場合はEventID順で最初の既存callback Traceをtargetとする。background failureは
  `background_job.failed`と`job.notification`が各一件のgroupだけを同一Triggerとする。
- IdleChatはJobとして数えず、`component_id=orchestrator`、Event typeが`idlechat.topic`、
  `idlechat.message`、`idlechat.summary`、payloadの`session_id`が同一のEventだけを候補groupとする。
  一つのtopicを持つgroupは、topicより前に任意で存在できる`from=user,to=mio`の無turn announcementと、
  1から欠番・重複なく増加する一件以上のturn列を一つのrunとする。targetはEventID順で最初の既存Traceとし、
  content本文や時刻の近さでは分類しない。
- topic／summaryを持たない`story-episode-*` groupは、`turn_index=1`から始まり欠番なく増加する列ごとに
  別runへsegment化する。同じepisode `SessionID`の再playbackを統合せず、各segmentの最初の既存Traceを
  targetとする。topicを持たない2 Eventの`forecast-*` failureは、無turnの`user -> mio` announcementと
  `turn_index=1`の`shiro -> mio` messageが各一件の場合だけ一つのrunとする。一Eventだけで既に一Traceの
  groupは変更不要としてverifiedに数え、その他の構造や不正turn列は理由付きunresolvedとして変更しない。
- conflicting job identity、group／segmentを跨ぐCausation／Dependency、invalid envelope、
  source columnとenvelopeの不一致は全体をfail closedとする。これらの構造矛盾と、証拠不足で
  未変更の`unresolved`を混同しない。
- `EventID`、Event順序、Payload、Canonical field、dependency edgeは保持し、変更可能fieldは対象Eventの`TraceID`だけとする。
- dry-run receipt schema v3はsource file SHA-256、Event count、Event set hash、verified／repairable／unresolved
  Job数とIdleChat run数、repair segment／Event数、列挙型に限定したrepair evidence／unresolved reason別件数、
  output Event set hashをbounded JSONで固定する。個別Event全件やPayloadをreceiptへ列挙しない。
- buildはchecksum一致するdry-run receiptを必須とし、存在しない別output pathだけへ新DBを作る。
- production applyはJobとIdleChat runの両方が`unresolved=0`のreceiptに限り、writer停止後にactive source checksumを再照合し、
  DB／WAL／SHMと旧binaryをrollback rootへ保存してからatomic swapする。`unresolved>0`のbuild成果物は
  調査用に保持できるがproductionへ適用しない。
- writer停止の確認と停止中receiptはservice managerの責務境界とし、apply CLIはactive DBの論理Event count、
  EventID set hash、非Trace content hashを再照合する。SQLite Backup APIのpage layout差があるため、
  activeとsourceのbyte SHA一致は要求しない。applyは`unresolved>0`、activeのWAL／SHM／journal残存、
  apply開始前に固定したbuild receipt SHA-256を引数として再照合し、build／source／output／runtimeの
  checksum不一致をswap前に拒否してから、DBとruntimeを同じrollback rootへ保存する。
  DBを先に、binaryを次にatomic replaceし、後段検証またはreceipt保存に失敗した場合は両方を復元する。
  cutover receipt schema v2のstatusは`applied`、`blocked`、`rolled_back`、`rollback_failed`に限定する。
  この`applied`はDB／runtime fileのchecksum-bound atomic swapとCLI内検査だけを示すsubreceiptであり、
  Step 02の運用完了、service readiness、または実Actor E2E成功を意味しない。
- swap後はrow count、EventID set、非Trace content hash、graph整合性、quick check、owner process、readiness、
  実Actor Trace E2Eをservice-manager receiptと配備後receiptで確認する。writer停止前からこの終端確認まで
  rollback rootを保持し、一項目でも失敗した場合は新DBへ追記を再開せず旧snapshotへ戻す。Step 02を完了扱いに
  できるのは、cutover subreceiptとこれらの運用receiptが同じsource／build／runtime checksum chainへ結合した後だけとする。

---

### Step 03: DCIのEventID誤用を置換

置換:

```text
旧 search EventID → ActionID
旧 derived EvidenceID → 独立EvidenceID
```

各検索StepへEventIDを追加する。

Test:

- Search success
- No evidence
- Limit reached
- File read failure
- Timeout
- Evidence作成Eventの逆参照

完了条件:

- DCIでEventIDが検索全体IDとして使われていない
- EvidenceIDがEventID文字列から派生していない

#### Step 03配備契約

- DCI検索は一つの`ActionID`と一つの`TraceID`を持つ。検索結果、検索trace、冪等再送、
  Data Write receiptは検索全体を`EventID`または`TraceID`で識別しない。冪等キーはIDとは別fieldに保存し、
  RequestIDのhashや文字列連結からActionIDを作らない。
- 新規検索では、ownerが`ActionID`と`EvidenceID`をUUIDv7で生成し、
  `dci.search.requested`、`dci.search.started`、`dci.source.selected`、`dci.file.read`、
  `dci.evidence.created`、`dci.search.completed`または`dci.search.failed`をCanonical Event Storeへ同期appendする。
  `SearchStep.EventID`は対応する`dci.file.read`を、Evidenceの`CreatedByEventID`は対応する
  `dci.evidence.created`を逆参照する。Canonical Event appendに失敗したrequestは成功応答や成功projectionを返さない。
- 新規runtime検索の`actor_attribution`は`authenticated`だけを許可し、認証済みuserまたは実CORE Agentの
  `ActorKind`／`ActorID`を必須にする。旧履歴の`Worker`のような実装roleはAgentへ推定置換しない。
  canonical Agent catalogまたは認証済みuser記録へ一致する根拠がない旧labelは、migration時だけ
  `actor_attribution=legacy_unattributed`、空の`ActorKind`／`ActorID`として保存し、元labelはmigration Event payloadと
  bounded receiptの分類Evidenceにだけ残す。通常のruntime writerは`legacy_unattributed`を生成・更新できない。
- DCIの現在状態SQLiteは`action_id`を親keyとし、stepの`event_id`、Evidenceの
  `created_by_event_id`を検索親keyと兼用しない。Viewer、public client、Data Recall、L1 current projectionも
  同じ`action_id`、`trace_id`、実Event参照だけを公開する。
- 新SQLite schemaは明示versionとforeign keyを持ち、旧`event_id`親schemaを通常起動時に自動変換しない。
  旧schema、version不明、必須column／index不足はfail closedとし、checksum-bound migration outputだけを受理する。
- 旧SQLite、旧JSONL、L1 staging current／archiveに同じ旧検索またはEvidenceが重複している場合、
  migration内の一時mapでdedupeし、同じ旧値を一つのCanonical ActionID、TraceID、EvidenceID、EventIDへ
  UUIDv5変換する。runtime alias、dual read、dual write、旧JSON key fallbackを残さない。
- 過去分は、旧trace rowの開始／終端、旧`read_file` step、旧Evidence rowまたはstaging rowのように
  発生済み事実を直接示すrecordだけをCanonical Eventへ変換する。独立recordが存在しない
  `dci.search.requested`と`dci.source.selected`は生成しない。旧`limit` stepは独立Eventへ偽装せず、
  terminal Event payloadとmigration receiptの除外理由へ保存する。
- 移行計画の`dci.search.completed`／`dci.search.failed`は、同じ検索の全`dci.evidence.created`を閉じたjoinで
  束縛する。Evidenceが1件以上なら決定的に最後のEvidence Eventを`CausationEventID`とし、それより前の全Evidence
  EventIDを重複なくsorted `DependencyEventIDs`へ入れる。Evidenceが0件なら従来どおり最後のreadまたはstartedをcauseとし、
  dependencyは持たない。各Evidenceのreadまたはstartedへのcausationは変更せず、全体graphを検証してから受理する。
- 移行計画のEvent payloadは旧IDを完全に置換し、`legacy_search_id`、`legacy_evidence_id`、
  `legacy_step_no`、`legacy_final_evidence_count`、`search_event_id`と、それらのraw valueを保持しない。
  step番号は`step_no`、最終件数はcanonicalな`evidence_count`へ統一する。migration metadataとして
  許可する旧語彙は`legacy_actor_label`だけであり、旧`limit` stepの除外を示すterminal payloadの
  `legacy_limit_steps`／`limitations`だけを例外とする。dry-runはplanned payloadを再帰的にexact-keyと
  raw legacy ID valueで検査し、1件でも検出したらblockedにする。`planned_zero_counters.legacy_key_zero`
  は旧keyとraw valueの検出数を合算した検査結果から測定し、固定値を出力しない。
- activeな旧DCI JSONLはruntime inputから外し、checksum-bound cutover時にrollback rootへ退避する。
  immutableな旧監査logはmigration入力Evidenceとして保持できるが、current DCI API、current projection、
  owner lookup、runtime writerから参照しない。
- legacy sourceの`TEXT`はinvalid UTF-8を含み得る。capture artifactは各source DBのraw bytesをimmutableなrollback
  evidenceとして保持し、L1／archiveの`raw_text`／`raw_hash`は変更しない。canonical derived DCI／Event snippetだけをplanning前に
  固定algorithm `rencrow.utf8.invalid-byte-replacement/v1`でnormalizeする。invalid byte一つを一つの`U+FFFD`へ
  置換し、既に有効な`U+FFFD`は変更しない。dry-run／build receiptはexpected／actual normalized valueと
  invalid byte countを束縛し、一致しなければfail closedとする。
- production snapshotのreceiptはschema `rencrow.identity.dci-migration/v2`と、固定algorithm
  `rencrow.sqlite.logical/v1`を持つ。`source_database_logical_sha256`と`source_schema_sha256`は
  `source_dci`、`source_event_store`、`source_l1`、`source_archive`の4 source、
  `source_dci_classification_sha256`はJSONLを含む5 source、`source_file_sha256`はJSONLだけ、
  `source_non_dci_logical_sha256`はEvent Store／L1／archiveの3 sourceをexact key setで束縛する。
  ready receiptは全値をlowercase SHA-256とし、旧v1 receiptはapplyへ渡さない。
- `event_plan_sha256`は、planned EventEnvelopeをpayload／Event graph検証した後、完全なcanonical EventEnvelopeを
  JSON化した行をsortedしてhashするlowercase SHA-256である。Event sliceの順序とsnapshot rootには依存せず、payload、
  envelope field、causationの変更を検出する。ready receiptでは必須かつ有効な値とし、blocked receiptだけは省略できる。
  receiptの明文fieldへ個別payloadやsource pathを含めず、hashはcanonical EventEnvelope JSON linesだけから計算する。
- SQLite logical hashはread-only/query-onlyの同一source-open windowで、headerの
  `user_version`／`application_id`／`encoding`、canonical `sqlite_schema`、`table_xinfo`、
  `sqlite_sequence`を含む全user／shadow tableの全row/valueをtyped length-prefixで読む。
  row order、rowid、page size、VACUUM、filesystem pathをhashへ含めず、tableごとのsorted 32-byte row digest、
  cell／row／column／context bound、未知型拒否でfail closedにする。L1 non-DCI hashは分類済みDCI stagingの
  primary keyとcurrent registryのsource_idだけを除外し、FTS／projectionを含む他のrow/tableを除外しない。
- production cutoverはCORE writer停止後に、DCI SQLite、Canonical Event Store、Conversation L1 current、
  Conversation archive、runtime binaryを同じrollback rootへ保存し、source logical hashとbuild receipt SHA-256を
  再照合してから一括置換する。一つでも失敗した場合は全対象を旧snapshotへ戻し、新旧を混在させない。
- build／apply receiptはsource別件数、dedupe後件数、変換Event数、除外理由別件数、ActionID／EvidenceID／EventID set hash、
  完全なplanned EventEnvelope内容の`event_plan_sha256`、
  output DB hash、旧column／旧JSON key／旧lookup zero、orphan zeroをbounded JSONで固定する。
  dry-runとbuild再実行は同じmapping hashと`event_plan_sha256`を返す。manifest validatorはreadyで欠落・不正な
  `event_plan_sha256`をfail closedにする。
- 配備後は実Actorが正規CORE routeからDCI検索を行い、APIのActionID／TraceID、全step EventID、
  Evidence CreatedByEventID、Canonical Event graph、L1 staging参照、Data Write冪等再送、再起動後lookupを
  一つのreceipt chainで照合する。

#### Step 03 Failure Knowledge: 検索ActionをEventとして保存したDCI履歴分断

- **Failure:** 検索全体を`EventID`と呼び、子stepの親key、EvidenceIDの文字列prefix、Viewer表示、
  Data Write audit reference、L1 staging EventIDへ兼用した。
- **Problem:** 一つのActionと複数の発生済み事実を区別できず、Evidenceから作成Eventを逆引きできない。
  同じ値をTraceIDとして返すconsumerも生じ、検索の因果graphと冪等性を証明できなかった。
- **Cause:** 検索単位、発生事実、保存根拠、冪等tokenを一つの文字列へ圧縮し、DCI state storeを
  Event Storeの代替として扱った。
- **Lesson:** prefix変更やfield renameだけでは意味は直らない。Action、Event、Evidence、Trace、
  idempotency keyを別々のowner fieldと永続参照で強制し、過去変換は実在recordだけから行う。
- **Invariant:** 検索全体はActionID、検索事実はEventID、保存根拠はEvidenceIDである。
  EvidenceIDはEventIDから派生せず、Evidenceは作成Eventを逆参照する。
- **Enforcement:** typed Canonical ID、SQLite column／foreign key、owner input validation、Canonical Event同期append、
  runtime JSONの旧key拒否、migration-only UUIDv5 map、legacy zero architecture testで強制する。
- **Tests:** success、no evidence、limit、file read failure、timeout、Evidence逆参照、Event Store failure、
  idempotent replay、snapshot dry-run、second-run mapping一致、partial cutover rollback、配備後実Actor DCIを検査する。

#### Step 03 Failure Knowledge: rollback aliasによる旧ID payload残存

- **Failure:** rollbackやlookupを簡単にするため、planned Event payloadへ旧検索／Evidence IDまたは旧JSON keyを
  aliasとして残し、`legacy_key_zero`を固定値0でreceiptへ出力した。
- **Problem:** 旧IDと新IDの併記がproduction payloadへ残り、Gate 7の旧JSON key／旧lookup zeroを証明できない。
  payloadを使ったrollbackは新旧の意味を混在させ、監査上のAction／Event／Evidence分離も再び崩す。
- **Cause:** rollbackの証拠をpayload aliasへ持たせ、同じsnapshotから再計算できるUUIDv5 mappingとmapping hashを
  使わず、planned payloadを実際に走査せずにzeroを宣言した。
- **Lesson:** rollbackは4 DBとbinaryを同じrollback rootへ保存したchecksum-bound artifactから行う。旧recordの変換は
  固定されたmigration-only UUIDv5 ruleで再現し、mapping hashを再照合する。rollback根拠をproduction payload aliasへ
  保存してはならない。
- **Invariant:** planned Event payloadに旧ID／旧keyは存在せず、許可されるmigration metadataは
  `legacy_actor_label`とterminalの`legacy_limit_steps`／`limitations`だけである。ready receiptはさらに完全な
  planned EventEnvelope内容を束縛する`event_plan_sha256`を持つ。
- **Enforcement:** 再帰的なexact-key／raw legacy ID validatorをbuild前に実行し、nonzeroならfail closedにする。
  receiptの`planned_zero_counters.legacy_key_zero`は旧keyとraw valueの検出数を合算した測定結果だけを反映する。
  payload／graph検証後にcanonical EventEnvelope全体をsorted JSON linesとしてhashし、ready manifestの欠落・不正な
  `event_plan_sha256`を拒否する。
- **Tests:** 全planned Eventの旧key／raw legacy ID zero、許可metadata保持、actor classificationとmapping／ID set hashの
  再実行一致、禁止payloadの注入拒否、Event plan hashのroot／slice order非依存とpayload／causation変更検出、ready欠落・
  不正値拒否、rollback root／UUIDv5／mapping hashの再照合境界を検査する。

#### Step 03 Failure Knowledge: terminal EventがEvidence branchを閉じない

- **Failure:** terminalのcauseを最後のEvidenceだけへ設定し、同じ検索の他の`dci.evidence.created` branchを
  `DependencyEventIDs`へ含めなかった。
- **Problem:** terminal Eventから全Evidenceが到達可能であることを証明できず、並列のEvidence作成結果を含む検索完了／失敗の
  closed graphにならない。
- **Cause:** Evidenceを単一の線形`lastEvent`として扱い、terminalをjoinとして構築しなかった。
- **Lesson:** 各Evidenceのreadまたはstartedへのcausationは保持したまま、terminalだけを全branchのjoinにする。最後の決定的な
  Evidenceをcause、それ以前のEvidenceをsorted dependenciesとして、payloadではなく既存のEvent graph参照で束縛する。
- **Invariant:** Evidenceが複数ならterminalのcauseとdependenciesの和集合は各Evidence EventIDを重複なく一度ずつ含み、causeをdependencyへ
  重複させない。Evidenceが0件は最後のread／started cause、1件は追加dependencyなしとする。
- **Enforcement:** planned Eventをgraph検証し、terminalの`CausationEventID`／`DependencyEventIDs`を含む完全なEnvelope setを
  `event_plan_sha256`へ束縛する。
- **Tests:** 2 read／3 Evidenceのclosed join、zero／one Evidence、sorted dependency、cause重複拒否、graph validation、
  root／order-independent plan hashを検査する。

#### Step 03 Failure Knowledge: selective source hashとrows保持によるcutover証拠欠落

- **Failure:** DCI分類行だけをsource hashへ記録し、L1非DCI row、schema、allocator、Event Store全tableを
  協調cutoverの証拠として束縛していなかった。また、SQLite table名rowsを開いたまま`table_xinfo`を同じ
  `MaxOpenConns(1)`接続へ発行し、dry-runがdeadlineまで自己待機した。
- **Problem:** DCI classificationが一致しても同時置換対象の別table／schemaを検出できず、sourceを正しく
  再照合できない。rows保持時はbounded receiptを発行する前に検査がtimeoutする。
- **Cause:** 「移行対象の意味行」と「一括置換するdatabase全体」を一つのhashへ混同し、DB connectionの
  cursor lifecycleを一覧取得とdescriptor取得で分離しなかった。
- **Lesson:** classification hashは対象行の説明でありcutover証拠ではない。v2はfull／schema／non-DCIを
  独立したalgorithm-bound hashとして発行し、schema rowsをboundedにmaterializeして`Err`／`Close`を確認後、
  別loopでcolumn queryを行う。
- **Invariant:** ready receiptはv2のexact hash mapと固定algorithmを満たし、v1はapply-eligibleにならない。
  logical hashはpath／page／row順に依存せず、memory／cell／row／context boundを超えたら失敗する。
- **Enforcement:** `table_xinfo`、typed length-prefix、table単位sorted digest、L1 classified primary-key-only
  exclusion、rows materialize-close-query sequence、manifest exact-map validation、atomic 0600 receiptで強制する。
- **Tests:** insertion order／page size／VACUUM不変、content／schema／allocator／duplicate、L1 DCI／non-DCI mutation、
  shadow／projection inclusion、旧rows-held構造のdeadlineと修正版の即時完了、bound／unknown type／v1 rejection、
  root-independent bounded receiptを検査する。

#### Step 03 Failure Knowledge: implicit encoding/json replacementによるDCI raw persistenceの分岐

- **Failure:** legacy `TEXT`のinvalid UTF-8をdecoder／`encoding/json`のimplicit replacementへ任せる一方、DCI raw
  persistenceは別のbytesを保持し、capture artifactの各source DB raw bytesをrollback evidenceとして固定しなかった。
- **Problem:** dry-runとbuildが同じsourceから異なるcanonical DCI／Event textを計画し、normalized value／invalid byte
  countを再現できず、rollback時に原bytesを復元・監査できない。
- **Cause:** raw source bytesとcanonical derived textを同じ表現として扱い、planning boundaryで明示的なnormalizationを
  せず、serializerのreplacement behaviorを正本にした。
- **Lesson:** capture artifactは各source DBのraw bytesをimmutableに保持し、L1／archiveの`raw_text`／`raw_hash`は変更せず、
  canonical derived DCI／Event snippetだけをplanning前に `rencrow.utf8.invalid-byte-replacement/v1` でnormalizeする。
  一つのplanは全てのdownstreamで同じnormalized valueを使う。
- **Invariant:** 一つのmigration plan内のexpected／actual normalized valueとinvalid byte countは一致し、capture artifactの各source
  DB raw bytesとL1／archiveの`raw_text`／`raw_hash`は変化しない。どれか一つでも不一致ならfail closedとする。
- **Enforcement:** capture receiptはphysical raw evidenceだけをhash-boundにし、dry-run／build receiptはnormalized valueと
  invalid byte countのexpected／actualを束縛する。`encoding/json`のimplicit replacementをcanonical valueの決定に使わず、
  固定algorithm以外の結果またはmismatchを受理しない。
- **Tests:** invalid byte一つごとの一つの`U+FFFD`置換、validな`U+FFFD`の保持、同一plan内のdry-run／build normalized valueと
  invalid byte countの一致、mismatchのblocked、capture artifact raw bytes／L1／archiveの`raw_text`／`raw_hash`の不変性と
  rollback evidenceを検査する。

---

### D1b-2c: offline four-store build and bounded receipt

D1b-2c は、D1b-2a の capture receipt と D1a dry-run の ready manifest を
同一の immutable snapshot として再検証し、保持された一つの migration plan から
offline の四つの SQLite 出力を作る工程である。公開入口は
`Build(ctx, BuildOptions)` だけとし、`build.json` は
`rencrow.identity.dci-build/v1`、`mode=build`、`status=ready|blocked` の
bounded receipt とする。この工程は production apply、cutover、rollback、service
runtime の変更を実行したことを意味しない。

#### Contract and invariants

- `BuildOptions` は `SnapshotDir`、`BuildDir`、`CaptureReceipt`、
  `DryRunManifest`、`AgentIDs` を明示する。snapshot root と capture／manifest は
  canonical な既存 root 内の regular non-symlink input で、capture receipt は
  ready capture schema、manifest は ready dry-run schema として strict JSON（一つの
  JSON value、unknown field と trailing token を拒否）で読む。
- receipt の file SHA-256、bytes、artifact-set SHA-256 と各 captured artifact を
  再計算し、分類／plan は保持された plan へ一度だけ束縛する。caller の expected
  count や AgentIDs で再計画せず、classifier の manifest が supplied manifest と
  semantic exact equality でない場合は blocked とする。source、capture receipt、
  dry-run manifest は operation 前後で同一でなければならない。
- fresh な canonical build root を 0700 で作り、owner API 経由で固定名
  `target-dci.db`、`target-event-store.db`、`target-l1.db`、`target-archive.db` を
  0600 で一度だけ生成する。owner の read-only verification evidence（schema／
  logical／non-DCI hash、counts、quick-check、foreign-key、sidecar zero）を
  `build.json` へ bounded projection として結合し、output artifact-set hash と
  exact key set を検証する。
- ready root の直下は四 DB と `build.json` の五 entry だけで、SQLite sidecar は
  zero である。途中失敗または receipt write／final verification failure は output
  と sidecar を全て削除する。safe に作成済みの root へ blocked receipt を durable
  に書ける場合だけ `build.json` を残し、blocked receipt 自体を書けなければ empty
  root とする。どちらの場合も ready を返さない。
- receipt は path、query、snippet、command、payload、secret、個別の canonical／
  legacy ID を含めず、capture／manifest hash、source hash maps、mapping／action／
  trace／evidence／event／event-plan hash、expected／actual／dedupe／actor counts、
  planned zero counters、四 output の hash／bytes／health と owner bounded checks
  だけを公開する。ready では measured legacy key、orphan、foreign-key、sidecar
  counters が全て zero でなければならない。

#### Failure Knowledge

- **Failure:** build が capture／dry-run を再実行して ID または actor attribution を
  再計算し、receipt と出力 DB／Event Store の plan が分裂した。
- **Problem:** 同じ snapshot でも output の mapping／event-plan hash と既存 manifest
  が一致せず、source drift を検出できない。
- **Cause:** prepare と materialize の境界を公開 Build から隠さず、caller flags／
  default source を別の入力として許した。
- **Lesson:** prepare は strict binding、classification 一回、plan retention を
  所有し、Build はその private prepared input のみを owner helper へ渡す。
- **Invariant:** ready receipt の manifest projection、capture／dry-run bytes hash、
  artifact set、四 output evidence は一つの prepared input と exact semantic equality
  で結合される。source/input drift、unsafe path、non-fresh root、owner evidence
  mismatch は fail closed である。
- **Enforcement:** strict bounded reader、canonical／alias path guard、owner helper
  の単一呼び出し、atomic 0600 receipt、root／parent sync、final exact-root／hash／
  input recheck、blocked cleanup と bounded generic error で機械的に強制する。
- **Tests:** ready／blocked receipt の schema、key set、size、permission、hash／bytes／
  aggregate、owner evidence／zero counters、path-free output、context／source drift、
  non-fresh／symlink root、各 output 後の failure、receipt writer failure、repeat build
  の plan determinism、CLI flag isolation と bounded stdout／stderr を検査する。

### Step 04: SessionID

置換:

- 日付埋込みSessionIDをCanonical UUIDへ移行
- 日付は`logical_date`へ分離
- Channel情報はChannelAddressへ分離

Test:

- Cutover
- Session reconstruction
- Existing history
- Date boundary
- Concurrent session creation

完了条件:

- SessionID文字列から日付、Channel、Userを解析するコードzero

#### Step 04配備契約

- `SessionID`は`ses_` prefix付きCanonical UUIDv7だけを受理し、`logical_date`と
  `ChannelAddress`は独立fieldとしてSession repositoryが保存・検索する。
- requestが`SessionID`を明示しない場合だけ、CORE ingressは`logical_date + ChannelAddress`で
  同一日Sessionをresolveし、不在時にCanonical `SessionID`を一度生成する。明示IDのload失敗を
  新規Session作成へfallbackしない。
- production cutoverはactive configが指すSession rootとwriter ownerを確認し、CORE停止後の
  snapshotからfresh siblingへmaterializeする。source/output hash、既存history、Session再構築、
  legacy count zeroを確認してから同一filesystem renameで切り替え、旧rootと旧runtimeをrollback用に保持する。
- 配備後はinstalled binary、service PID executable、fixed `rencrow.service`、`:18790` listener、
  readiness、実Actor `POST /viewer/send`、保存されたCanonical Session JSONを一つの証拠鎖として照合する。
- cutover専用変換CLI／packageはproduction receipt確定後に削除し、runtime dual read／dual write、
  legacy constructor、legacy JSON fieldを残さない。

#### Step 04 Failure Knowledge: default Session pathをactive sourceと誤認した

- **Failure:** inactiveな`~/.rencrow/sessions`をproduction source候補として先に監査した。
- **Problem:** 正しいactive rootより少ないlegacy Sessionとhistoryをproduction現況として扱う危険があった。
- **Cause:** default pathの存在をactive config、service environment、writer ownerより先に根拠化した。
- **Lesson:** data pathは既定値や過去配置から推測せず、active configとservice ownerから解決する。
- **Invariant:** Session cutover inputはfixed serviceの`RENCROW_CONFIG`が指すexisting rootに限定し、
  writer停止後に取得したsource hashだけをapplyへ渡す。
- **Enforcement:** active config／service identity確認、stopped PID／listener evidence、dry-runとapplyの
  exact receipt binding、fresh output、post-start canonical-only scanで強制する。
- **Tests:** source drift、fresh output、receipt path containment、legacy/canonical/non-session counts、
  history再構築、date boundary、concurrent creation、production E2Eを検査する。

#### Step 04 Failure Knowledge: 短いreadiness期限をstartup failureと誤認した

- **Failure:** restart後30秒の最初のpollでは`:18790`がまだlistenせずreadinessを得られなかった。
- **Problem:** processが正規startupを継続中でも早期rollbackすると、正常な配備を失敗扱いにする。
- **Cause:** COREのL1 store等の初期化時間をservice ownerのbounded running timeoutより短く評価した。
- **Lesson:** listener未生成だけで失敗を断定せず、PID、cgroup、restart count、startup phase、DB ownerを
  同時に観測し、正規owner timeout内は同じgenerationを追跡する。
- **Invariant:** `rencrow.service`がactive、MainPID positive、NRestarts zeroで進行中なら、fatal／panic／
  typed startup errorがない限り最大300秒のowner timeoutまで同じprocessを監視する。
- **Enforcement:** fixed timeout、30秒単位のpoll、journalとprocess evidence、最終readiness／listener／
  executable hash照合で強制する。
- **Tests:** delayed readiness、process exit、restart、wrong executable、timeoutをservice lifecycle testで検査する。

---

### Step 05: ThreadID

置換:

- 数値ThreadIDを`ThreadSeq`へ改名
- UUID ThreadIDを新主キーにする
- Agent discussion、IdleChatもThreadへ統一
- DiscussionIDを削除

Test:

- Thread open / close
- Boundary
- ClosedThread
- Thread follower
- Cross-reference
- Concurrent creation

完了条件:

- `DiscussionID` zero
- ThreadIDを整数として扱うコードzero

#### Step 05 migration invariant

- 移行入力は、writer停止中の一つの immutable cohort に含まれる L1、Archive、
  ChatGPT Raw、IdleChat topic、Redis論理snapshot、Qdrant論理snapshotだけとする。
  各surfaceが別の時点で作ったplanを結合せず、同じsource hash群から一つの
  migration-only mapping planを決定的に作る。
- Redisは取得時の相対TTLではなくserverが保持する絶対失効時刻をsnapshotへ保存する。
  apply時はその時刻から残存時間を決定し、期限切れkeyを復活させない。
- Qdrant取得は既存collectionに対するread-only scrollだけを使う。collection作成、
  index作成、upsert、deleteを行うruntime store constructorは移行captureに使わない。
- RedisのSCANとQdrantのscrollは単独でsnapshot isolationを証明しない。論理artifactは
  自己hashと件数を持つが、ownerがwriter停止、active source route identity、sourceの論理snapshot
  hash群、fresh-only targetと保持済み旧configのrollback evidence、artifact hashを同一停止窓の
  receiptへ束縛するまで`runtime_ready`を名乗らない。
- persistent WALを使うL1／Archiveは、固定CORE serviceのPIDとlistenerがzeroになった後、
  owner CLIが`wal_checkpoint(TRUNCATE)`のbusy zero、同一file identity、`journal_mode=DELETE`、
  sidecar zeroを証明してから同じ停止窓のsnapshotへ含める。WAL／SHMを手動削除しない。
- sourceは不変、出力はfresh-onlyとし、in-place更新は行わない。L1、Archive、topic、
  Redis、Qdrantの全出力が同一mapping hashを持ち、件数、NULL、duplicate、orphan、
  legacy field zeroを検証した後だけcutover対象にできる。
- `l1_profile_promotion_job` の `l1_memory_event` evidence欠損は、予期しないorphanとして
  常にzeroにする。ただし通常のpruneで発生する `state=completed` または `state=failed` の
  terminal jobだけは、job自身の厳密な `(session_id, thread_id)` をgeneric factとして保持し、
  `preserved_terminal_orphans` 件数をSQLite inventory receiptとoffline build receiptへ束縛する。Archive等が
  同じtupleを識別した場合は後段のcanonical分類を優先し、`pending`等の非terminal orphanは拒否する。
- cutoverはwriter停止中にL1、Archive、topic、active config、CORE runtimeを同じbuild／stage
  receiptへ束縛して置換し、旧5 artifactをhash由来の固定名で保持する。配備後検証に失敗した
  場合はowner CLIがimmutableなcutover receipt、旧／新hash、SQLite sidecar zeroを再検証して
  5 artifactを一括復旧する。fresh Redis DBとQdrant collectionは削除せずunclaimedのまま残し、
  旧configが指す既存routeへ戻す。

#### Step 05 Failure Knowledge: 相対TTLと非停止scrollを移行snapshotと誤認した

- **Failure:** Redisの相対TTLと、writer稼働中のRedis SCAN、Qdrant scrollをそのまま
  apply-ready snapshotと扱った。
- **Problem:** applyが遅れるとkeyの寿命が延び、page間の更新で取りこぼし、重複、時点の
  異なるsurfaceが同じmappingへ混入する。
- **Cause:** 論理値の読取り成功と、ownerによる停止窓・rollback・source identityの証明を
  一つの保証とみなした。
- **Lesson:** 有効期限は絶対時刻で固定し、論理captureとquiescence証明を分離した上で、
  outer owner receiptが同じ停止窓へ束縛する。
- **Invariant:** 相対TTL、未停止SCAN／scroll、collectionを作成し得るcapture route、未束縛の
  `runtime_ready`はすべて拒否する。
- **Enforcement:** Redis絶対失効時刻型、Qdrant read-only client、bounded pagination、strict snapshot
  hash、fresh-only publication、owner stop-window receipt、全surface mapping hash照合で強制する。
- **Tests:** TTLのapply遅延・期限切れ、SCAN／scrollのcursor非進行・重複・中断、Qdrantの
  mutation zero、artifact tamper、source hash不一致、writer停止未証明、異mapping hashを検査する。

#### Step 05 Failure Knowledge: terminal profile jobをevidence orphanとして捨てた

- **Failure:** 通常のL1 pruneで親`l1_memory_event`だけが消えたterminal profile jobを、予期しない
  orphanとして一律に移行拒否した。
- **Problem:** 完了／失敗のdiagnostic stateと厳密なlegacy tupleが失われ、RetryFailedProfilePromotionJobs
  と移行後の監査追跡が分断された。
- **Cause:** evidence存在をjob stateより強い前提とし、terminal lifecycle orphanと非terminal整合性違反を
  同じ扱いにした。
- **Lesson:** job identityを先に検証し、terminal stateだけを明示的に保持してreceipt件数へ束縛する。
- **Invariant:** evidence欠損terminal jobはgeneric factとして保持し、Archive等の同一tuple識別時は
  ChatGPT分類へ再分類する。非terminal orphanと他surfaceのpreserved countはzeroで拒否する。
- **Enforcement:** SQLite inventory receipt v2の`preserved_terminal_orphans`、state allowlist、canonical
  mapping hash、materializerの厳密tuple解決で強制する。
- **Tests:** completed／failed orphanのcount・generic／ChatGPT mapping・materialization、pending orphan拒否、
  preserved countの範囲／surface制約とreceipt改ざんを検査する。

#### Step 05 Failure Knowledge: legacy turn receipt/outbox SessionIDをcanonical inputと誤認した

- **Failure:** productionの`conversation_turn_receipt`／`conversation_turn_outbox`に残る`viewer-user`や
  `agent-ops`などのlegacy `session_id`を、inventoryの入力段階でcanonical `ses_<UUID>`でなければ拒否した。
- **Problem:** 既存の決定的な`canonicalGenericSessionID`／UUIDv5 migration routeへ到達する前にStep 05が停止し、
  SQLと埋込みJSONのidentityが整合しているlegacy turnをmaterializeできなかった。
- **Cause:** legacy入力のpresence／cross-row equality検証と、migration後に要求されるcanonical出力検証を同じ
  preconditionとして扱った。
- **Lesson:** inventoryは非空のlegacy session文字列をbyte-exactに保持してSQL／embedded identityの一致だけを
  検証し、plan normalizationとmaterializerの出力境界でのみcanonical `SessionID`へ変換・検証する。
- **Invariant:** empty／whitespace session、SQL／embedded／receipt間のtuple mismatch、numericまたは不正JSONは
  fail closedのまま、非空legacy sessionは`session_files.id`をsourceにした同一UUIDv5 migration IDへ収束する。
- **Enforcement:** `contextAndIdentity`のpresence／SQLite integer gate、receipt／outboxの厳密なSQL／JSON equality、
  `canonicalGenericSessionID`、canonical SQL／JSON materialization auditを分離して強制する。
- **Tests:** `viewer-user`等のlegacy receipt／outbox受理、planのcanonical mapping、materialized SQL／embedded JSONの
  exact SessionID、empty／mismatch／numeric／JSON violationの拒否を検査する。

#### Step 05 Failure Knowledge: Archiveのoptional-zero空Sessionを不完全tupleと誤認した

- **Failure:** `l1_memory_event_archive` の正当な `(session_id='', thread_id=0)` を inventory が拒否した。
- **Problem:** `legacyOptionalZeroSurfaces` が許可する unthreaded archive row が cohort prepare で停止し、mapping／receipt／materialization へ到達できなかった。
- **Cause:** `contextAndIdentity` の空Session許可面に Archive が含まれていなかった。
- **Lesson / Invariant:** `allowZero` が選択され `thread_id==0` の L1 event／event log／archive だけは空Sessionを許可し、mappingを出さず optional-zero receiptへ数える。Archive materialization は空Session＋0を `(session_id='', thread_id='', thread_seq=0, thread_kind='')` として保持する。positive thread＋空Sessionと ChatGPT source＋0 は引き続き fail closed とする。
- **Enforcement / Tests:** `legacyOptionalZeroSurfaces`、`contextAndIdentity`、receipt count relationship、`resolveSQLiteOptionalThreadTuple`、canonical archive tuple CHECK、および空／非空 optional-zero・positive／ChatGPT rejection testsで強制する。

#### Step 05 Failure Knowledge: ChatGPT Raw bindingのN+1 scan

- **Failure:** pending bindingごとに、indexのない`l1_raw_record`を全走査していた。
- **Problem:** 同じRaw sourceへの反復参照があるcohortで、ChatGPT provenance検証の計算量が参照数に比例して増え、inventoryがboundedな検証期限へ到達できなくなった。
- **Cause:** binding単位のtable／column確認と`source_record_id` queryを、同じRaw storeに対して繰り返していた。
- **Lesson / Invariant:** pending bindingを`source_record_id -> exact conversation_id`へ決定的に縮約し、同一conversationの重複参照は一つの期待値として扱う。conflict、missing、duplicate、wrong source／thread／SQLite typeはfail closedとし、decoy Raw rowは無視する。pendingがあるinventoryではtable確認、column確認、ordered Raw scanを各一回だけ行う。
- **Enforcement / Tests:** sorted pending／expected order、one-pass ordered query、sourceごとのmatch count、`TestInventorySQLiteAcceptsRepeatedChatGPTRawBindingAndIgnoresDecoys`、およびconflict／missing／duplicate／wrong provenance rejection testsで強制する。

#### Step 05 Failure Knowledge: shared short timeoutによるoffline build中断

- **Failure:** capture／verify／stage／cutover／rollback／quiesce向けの`externalOperationTimeout=5m`を、複数passのdeterministicなlocal offline buildにも共有したため、cohort prepareがcontext deadlineで終了した。
- **Problem:** source fingerprint、SQLite clone、topic／Qdrant prepare、L1／Archive materialize／finalize／hashを含む外部通信不要のbuildが、boundedな出力receiptをreadyまで完了できず`cohort_prepare`でblockedになった。
- **Cause:** 異なるcost profileとfailure boundaryを持つ操作へ一つの短いdurationを適用し、network／external操作のdeadlineをlocal buildの安全境界として流用した。
- **Lesson:** timeoutの安全性は一つの共通durationではなく、owner operationごとのfixed／bounded contextで保つ。external operationは5分を維持し、offline buildだけ30分を使い、unbounded contextやtimeout flagへ逃がさない。
- **Invariant:** `build`だけが`offlineBuildOperationTimeout=30m`を使い、capture／verify／stage／cutover／rollback／quiesceは`externalOperationTimeout=5m`を使う。各operationのcontextはreturn時にcancelし、invalid argumentsはoperationを呼ばずに拒否する。
- **Enforcement / Tests:** command-local constantsと`context.WithTimeout`で強制し、`TestRunBuildUsesOfflineOperationDeadline`と既存のcapture deadline／cancellation／invalid-argument testsで、deadline、return後cancel、bounded path-free receipt、operation非呼出しを検査する。

#### Step 05 Failure Knowledge: owner外triggerのL1 swap参照切断

- **Failure:** disposable cloneでcanonical六表をdropしてstage tableをrenameする間、非対象tableに付いたowner triggerが`l1_profile_promotion_job`を参照し、`rename_stage`で`no such table`になった。
- **Problem:** triggerが付いた非対象tableは六表のdropで自動削除されず、canonical nameが一時的に存在しないswap windowをSQLiteのschema再検証が観測したため、L1 materializationはatomic commitへ到達できなかった。
- **Cause:** generic Step05 materializerがowner triggerの名前やSQLを所有せず、依存triggerを一時退避する境界も持たないまま、target tableだけをdrop／renameしていた。
- **Lesson:** swap transaction内で`sqlite_master`から、canonical六表以外に付いたtriggerのうちSQLがcanonical table nameを参照するものをnameとexact SQLで決定的にsnapshotし、target drop前にそのtriggerだけをdropし、六表のrename後にexact SQLで再作成する。owner schemaの定義やtrigger名をthreadmigrationへ複製しない。
- **Invariant:** sourceは変更せず、destinationの六表swapと依存trigger退避／再作成は同一transactionでatomicに扱う。snapshot／drop／recreateの失敗はbounded typed errorでrollbackし、commit後にcanonical nameの参照gapを残さない。receiptは従来どおり`materialized_l1_not_runtime_ready`かつowner schema reconciliation requiredのままとする。
- **Enforcement / Tests:** `snapshotDependentL1Triggers`の決定的なname順列挙、外部triggerだけのquoted drop、exact SQL再作成、および`TestMaterializeL1SQLitePreservesExternalDependentTrigger`でtriggerのname／table／SQL保持とcanonical swapped tableを読む発火結果を検査する。後続のowner open／reconciliationは従来どおりowner moduleが行う。

#### Step 05 Failure Knowledge: runtime write後のmutable rollback hash拒否

- **Failure:** cutover後の正規runtimeがL1／Archive／Topicへ正当なwriteを行った後、rollback-cutoverがactive artifactのhashをcutover receiptのnew hashと比較して拒否した。
- **Problem:** post-cutover runtime failureでold generationへ戻す際、mutable active artifactを安全に退避できず、rollbackの終端へ到達できなかった。
- **Cause:** runtime／configと、稼働中に変化し得るL1／Archive／Topicを同じimmutable new-hash前提で扱い、displaced active bytesを検証する観測hashを保持していなかった。
- **Lesson / Invariant:** rollback preflightはL1／Archive／Topicだけ既存のregular-file／mode／SQLite sidecar／path／overlap検査後の観測hashを内部保持し、そのactive bytesを既存candidate pathへ退避する。config／runtimeは引き続きcutover receiptのnew hashへ完全一致し、old rollback artifactはold hashへ完全一致する。postcheckはmutable candidateを観測hash、immutable candidateをnew hash、全targetをold hashで検査する。
- **Enforcement / Tests:** `isMutableRollbackRole`、`prepareExplicitRollbackState`、`postcheckExplicitRollback`と`TestPrepareExplicitRollbackAcceptsMutableDriftAndPreservesDisplacedActive`、config／runtime drift rejection testsで強制する。swapは既存`rollbackCutoverSwaps`だけを使い、receipt schema、route、manual file moveを変更しない。

#### Step 05 Failure Knowledge: TaskのChatIDをcanonical SessionIDへ誤用した会話commit

- **Failure:** Viewer requestのcanonical `SessionID`とexternal `ChatID`が別identityなのに、TaskへSessionIDを保持せず、Agentの会話BeginTurn／CommitConversationTurn／LightMemoryへChatIDを渡した。
- **Problem:** Mio等の会話commitがcanonical session validationで拒否され、正規Viewer routeが応答後のconversation persistenceへ到達できなかった。
- **Cause:** Taskはexternal transport addressの`ChatID`だけを保持し、CORE conversation ownerのSessionIDをtask builder、distributed attribution再構成、Agent memory pathへ伝播していなかった。
- **Lesson / Invariant:** Taskは`WithSessionID`／`SessionID`でcanonical SessionIDを保持し、local／distributed builderとattributionが保存する。Agent conversationとLightMemoryはSessionIDだけを使い、ChatIDはexternal routing／events用に残す。SessionIDからChatIDへのfallbackは禁止する。
- **Enforcement / Tests:** 四Agent sourceの`t.ChatID()`不使用guard、Task／builder／distributed／attribution tests、Mioのcanonical SessionID begin／commit regression、OPS／heartbeat／repair／JSON historyのconstructor testsで強制する。

---

#### Step 05 Failure Knowledge: 異なるfilesystem間のrenameをcutover適用後に検出した

- **Failure:** candidateとactive targetが異なるfilesystemにある状態で、target退避後にcandidateをtargetへrenameし、EXDEVで途中失敗した。
- **Problem:** apply前の不変性検査がdevice境界を含まず、部分適用とrollbackを発生させた。
- **Cause:** FileInfo identity、hash、modeは検証したが、candidateとtargetのfilesystem identityを検査していなかった。
- **Lesson:** prepare中に全candidate/target pairを再検証し、exact FileInfo identityと同一filesystemをfail closedで確認してからapplyする。rollbackはtargetのsiblingなので同じpair検査で覆われる。
- **Invariant:** apply開始前に全5 pairが再検証され、stat／handle／type不一致または異なるfilesystemが一つでもあれば`artifact_preflight`で拒否し、renameを呼ばない。
- **Enforcement:** Unixは`syscall.Stat_t.Dev`、WindowsはFILE_READ_ATTRIBUTES handlesの`ByHandleFileInformation.VolumeSerialNumber`とexact identity比較を使う。package-local seamはproduction verifierで初期化し、各pairをprepareで検査する。
- **Tests:** 後段pairのverifier false injectionがprepareを拒否し、rename非呼出しと全artifact byte保持を検査する。

#### Step 05 Failure Knowledge: one-shot migration sourceとformat-5 restore consumerのowner衝突

- **Failure:** `rencrow-thread-migrate` command/packageが、legacy ThreadIDのbuild／stage／cutover／rollbackと、backup／restoreが使うformat-5の`capture-external`／`verify-external`／`quiesce-sqlite`を共有していた。
- **Problem:** canonical cutover後のcapture path自体はlegacy-onlyで、将来のcanonical snapshot pathとして無効である。一方、sourceを今すぐ全削除すると、明示的にdeferされたformat-5 restore consumerを破壊する。
- **Cause:** one-shot migration sourceと継続的backup／restore contractのowner境界を、同一command/packageへ束ねていた。
- **Lesson / Invariant:** future one-shot migration sourceはongoing backup／restore contractを所有しない。Gate 7で削除する前に、frozen migration recovery consumerをcompleted／packagedするか、実際に継続利用するcanonical snapshot contractを既存storage ownerへ移して独立検証する。legacy readerを恒久compatibility layerとして残さない。
- **Enforcement / Tests:** Gate 7 architecture guardは旧threadmigration scopeを走査し、one-shot sourceが存在しないことを検査する。storage contractはMakefileとstorage scriptsの`rencrow-thread-migrate`／`threadmigration` dependency zeroを証明し、syntheticな`format_version=5` manifestを拒否する。one-shot sourceと継続的backup／restoreのformat-5 couplingは削除済みで、現行`core-export`はformat-4 cohortを使う。保持されたrecovery binaryはfrozen cohort artifactであり、installed ongoing ownerではない。

### Step 06: TurnIDとMessageID

正本契約:

- 一つの利用者入力について、CORE ingress ownerは`TurnID`、`TraceID`、`RootTaskID`、
  user `MessageID`、actual Agent response `MessageID`をUUIDv7で一度だけ確定する。
  欠落したtrusted internal入力はCORE orchestration boundaryで同じ契約を補完するが、
  非空のwrong-type／malformed IDは別IDへ黙って修復せず拒否する。
- `conversation.TurnInput`への改名前であるStep 06では、既存`task.Task`は上記identityの
  一時carrierに限定する。legacy `JobID`を`TurnID`、`TraceID`、`RootTaskID`のいずれにも
  代用しない。`Task`／`Job`語彙の廃止はStep 07／08、orchestrator全体のTask Event化は
  Step 09が所有する。
- `ConversationTurnRequest`はtyped `TurnID`、`TraceID`、`RootTaskID`、
  `UserMessageID`、`AgentMessageID`を必須入力とし、canonical payload、receipt、
  outbox、recall traceへ同じ値を保存する。MessageIDは`modules/core.NewMessageID`だけで
  生成し、ConversationTurn固有generatorやTurnIDからの派生を作らない。
- multi-Actor routeでは、`AgentMessageID`はEndTurnに記録するactual Agent発話とその
  `agent.response` Eventへ割り当てる。別Actorによる転送／利用者向け発話が別発話なら、
  同じIDを流用せず別のcanonical MessageIDを持つ。
- idempotent replayは同じ`TurnID`と同じcanonical payloadに限り同じreceiptを返す。
  TraceID、RootTaskID、MessageIDまたは内容が異なる再利用は`conflict`でfail closedする。

既存Recordのmigration:

- 既にcanonicalな`msg_`の`user_message_id`／`agent_message_id`は履歴identityとして保持し、
  新しいUUIDへ不要に付け替えない。wrong-typeのTurnID／TraceIDだけを固定namespaceの
  field-path付きUUIDv5規則で別々に生成する。
- `conversation_turn_receipt.turn_id`をTurnとRoot Taskの正本source、同rowの`trace_id`を
  Trace正本sourceとする。outbox、receipt JSON、outbox JSON、receiptに結合するrecall traceと
  その子recordは、個別再採番せず、この正本mappingを参照して同じ外部キーへ更新する。
- receiptの`user_message_id`／`agent_message_id`が所有する`l1_memory_event`は、canonical
  `msg_` IDを保持したまま、`meta_json.turn_id`だけを同じreceiptのTurn mappingへ更新する。
  既存のmetadata shapeとmessage bodyを厳格に検証し、欠落fieldを補完しない。receiptへ結合しない
  opaque legacy eventは、対応するTurn／Messageを推測して再採番せず、Step 06の対象外として保持する。
- receiptに結合しない`recall_trace`は、そのrowの`turn_id`／`trace_id`をそれぞれsource pathと
  してTurn／Root TaskとTraceを独立生成する。同じlegacy文字列でもtarget typeが異なるため
  結果は一致しない。同じlegacy `turn_id`を持つ複数recall traceは、一つのTurn中に複数回の
  recallが行われた履歴なのでTurn／Root Task mappingを共有し、TraceIDだけをrowごとに分ける。
- cutoverはwriter停止、exact DB／sidecar identity確認、hash-bound recoverable copy、
  deterministic dry-run receipt、transactional apply、integrity／relationship／embedded JSON検査、
  rollback可能性確認の順とする。runtime alias、dual read、dual write、旧ID fallbackを残さない。

置換:

- TurnIDをTraceID、TaskIDから分離
- MessageID generatorを一本化
- ConversationTurnのroot identity兼用を廃止

Test:

- EndTurn
- Idempotent replay
- User / Agent message pair
- Partial / failed Turn
- Outbox
- Recall trace

完了条件:

- `TurnID == TraceID`
- `TurnID == TaskID`

を前提にするコードzero

Failure Knowledge:

- **Failure:** receipt、outbox、recallだけをmigrationし、receiptが所有するL1 message metadataの
  legacy `turn_id`を残したため、配備後のactual Agent EndTurnがactive thread projection検証で
  `conversation turn invalid`になった。
- **Problem:** relational rowとembedded JSONが整合していても、実際のEndTurnが先に読むL1 message
  projectionが同じTurn mappingへ移行していなければ、正規routeは利用不能になる。
- **Cause:** migration inventoryが`l1_memory_event`をcanonical MessageIDの存在確認だけに使い、
  receipt-owned message metadataを同じImplementation Unitの参照として扱わなかった。
- **Lesson:** ID cutoverは保存先の列挙ではなく、正規runtimeが終端まで読む全projectionを同じowner
  mappingへ束縛する。
- **Invariant:** receipt-owned user／Agent messageの`meta_json.turn_id`は、receiptのcanonical
  `turn_id`と常に一致する。receiptを持たないlegacy eventのidentityは推測しない。
- **Enforcement:** one-shot migration planはcanonical MessageIDごとにreceipt ownershipと既存6-field
  metadataをfail closedで検証し、同一transaction内でTurnIDだけを更新する。plan hashはこの更新を含む。
- **Tests:** dry-run non-mutation、linked user／Agent metadataのexact rewrite、metadata mismatch／欠落rowの
  pre-mutation rejection、unowned opaque row不変、配備後actual Agent EndTurn receiptを検査する。

Gate 7:

- production cutoverとactual Agent EndTurn receiptが成功した後、`rencrow-turn-message-migrate`と
  `turnmigration`のone-shot sourceはproduction source treeから削除する。
- rollbackに必要な実行済みbinaryとdry-run／apply receiptは、cutover時にhash-boundされた
  recovery artifactとして保持する。runtime alias、ongoing migration owner、backup／restore ownerにしない。
- architecture testは両source pathの不存在を検査し、one-shot migrationの再混入を拒否する。

---

### Step 07: 入力Value Objectの改名

置換:

```text
task.Task → conversation.TurnInput
jobID     → rootTaskID
ChatID    → ChannelAddress
```

Test:

- Attachment
- Forced route
- Viewer recipient
- Text input
- Voice input

完了条件:

- User入力Value Objectを`Task`と呼ぶコードzero

#### Step 07配備契約

- `conversation.TurnInput`は`RootTaskID`、`TurnID`、`TraceID`、User／Agentの
  `MessageID`、`messageText`、`ChannelAddress`、`SessionID`、Attachment、Viewer recipient、
  forced／selected routeを持つ。`JobID`や`ChatID`を保持・復元・派生しない。
- 5つのCanonical IDはingressで一度だけ付与し、通常のimmutable modifierでは
  差し替えない。既存の正規投影はvalidation付き`ReconstructTurnInput`だけで復元する。
- Session repositoryはStep 04で分離したparent `ChannelAddress`を維持し、historyは
  `TurnInput`を保存する。旧`channel`／`chat_id`と旧`ChannelAddress` JSON投影の
  読取compatibilityを通常runtimeに残さない。
- production migrationはwriter停止後のSession directoryをsourceとし、`ses_*.json`の
  historyだけを変換する。同じdirectoryの非Session regular fileは全てbytesとpermissionを
  維持し、symlink、nested entry、partial／mixed schemaはfail closedにする。
- 旧`job_id`から既存会話identityを再利用するのは、read-only Event Storeから一意な
  `TraceID`へ結び、そのTraceの一意なconversation receiptとUser／Agent eventが
  `SessionID`、`ChannelAddress`、本文、route、両`MessageID`についてexact matchする場合だけとする。
  receiptがないrowは、`RootTaskID`、`TurnID`、`TraceID`を
  `NewMigrationID(target type, "session_history", "job_id", legacy job_id)`で決定的に生成する。
  Userの`MessageID`は`CanonicalMessageID / session_history / user_message / legacy job_id`、
  Agentの`MessageID`は`CanonicalMessageID / session_history / agent_message / legacy job_id`を
  `NewMigrationID`へ渡す。同じtarget typeの両MessageIDを役割別source field名前空間で
  分離し、矛盾するreceipt、複数Trace、ID衝突、両MessageIDの一致は拒否する。
- dry-runはsource、関連Event／receipt evidence、mapping、outputをhash-boundし、applyは
  そのreceiptとexact matchするfresh directoryだけへmaterializeする。Event Storeとconversation DBは
  `mode=ro` / `query_only`で開き、receiptへ本文、path、個別IDを公開しない。
- 配備後はsource、installed binary、service PID、listener／readiness、実ActorのText応答、
  保存・再loadした5 IDと`ChannelAddress`を一つの証拠鏖で照合する。VoiceはStep 07で
  `TurnInput`表現とidentity伝播を検証し、全ConversationTurn永続routeはStep 17で閉じる。

#### Step 07 Failure Knowledge: Session JSONだけをdirectory全体と誤認した

- **Failure:** Session directoryのJSON件数をSession件数とみなし、同居するIdleChat等の
  非Session資産をcandidateに含めない計画を作った。
- **Problem:** directory swap後にSessionは読めても、別ownerの稼働データが消失する。
- **Cause:** filename／schemaによるSession分類と、cutover単位としてのdirectory inventoryを
  別々に固定しなかった。
- **Lesson:** 変換対象とswap対象は同じとは限らない。owner schemaで対象を分類し、
  containerの全entryにpreserve／transform／rejectのどれかを割り当てる。
- **Invariant:** fresh outputはsourceの全regular fileを一度だけ持ち、Session以外は
  bytes／permissionが一致し、対応不明entryがzeroのときだけcutover可能である。
- **Enforcement:** sorted inventory、strict Session filename／schema、stream copy、source／output hash、
  fresh-output guard、owner repository reloadで機械的に強制する。
- **Tests:** non-Session JSON／JSONL／backupのexact copy、permission、symlink／nested entry拒否、
  source drift、history順序、canonical reloadを検査する。

#### Step 07 Failure Knowledge: payload TraceIDをCanonical Event traceと同格に扱った

- **Failure:** Event Storeのtop-level `trace_id`とpayload内`trace_id`の不一致を、
  canonical identityの矛盾としてmigrationを拒否した。
- **Problem:** canonical Eventへ意図的に昇格されなかった旧orchestrator trace投影のために、
  正規receiptと結び付く履歴まで移行できない。
- **Cause:** `EventEnvelope.TraceID`と、業務payload内に残るlegacy `trace_id`のownerと
  lifecycleを分けず、aliasとして解釈した。
- **Lesson:** Canonical Event identityはEvent Store列と`EventEnvelope`直下に属する。payload内の
  同名fieldは業務dataであり、identityを上書きまたは否定できない。
- **Invariant:** migrationが使うTraceIDは`event_envelope.trace_id`columnとtop-level
  `EventEnvelope.trace_id`のexact matchだけであり、payload `trace_id`はmappingとevidence hashから除外する。
- **Enforcement:** relevant event typeだけをread-only queryし、EventID、TraceID、event typeの
  canonical column／envelope一致を確認してから旧`job_id`を投影する。
- **Tests:** top-level canonical TraceIDとpayload legacy traceが異なるfixtureの成功、
  canonical column／envelope不一致の拒否、relevant／unrelated evidence driftを検査する。

Gate 7:

- production cutover、actual Agent Text receipt、保存した5 IDのowner repository再loadが
  成功した後、`rencrow-turn-input-migrate`と
  `turninputmigration`のone-shot sourceをproduction source treeから削除する。
- rollbackに必要な旧／新runtime、実行済み移行binary、writer停止snapshot、
  固定Check Plan、dry-run／apply／deployment receiptは、cutover時にhash-boundされた
  別filesystemのrecovery artifactとして保持する。installed owner、ongoing migration route、
  backup／restore compatibility layer、runtime dual read／dual writeにしない。
- architecture testは両source pathの不存在を検査し、one-shot migrationの再混入を拒否する。

---

### Step 08: JobをTaskへ完全置換

置換:

```text
internal/domain/job        → internal/domain/task
internal/application/jobmanager → internal/application/taskmanager
Job                        → Task
JobID                      → TaskID
job_id                     → task_id
ParentJobID                → ParentTaskID
```

既存のCanonical Task aggregateへ、Status、Priority、Assignee、Dependency、Originを統合する。

Test:

- Create
- Queue
- Start
- Wait
- Block
- Resume
- Succeed
- Fail
- Cancel
- Supersede
- Parent / dependency
- Parallel limit

完了条件:

- Task / Job subsystemと、その直接ConsumerでInternal `JobID` / `job_id` zero
- `internal/domain/job`と`jobmanager` directory削除
- Schedulerに残る同名別義の`JobID`はStep 15で`ScheduleID`へ削除

境界:

- Step 08の直接Consumerはdurable Task storeへ接続するowner CLI、Task Viewer API、
  Task通知projectionである。`/viewer/jobs`、`/viewer/job/detail`、event/logの`job_id`、
  `modules/worker.JobID`、Root／Child JobはOrchestrator contractとしてStep 09で一体置換する。
- 旧`task.JobID`値objectを削除するとき、Step 09対象のfield名を先行改名せず、内部値の型だけを
  `modules/core.TaskID`へ収束させてcanonical ID validationを強制してよい。
- `waiting_user`はTask statusへ移さない。外部systemまたはdependencyによる停止は、機械的な
  `waiting`と非空reasonとして確定し、人の返答で解除するgrant／queueを作らない。

Failure Knowledge:

- **Failure:** durable Jobの直接Consumerと、同じ`job`語彙を持つOrchestrator monitor／eventを
  一括してStep 08と解釈すると、routing／assignment契約の半端な先行変更になる。
- **Cause:** 同じ名称を、owner、lifecycle、failure domainではなく文字列だけで分類した。
- **Invariant:** durable Task storeへの直接依存だけをStep 08で閉じ、Orchestrator相関契約は
  Step 09、Scheduler定義はStep 15でそれぞれ一つのImplementation Unitとして閉じる。
- **Enforcement:** architecture testはStep 08 ownerと列挙済み直接Consumerを検査し、
  Orchestrator／Schedulerの残存は各後続Stepのzero checkで検査する。
- **Tests:** Task owner／CLI／Viewerで旧語彙zero、旧Task route拒否、Orchestrator monitor routeの
  非接続、Gate 8後のone-shot source不存在を確認する。

Gate 8:

- production cutover、canonical TaskのCreate／Start／Succeed、CLI／Viewer／通知の
  同一`TaskID`投影、再起動後のowner repository再loadが成功した後、
  `rencrow-task-store-migrate`と`taskmigration`のone-shot sourceをproduction source treeから削除する。
- rollbackに必要な旧／新runtime、実行済み移行binary、writer停止snapshot、固定Check Plan、
  dry-run／apply／deployment receiptは、cutover時にhash-boundされた別filesystemの
  recovery artifactとして保持する。installed owner、ongoing migration route、runtime dual read／dual writeにしない。
- architecture testは両source pathの不存在を検査し、one-shot migrationの再混入を拒否する。

---

### Step 09: OrchestratorをTask基準へ置換

置換:

- Root Job / Child JobをRoot Task / Child Taskへ変更
- Routing結果をEvent化
- Agent割当をEvent化
- `Seq`を`EventSeq`へ変更
- TraceIDをTaskIDから分離

Test:

- CHAT
- WORKER
- CODE
- RESEARCH
- route change
- handoff
- parallel Coder
- failure / retry
- interruption

完了条件:

- Orchestrator内`job`語彙zero
- Routing EventとAssignment EventをEventIDで逆参照可能

#### Step 09配備契約

- 受付済みTurnは、ingressで既に発行した`RootTaskID`をそのまま使うdurable root Taskを1件だけ
  作る。実行可能な仕事をMioから別のactual CORE Agentへ委譲する場合だけ、同じ`TraceID`と
  `ParentTaskID=RootTaskID`を持つchild Taskを作る。Coder、model、provider、controller、worker
  mechanismをassignee Actorにしない。
- `routing.decision`はroot Task、`agent.assignment`は実行対象TaskへCanonical Eventとして保存する。
  assignmentの`CausationEventID`は有効なrouting Eventを指し、Taskは両EventIDを保持する。
- Event Storeはappend前に正の単調増加`EventSeq`を割り当て、SQLite、Monitor、SSE、再起動後の
  次Eventまで同じ順序を維持する。`EventID`、`TraceID`、`TaskID`、`SessionID`、`ThreadID`、
  `TurnID`、`MessageID`はtyped validationを通し、相互変換しない。
- COREからRenCrow LLM Gatewayへ送るprompt-free observation metadataは`task_id`だけを使う。
  Gatewayは同じ値をqueue、active status、structured log、prompt-debug receiptへ保持し、Backendへ
  private `rencrow` envelopeを渡さない。Gateway／Nodeがhost-supervised Backend lifecycleに使う
  別ownerの`task_id`はOrchestrator Taskへ統合しない。
- production cutoverは全writer停止後のsnapshotをsourceとし、one-shot owner CLIでfresh Event Store、
  fresh execution report JSONL、complete fresh Resilience rootを一体生成する。Eventの旧`job_id`は
  exact `TraceID`のconversation receiptが一意ならその`RootTaskID`、無ければ
  `NewMigrationID(CanonicalTaskID, "event_envelope", "trace_id+job_id", exact pair)`を使う。
  execution reportは同じlegacy IDの一意なEvent mappingを再利用し、無ければ`execution_report`
  namespaceから決定的に生成する。Resilienceの`repair_job_id`は一意なexecution report mappingを
  必須とし、`repair_task_id`へ置換する。曖昧join、duplicate、未知field、symlink、unsupported entry、
  source driftをfail closedにし、dry-runとapplyの全input／output hashを一致させる。
- 旧session log、Prompt receipt、Gateway prompt-debug logはTaskだけを書き換えない。旧rowには
  `trace_id == job_id`があり、一部置換すると壊れたIdentityをactive logへ残すため、COREとGatewayの
  writer停止後に各artifactをowner-only legacy archiveへ原子的にrenameし、path、size、mode、SHA-256を
  receiptへ固定してfresh `task_id` writerを開始する。archiveをactive pathまたはcompatibility readerへ
  戻さない。大容量prompt-debug logをmigrationのために再書込みしない。

Gate 9:

- COREとRenCrow_LLMのsource、artifact、active config、service PID、listener、正規Gateway routeを照合し、
  actual AgentのCHAT／WORKER／CODE／RESEARCH、route change、handoff、failure／retry／interruptionで
  Task graph、Event graph、EventID lookup、EventSeq restart継続、`task_id` log／Gateway相関、旧route拒否を
  確認した後だけStep 09を閉じる。
- rollbackに必要な旧／新runtime、writer-stopped snapshot、固定Check Plan、dry-run／apply／log rotation／
  deployment receiptを別filesystemのrecovery artifactとして保持する。旧runtimeへ戻す場合は3つの
  canonical state artifactと3つのactive observation artifactを同じgenerationへ戻す。
- production cutover成功後、`rencrow-event-task-migrate`と`eventtaskmigration`のone-shot sourceを削除する。
  architecture testはone-shot sourceの再混入と、列挙済みStep 09 owner／consumerでの`JobID`／`job_id`／
  旧`Seq`を拒否する。Scheduler `JobID`はStep 15、その他の未分類domain-local jobは各owner Stepへ残す。

Failure Knowledge:

- **Failure:** production preflightで、同じlegacy `job_id`が独立した通常Turnとresume Turnの別Traceに
  再利用されていたため、Eventだけを読む段階で全体一意性を要求した移行CLIが停止した。
- **Problem:** 正本の`trace_id+job_id`規則は別Traceを別Taskへ分離するのに、旧ID単体の一意性を
  先に要求すると、曖昧なjoinが存在しない履歴まで移行不能になる。
- **Cause:** Eventのper-Trace mappingと、単一Taskを必要とするexecution report joinの検査境界を
  同じmapへ潰していた。
- **Lesson / Invariant:** legacy Eventはexact TraceごとにTaskへ写像し、同じ旧IDから複数Taskが
  生じることを許す。execution reportがその旧IDを参照するときだけ、候補が一つでなければ
  `report_job_ambiguous`でfail closedにする。
- **Enforcement / Tests:** 移行CLIは旧IDごとのTask候補集合を保持し、report joinでだけ一意性を
  検査する。別Traceの同一旧IDにreport行が無い成功caseと、report行がある拒否caseをtestする。

- **Failure:** Orchestrator経由のLLM呼出しがprompt-free observationの`task_id`、`trace_id`、
  `session_id`なしでGatewayへ到達し、route decisionと実行childの相関を失っていた。
- **Problem / Cause:** Message／Distributed OrchestratorがLLM-capableなroute decision前に観測を
  設定せず、activation後の実行Taskへの帰属更新も行っていなかった。直接Shiroのdefaultsが一部を
  覆い、上流の欠落を検出しにくくしていた。
- **Lesson / Invariant:** Orchestratorはroot TaskID、ingress TraceID、resolved SessionIDを共有
  observationへ設定し、activation後はTaskIDだけを実行Taskへ置換する。RequestIDは独立生成して
  再利用し、TaskID／TraceID／SessionIDを相互変換しない。
- **Enforcement / Tests:** Message／Distributed共通helperが`WithExecutionObservation`を所有し、
  downstream defaultsの非上書きとdaily-news fallbackのcollector／Mio／Shiro観測を含む
  route／execution観測テストで強制する。

#### Failure Knowledge: Viewer受付が未作成SessionIDを明示した

- **Failure:** Step 09配備後の実`POST /viewer/send`で、受付responseはCanonical `session_id`を
  返したが、そのIDをSession repositoryへ作成していなかったため、非同期処理がTask作成前の
  `session not found`で停止した。
- **Problem:** Step 04契約はSessionID未指定時だけ`logical_date + ChannelAddress`からSessionを
  resolveする。Viewer adapterがUUIDを先に発行して明示IDとして渡すと、正規resolveを迂回し、
  accepted response、Session owner、Task originが分裂する。
- **Cause:** Step 09で同期responseへ`root_task_id`と同時に`session_id`を追加した際、SessionIDを
  identity generatorだけで作り、既存Session repository ownerとの受付前同期を結合しなかった。
- **Lesson / Invariant:** 非同期Viewer ingressは、受付成功を返す前にSession repositoryの
  `LoadOrCreateCanonical`で`viewer + viewer-user + logical_date`を解決し、その同一SessionIDだけを
  response、orchestrator request、Task originへ渡す。resolve失敗時はTaskをacceptedにしない。
- **Enforcement / Tests:** Viewer adapterは注入されたresolverのCanonical SessionIDを検証し、失敗を
  HTTP 503で同期拒否する。production bridge testは実JSON Session repositoryでaccepted responseと
  processor requestのSessionID一致を検査し、adapter testはresolver失敗後にhandlerが未実行であることを検査する。

#### Failure Knowledge: rootとchildが同じ実行枠を二重消費した

- **Failure:** 実`/viewer/send`のOPS routeでroot Taskを開始した後、Shiro child Taskが
  `parallel limit exceeded: operations task limit reached`で開始不能になった。
- **Problem:** rootは一つのTurn全体を追跡するcoordination containerであり、childだけが同じrouteの
  実行を担う。両方を独立したparallel executionとして数えると、operations limit 1、research limit 1、
  coding limit 2の契約が自分自身のTask graphを拒否する。
- **Cause:** `CanStart`がrunning Taskを平坦に数え、開始候補のexact `ParentTaskID`を同一実行の
  containerとして識別しなかった。
- **Lesson / Invariant:** child Taskのparallel capacity判定では、そのchildのexact running parentを
  global／module／route別countから一度だけ除外する。他Task graphのrootやsiblingは除外しない。
- **Enforcement / Tests:** task manager testはglobal／operations limit 1でrunning rootからchildを
  開始でき、無関係なOPS Taskは同じlimitで拒否されることを検査する。実routeでは失敗rootを保持し、
  修正版deploy後の新root／childでretryを証明する。

---

### Step 10: RunID

置換:

- AgentRun
- WorkflowRun
- ToolLoop
- Background process
- Browser trace
- Dream consolidation
- Generation process

を一つのRun意味へ統一する。

廃止:

- ParentRunID
- TraceRunID
- GenerationID

Test:

- First run
- Resume
- Lease reacquire
- Agent reassignment
- Checkpoint resume
- Run terminal state

完了条件:

- `ParentRunID / TraceRunID / GenerationID` zero
- 子実行は子Taskとして表現

#### Step 10配備契約

- Canonical Runは新しい独立moduleやglobal registryを作らず、Step 08で確立したCOREのTask ownerを
  拡張して所有する。`internal/domain/task`が`TaskID`に属する一回の実行を表すtyped `RunID`、
  開始理由、actual CORE Agent assignee、状態、開始／終了時刻を定義し、`taskmanager`と同じ
  `workspace/tasks` storeがRun履歴を専用streamへ保存する。Task stateへRun履歴を埋め込んで
  latest-by-TaskIDで潰さず、別のruntime route、alias lookup、dual ownerを作らない。
- 新しいRunIDはTask ownerだけが`modules/core.NewRunID`で発行する。初回実行、process再起動後の再開、
  lease再取得、実行Agent変更、checkpointからの再開、明示的再実行は、同じTaskIDを維持して
  必ず別RunIDを持つ。Runを再利用、TaskIDから派生、階層化しない。停止したRunは一つの終端状態へ
  確定し、子作業は新しい`TaskID + ParentTaskID`とそのTask自身のRunで表す。
- SuperAgent、AIWorkflow／ToolLoop、background process、Browser trace、Dream consolidation、
  IdleChat generationは、入口でTask ownerが返した同じ`TaskID + RunID`を受け取り、各owner固有の
  checkpoint、artifact、trace、生成内容等だけをprojectionとして保存する。`AgentRun`詳細は
  canonical Runを上書きせず、`leadAgentRunID`のような派生ID生成を廃止する。Canonical Eventは
  top-level `TaskID`と`RunID`を保持し、payload referenceをIdentityとして再解釈しない。
- SuperAgentの子実行は`SubagentID + ParentRunID`を廃止し、Task ownerが作る子Taskへ置換する。
  actual CORE Agentだけをassigneeにでき、Coder、LLM、model、provider、controller、worker mechanismを
  ActorまたはRun ownerにしない。Run間の因果は`CausationEventID`とTask親子関係で表す。
- production cutoverは全対象writer停止後のsnapshotをsourceとし、one-shot owner CLIがfresh Task／Run
  store、fresh Event Store、fresh SuperAgent／Browser trace／Knowledge Memory projection、fresh IdleChat
  episode／stock／checkpoint artifactを一体生成する。既にCanonicalなRunIDは意味と所属Taskが一意な
  場合だけ維持し、legacy `run_id`、`trace_run_id`、`generation_id`、`subagent_id`はsource owner、field、
  exact valueからUUIDv5へ決定的に写像する。所属Taskが存在しないlegacy executionには同じsource identity
  からmigration Taskを一件だけ生成し、曖昧join、duplicate、orphan、未知field、symlink、source driftを
  fail closedにする。dry-run／applyの入力、変換件数、出力hashを一致させ、second-runはno-opにする。

#### Task / Runの永続更新境界

- Task作成とSharedRoleContext、実行開始時のTaskと新旧Run、担当変更時のTaskと新旧Run、
  終端時のTaskとRunとNotificationは、各操作を同じTask ownerの一つのtransactionとして確定する。
  状態の読取、並列数判定、更新の間も同じ排他境界を維持する。途中の保存失敗で一部だけを公開しない。
- 既存のJSONL streamを正本として維持し、append前のprepare、dataのsync、commitのsyncによって
  複数streamの更新を回復可能にする。journalは回復用であり、独立更新できるTask正本ではない。
  readerもtransaction lockを通り、未確定journalや破損を検出した場合は状態を返さず失敗する。
  回復は正規writerだけが行い、対象外file、内部破損、根拠のない切詰めを拒否する。
- writer generationの単調増加と生存中のwriter排他を維持する。admissionは一つのsnapshot上で
  Task、Run、実Actor、writer generationを照合する。これだけで実行中の外部効果を停止したとは扱わない。
- downstream queue／leaseのCAS失敗で発行済みRunを閉じる`InterruptRun`は、そのRunだけを終端化し、
  Taskを変更しない。Taskがrunningなら常にactive Runが存在する、という別の不変条件は導入しない。

Gate 10:

- First run、resume、lease reacquire、Agent reassignment、checkpoint resume、明示rerun、全終端状態で、
  同一Taskと別RunID、actual assignee、Run Event、owner reloadを確認する。Browser trace、Dream、IdleChat、
  background executionの正規routeが同じTask／Run契約を使い、Viewer Task detailからRun履歴とreceiptを
  確認できることを配備後に検証する。
- source、artifact、active config、user service PID、listener、readiness、正規Gateway route、実Actorの
  user-visible resultを一つの証拠鎖で照合する。全Identity移行のfull restorecheckは最終Stepへdeferし、
  Step 10では対象routeとmigration／rollbackを検証する。
- production cutover成功後、Step 10 one-shot migration sourceをproduction source treeから削除し、
  rollbackに必要な旧／新runtime、writer-stopped snapshot、固定Check Plan、dry-run／apply／deployment
  receiptを別filesystemのrecovery artifactとして保持する。architecture testはone-shot sourceの再混入、
  `ParentRunID`、`TraceRunID`、`GenerationID`、`SubagentID`、legacy JSON key、Task由来RunIDを拒否する。

---

### Step 11: ActionIDとAttemptID

置換:

- Tool
- DCI
- LLM
- STT
- TTS
- Playback
- Patch apply
- External send
- Verification
- Memory promotion

をActionへ統一する。

RetryはAttemptへ統一する。

Action作成と初回Attempt、retry時の旧Attempt終端と新AttemptとCurrentAttemptID、完了時の
AttemptとActionは、Action owner内の一つのtransactionとして確定する。current pairの照合と
更新を同じ排他境界に含め、異なるManager／store instanceからの競合も拘束する。readerは一貫した
snapshotを取得し、途中失敗や再起動で片方だけ確定したpairを正常値として返さない。物理JSONLの
prepare／sync／commit／回復契約はTask / Runと共通だが、domainの判断と保存先は各ownerに残す。
TaskとActionの別ownerをまたぐtransactionがあるとは扱わない。

Test:

- Success
- Retry
- Fallback
- Timeout
- Cancel
- Idempotency
- Duplicate suppression
- Side effect failure

完了条件:

- 同じAction RetryでActionID不変
- AttemptIDは毎回更新
- ApplyID、SubmitID、内部RequestID zero

---

### Step 12: RequestID / ResponseID

置換:

- RequestIDをTransport callだけに限定
- ResponseIDを対応するResponseへ付与
- Provider固有IDはExternalRefへ隔離
- ToolCallIDをProviderToolCallIDへ改名

Test:

- One request / one response
- Streaming response
- No response terminal failure
- Provider retry
- Multi-request attempt
- External ID preservation

完了条件:

- 内部CommandをRequestIDと呼ぶ箇所zero
- ProviderToolCallIDをActionIDとして使う箇所zero

---

### Step 13: ArtifactID

置換:

```text
ReportID
DraftID
ContextPackID
内部ImageID
PatchID
SpecificationID
TranscriptID
```

をArtifactID + ArtifactKindへ統一する。

Domainとして独立EntityであるSpecなどは、Domain IDを残してもよいが、生成物としてのFile / BodyはArtifactIDで指す。

Test:

- Artifact create
- Update
- Supersede
- Content hash
- Source Event
- Workstream relation

完了条件:

- Generic ReportID / DraftID / ContextPackID zero

---

### Step 14: EvidenceIDとMemoryID

置換:

- EvidenceIDを独立Entity IDへ統一
- MemoryへCreatedByEventID、UpdatedByEventIDを追加
- Profile PromotionをTask / Run / Actionで表す
- Verification ReportをArtifactへ変更

Test:

- observed
- candidate
- confirmed
- pinned
- sensitive reject
- missing evidence reject
- recall
- supersede
- decay

完了条件:

- EvidenceID文字列派生zero
- Evidence orphan zero
- Memory promotionにTask / Run / Eventが揃う

---

### Step 15: Scheduler

置換:

```text
scheduler.JobID → ScheduleID
scheduler.Job   → Schedule
HeartbeatID     → ScheduleID
```

Schedule発火時にTaskとRunを生成する。

Test:

- Manual fire
- Due fire
- Deferred
- Retry
- Disable
- Next run
- Duplicate fire prevention

完了条件:

- Scheduler domainにJobID zero
- Schedule定義とTask実行のID共有zero

---

### Step 16: QueueItemID / CheckpointID / ReceiptID

置換:

- QueueIDをQueueItemIDへ変更
- Checkpoint Key / Generation checkpointをCheckpointIDへ統一
- ReceiptはReceiptIDへ統一
- ResolutionRequestID、Stage RequestIDをActionIDへ変更

Test:

- Queue claim
- Lease
- Expiry
- Reclaim
- Checkpoint resume
- Receipt replay
- Idempotency conflict

完了条件:

- Generic QueueID zero
- CheckpointをGenerationIDで指す箇所zero
- ReceiptIDをEventIDとして使う箇所zero

---

### Step 17: VoiceとIdleChat

置換:

- Voice Input、STT、LLM、TTS、Playback、Cancelを同一Traceへ接続
- 各処理をAction / Attempt / Request / Responseへ統一
- IdleChatをSession / Thread / Turnへ統一
- generation制御は維持

Test:

- Voice → STT → Mio
- Mio → TTS → Playback
- Interrupt
- Cancel
- Drain
- Timeout
- Viewer absent
- Old generation reject
- New generation preserve

完了条件:

- ChatID zero
- GenerationID zero
- generationをIDとして扱うコードzero
- 古い音声復活zero
- 新しい音声誤停止zero

---

### Step 18: Workstream / Atlas / Backlog

置換:

- Generic ItemIDをDomain qualified IDへ変更
- State transitionをEventIDで記録
- Report / Spec / TranscriptをArtifactへ接続
- Stage operationをTask / Run / Action / Receiptへ接続

Test:

- Maturation
- Revalidation
- Promotion
- Implementation stage
- Freeze
- Resolution
- Closure
- Replay
- Duplicate prevention

完了条件:

- Cross-module Generic ItemID / ResultID / RecordID zero
- Atlas状態遷移にEvent参照がある
- ReceiptとEventの意味共有zero

---

### Step 19: Viewer / OTel / Graph

置換:

- ViewerをCanonical Event Projectionへ変更
- OTel TraceIDとCanonical TraceIDを接続
- Event Graph、Task Graph、Communication GraphをCanonical IDから生成
- Legacy payload pathを削除

Test:

- SSE disconnect
- Reconnect
- Backfill
- Graph reconstruction
- Event ordering
- Secret redaction
- Viewer unavailable

完了条件:

- Viewer停止中もCORE継続
- Viewerに旧ID field zero
- Graph queryにLegacy lookup zero

---

### Step 20: 全体Cleanup

削除:

- Runtime migration map
- Compatibility adapter
- Dual read / write
- Deprecated type alias
- Deprecated JSON key
- Deprecated DB column
- Deprecated test fixture
- Deprecated log parser
- 旧ID generator
- 旧Viewer query
- 旧Graph projection

最終疎通:

- 全Golden flow
- Restart
- Recovery
- Retry
- Long-running Task
- Background Task
- Voice
- Atlas
- Memory
- Viewer
- ID integrity scan

最終完了条件:

```text
JobID                  zero
job_id                 zero
DiscussionID           zero
ParentEventID          zero
ParentRunID            zero
TraceRunID             zero
GenerationID           zero
SubagentID             zero
DecisionID             zero
AssignmentID           zero
ApplyID                 zero
SubmitID                zero
ReportID                zero
DraftID                 zero
ContextPackID           zero
Generic QueueID         zero
Internal ChatID         zero
Runtime alias lookup    zero
Dual read / write       zero
```

---

## 13. Test体系

### 13.1 AST Identity Linter

CIで次を検査する。

- 禁止Identifier
- 禁止JSON key
- 禁止DB column
- Generic ID field
- ID typeをstringで受けるCross-module contract
- ID prefixとtype不一致
- UUID generatorの独自実装
- EventIDをRun / Evidence / Artifactへ代入
- TaskIDをTraceIDへ代入
- External IDをCanonical IDへ代入

### 13.2 Property Test

- UUID uniqueness
- Migration UUIDv5 determinism
- 同じLegacy値でもTarget typeが違えば別ID
- Event Graph acyclic
- Task Graph acyclic
- Request terminality
- Attempt ownership
- Artifact source Event
- Evidence source
- Memory evidence completeness

### 13.3 Migration Test

Production Snapshot copyに対して、各Migrationを実行する。

必須検査:

```text
Before row count
After row count
Converted row count
Duplicate count
NULL count
Foreign key violations
Orphan count
Checksum
Dry-run / real-run一致
Second-run no-op
```

### 13.4 Golden Connectivity Test

```text
User Message
  → TurnID
  → RootTaskID
  → RunID
  → Routing Event
  → Assignment Event
  → ActionID
  → AttemptID
  → RequestID
  → ResponseID
  → ArtifactID
  → Verification Event
  → MemoryID
  → Response MessageID
```

この一本を、Text、Code、Research、Voice、Scheduler、IdleChatで検証する。

### 13.5 Fault Injection

- Event Store unavailable
- Provider timeout
- Tool error
- DB transaction rollback
- Queue lease loss
- Process kill
- Viewer disconnect
- TTS cancel race
- Duplicate webhook
- Migration interruption

---

## 14. 工程ごとのCommit規則

各Stepは一つのBranchで完結させる。

Branch例:

```text
identity/02-event
identity/08-task
identity/17-voice
```

Commit順:

```text
1. final type / schema
2. producer replacement
3. consumer replacement
4. persistence migration
5. test replacement
6. old code deletion
7. connectivity evidence
```

StepのMerge条件は、旧コード削除まで完了していること。

部分互換の状態でMainへMergeしない。

---

## 15. Rollback

RollbackはCompatibilityによって行わない。

```text
1. Writer停止
2. Failed deployment停止
3. DB Snapshot restore
4. Previous binary deploy
5. Baseline connectivity
6. Failure report
```

新Schemaから旧SchemaへRuntime変換するDown adapterは作らない。

Migration scriptはVersion controlへ残すが、Runtimeから呼ばない。

---

## 16. 最終Source of Truth

```text
ID名称・意味
└─ 本書

ID型・生成
└─ CORE Identity package

現在状態
└─ 各Domain Store

発生済み事実
└─ Canonical Event Store

成果物
└─ Artifact Store

根拠
└─ Evidence Store

記憶
└─ Memory Store

Graph
└─ 上記正本から再構築するProjection
```

---

## 17. 最終到達形

```text
External Trigger
└─ TraceID
   └─ EventID: trigger.received
      │
      ├─ SessionID
      │  └─ ThreadID
      │     └─ TurnID
      │        ├─ MessageID
      │        └─ RootTaskID
      │
      └─ TaskID
         └─ RunID
            └─ ActionID
               └─ AttemptID
                  ├─ RequestID
                  └─ ResponseID

発生したすべての重要事実
└─ EventID
   ├─ CausationEventID
   └─ DependencyEventIDs

保存された結果
├─ ArtifactID
├─ EvidenceID
├─ MemoryID
├─ RelationID
├─ CheckpointID
└─ ReceiptID
```

この構造を満たした時点で、RenCrowのID統一は完了とする。

## D2d-2b service-manager cutover subreceipt

D2d-2b は、D2d service manager が実行した停止・再開境界を D2c の file-swap
subreceipt と結合する、CORE 内の bounded な証跡境界である。schema は
`rencrow.identity.dci-service-cutover/v3`、mode は `cutover`、status は
`applied`、`blocked`、`rolled_back`、`rollback_failed` のいずれかに固定する。
この receipt は service-manager subreceipt であり、配備後の readiness、実 Actor
Trace、Data Write、restart 後の exact lookup を成功とは主張しない。

### Invariants and enforcement

- service receipt path は service command の前に canonical parent の fresh regular
  target として解決し、build root／固定四 output／旧新 runtime／active 五 source／
  rollback root／D2c receipt と path containment、symlink、hardlink、alias を拒否する。
- outer receipt の `cutover_subreceipt_sha256` は durable な D2c receipt の物理
  hash だけを束縛し、その `cutover_subreceipt_status` は常に `applied` である。
  D2c が pre-mutation に blocked／rolled_back となり receipt を発行しない場合だけ
  空を許す。service lifecycle が後で rollback しても、immutable な D2c applied
  file の意味を `rolled_back` と書き換えない。`cutover_terminal_status` は D2c の
  in-memory terminal 状態を別に示す。
- `initial_state` は `running` または `maintenance_stopped` に固定する。`running` は
  `initial_running` だけを要求する。`maintenance_stopped` は fixed unit が enabled／unmasked、
  inactive、PID-zero、listener-zero であり、unit の固定 ExecStart／config／installed runtime
  hash が canonical owner と一致する `initial_maintenance_stopped` だけを要求する。二つの
  masked stopped proof と `final_running` を含め、path、PID値、socket、command、config、query、
  payload、secret、個別 ID、raw error は含めない。二つの initial projection の同時 claim を
  拒否し、空 projection は未到達 phase に限る。
- 最初の masked stopped proof 後、active source binding 前に、owner は固定4 SQLite sourceを
  `mode=rw`／busy timeout 0で開き、`wal_checkpoint(TRUNCATE)`のbusy=0、
  `journal_mode=DELETE`、base fileの同一inode、WAL／SHM／journal zeroを確認する。
  `active_sources_quiesced`は`sqlite_sources=4`と各Boolean proofをexact値で持ち、`applied`／
  `rolled_back`では必須とする。busy、alias、symlink、file replacement、sidecar残存、context cancellationは
  `active_quiesce`でfail closedし、file swap前に旧runtimeのrunning proofまで復旧する。通常runtimeの
  persistent WAL policyは変更せず、この変換をservice-managed cutover Boundaryの中だけに限定する。
- D2c apply の成功後に new service の start/readiness または service receipt の
  durable publication が失敗した場合は、detached recovery context で停止を再証明し、
  D2c rollback と old service の running proof を完了してから `rolled_back` を出す。
  receipt write／readback／cleanup が証明できない場合は `rollback_failed` とし、unknown
  final、symlink、hardlink を削除・上書きしない。
- receipt は strict one-value JSON、unknown field／trailing token 拒否、64 KiB 以下、
  same-parent temp、file sync、fresh-only atomic publication、parent sync、exact inode
  readback、非 Windows 0600 を満たす。`applied` の ErrorCode は空、他の terminal status
  は bounded machine code を持つ。

### Failure Knowledge

- **Failure:** D2c の applied receipt を service rollback 後に `rolled_back` と書き換え、
  または service lifecycle の証拠を同じ file receipt に混在させた。
- **Problem:** file-swap が実際に成功した証跡と service manager の復旧結果が区別できず、
  durable audit chain と失敗時の責任境界が失われる。
- **Cause:** D2c owner と service-manager owner の terminal 状態を一つの mutable な
  status として扱い、receipt publication を active cohort の成功と同一視した。
- **Lesson:** D2c applied file は immutable historical subreceipt として hash/status を
  保持し、service の outer terminal status と phase projections を別の bounded receipt
  に結合する。publication failure は file rollback の trigger であり成功の根拠ではない。
- **Invariant:** `applied` は valid old-running または maintenance-stopped initial proof の
  片方だけ、二つの stopped proof、D2c applied subreceipt、new-running を全て持つ。
  `rolled_back` は同じ initial proof、D2c terminal rollback、old running を持つ。
  `rollback_failed` は完全復旧を claim しない。
- **Enforcement:** `executeServiceCutover` の唯一の service command order を再利用し、
  private result へ bounded evidence を保持する。strict validator、fresh-only owner
  writer、inode binding、detached recovery、generic error code、D2c hash cross-binding
  で強制する。
- **Tests:** happy applied、pre-mutation blocked、readiness/write failure rollback、
  durable D2c hash retention、context cancellation、fresh/symlink/hardlink/alias、
  unknown/trailing/oversize JSON、receipt substitution preservation、path／payload／ID
  非漏洩を fake manager と temp fixture で検査する。production apply、post-deploy E2E、
  service restart の成功はこの単位の受入条件に含めない。

#### Failure Knowledge: 停止後persistent WALをactive sourceとして拒否したcutover

- **Failure:** canonical serviceをPID-zero／listener-zeroまで停止してもL1／archiveのpersistent
  WAL／SHMが残り、active source bindingが`active_source`でcutoverを拒否した。
- **Problem:** process停止とSQLiteのcheckpoint完了を同一視したため、通常runtimeとして正しい
  persistent WAL policyと、sidecar-zeroを要求するatomic cutover contractが接続されなかった。
- **Cause:** storeの通常`Close`へjournal policy変更を混ぜずに済む、service owner固有のquiesce
  Boundaryが停止証明とsource bindingの間に存在しなかった。
- **Lesson:** runtime store lifecycleはWALのまま維持し、production state変更を所有するcutoverだけが、
  service停止証明後に固定sourceをcheckpointしてDELETE modeへ移す。手動SQLite commandや別CLIを
  operator手順へ追加しない。
- **Invariant:** active source bindingへ到達する全service cutoverは、4 sourceすべてについてbusy=0、
  DELETE mode、same-file、sidecar-zeroの一つのexact evidenceを持つ。部分成功や推定値をreceiptへ投影しない。
- **Enforcement:** fixed source set、canonical non-symlink binding、inode alias検査、`mode=rw`、no-wait
  checkpoint、post-close sidecar拒否、service receipt v3 strict validator、失敗時old-runtime recoveryで強制する。
- **Tests:** persistent WAL happy path、busy writer、symlink／alias、cancellation、same-file、sidecar-zero、
  stopped-before-quiesce順序、applied／rolled_back receipt必須性、pre-quiesce blocked zero projectionを検査する。

#### Failure Knowledge: active quiesce失敗境界を単一codeへ潰したreceipt

- **Failure:** production quiesceがapply前にblockedとなったが、4 sourceとopen／checkpoint／close／sidecarの
  全失敗を`active_quiesce`へ潰し、同じ操作を再試行せずに原因を限定できなかった。
- **Problem:** pathやdriver raw errorを非公開にする安全境界と、ownerが次の修正対象を機械判定するための
  bounded observabilityを同一視し、receiptが再発防止に必要なphaseを失った。
- **Cause:** quiesce helperの固定source roleと固定phaseをerror codeへ投影せず、最外層のgeneric codeだけを
  durable receiptへ保存した。
- **Lesson:** 秘密path、SQL、raw errorは公開せず、owner内で固定されたroleとphaseだけをbounded machine codeへ
  投影する。未知の動的値をcode生成へ渡さない。
- **Invariant:** source固有のquiesce失敗は`active_quiesce_<role>_<phase>`でfail closedし、zero evidence、
  subreceiptなし、old-runtime running proofを維持する。generic codeはsource特定前の失敗だけに使う。
- **Enforcement:** fixed role table、fixed call-site phase、bounded error syntax、strict service receipt、
  path／raw error非漏洩testで強制する。
- **Tests:** busy DCI fixtureが`active_quiesce_dci_checkpoint`を返すことと、従来のrecovery／receipt
  projectionを検査する。

#### Failure Knowledge: 非retryableなVector契約不一致が実Actor期限を消費した

- **Failure:** D2e-3の実Shiro DCI requestで、1024次元queryと3584次元collectionの
  gRPC `InvalidArgument`を3回再試行し、optional candidate providerだけで全体10秒deadlineの大半を消費した。
- **Problem:** direct corpusからevidenceを得ても終端保存前にdeadlineとなり、正規routeはHTTP 500を返した。
- **Cause:** conversation retry policyが型付き`InvalidArgument`を未知のtransient errorとして扱った。
- **Lesson:** 同じ入力で結果が変わらないrequest／schema／dimension不一致は即時失敗させ、owner workflowへ
  bounded limitationとして返す。timeout延長で契約不一致を隠さない。
- **Invariant:** gRPC `InvalidArgument`は一回でnon-retryable terminalとなり、DCIの残りdeadlineを
  canonical direct corpus、保存、receiptへ残す。
- **Enforcement:** shared conversation retry classifierでcodeを判定し、error textの文字列判定は使わない。
- **Tests:** `codes.InvalidArgument` operationのattempt countがexactly 1であることを検査する。

#### Failure Knowledge: Vector契約検査がembedding後でDCI終端を欠落させた

- **Failure:** retryを一回へ制限しても、1024次元embeddingを生成した後で既存`kb_general`の3584次元契約を
  初めて検出したため、実Shiro DCIは`dci.file.read`後に10秒deadlineへ到達した。
- **Problem:** requestはHTTP 500となり、期限切れ直後の`dci.evidence.created` appendもcanceled contextで失敗して、
  `dci.search.failed`とfailed traceが残らなかった。
- **Cause:** KB collection契約をembedding前に検査するowner境界がなく、Explorerもfile read後のEvidence appendだけ
  recovery contextへ切り替えていなかった。さらにproviderが候補を返した後もallowlist walkで候補上限を埋め、
  narrowing結果を無視してcontent rankの探索時間を増やしていた。
- **Lesson:** 永続collectionの決定的なvector契約は高コストembeddingより前に検査する。探索期限後も、期限前に
  読み取ったbounded evidenceとfailed terminal／traceは新しい短時間のrecovery contextで閉じる。
- **Invariant:** incompatible KB collectionはembedding call zeroでtyped `InvalidArgument`となる。DCIがfile read直後に
  canceledとなっても、Evidence event、failed terminal、failed traceを一つのAction／Traceへ永続化する。
- **Enforcement:** `VectorDBStore.ValidateKBVectorContract`を`RealConversationManager.SearchKB`のembedding前gateとし、
  ExplorerのEvidence appendはexpired search contextをbounded recovery contextへ置換する。provider候補が一件以上なら
  それをcanonical narrowed setとし、filesystem walkは全providerが空の場合だけのfallbackとする。独立read-only providerは
  4秒のshared sub-budgetで並行取得し、登録順に結果とlimitationを統合して決定性を保つ。残りの全体budgetは
  canonical Event append、file read、terminal／trace永続化のために保持する。cancellation非準拠のprovider終了を待たず、
  deadline時点でbuffered result収集を閉じ、未応答providerをbounded limitationへ固定する。
- **Tests:** collection contract failure時のembedding call zeroと、file read直後cancel時のEvidence／failed terminal／
  failed traceおよびfresh recovery context、provider候補取得後にwalk-only fileが混入しないことを検査する。

## D2d-2c production cutover owner CLI

D2d-2c は、D2d-2b まで private に閉じていた service-managed cutover を、既存の
`rencrow-dci-migrate --mode cutover` だけから実行可能にする owner operation 境界である。
新しい binary、service、endpoint、apply route は作らない。公開 API は path／hash を受ける
薄い `dcimigration.Cutover` facade に限定し、service command order、file swap、rollback、
receipt publication は既存の private `executeServiceCutoverWithReceipt` を唯一の正本として
再利用する。

### CLI / Boundary / LLM classification

- `CLI`: build cohort、active 五 source、old/new runtime、active config、fresh rollback／
  D2c／service receipt target を明示入力とし、`ServiceCutoverReceipt`、bounded stderr code、
  exit status を決定的に返す。owner は `RenCrow_CORE` の `dcimigration` である。
- `Boundary`: 固定 `rencrow.service`、`:18790`、fixed readiness、service identity、
  source／artifact checksum、canonical path／alias、stop/start、rollback、durable receipt を
  existing service manager と cutover owner が拘束する。
- `LLM`: 0。意味復元、生成、model routing はなく、LLM を採用する必須性／品質優位性はない。

### Public operation and fixed flags

- `dcimigration.Cutover(ctx, CutoverOptions)` は public execution contract だけを公開し、
  private manager、private applied state、command runner、unit、port、readiness URL、polling、
  arbitrary shell を公開しない。入力を private `cutoverArtifactOptions`／
  `cutoverActiveOptions` へ一度だけ写像し、既存 facade を一回だけ呼ぶ。
- CLI の cutover mode は次の明示 flag を全て要求する。
  `--build-dir`、`--build-receipt`、`--expected-build-receipt-sha256`、
  `--installed-runtime`、`--staged-runtime`、`--expected-installed-runtime-sha256`、
  `--expected-staged-runtime-sha256`、`--active-dci`、`--active-dci-jsonl`、
  `--active-event-store`、`--active-l1`、`--active-archive`、`--active-config`、
  `--rollback-dir`、`--cutover-receipt`、`--service-receipt`。
  `--initial-service-stopped` は cutover 専用の optional flag であり、指定時は owner が
  canonical service の maintenance-stopped proof を mutation 前に検証する。他 mode では
  parse/form error として拒否する。
  dry-run／capture／build 用 flag、positional argument、空値、uppercase／不正 hash、
  unknown flag は state mutation 前に拒否する。
- exact flag set を受理した cutover invocation の stdout は path-free な
  `ServiceCutoverReceipt` 一 JSON value と改行だけ、stderr は bounded machine code だけとする。
  exit 0 は durable `status=applied` に限り、`blocked`、`rolled_back`、`rollback_failed`、
  semantic invalid／unsupported は nonzero とする。unknown／positional／mode-incompatible／
  missing／empty flag は既存 CLI と同じ parse/form error として stdout なし、fixed stderr、
  exit 2 で mutation 前に拒否する。それ以外の service command 前 semantic invalid／unsupported
  は durable service receipt を捏造せず、同じ schema の bounded in-memory blocked result だけを返す。
- Linux は既存の fixed systemd manager を構築する。Windows／macOS は同じ API／CLI を
  compile できなければならず、対応する canonical service manager が存在しない間は、path、
  command、service、config 内容を出さず mutation 前に `service_manager_unavailable` で
  fail closed にする。systemd substitute や direct process control を fallback にしない。

### Invariants and tests

- active config は caller が明示する canonical existing path であり、fixed service の
  `RENCROW_CONFIG` と一致することを manager が検証する。caller は unit／port／readiness／
  command を変更できない。installed runtime は同じ値を artifact cohort と service identity
  の両方へ渡し、別の runtime owner を作らない。
- public facade／CLI は既存 D2c／D2d-2b の source hash、build receipt hash、runtime hash、
  fresh target、alias、stopped proof、rollback、receipt fsync/readback を弱めない。
  production cutover を test helper、private package test、手動 file swap から起動しない。
- TDD は public option mapping、fixed manager factory、single owner call、cutover flag exact set、
  incompatible flag、invalid hash、stdout one JSON、stderr bounded code、path／secret non-leak、
  applied／blocked／rolled_back／rollback_failed exit、Linux fixed owner、非 Linux compile／
  unavailable を検査する。unit test は fake manager と isolated fixture だけを使い、production
  service、DB、runtime を変更しない。
- この receipt は production file/service cutover の terminal だけを証明する。D2e-3 pre、
  canonical restart、D2e-3 post、logs／durable state review、final Step03 receipt chain が揃うまで
  Step03 の operational completion を主張しない。

### Failure Knowledge

- **Failure:** 完成済みの private cutover を test package から直接呼ぶか、手動 systemctl と
  file copy を組み合わせ、CLI receipt を持たずに productionへ適用した。
- **Problem:** operator intent、input hash、service owner、rollback、exit status が一つの再現可能な
  route に束縛されず、成功／復旧／未実行を機械判定できない。
- **Cause:** safety-critical logic を private に保つことと、owner operation を外部から一切
  起動不能にすることを混同した。
- **Lesson:** lifecycle logic は private のまま保ち、public surface は exact options と bounded
  receipt の薄い facade にする。既存 migration CLI がその facade の唯一の運用入口を持つ。
- **Invariant:** production cutover は `rencrow-dci-migrate --mode cutover` ->
  `dcimigration.Cutover` -> private D2d-2b owner の一方向だけであり、alternate route はない。
- **Enforcement:** exact flag matrix、private types、platform factory、fixed manager constants、
  facade mapping test、non-test caller architecture test、cross-compile、production receipt chain で
  強制する。

#### Failure Knowledge: 起動時書込みと frozen cohort を同時に要求した cutover

- **Failure:** offline snapshot／build 後に旧 service を再起動して running preflight を通し、
  その後に同じ frozen cohort を active source と一致させようとした。
- **Problem:** service startup 自体が Event Store へ一件書くため、running proof と frozen
  source hash が構造的に両立せず、同じ手順の timing retry では cutover が成立しない。
- **Cause:** service lifecycle の安全確認を「必ず起動中から開始」と同一視し、保守停止を
  owner が認証・証跡化して引き継ぐ開始状態を contract に持たせていなかった。
- **Lesson:** write quiescence が必要な cohort は、canonical service を enabled／unmasked の
  まま停止した maintenance state から owner CLI が引き継ぎ、runtime mask を取得した後に
  prepare／stage／apply を一方向で完了させる。
- **Invariant:** `--initial-service-stopped` 指定時は、fixed unit、ExecStart、config、旧 runtime
  hash、inactive、PID-zero、listener-zero の全 proof が mutation 前に一致しなければ
  `service_maintenance_stopped` で fail closed にする。proof 前は mask／start／file mutation を
  行わず、mask 取得後の失敗は旧 runtime の running proof まで復旧する。
- **Enforcement:** `VerifyMaintenanceStopped`、相互排他的な v2 initial projections、固定 CLI
  flag isolation、既存 `MaskAndStop`／D2c／recovery chain の再利用で強制する。手動 file swap、
  direct backend、別 port、alternate service route は作らない。
- **Tests:** maintenance-stopped happy order、invalid proof pre-mutation、old runtime recovery、
  unit state／ExecStart／config／runtime hash／PID／listener rejection、bounded error、receipt
  exclusivity、通常 running cutover regression、三 OS compile を検査する。

## D2e-1 owner post-deploy identity evidence

D2e-1 は、D2d-2b の service receipt や D2c の build receipt を置き換えず、完了済みの
一つの DCI Action が owner の正本間で同じ実行として読めることを検査する read-only
subevidence である。公開 schema は `rencrow.dci.identity-evidence/v1`、状態は
`passed` に固定し、`IdentityEvidenceVerifier.VerifyAction(ctx, ActionID)` は一つの
authenticated actor action だけを対象にする。

### Owner readers and exact contract

- DCI owner は `FindSearchResultByActionID(ctx, actionID)` で Action を一件だけ読み、
  `ValidateStoredSearchResult`、authenticated `ValidateActor`、`mode=dci`、
  `status=completed`、`steps>0`、`evidence>0`、`FinalEvidenceCount` の一致を要求する。
  legacy attribution、failed result、ActionID／TraceID／actor の不一致は拒否する。
- Canonical Event Store owner は DCI TraceID に対して `ListByTraceID(ctx, traceID, 256)`
  を一度だけ呼ぶ。256 は verifier 内の固定上限であり、caller が拡張できない。返る
  Event は一つの exact set として扱い、`3 + 2*step_count + evidence_count` 件と一致し、
  `dci.search.requested`、`dci.search.started`、step ごとの
  `dci.source.selected`／`dci.file.read`、`dci.evidence.created`、
  `dci.search.completed` だけを許す。failed、unknown、extra、duplicate は fail closed とする。
- 全 Event は `ActionID`、`TraceID`、authenticated actor、`component_id=dci` を共有する。
  Event graph は `ValidateEventEnvelopeGraph` を通し、requested root -> started ->
  selected/read の順を再構成する。最初の selected は started に、次の selected は
  前 step の pack-order 最後の evidence（なければ前 step の read）に束縛する。
  Evidence は対応する read を cause とし、selected/read/evidence-created の dependency は
  空、completed の cause と sorted dependencies は terminal event join と完全一致させる。
  各 payload は canonical key/value のみを持ち、query、file path、read status/count/error、
  evidence の file／line／snippet／source／reason／confidence、terminal の status／count／
  limitations を結果と相互照合する。
- L1 current と archive は Evidence ごとに
  `FindStagingItemByNamespaceEventID(ctx, "kb:dci", Evidence.CreatedByEventID)` を exact
  lookup する。kind は `search_result`、EventID は Evidence の `CreatedByEventID`、
  `RawText` と SHA-256、canonical DCI `SourceID`／synthetic `SourceURL`、および
  `source_kind=dci`、`search_action_id`、`trace_id`、`evidence_id`、
  `evidence_created_event_id` の meta binding を検査する。current／archive は ID、内容、
  metadata、keywords、status、timestamps を含む full staging projection として等しくなければならない。
- 公開 projection は `schema_version`、`status`、Action／Trace／actor、search status、
  event／step／evidence／current projection／archive projection counts、
  `event_graph_sha256` だけを持つ。graph hash は full Event envelope を occurred time、
  同時刻なら EventID で deterministic に並べ、`encoding/json` で計算する。query、path、
  snippet、URL、payload、meta、DB path、secret、raw error は receipt に出さず、失敗も固定
  bounded code のみを返す。

これは read-only subevidence の受入であり、service receipt、build artifact／runtime
checksum、Data Write の idempotency、restart 後の lookup、正規 service の readiness、または
実 Actor が正規 runtime route を通ったことを証明しない。それらは D2d-2b、D2c、および
後続の post-deploy route acceptance の owner evidence として別に検査する。

### Failure Knowledge

- **Failure:** DCI Action、Event、current L1、archive L1 の一部だけを照合し、検索 query や
  内部 path を返した結果を post-deploy identity proof と扱った。
- **Problem:** owner 間の Action／Trace／actor／Evidence binding と Event graph の欠損を
  検知できず、別の結果や projection を同じ実行として公開する。
- **Cause:** exact Action lookup と bounded Trace lookup を分離せず、terminal join、payload、
  current／archive full projection を独立の正本として扱った。
- **Lesson:** verifier は既存 owner reader と canonical validator だけを使う read-only 境界とし、
  一つの固定 Event set、Evidence created EventID、current／archive projection を同時に満たした
  ときだけ bounded subevidence を返す。
- **Invariant:** `passed` は authenticated completed DCI、positive counts、exact event formula、
  graph／payload／actor binding、Evidence ごとの current＋archive equality、lowercase 64 桁
  graph hash を全て満たす。どれか一つでも欠ければ receipt は発行しない。
- **Enforcement:** `ValidateStoredSearchResult`、`ValidateActor`、`ValidateEventEnvelopeGraph`、
  fixed 256 limit、exact event/payload cardinality、terminal join、canonical L1 source/hash/meta
  checks、path/content-free fixed errors を `IdentityEvidenceVerifier` 境界で強制する。
- **Tests:** happy path、reader order に対する hash determinism、missing／legacy／failed／zero
  evidence、wrong binding、unknown／extra／duplicate／overbound Event、bad graph／chain／payload／
  terminal、current／archive missing／mismatch／hash／meta／reader error、receipt tamper、error／
  receipt non-leak を isolated fake reader で検査する。service command、build、runtime restart、
  production write、post-deploy route は実行しない。

## D2e-2 actual Shiro deterministic post-deploy route acceptance

D2e-2 は D2e-1 の owner identity evidence を、認証済みの実 Shiro が既存の
`/v1/agent/ops` route から決定的に呼び出したことへ結合する bounded な route acceptance
subevidence である。新しい endpoint、direct DB reader、generic tool dispatcher、
自然言語の `RouteOPS`、または LLM を acceptance 経路へ追加しない。D2e-2 の実装・unit
test は handler 契約と `Shiro.ExecuteTool` -> `ToolRunner` の接続だけを検査し、production
deploy、初回／再起動後の実 request、service receipt、artifact checksum、readiness、および
最終 receipt chain は未検証境界として残す。

### Existing route and strict request contract

- 利用する経路は既存の認証済み local-only `POST /v1/agent/ops` 一つだけである。既存の
  client/profile 認証、Bearer、`X-Request-ID`、local-only 制約、body size bound、strict
  one-value JSON、unknown field／trailing token 拒否をそのまま適用する。固定 operation の
  request は `{ "operation": "dci_identity_acceptance", "query": "..." }` とし、legacy
  request `{ "message": "..." }` と mutually exclusive にする。空値、unknown field、両方の
  field、どちらでもない shape は bounded error で拒否し、message branch や LLM へ fallback
  しない。`tool`、`args`、任意の operation 名、任意の DB／path 指定は公開しない。
- `dci_identity_acceptance` は実際に設定された CORE-managed Shiro を actor として使い、
  既存の `Shiro.ExecuteTool` -> `ToolRunner.ExecuteV2` owner route を一つの認証済み
  `agent=shiro`、`role=worker`、`purpose=ops`、`access=internal` scope で呼ぶ。この branch
  では自然言語 `Execute`／`RouteOPS` を呼ばず、LLM を一度も使わない。scope はこの request
  の処理全体で再利用し、次の三つの tool call 以外の tool call を発生させない。

  1. `data.write` / `dci/search` に query を渡す。
  2. 同一 scope、同一 query、同一 request の idempotency key で、同じ
     `data.write` / `dci/search` を直ちにもう一度呼ぶ。
  3. 一回目の write receipt の `audit_ref`（DCI ActionID）を指定し、`data.recall` /
     `dci/identity_evidence` を `limit=1` で一回だけ呼ぶ。

- write の両 receipt と recall projection は strict に decode／validate し、どれか一つでも
  欠ける、owner／route が違う、actor／role／purpose／internal scope が違う、schema／policy／
  validation が成功状態でない、ActionID が一致しない、または D2e-1 の identity evidence
  が `passed` でない場合は fail closed とする。二回目 write は必ず
  `idempotent_replay=true` でなければならない。最初の write の replay は新規 request では
  `false`、再利用 request では `true` を許すが、acceptance runner は fresh な pre-restart
  request で `false`、同じ `X-Request-ID` と query を用いた post-restart request で
  `true`、かつ post-restart の二回とも `true` であることを要求する。ActionID、TraceID、
  Event graph、event／step／evidence／current projection／archive projection counts は
  restart の前後で完全一致しなければならない。

### Public receipt and error boundary

- 固定 operation の成功結果は専用の machine-readable schema
  `rencrow.agent-ops.dci-identity-acceptance/v1` とし、次の field だけを持つ。
  `schema_version`、`status`、`request_id`、`agent_id`、`role`、`operation`、`action_id`、
  `trace_id`、`first_write_replay`、`second_write_replay`、`event_count`、`step_count`、
  `evidence_count`、`current_projection_count`、`archive_projection_count`、
  `event_graph_sha256`。成功 status は D2e-1 の `passed` と結合した `passed` のみとする。
  この branch では `job_id` と `output` を返さない。legacy message branch の既存 response
  は従来どおり six-field shape を維持する。
- error は既存の one-field bounded error envelope だけを返す。malformed／mutually mixed
  request、認証／scope failure、Shiro／ToolRunner unavailable、tool failure、receipt／
  identity tamper、schema／policy／validation failure は同じ非詳細の bounded code に収束し、
  query、path、snippet、URL、payload、meta、tool output、database path、secret、raw error、
  arbitrary ID を漏らさない。失敗した branch を message branch、RouteOPS、LLM、direct DB
  へ切り替えて成功に見せてはならない。

### Implementation boundary and acceptance sequence

- D2e-2 の実装／unit test は strict request dispatch、mutual exclusion、exact three-call
  order、同一 Shiro internal scope、typed/narrow test double を通した ExecuteTool ->
  ToolRunner 呼出し、write receipt／replay／ActionID binding、D2e-1 recall projection の
  bounded mapping、success field allowlist、malformed／unavailable／tamper の non-leak を
  検査する。test double は seam の検査に限り、実 Shiro actor、production identity、deploy
  または post-deploy E2E の証拠を名乗らない。
- production acceptance は、artifact と active config の checksum／owner を先に照合し、
  正規 service の readiness を確認した後、同じ認証と固定 route で fresh pre-restart
  request（first `false`, second `true`）を保存する。正規 service を restart し、owner／
  readiness と旧 generation の消失を確認してから、同じ `X-Request-ID` と query の
  post-restart request（first `true`, second `true`）を実行する。二つの成功 response と
  service／build receipt を一つの final receipt chain として保存し、Action／Trace／Event
  graph／counts の一致、ユーザー利用主体からの route 到達、ログと durable state を照合
  できた時だけ D2e-2 post-deploy route acceptance を passed とする。これらが未実行の間は
  D2e-2、Step 03、または全体 ID 統一を完了と報告しない。

### Failure Knowledge

- **Failure:** 自然言語 `RouteOPS` の LLM 応答、direct DB read、fake actor、または D2e-1
  verifier の内部呼出しだけを post-deploy route acceptance と扱った。
- **Problem:** 実際の authenticated Shiro が既存 owner route と policy を通った事実、write
  idempotency、restart 後の同一 Action／Trace／Event graph を証明できず、別 actor／別経路の
  結果を identity evidence と誤認する。
- **Cause:** 既存 `/v1/agent/ops` の message／LLM 経路と deterministic operation を区別せず、
  または route の前後を同じ test double／DB projection で置き換えた。さらに fresh と replay
  の replay semantics を一回の成功値へ潰した。
- **Lesson:** 既存 endpoint の strict tagged branch だけを拡張し、実 Shiro の既存
  `ExecuteTool` -> `ToolRunner` を一つの authenticated internal scope で三回だけ呼ぶ。fresh
  pre-restart と同一 request の post-restart を別 phase として記録し、二回目 replay と D2e-1
  identity evidence を同じ bounded receipt chain へ結合する。
- **Invariant:** deterministic branch は自然言語／LLM／direct DB／generic tool を使わず、
  exact three-call order、correct owner／route／Shiro actor／role／purpose／internal scope、
  matching ActionID、second replay、passed identity evidence、restart 前後の Action／Trace／
  graph／counts equality を全て満たす。公開 success は許可された固定 field のみである。
- **Enforcement:** strict JSON decoder、tagged request allowlist、single authenticated scope、
  fixed operation dispatch、typed receipt validators、trusted request-id idempotency、D2e-1
  schema／status／count／hash validation、one-field bounded error envelope、raw output
  suppression、および acceptance runner の fresh/restart replay assertions で強制する。
- **Tests:** legacy compatibility、operation／message mutual exclusion、unknown／trailing／
  oversize JSON、exact call order／scope／no-LLM、first／second write receipt mismatch、replay
  false／true、Action／Trace／identity evidence tamper、projection／graph/count mismatch、
  unavailable／tool error、success／error non-leak を typed/narrow test double で検査する。
  production deploy、service restart、artifact／readiness、実 Shiro route、ユーザー E2E、final
  receipt chain はこの unit では実行せず、後続の実運用 acceptance に委譲する。

## D2e-3 fixed pre/post-restart verifier checks

D2e-3 は D2e-2 の実 Shiro route acceptance を、fresh request の前半と canonical service
restart 後の同一 request の後半へ分けて固定する `RenCrow_CORE` 所有の運用 verifier 境界である。
実装する check は次の二つだけであり、phase flag、generic command、任意 query flag、第三の
互換 check は作らない。

| check_id | command_id | 役割 |
| --- | --- | --- |
| `core_dci_identity_pre_restart` | `core-dci-identity-pre-restart` | restart 前の fresh な実 Shiro request と pre evidence を発行する |
| `core_dci_identity_post_restart` | `core-dci-identity-post-restart` | 明示された pre evidence と restart 後の実 Shiro request を deterministic に照合する |

### Common route and bounded fixture

- 両 check の owner は既存の `cmd/rencrow-core-verify` であり、既存の認証済み local-only
  `POST /v1/agent/ops`、active config の client/profile、Bearer、`X-Request-ID`、local-only
  制約、body bound、strict one-value JSON、unknown field／trailing token 拒否をそのまま使う。
  実際に設定された CORE-managed Shiro を actor とし、D2e-2 の
  `Shiro.ExecuteTool` -> `ToolRunner.ExecuteV2` -> `data.write`／`data.recall` owner route を
  通る。LLM、自然言語 `RouteOPS`、direct DB、generic tool、別 endpoint、別 actor、別 route は
  許可しない。
- request body は D2e-2 と同じ固定 operation の strict shape
  `{ "operation": "dci_identity_acceptance", "query": "<owner-fixed fixture>" }` だけを使う。
  query は一つの owner-fixed な非秘密 fixture とし、その値の正本は
  `cmd/rencrow-core-verify` の source に置く。manifest は `owner_fixed_fixture` の acquisition
  contract だけを宣言し、独立編集可能な query 値を持たない。
  caller は query、operation、DB、path、tool、args を指定できず、CLI に任意 query を受ける flag
  を追加しない。evidence には query 自体を保存せず、fixture の lowercase SHA-256 だけを保存する。
- D2e-2 の success response allowlist（`schema_version`、`status`、`request_id`、`agent_id`、
  `role`、`operation`、`action_id`、`trace_id`、`first_write_replay`、`second_write_replay`、
  `event_count`、`step_count`、`evidence_count`、`current_projection_count`、
  `archive_projection_count`、`event_graph_sha256`）を strict に受け入れる。query、body、Bearer、
  path、tool output、payload、meta、secret、raw error、未許可 field、または unrelated／arbitrary ID は
  receipt／evidence に出さない。ID はこの allowlist にある `request_id`、`agent_id`、`action_id`、
  `trace_id` のうちこの chain の binding に必要な canonical 値だけを許可する。

### Pre check

`core_dci_identity_pre_restart` は、既存の canonical systemd service owner の現在 generation、
固定 listener、readiness を観測してから、fresh な request ID（または canonical request-ID 規則を
満たす明示 caller ID）で `/v1/agent/ops` を一回の authenticated route acceptance として呼ぶ。
D2e-2 の三つの tool call と response validation を通し、最初の write は
`first_write_replay=false`、二回目は `second_write_replay=true` でなければ `passed` としない。
成功時は標準 `rencrow.check-receipt.v1` の check receipt と、owner-only の bounded pre evidence
を同じ acceptance cohort として発行する。

pre evidence に残してよいのは、D2e-2 success response の allowlist field（そのうち
`request_id`、`agent_id`、`action_id`、`trace_id` はこの chain に必要な canonical 値だけ）、
`phase=pre_restart`、`observed_at`、固定 fixture の lowercase SHA-256、bounded な非秘密
`service_main_pid`、観測した `service_generation_sha256`、`artifact_sha256`、`config_sha256`、
listener／readiness の bounded boolean、および response facts の canonical hash だけである。
`artifact_sha256` と `config_sha256` は pre／post の観測を同じ artifact／active config に束ねるための
hash であり、deploy／catalog の検証結果を意味しない（その owner evidence は
`core_deploy_identity_chain` に残す）。request の query／body／token／path／output／secret や
allowlist 外の unrelated／arbitrary ID は保存しない。standard check receipt は通常の
`evidence_ref` で evidence publication 後にこの物理 evidence を参照する。chain は
`observed_at`、post が記録する物理 pre evidence SHA-256、および通常の `evidence_ref` で構成し、
evidence 作成時に最終 receipt を循環参照しない。
fresh request、service generation、fixture／artifact／config hash、allowlist response、0600 publication
のいずれかを検証できなければ、詳細を返さず固定された `failed` または prerequisite の
`blocked` として nonzero exit で終了する。

### Post check and exact chain

`core_dci_identity_post_restart` は、caller が明示した一つの pre evidence file だけを入力とする。
その file は canonical owner が発行した regular non-symlink file、owner-only（Unix は mode 0600、
同等の ACL が必要な platform は owner-only ACL）、サイズ／schema／single JSON value が bounded
で、`check_id`、`command_id`、`phase`、`status=passed`、freshness、fixture SHA-256、pre の request
ID と D2e-2 facts／hash、`service_main_pid`、`service_generation_sha256`、`artifact_sha256`、
`config_sha256` が strict に一致しなければ拒否する。caller が path の代替、query、request body、
token、service command、shell command を差し込むことはできず、pre evidence の freshness bound は
check 側の固定値であり CLI override を持たない。

post check は canonical service manager が起動した現在の generation、固定 listener、readiness を
観測する。pre evidence の generation と異なり、pre evidence の `service_main_pid` に対応する
`/proc/<pid>` が存在せず、旧 generation が残っていないことを確認する。この raw prior PID は
旧 generation の不在確認にだけ使い、post receipt／evidence へ再出力しない。pre／post で観測した
`artifact_sha256` と `config_sha256` がそれぞれ一致して同じ artifact／active config を束ね、同じ
fixture hash、同じ request ID、同じ固定 query を canonical `/v1/agent/ops` へ送った場合だけ続行する。
これらの hash は deploy／catalog 成功の主張ではなく、deploy／catalog の owner evidence は
`core_deploy_identity_chain` が持つ。post response
は最初と二回目の write がともに `true` でなければならず、`action_id`、`trace_id`、
`event_graph_sha256`、event／step／evidence／current projection／archive projection counts は
pre response と完全一致しなければならない。異なる generation、旧 generation の残存、listener／
readiness unavailable、pre evidence の stale／tamper、request／fixture／identity／count mismatch
は固定 non-leaking `failed` または `blocked` とし、message／LLM／direct DB／alternate route に
fallback しない。

post evidence は pre と同じ D2e-2 allowlist facts（chain に必要な canonical allowlist ID だけ）に
`phase=post_restart`、`observed_at`、post generation の非秘密 `service_generation_sha256`、
pre／post で一致した `artifact_sha256` と `config_sha256`、listener／readiness／
`old_generation_absent` の bounded boolean、fixture／response facts の hash、および入力 pre evidence
の物理 SHA-256 だけを加える。pre の request body、query、token、path、output、secret、allowlist 外の
unrelated／arbitrary ID は再出力しない。二つの check の標準 receipt は通常の `evidence_ref` で各
evidence を参照し、service／build receipt、logs、durable-state review とともに一つの final receipt
chain へ結合する。

### Ownership, sequence, and non-goals

- verifier は観測と検証だけを行い、restart、stop／start、install、deploy、Git、任意 shell、
  任意 request body／query、artifact publication、DB migration、alternate topology を実行しない。
  canonical service manager が restart の唯一の owner であり、`core_deploy_identity_chain` と
  `core_runtime_identity_lifecycle_security` が source／artifact／publication／full lifecycle を
  所有する。既存 readiness check と service/lifecycle observation は D2e-3 の generation binding
  に再利用できるが、artifact／deploy 判定を複製しない。
- 固定された運用順序は次の通りである。
  `core_deploy_identity_chain` + `core_runtime_identity_lifecycle_security` + readiness
  -> `core_dci_identity_pre_restart`
  -> canonical service-manager restart
  -> runtime identity／lifecycle + readiness／old-generation-absent
  -> `core_dci_identity_post_restart`
  -> logs／durable state review
  -> final Step03 receipt chain。
  pre／post、実 Shiro route、service generation、Action／Trace／Event graph／projection counts の
  全てが揃うまで D2e-3、D2e-2、または Step 03 を complete と報告しない。

## D2e-4 final Step03 receipt publisher

D2e-4 は上記 sequence の結果を一つの machine-readable chain に束ねる read-only owner check である。
`cmd/rencrow-core-verify` に固定 command `core-dci-identity-final`、固定 check
`core_dci_identity_final` を一つだけ追加する。generic finalizer、phase flag、任意 command、任意 DB／
path discovery、restart／deploy／migration は実装しない。

caller が明示できる入力は、同じ acceptance cohort の owner-only regular file である pre evidence、
post evidence、service-cutover receipt、cutover subreceipt、deploy receipt JSONL の五つだけとする。
symlink、owner 外 permission、bounded size 超過、unknown／duplicate field、trailing JSON、schema／status／
hash 不一致を fail closed で拒否する。pre／post は D2e-3 の strict validator を再利用し、post が保持する
pre physical SHA-256、stable Action／Trace／Event graph／counts、artifact／config hash、異なる generation、
old-generation-absent を再照合する。service-cutover v3 と cutover v2 は各 owner schemaを strict に読み、
`applied`、subreceipt physical SHA-256、内部 build／runtime binding、final running、quick-check、foreign-key、
orphan、legacy key、sidecar zeroを検査する。migration cohort の runtime hashを後続 remediation artifact
hashと同一視しない。

deploy JSONL は bounded line数／line size／owner deploy receiptの成功・失敗・deferredを含むstrict schemaで読み、最後の成功した `rencrow` と
`rencrow-core-verify` の共通 full Git revisionを決定的に選ぶ。現在の canonical service owner、artifact、
active config、listener、readinessを再観測し、pre／postのartifact／config hashと一致させる。post evidence
の時刻以降の canonical unit journalをboundedに取得し、warning／error／panic／fatalが一件でもあれば
`failed`、journal取得不能なら`blocked`とする。raw log本文は公開しない。

成功 evidence schema は `rencrow.identity.dci-final/v1`、phase は `final` とする。公開してよいfieldは、
command／phase、pre／post／service-cutover／cutover／deploy-logの各physical SHA-256、deploy revision、
request／agent／Action／Trace、Event graph hash、event／step／evidence／current／archive counts、artifact／
config／current generation hash、listener／readiness／old-generation-absent／migration-integrity／
deploy-pair／journal-clean のbooleanだけである。filesystem path、PID、query、body、token、raw log、DB内容、
任意IDは出力しない。standard receiptとfinal evidenceをowner-onlyで発行し、これが`passed`になるまで
Step 03をcompleteとしない。

### D2e-4 Failure Knowledge: 実行不能候補のcontent rankによるdeadline消費

- **Failure:** provider／registryが順位付き候補を返した後も、`MaxCandidateFiles`全件をcontent読取し、
  実行対象をcontent rankと最終scanで二重に読んだ。
- **Problem:** evidence生成前に検索deadlineを消費し、正しい候補と部分Evidenceが存在してもfresh DCIが
  `context deadline exceeded`になった。
- **Cause:** 候補収集上限と実行読取上限を同じbudgetとして扱わず、実行されない候補へI/Oしたうえ、
  rank済みcontentをrequest内で再利用しなかった。
- **Lesson:** canonical metadata rankがある場合は先に決定的に順位付けし、content rankは実際に実行可能な
  `MaxFilesRead`範囲だけへ限定し、rank時のcontentを同一requestの最終scanで再利用する。
  metadataのないfilesystem fallbackは全候補content rankを維持する。
- **Invariant:** provider／registry候補に対するfile readは、content rankと最終scanを合わせて
  最大`MaxFilesRead`であり、同一request内の各候補を一度だけ読む。`MaxEvidence`等の成功終端を
  満たした後は、同時にdeadlineへ達しても次候補のcontext判定で成功結果を失敗へ上書きしない。
- **Runtime bound:** foreground開始時のbackground ToolRunner退避時間を含めても正規routeを完遂できるよう、
  DCIの未指定時budgetは30秒とする。明示設定した短いbudgetと、Evidence未到達時のdeadline failureは維持する。
- **Enforcement / Tests:** Explorerの順序とbounded read testで強制し、fallbackのcontent discovery、
  provider統合、metadata優先、実Shiro production routeを回帰検証する。

### Failure Knowledge

- **Failure:** pre と post を一つの phase flag／generic verifier command にまとめ、restart を
  verifier 内で実行したか、pre response を保存せず post を新規 request として通した。
- **Problem:** canonical service generation の切替、同じ request ID の idempotency、実 Shiro の
  同じ Action／Trace／Event graph、owner の artifact／lifecycle 証拠を一つの境界で追跡できない。
- **Cause:** D2e-2 route response、service-manager lifecycle、artifact/deploy verifier を同じ
  owner として扱い、fresh／replay semantics と pre evidence の freshness／hash binding を
  省略した。
- **Lesson:** D2e-2 route は変更せず、`cmd/rencrow-core-verify` に pre と post の二つの fixed
  check だけを置く。pre は fresh false／true、post は同じ request の true／true と旧 generation
  消失を、owner-only evidence と deterministic hash chain で結合する。
- **Invariant:** `passed` は固定 fixture、strict auth/body/response、実 Shiro、canonical route、
  pre の false／true、post の true／true、異なる service generation、旧 generation 不在、同じ
  Action／Trace／graph／counts、non-leaking bounded evidence を同時に満たす。restart／artifact／
  deploy の実行権限は verifier にない。
- **Enforcement:** fixed manifest allowlist、二つの command ID、strict request／header／response
  validation、canonical allowlist ID の限定、owner-only 0600 evidence、fixed freshness、fixture／
  response／artifact／config／prior-evidence SHA-256、service generation／listener／readiness
  observation、prior PID の `/proc` absence と old-generation-absent check、fixed status／exit code、
  one-field non-leaking errors で強制する。
- **Tests:** manifest allowlist／fixed-fixture acquisition、strict body／auth headers、pre replay、
  post evidence schema／0600／freshness／hash、same request ID／fixture、different generation／old
  absent、Action／Trace／identity／count equality、response field allowlist、success／error non-leak、
  `passed`／`failed`／`blocked` の status／exit code を fake service／typed route seam で検査する。
  unit test は実 service restart、deploy、artifact publication、Git、shell、または production
  Shiro route を実行しない。


### Step 20 follow-up: Complexity Coder diff Task identity

- Complexity Coder diffのrequest／resultはtyped `TaskID`とJSON `task_id`を使う。旧`JobID`／`job_id`をruntime互換入力として受理しない。
- Viewer受付は任意の非空TaskIDをcanonical validatorで検証し、未指定時だけ新規TaskIDを生成する。service直接呼出しも同じ検証を行い、生成した`TurnInput.RootTaskID`と結果の`TaskID`を一致させる。Turn／Trace／Messageは別identityを維持する。
- malformed／wrong-type ID、未知field、複数JSON valueはCoder実行・report保存より前に拒否する。失敗reportの相関表示もTask IDに統一する。既存のreview-only／patch未適用境界を維持する。
- Failure: 旧JobIDをTaskID値で埋めた一方、Coder inputには別TaskIDを生成して結果相関を切断した。Invariant: request→Coder input→resultは同一TaskID。Enforcement: typed ID、受付validation、単一生成、旧JSON拒否。Tests: 指定／未指定の同一identity、wrong-type拒否、旧key拒否、review-onlyの回帰検査。
- この修正のsource testはdurable Task作成、production Coder route、配備後E2E、Step20全体完了の証拠を代替しない。


### Step 19 follow-up: Home / Reports Task projection

- Home／ReportsはExecution EvidenceとVerification APIの`task_id`で同じTaskの結果を結合し、表示・copy text・detail queryにも同じ値を使う。旧`job_id` fallbackを追加しない。
- Failure: APIが`task_id`へ移行済みでもHome側が旧keyでgroupingし、正しいレポートを一覧から落とした。Invariant: 同じTaskのEvidence／Verificationは一つの表示へ結合し、両detail linkは`task_id`を指定する。Enforcement / Tests: canonical-only API fixtureによる一覧件数、相関、表示、リンク、旧key非採用を既存Viewer Node suiteで検査する。
- 本項はHome／ReportsのTask相関修正である。Viewer内部の別Job名称、Report projection key、Develop／Progress／System／Instructions、全Graph再構築、実runtimeの配備後E2Eは別途閉じる。


### Step 19 follow-up: Develop Task correlation

- Developは現在TaskのIDとEvidence／Verification／logの`task_id`を照合する。旧`job_id`をfallbackとして採用しない。Task未選択時はEvidence／Verificationなし、関連logなしとし、他Taskの結果を表示しない。
- Failure / Problem: API移行後もDevelopが旧keyを読み、現在Taskの成果物・検証結果・logを失った。Cause: 表示側の相関key残存と、空ID時に全logを採用する条件。Lesson / Invariant: 相関はcanonical Task IDの一致を要し、未選択を全件表示として扱わない。
- Enforcement / Tests: 既存Viewer Node suiteで異なるTask、旧keyだけのデータ、現在Task、空選択を混在させ、成果物・検証結果・直近8件logの選択と非混入を検査する。実ブラウザのdesktop／mobile幅でも独立した描画を確認する。
- 本項はDevelopの表示相関に限定する。共有`state.jobs`の内部名称、Instructions保存／読出しの`job_ids`、配備後の認証済み実Actor routeと再起動後E2Eは未完了境界として残す。独立描画testはproduction E2Eの代替ではない。


### Step 19 follow-up: Instructions canonical Task links

- CORE Viewerのブラウザ下書きは`rencrow.viewer.instructions.v2`を保存正本とし、Developからの作成、正規化、状態編集、カード表示に`task_ids`を使用する。サーバーのTask作成・実行状態をこの下書きで代替しない。
- v1領域は変更・削除せず保持する。存在時はInstructionsで旧下書きの残存と未移行を明示する。v1を通常の読出しfallback、dual write、ID別名として使用しない。旧IDのTask相関が証明されていないため、既存下書きの移行は未完了であり、本項だけで利用者データ移行を完了としない。
- Failure / Problem / Cause: Developの旧`job_ids`書込みとInstructionsの旧key正規化が同じ名称を永続化した。Lesson / Invariant: 新規下書きの作成→保存→状態編集→再読込→表示で同じ`task_ids`を保持し、元の旧下書きを破壊しない。Enforcement / Tests: 既存Viewer Node suiteでこの往復、旧領域のbyte不変、旧key非採用を検査する。
- Full regression（persistence変更）、既存下書き移行、配備後の実Actor／認証済み経路確認は未完了境界として残す。


### Step 19 follow-up: Archived instruction text recovery

- 旧ブラウザ下書きはInstructionsの原本閲覧面から本文・保存時記録を確認できる。明示操作で本文だけを空の編集欄へ戻し、通常の新規下書き作成へ進める。編集中本文を上書きせず、元領域を書換えず、旧ID・実行状態・Task相関を継承しない。
- 原本読出しは復元画面に限定し、通常queueのfallback／dual readには使わない。表示時のsnapshotを操作対象とし、不正JSON／非配列／storage拒否時は失敗を表示してsnapshotを破棄する。本文・記録はHTML escapeし、本文が文字列でない行は復元対象にしない。
- Failure / Problem: 保存領域切替後に旧下書きの存在通知だけを残し本文へ到達できなくした。Cause: 非破壊保存を利用可能性と取り違えた。Lesson / Invariant: 保持した利用者データには内容確認と安全な再利用経路が必要。Enforcement / Tests: 本文完全一致、原本byte不変、旧ID非継承、編集欄非上書き、不正入力、storage拒否、HTML escape、実ブラウザ操作を検査する。
- 本文再利用は旧Task IDの移行ではない。旧Task対応の証明、production配備後検証とFull regressionは別の未完了境界として残す。


### Step 19 follow-up: Progress Task event correlation

- ProgressはEventのtop-level `task_id`で進捗を集計する。IDなしイベントと旧`job_id`だけのイベントは採用せず、canonical keyと旧keyのdual readを行わない。画面の見出し・ID列・Agentカード・件数はTask表記にする。
- Failure / Problem: canonical EventをProgressが旧keyで読むため、現在Taskの進捗が欠落した。Cause: 入力契約移行と表示consumerの不一致。Lesson / Invariant: Task単位の状態・失敗・詳細イベントは同じcanonical IDから導出する。Enforcement / Tests: 成功Taskと失敗Taskを混在させた集計、旧key非採用、詳細Open／Hide、Task表示を既存Viewer Node suiteと独立ブラウザ操作で確認する。
- 共有frontend stateの`jobID`／`progressOpenJobs`と関数名は内部投影名として今回維持する。これらの名称整理、phase／Actor帰属の全面改修、Systemタブ、Full regressionと配備後E2Eは未完了境界として残す。


### Step 19 follow-up: System event Task display and copy

- SystemタブのTask列とRowコピーJSONはEventのtop-level `task_id`を使用し、旧`job_id`を採用・出力しない。Taskなしのsystem eventは表示対象のまま、列は`-`、JSONは空`task_id`とする。Textコピーは原contentを維持する。
- Failure / Problem: canonical EventのTask相関がSystem列とRowコピーから消失した。Cause: 描画とコピーpayloadの両consumerが旧keyを参照した。Lesson / Invariant: 同じEventの表示と外部へコピーするJSONは同一Task IDを保持する。Enforcement / Tests: canonical IDと矛盾する旧key、旧keyのみのEvent、Text／Row payload、既存filterをNode suiteとPC／mobile幅のブラウザ操作で検査する。
- 本項はSystem表示・copyに限定する。全Viewer内部のJob名称整理、残るタブ、Full regressionとproduction配備後E2Eは別途確認する。


### Step 19/20 follow-up: Shared Viewer identity cleanup

- Viewerの共有投影は`state.tasks`、Agentの相関は`taskID`、Tasksタブは`panel-tasks`／`tabs/tasks.js`を使用する。Evidence選択、Progress詳細開閉、voice-direct相関、各タブ間の遷移、WebMCP snapshotの`task_count`／`running_task_count`も同じ概念へ揃え、旧名の別更新経路を残さない。
- Task通知のingest／dedupe／Timeline描画は一系統とし、旧Job通知の関数・event branch・styleは削除する。Task通知styleをcanonical classに結び付ける。既存APIに追随していなかったRevenue／Skill PRのAction fixtureとEvidence／Coder／VoiceのTask fixtureを現契約へ修正した。
- Failure / Problem: wire key移行だけでは内部投影・navigation・copy/snapshot・通知経路の旧概念が残存した。Cause: consumer全体の追跡不足。Lesson / Invariant: 同じTask情報のproducerと全consumerを同じ実装単位で切り替え、別名／旧経路を機械的に検出する。Enforcement / Tests: Viewer Node suiteにTask projection/navigation、retired identity source scan、asset実在、Task通知dedupe、WebMCP count契約を追加する。
- 前項までに残件とした共有frontend Job名称、Tasksタブ見出し、System相関はこのsource単位で修正した。配備後／再起動後の実Actor・SSE／Graph全体の受入証跡をsource検査で代替しない。

### Step 20 follow-up: External movie crawler reference

- Movie Catalogのsidecar crawl参照はCORE内で`ExternalCrawlJobID`と明示する。値を生成するownerはTools映画crawl gatewayであり、CORE Task identityではない。COREのTaskID生成・検索・権限判断へ転用しない。
- sidecar transportおよび既存movie fetch responseの`job_id`は、この外部crawl lifecycleのwire metadataとして保持する（既存公開契約は`docs/06_Public_API仕様.md`のMovie Catalog節）。CORE canonical IDの旧aliasとしてのJobIDとは区別する。外部IDをTaskIDへ文字列置換してcanonical化したことにしない。
- Failure / Problem / Cause: 外部IDを単にJobIDと命名しCORE Taskの旧名称との判別を難しくした。Lesson / Invariant: 外部IDはownerと用途を名前で区別し、実際のupstream値を保持する。Enforcement / Tests: crawler／backfillテストの外部値round-trip、HTTP pollingとartifact hash検査を維持する。sidecar実配備・外部I/Oは未検証。

#### Follow-up: atomic SSE subscription and typed client contract

- EventHubの購読登録と履歴snapshot取得を同じlockで行う。Eventの履歴追加とlive enqueueも同じlockで行い、snapshot/live境界の二重送信・取りこぼしを防ぐ。ViewerとChrome bridgeはこの単一subscription契約を使用する。
- audio routerはlive audio chunkだけを配信する。従来の履歴loopはtransient除外とaudio専用filterにより常に空だったため削除した。再生済み音声を再送しない境界を維持する。
- Failure / Problem: Viewerのsubscribe後History取得は同じEventを二重送信し、ChromeのHistory取得後subscribeは境界Eventを取りこぼした。Cause: snapshot取得と購読登録の別操作。Lesson / Invariant: 履歴とlive streamの分割点はownerがatomicに確定する。Enforcement: EventHubの単一lockとsnapshotを返すSubscribe契約。Tests: 購読callbackでEventを同期発行する二重送信回帰、切断後client数、履歴cursor、transient音声除外、Chrome session filter。
- Complexity Go clientはtyped TaskIDを送受信し、指定時のrequest/result一致、未指定時のserver生成ID、欠落・malformed・wrong-type結果拒否を検査する。clientで別TaskIDを生成しない。
- deprecated test fixtureのItemID／ChatID／RequestID prefixと、Viewer audio harnessの実Memory module読込みを現行契約へ揃えた。旧IDをproductionで受理する互換経路は追加しない。
- 上記はsource検証であり、race検査のtimeout、永続Event Storeからの容量外backfill、全Graph再構築、実Actor経路・再起動後検証、Full regressionは未完了境界として残す。

#### Follow-up specification: bounded canonical identity graph projection

- ownerはCORE Viewer。`GET /viewer/identity-graph?trace_id=<Canonical TraceID>`は既存Viewer認証・profile制約の内側で実行するread-only queryとする。未知query、空・不正TraceID、GET以外を拒否する。独立Graph Store、legacy lookup、LLM解釈は追加しない。
- Event Storeの既存`ListByTraceID`で一つのTraceを最大1000 Eventまで取得する。超過・store失敗・閉じていないEvent参照・cycleは成功扱いにせず、bounded errorを返す。EventSeq順で安定出力し、`ValidateEventEnvelopeGraph`で正本関係を検証する。
- response schemaは`rencrow.identity-graph.v1`。`trace_id`、`events`、`tasks`、`messages`と、各graphの明示的なedgeを返す。payload、本文、任意filesystem path、module root、raw error、secretを返さない。
- Event nodeはEventID、EventSeq、event type、TraceID、TaskID、RunID、MessageID、SessionID、ThreadID、TurnIDと、正本に存在するActorKind／ActorIDだけを持つ。Event edgeはcausation／dependency。model、provider、From／To routing labelからActorを推測しない。
- Task nodeはEventに紐づくTaskIDを起点とし、Task Storeのparent／dependency／supersedes参照を最大256 Taskまで辿る。TaskID、status、上記関係だけを投影し、本文・共有contextを複製しない。参照欠落、cycle、上限超過は明示的に拒否する。
- Communication graphはMessageIDと、そのMessageを記録したEventIDの関係を表す。発話主体は該当Eventに記録されたActorだけを示し、未記録のrecipientやspeakerを補完しない。Message間の因果はEvent edgeを通して追跡する。
- ViewerのTasks表示から観測済みTraceを選んでGraphを表示できるようにする。Taskの実行状態をGraphから更新しない。query失敗・未観測Traceは画面へ明示する。
- CLI工程: 正本query、ID照合、graph構築、安定順序。Boundary工程: HTTP入力・認証・件数制限・secret-free DTO・失敗status。LLM工程は不要。
- 受入証拠: 正本storeからの再構築、Event／Task参照とcycle検査、unrelated trace除外、秘密値非投影、再読込みの同一結果、認証済みViewerの実queryと再起動後の同一Graph。source testのみでは最後の運用条件を完了としない。

#### Follow-up specification: durable SSE replay

- reconnectの`Last-Event-ID`はEvent Store正本のEventSeqとして扱う。メモリ履歴容量を越えたEventをsilentに省略しない。
- Event Storeはcomponent、after EventSeq、固定through EventSeq、page limitを受け、EventSeq昇順のpageと固定throughを返す。初回のみthroughを正本queryで確定し、後続pageは同じthroughを保持する。各pageは最大1000 Event、負cursor・不正bound・正本より先のcursorは拒否する。
- Viewerの再送はCanonicalEventLogの既存projectionを使用する。購読登録後に固定範囲を再送し、live channelと重複するEventSeqは一度だけ送信する。transient audio等の既存再送禁止を維持する。
- replayはrequest contextで中止可能とし、途中失敗時に未取得範囲を取得済みと報告しない。slow clientのlive queue overflowはsilent dropではなく切断して再接続による正本再送へ戻す。各consumerはchannel closeを終端として扱う。
- CLI工程はindexed range queryと順序・重複制御、Boundary工程はcursor validation・page bound・context・エラーと切断。LLM工程は不要。source受入には保持容量外、restart後、再送/live境界、overflow回復、拒否・取消しを含める。実Viewer再接続は配備後に別途検証する。

#### Follow-up specification: STT transport identity boundary

- COREのSTT adapterが受け取るproviderの`event_id`は外部transport参照であり、正本EventIDとして採用しない。providerが返した値は保持し、不在時に日付・counter等からEventIDを補完しない。既存responseのoptional/empty key契約は維持する。
- `/stt` WebSocketのready通知はproviderと接続準備状態を示す。会話ownerからSessionIDを受け取らない経路では`session_id`を出力しない。計測用logにも未保存のSessionIDを生成せず、既存durationとmodeを記録する。Sessionを要求する後続会話は既存CORE ingressで解決する。
- Failure / Problem: STT transportのcounter IDを会話SessionIDにも転用し、wireと計測logに異なる偽Sessionを生成した。Cause: provider参照とCORE identityの混同。Lesson / Invariant: 正本に存在しないidentityを補完しない。Enforcement / Tests: 旧generatorとallowlistを削除し、外部参照の保持／不在、WebSocket通知のSessionID非生成、既存Gateway route、計測durationを検査する。
- CLI工程はSTT応答と計測、Boundary工程は公開payloadとID非補完。LLM工程は不要。source検証と実音声経路の配備後検証を分ける。

### Step 20 follow-up: Ops Recall Trace projection

- MemoryのRecall一覧とNews Packの利用履歴も、同じRecallTraceの`TraceID`をTrace列へ表示する。項目0件の行を含め旧ResponseIDへfallbackしない。各画面の既存項目・列数・matching・警告表示を維持する。

- OpsのRecent TraceはRecallTrace正本の`TraceID`と`TurnID`を表示し、件数をTracesとして示す。廃止済み`ResponseID`を読まず、Trace／Turn欠落を旧fieldへのfallbackで補わない。
- 既存のRecall API、L1 owner、5カードの構成、安全なIDのみの投影を維持する。Recall本文、prompt、raw outputを表示へ追加しない。
- Failure / Problem: L1のRecallTrace契約は移行済みでも、Opsの詳細とブラウザfixtureが旧ResponseIDを前提としていた。Cause: 保存側と表示consumerの同時切替が不足していた。Lesson / Invariant: UIは正本APIの識別子をそのまま投影し、テストは旧固定文字列ではなく実APIのTraceID／TurnIDと画面を照合する。Enforcement / Tests: 既存Viewer Node suiteでTrace／Turn表示と旧field非採用を検査し、populatedブラウザE2Eで実ストア→API→desktop／mobile表示を照合する。
- 隔離Viewer E2Eは配備後の実ActorによるRecall検証の代替ではない。

### Step 12 follow-up: Vision transport Request identity

- CORE Vision processorは、サイズ等のローカル検証を通った各画像／動画のAnalyze通信ごとに、`modules/core.NewRequestID`で正本RequestIDを一度だけ生成する。一つの会話Traceに複数の添付があってもRequestIDは別々とし、TraceID・SessionID・TaskIDをRequestIDへ流用しない。
- CORE Vision clientは送信前にRequestIDを検証し、不正値を`VISION_INVALID_REQUEST_ID`として通信前に拒否する。既存の`CORE -> RenCrow_Vision -> Wild`経路とmultipart仕様を維持し、headerの`X-Request-Id`とformの`request_id`へ同じ値を渡す。
- 成功応答の`request_id`は送信した正本RequestIDとの完全一致を必須とする。欠落、不正形式、別要求のIDは`VISION_IDENTITY_MISMATCH`とし、解析本文を会話へ取り込まない。identity errorへ応答本文や未検証IDを埋め込まない。remote failureは既存typed errorとして返し、成功した解析結果に変換しない。
- 親のTraceID・SessionIDと添付原本を保ち、解析後のテキストだけを既存Agent経路へ渡す。RequestIDから新しいTraceを生成しない。
- Failure / Problem: 複数のVision通信へ同一TraceIDをRequestIDとして渡し、成功応答の相関を検査していなかった。Cause: 会話の因果識別子と一回の通信要求の識別子を兼用した。Lesson / Invariant: 通信ごとの独立IDと応答照合は決定的なCORE境界の責務とする。Enforcement / Tests: 複数添付のID一意性、親Trace保持、header/form一致、不正入力で通信0件、欠落／不正／別IDの成功応答拒否をprocessor／HTTP contract testで検査する。
- 本項のsource testは実Actorによる配備後の画像解析、Action／Attempt／Response Eventの完全な連結、全Identity工程の完了を代替しない。

### Step 12 follow-up: Subagent execution context binding

- Subagent ManagerがCORE所有のSuperAgent runtime contextを受け取った場合、同じTaskID・RunID・TraceIDを既存のexecution contextへ接続してから開始記録、LLM呼出し、Tool実行へ進む。新しいIDを発行せず、TaskからTraceを導出しない。
- 既にexecution contextがある場合は3つのIDの完全一致を要求する。欠損・不正なSuperAgent runtime identity、または既存execution contextとの不一致は開始記録と実行より前に拒否する。Recorderを無効にしてもこの境界は維持する。
- SuperAgent runtime contextを使用しない独立したowner routeは、既存のToolLoop Config／execution context契約を維持する。本項は非会話routeで許容される任意Traceを無条件に必須化しない。
- Failure / Problem: ManagerのSuperAgent記録用contextとTool実行用contextが別々で、前者だけを渡した場合にToolLoopがTraceなしのexecution identityを作った。Cause: Task／RunをConfigへコピーする一方、Trace接続を上流callerだけに依存した。Lesson / Invariant: Managerは受け取った正本runtime identityを実行境界へ一貫して接続し、競合時に上書きしない。Enforcement / Tests: 実ActionStoreとTool runnerを通した同一identity保持、Recorderの有無、Task／Run／Trace競合でprovider・Tool・開始記録が0件であることを検証する。
- Source検証は配備後の実Actor Tool経路、全Event参照の連結、再起動後の運用確認を代替しない。

### Step 15 follow-up: Scheduler execution outcome truth

- Schedulerの発火結果`completed`は、対応するExecutorが処理を実行し成功を返した場合に限る。Executor未設定、またはExecutorがそのSchedule targetを処理できない場合は、共通の`ErrExecutorUnavailable`を失敗として扱い、RunLogの`status=failed`とerrorへ記録する。実行せず成功扱いにする経路を残さない。
- Pronunciation用Executorは既定target以外を受理せず、inner Executor未設定も同じエラーを返す。既存の実行成功、実行失敗、GPU busy等の`DeferredError`と再実行間隔の契約は維持する。
- Failure / Problem: Scheduler本体とPronunciation dispatchが、Executorなし／対象外を正常終了として記録した。Cause: 発火記録の保存と処理成功を同一視した。Lesson / Invariant: ID付きRunLogの存在は処理成功の根拠ではない。Enforcement / Tests: 未設定・対象外でcompletedを保存しないこと、対応targetは一度だけ委譲すること、既存failed/deferred/successの保存結果を検証する。
- 本項はRunLogの結果表現の修正であり、既存の発火時TaskID／RunIDを正本TaskManagerへ接続する残件、配備後の実Actor経路、再起動／再試行の受入条件を完了扱いにしない。

#### Step12 follow-up: TTS public playback reference

- **Failure / Problem:** COREのTTS公開再生キーが`ResponseID` / `response_id`と呼ばれ、通信応答のCanonical `rsp_` identityと混同されていた。
- **Cause:** 通常会話のTask文字列とIdleChatの`session:sequence`を、音声キューの相関参照として同じ名前で扱っていた。
- **Lesson / Invariant:** 公開再生キーの名前は`PublicPlaybackRef`、Viewerのwire fieldは`public_playback_ref`とする。これはopaqueな再生相関参照であり、Canonical ResponseIDではない。値、順序、generation、timeout、再生完了時のpending解除を維持する。型付き`SynthesisRequest.ResponseID`と`ChunkRef.ResponseID`は通信契約として維持する。
- **Enforcement:** COREの公開session生成、bridge callback、chunk/completion payload、Viewer音声queue、acknowledgement、再生状態snapshotを一つの変更単位で更新する。旧public `response_id`のfallbackは追加しない。Viewerの汎用Eventにある本来のResponseIDを置換しない。active audio Viewerによる既存の操作権限を保持する。
- **Boundary:** 実際のCORE bridgeの`/api/tts` provider payloadはtext/voice_id/speed/pitchを送り、公開再生キーを送らない。この単位はGatewayのtransport request header、通信identity生成、Task/Actionの永続owner bindingを変更しない。履歴のTTSイベントは既存のlive-only replay policyを維持する。稼働中queueとViewer assetの切替、実音声、実Actorの受入は配備時に別途検証する。
- **Tests:** 公開wire fieldの正確なechoと旧キー不受理、親Trace伝播、chunk順序、stale generation、timeout、中断、完了、active audio権限をGo/Viewer Nodeで検査する。正規音声経路と最終Full回帰が未検証の間は本programを完了にしない。

#### Stage / Closure receipt action binding

- **Failure / Problem:** 一回のLIVE_VERIFIEDからDONEへの自動終了処理が、Stage、DONE Stage、Closureごとに別ActionIDを生成していた。
- **Cause:** 空またはopaqueなcaller RequestIDを各receipt生成箇所で独立してActionIDへ解決し、最初に永続化したActionを後続へ渡していなかった。
- **Invariant / Enforcement:** 新規stageで一度決定したActionIDを後続closureへ渡す。再開時は同じstage keyの永続receiptにあるActionIDを再利用し、再試行callerの相関文字列で置換しない。DONEのprepared receiptからclosureを再開する場合も同じ規則を使う。既存store、idempotency key、payload hash、lease、終了順序を維持する。
- **Tests:** LIVE_VERIFIED / DONE / ClosureのAction一致、prepared receiptからの再開と再試行、既存payload競合・終了処理の試験で保証する。過去に分裂したreceiptの移行、Revise入力の命名、DevelopmentEventのTraceIDとcaller相関の分離、配備後の実Actor検証は別の未完了境界として保持する。

#### Persisted closure ActionID consistency gate

- **Failure / Problem:** LIVE_VERIFIED Stage、DONE Stage、DONE Closureに異なるActionIDが保存されていても、再実行や復旧が成功扱いになり、resource更新やlease解放を継続できた。
- **Cause:** 各receiptの状態とpayload hashだけを検査し、同じUnit・Revisionに属する終了処理のAction束縛を横断照合していなかった。
- **Invariant / Enforcement:** 終了処理の入口では、既存owner storeから同一Unit・RevisionのLIVE_VERIFIED Stage、DONE Stage、DONE Closureを読む。存在する各receiptのActionIDは有効かつ同一でなければならない。欠落・不正・不一致は`ErrLifecycleConflict`、lookup失敗はそのerrorを返し、Event発行、状態変更、resource更新、lease解放に進まない。receiptがまだ存在しない場合だけ通常の新規処理へ進む。保存済みidentityを自動修復・置換しない。
- **Tests:** 通常Revise、closure resume、completeDoneの各入口で、stage間不一致、closure不一致、不正値、空値を拒否し、storeとleaseが変更されないことを検査する。正常終了・復旧・関連storeの回帰試験を維持する。
- **Remaining:** このgateは既存データの移行、過去のDONE projectionの是正、実Actorの配備後検証を代替しない。これらの証拠が揃うまでIdentity programは未完了である。

#### Current / Pipeline closure evidence projection

- **Failure / Problem:** 永続receiptのAction束縛が欠落・不整合でも、CurrentやPipelineがDONEを成功表示していた。lifecycle情報がないitemにも無条件のCurrent互換表示があった。
- **Invariant / Enforcement:** DONE / LIVE_VERIFIEDの投影では、COREの同じclosure Action検査を再利用する。有効なUnit・Revisionと保存済みActionの証拠が必要である。不整合はCurrentから除外するがPipelineから隠さず、派生DeliveryStateとDONE stageをblockedにし、既存Reason欄へ根拠を表示する。storeのDeliveryStateやreceiptは書き換えない。identity conflict以外のlookup errorは投影全体のerrorとして返す。
- **Ownership:** Action判定はBacklog serviceに一意に置き、Viewerへ再実装しない。投影中のUnit・Revision別結果は一回のrequest内でのみ再利用し、永続cacheや新しい正本を作らない。
- **Tests:** 正常closureのCurrent表示、不一致・不正・Action欠落・Unit欠落・receipt欠落の除外と可視blocked理由、保存状態不変、Viewer既存rendererのblocked表示を検査する。稼働Viewer、実Actor、過去データの移行と最終Fullは別途確認する。

#### Development event correlation and canonical persistence

- **Failure / Problem:** transitionのcaller RequestIDをTraceIDに流し、開発recordとpayloadから合成した診断キーをMessageIDに入れていた。正規ID検査がこれを拒否し、event store未接続でも成功が返っていた。
- **Invariant / Enforcement:** producerのcaller相関は`DevelopmentEvent.request_id`として保持する。開発record／診断referenceはevent contentに残し、MessageIDを名乗らない。sinkは既存execution contextのTaskID・RunID・TraceIDと、検証済みTool scopeのActorを引き継ぐ。Trace競合、Actor欠落、不正scopeは保存前に拒否する。execution contextがないイベントに実Actorを捏造しない。Trace未指定の場合のroot発行は既存canonical event ownerに一意に委ねる。store未接続と保存失敗はerrorで返す。
- **Tests:** producerのRequestID分離、正規sinkへの保存、Trace保持と未指定root、execution identity継承、競合・不正Trace・Actor欠落・store障害を検査する。
- **Remaining:** Atlas HTTP／Heartbeatの実行入口での完全なActor・Run束縛、Action／Attemptのevent envelopeへの接続、開発Artifactの正規owner化、過去event移行、配備後の実Actor／Viewer traceは未完了であり、この局所修正の試験を運用証拠にしない。

#### Heartbeat Task / Run owner integration

- **Failure / Problem:** HeartbeatがTaskIDを直接生成してWorkerへ渡し、Task／Run永続ownerを経由しないため、実行、失敗時Atlas記録、終了状態を同じ正本で追跡できなかった。
- **Invariant:** Heartbeatからの各Worker実行は、既存TaskManagerのCreateとStartRunWithReasonで確定したTask／Runを使用する。owner未接続・発行失敗・返却identity不整合の場合はWorkerを呼ばない。実Actorはruntimeで接続したShiroと一致する認証済みscopeに限り、LLM観測文字列から推定しない。
- **Execution boundary:** 新規Heartbeat処理に既存の異なるexecution identityを上書きしない。取得したcontextはWorker、失敗時Atlas Revise、後処理へ伝搬する。保存ownerはTaskManager一つとし、Heartbeatへ別Task／Runストアを新設しない。
- **Terminal boundary:** Workerだけでなく同じ処理内のAtlas更新・通知・保存失敗も終了結果に反映する。成功、失敗、取消はownerの終端APIへ記録する。取消後の終端保存は時間を限定したcleanup contextで行い、保存失敗を成功に変換しない。
- **Scope:** 三OS共通のGo APIを使用する。新しいLLM処理や直接backend経路を作らず、既存のShiroとmodule routeを維持する。HTTPのユーザー操作をAgent実行に偽装しない。
- **Acceptance:** 永続Task／Run発行前のWorker実行禁止、ownerエラー伝播、同一context、終端状態、既存Heartbeat回帰を検証する。再起動時の中断処理回復、稼働実Actor／Viewer trace、最終Fullは別の必須終端証拠であり、ソース試験だけでは完了としない。

#### Task / Run restart ownership prerequisite

- **Failure / Problem:** Task JSONLはinstance内mutexしか持たず、runtimeとTask CLIが同じ保存先へ別々のManagerを開ける。RUNNINGやAssigneeだけでは前processの中断実行であることを証明できない。
- **Invariant:** restart recoveryは、同じTask／Runの実行owner、排他権、旧generationの失効を正本Evidenceで確認してからowner APIで行う。Task status、title、Actor名、PIDだけを根拠に他の実行を終了・再開しない。過去recordにownerの証明がなければ新しいidentityを捏造せず未確認として残す。
- **Required implementation:** Task保存ownerのprocess間writer排他、実行generation／lease束縛、Close／crash時の解放、CLI書込との排他、Viewer読取、三OS共通の失敗契約を一体として実装・検証する。その後にTaskManagerの同じTaskと新Runによる再開を接続する。別のTaskストアや独立recovery正本を追加しない。
- **Status:** writer排他とRunへの数値generation保存は下記の通り実装済み。実行境界への接続、旧実行の停止証明、restart回復は未完了であり、Heartbeatの通常実行試験成功を再起動保証として扱わない。

#### Canonical Task JSONL writer lease

- **Enforcement:** Task JSONL writerは保存rootの永続`.writer.lock`をOS advisory lockで排他的に保持する。Linux／macOSはFlock、WindowsはLockFileExを使用し、競合は`ErrTaskWriterBusy`で即時拒否する。lock fileの存在やPIDを生死判定に使わず、lock fileを削除しない。Closeとprocess終了でOS排他権を解放する。
- **Read boundary:** 読取専用handleは同じ正本fileを読み、writer権を取得しない。更新は拒否する。CLIのlist／show／notificationsはこの読取経路を使用する。CLIの変更操作はwriter権が必要であり、稼働writerと競合した場合に別pathや別storeへ迂回しない。
- **Lifecycle:** TaskManagerのCloseは所有storeを閉じ、runtimeは利用側停止後にTaskManagerを閉じる。Close後のwriteは禁止する。再起動試験は旧writerのCloseまたは実process終了を挟む。
- **Tests / Boundary:** 同時writer拒否、読取専用とClose後の更新拒否、Close後の再取得、子process強制終了後の再取得を検査する。OS lockは協調する新実装のwriter排他であり、旧binaryや独立した手動file編集まで停止させる証拠ではない。Task／Runのexecutor generation束縛、過去record移行、回復policy、配備後の実Actor検証は引き続き必須である。

#### Run writer-generation binding

- **Contract:** `writer_generation`はTask store内の単調増加する数値fenceであり、GenerationIDでもAgent identityでもない。OS writer排他の取得後に保存counterを追記で進め、同期書込みが成功してからwriterを公開する。末尾はbounded readで検査し、途中書込み・不連続値を拒否する。保存済みRunの最大世代以下をwriterとして公開しない。counter不正・overflowはfail closedとし、0へ戻さない。
- **Run ownership:** TaskManagerは現在writerのgenerationを取得して新Runへ束縛する。保存・返却Runは同じ値を持ち、後から変更しない。新Runのgenerationは現在writerと一致しなければ保存しない。既存Runの0は歴史的な所有権未確認を表し、現在generationを後付けしない。
- **Recovery boundary:** 新generationの取得は旧writerの排他権が失われた証拠であり、旧Actorの外部効果が全て停止した証拠ではない。external effectに至る実行境界でのfence検査・quiescence、checkpoint／再開policy、過去Runの扱いが確定するまで自動回復を完了扱いにしない。

#### Task owner execution admission

- **Problem / Cause:** 正規形式のTaskID／RunIDだけでは、そのRunが現在writerの下で同じAgentにより実行可能かを証明できない。保存済みRUNNINGも再起動を越えて残り得る。
- **Invariant / Enforcement:** TaskManagerの`ValidateRunExecution`を照合の唯一のownerとする。Task／Runの正規性、同一Task、両方のRUNNING、未完了Run、呼出Actorと両方のAssigneeの厳密一致、唯一のactive Run、現在の非0 writer generationとの一致を要求する。読取失敗、writer終了、取消、歴史的generation 0は許可へ変換しない。照合は状態を書き換えず、新Task／RunやActorを発行しない。
- **Tests:** 同一ownerの有効Runを許可し、Actor／Task／Run不一致、終端Run、閉じたwriter、新writerから見た古いRUNNINGを拒否する。明示的な再開で作られた新Runだけが現世代に一致することを検査する。
- **Boundary:** これは検査時点のadmissionであり、検査後の状態遷移と外部効果を原子的に固定しない。Security設定に依存しない正規ツール実行境界への接続、実行中処理の停止／取消、外部ownerでの冪等性、配備後の実Actor検証は別の必須境界として残す。未接続の検査を実行経路の保護済み証拠にしない。

#### Runtime Task owner initialization order

- **Problem:** Task ownerをViewer登録の副作用として生成すると、それより先に構築されるTool runtimeへ同じownerを必須依存として渡せない。
- **Invariant:** runtime起動時にTask storeとTaskManagerを一度だけ生成し、その後にTool、Agent、Viewerを構築する。空workspace、writer競合、重複初期化はerrorとし、別保存先へ迂回しない。Viewerは同じstoreを参照するだけでwriterを生成しない。終了時のCloseは既存TaskManager経路に一意に委ねる。
- **Acceptance:** 初期化成功後にViewerが同じownerの保存結果を読むこと、再初期化がownerを置換しないこと、writer競合時に部分的なownerを公開しないことを検査する。これは実行admission接続の前提であり、接続前に外部効果が保護されたとは扱わない。

#### Mandatory runtime tool admission

- **Invariant:** productionのChat／Worker RunnerV2はSecurity設定にかかわらずTask owner admissionを通す。検証済みAgent scopeと既存execution Identityを要求し、現在writerの同一Task／Run／Assigneeだけを実行へ渡す。owner、scope、identityの欠落を補完・推定しない。
- **Placement:** CompositeとPolicyRunnerの構築後、Harness／Budget／Subagentへの接続前に必須admissionを配置する。Registry fallbackを含むinner実行より前に拒否する。raw ToolRunnerは登録・設定専用であり、production実行consumerへ渡さない。ListToolsは実行ではないためidentityなしで参照可能とする。
- **Acceptance:** 有効Runだけがinnerを一度呼ぶこと、不一致・取消・終端・旧世代・owner未接続ではinnerが呼ばれないこと、Security無効でも必須となることを検査する。これは実行開始時の照合であり、開始後のquiescence、外部効果の冪等性、配備実Actorの成功とは区別する。

#### Orchestrator Agent scope handoff

- **Invariant:** Message／Distributed dispatcherは既存`actualCoreActorForRoute`をAgent選択の正本として、同じ入口でTask／Run／TraceとAgent scopeを束縛する。保存Assigneeとの一致は必須runtime admissionが検査し、不一致時に保存内容やActorを修正して通さない。
- **Request correlation:** 有効な親scopeがある場合はRequestIDを保持する。親scopeがない内部Agent入口では独立したRequestIDを発行し、TaskIDをRequestIDへ流用しない。
- **Permissions:** 既存handoff helperで親scopeを検証し、public／user範囲と認証済みuserだけを引き継ぐ。親scopeがない場合の既定はpublicのみ。internal範囲は自動継承せず、既存OPSの明示grantだけを維持する。不正な親scopeは拒否し、正規化で救済しない。
- **Acceptance:** routeとAgentの対応、既存execution identityの不変性、親RequestID保持、親なしの独立RequestID、権限の非拡大、両dispatcherへの接続を検査する。実Agent利用・配備後の全route成功は別の未確認境界として扱う。

#### Distributed response correlation

- **Problem:** TaskIDだけの応答照合では、別の送信先・受信先・Sessionの応答を同じTaskの成功として受け入れ得る。
- **Invariant:** SSH／local mailbox共通の応答検査で、正規Messageと同一TaskIDに加え、応答From＝要求To、応答To＝要求From、同一SessionIDを要求する。照合前に応答をCentralMemoryへ記録せず、違反はerrorとして呼出側へ返す。
- **Boundary:** From／Toはtransport addressの相関であり、認証済みActorの証明ではない。Run／Requestの転送、受信側の認証・policy・正本owner照合、再試行／再起動を越える応答の識別は別途必要であり、SessionをRunの代用にしない。
- **Tests:** 正しい応答、送信元・宛先・Session不一致を共通経路で検証し、不一致応答が成功や受信記憶へ投影されないことを確認する。

#### Worker proposal admission

- **Problem:** Worker提案はToolRunnerを通らずpatch／commit／commandを実行できるため、共通入口で同じTask owner照合を必要とする。
- **Invariant / Enforcement:** `TaskManager.ValidateExecutionContext`がexecution IdentityとAgent scopeを検証し、既存`ValidateRunExecution`へ委ねる。ToolRunnerとWorker proposalはこの同一ownerを使う。Proposalはparse、log、commit、commandの前にownerとargument TaskIDの厳密一致を要求する。workspace overrideもownerを保持する。owner未接続を独立storeや架空Runで補完しない。
- **Tests:** 有効な保存Runの実行と、owner／context欠落、Task／Actor不一致、終端／閉じたwriterの副作用前拒否を検証する。既存patch試験も実Task ownerを使用する。
- **Boundary:** 受信側の認証済みhandoff、未接続呼出元、observation経路、実行中quiescence、配備後の実Actor検証は残件であり、入口guard成功だけでは運用完了としない。

#### Worker result projection

- **Problem / Cause:** command失敗件数だけで応答Successを再計算すると、patch後の検証失敗やblockedが成功へ変わる。localと別process受信側の手書き変換も失敗情報を欠落させる。
- **Invariant / Enforcement:** CORE実行サービスのPatchExecutionResultを正本とし、transport DTOの共通変換でSuccess、失敗理由、retry情報、TestStatus／TestReceiptを保持する。受信側は成功を再判定しない。TestReceiptはownerの参照であり、受信hostのfilesystemとして解釈・openしない。
- **Acceptance:** FailedCmdsが0でもSuccess=falseを保持し、両受信経路とJSON round-tripで確認する。成功、command失敗、検証失敗、blockedを区別する。配備／実Actor検証は別途必要。

#### Distributed Worker failure terminal propagation

- **Problem / Cause:** Success=falseで再試行しないWorker結果がnil errorへ変わると、自然言語のcontract検証を通過しただけでTask／Runが成功する。
- **Invariant:** proposal実行結果の欠落と、再試行不要またはretry budget終了後の失敗はtyped terminal errorへ変換する。既存の許可されたbounded retryを先に評価する。retry判定はcoordinatorの設定済み上限を引数で受け、別の固定上限を持たない。失敗理由は保存・報告し、成功完了の発話を発行しない。外側Autonomous executorはtyped errorを文字列から再分類してretryを復活させない。Task lifecycleの既存error経路で保存Task／Runをfailedにする。
- **Acceptance:** 成功に見える本文でもowner失敗はfailed、成功は成功、許可済みretry後の成功は維持、非retryable結果の再実行は0、保存Task／Runの失敗を確認する。これはsource上の境界証明であり実Actor配備E2Eを代替しない。

#### Local Worker execution context delivery

- **Invariant:** CORE内のlocal配送は同じbounded FIFO inbound queueへMessageと元execution contextを一緒に保存する。local専用APIとし、SSH／wire Messageへcontextをserializeしない。応答待ちtimeoutはenqueue前に確定し、要求実行と応答待ちを同じ取消期限へ束縛する。
- **Boundary:** Worker受信時は正規Message、宛先、TaskManagerのcontext admission、実Shiro scope、TaskIDと任意TurnInput TraceID一致を確認する。contextなしや取消済みの配送をBackgroundで救済せずerror応答にする。実行には受け取った同じcontextを渡す。message-only APIは応答と既存非実行consumer用でありWorker実行を許可しない。
- **Acceptance:** 同一queueの順序と容量、contextの保持、enqueue前／後の取消、閉じたtransportの受信拒否、実Worker入口の拒否と正規ownerでの通過、sender timeoutの受信側伝播を検証する。transport Close後の実行中quiescence、remote認証handoff、実Actor配備E2Eは別途必要。

#### Local Coder request ownership

- **Invariant:** local Coderも同じLocalDelivery contextで提案を生成する。Coder自身をActorへ昇格させず、Task／Run／scopeはCORE所有Agentの値を保持する。Workerと共通のlocal admissionでMessage、宛先、owner、Task、任意Traceを検査し、欠落／取消／不一致はLLM呼出し前に拒否する。Worker固有のShiro制約はWorkerだけで維持する。
- **Acceptance:** 有効要求のcontextがCoder adapterを通してproviderへ届き、無効要求ではproviderを呼ばない。実行中取消を伝播し、応答前にもownerを再照合し、取消やTask状態変更後の遅延結果を成功応答にしない。未認証wireからscopeを再構築しない。LLM出力は既存proposal parser/self-checkを通る。source試験と実LLM／Actor配備試験は区別する。

#### Coder transport return address

- **Failure / Cause:** local CoderだけがShiro宛返信をMioへ書き換えると、request.Fromとの厳密照合が失敗する。transport宛先と実Actorを混同した特殊routeが原因。
- **Invariant / Enforcement:** coordinatorがMio mailboxで応答を待つCoder要求はFrom=mioとする。local／remoteの成功・error返信はrequest.FromをそのままToへ返す。ShiroのTask／Run／認証scopeは元contextに保持し、mailbox名からActorを再構築しない。localだけの宛先書換えhelperを削除し、既存response correlationを維持する。
- **Tests / Acceptance:** coordinatorのFromとreceiveOn一致、Task／TurnInput保持、正逆宛先の厳密照合、実local queueとCoder handlerの成功／error返信を確認する。実LLM／Actor配備とshared mailboxの並行要求分離は別の未確認境界とする。

#### Worker observation execution ownership

- **Failure / Cause:** proposal入口だけにTask owner検査を置くと、CoderLoopのobservationから同じWorkerの外部操作を未束縛contextで呼べる。
- **Invariant / Enforcement:** ExecuteObservationは同一TaskManagerのValidateExecutionContextで開始前に検査し、各actionへ同じcontextを渡す。nil owner／context、Task／Run／scope不一致、取消、終端Task、閉じたwriterを拒否する。action応答後もownerを検査し、失効後の結果をerrorへ変えて後続actionを実行しない。新しいTask／Runやscopeを観測入力から発行しない。
- **Tests / Boundary:** MCP呼出し数と保存ownerを使い、入口拒否、context保持、実行中取消／Task終端後の結果拒否を検証する。観測commandの安全policy、canonical test selection、実行中quiescence、実Actor配備はこのidentity admissionとは別の保証であり、残件として維持する。

#### CoderLoop terminal result

- **Failure / Cause:** CoderLoopが連続失敗やturn上限をPartial結果として返しても、Executeがnil errorのhandled応答へ変換すると、外側が正常終了と誤認する。
- **Invariant / Enforcement:** loop内部の許可されたbounded repairは維持する。外部へ返す時点のPartial／結果欠落はtyped terminal errorとし、Autonomous executorが本文から再試行可能エラーへ再分類しない。診断結果を保持し、Task lifecycle既存error経路でTask／Runをfailedにする。
- **Acceptance:** 検証失敗、連続parse失敗、turn上限は成功にしない。修正後の検証成功は成功を維持する。保存Task／Runのfailedと外側の追加実行なしを確認する。実Actor配備証明は別途必要。

#### MCP observation canonical runner

- **Failure / Cause:** Worker観測にSerena clientを直接注入すると、Task入口検査があってもruntime Tool policy／harness／auditの正規routeを迂回する。
- **Invariant / Enforcement:** 観測adapterは起動時MCP catalogの厳密なRemoteNameからToolIDを参照し、同じWorkerRuntimeRunnerV2へ元contextとargsを渡す。catalogにない名前やrunner欠落を拒否する。remote clientはcatalog背後のToolRunnerだけが呼ぶ。ToolResponse.Error、transport error、nil responseを成功文字列へ変換しない。
- **Tests / Boundary:** catalogの衝突解消済みID、context、runner拒否時のremote未実行、実runtime runner経由の応答を確認する。Tool metadataによるread-only保証とshell観測policy、実Actor配備検証は別の残件とする。

#### MCP observation read-only classification

- **Failure / Cause:** tools/listに存在することだけでCategory=queryへ分類すると、更新toolも読み取り専用観測として実行できる。
- **Invariant / Enforcement:** COREのSerena adapter metadataが、対応する既知の照会操作の厳密なremote名だけをqueryへ分類する。未知名、正規化による類似名、他namespaceはmutationを既定とする。tool名からのLLM推定やremoteの自己申告で昇格しない。観測adapterは正規runnerのmetadataに同じToolIDが一意に存在してCategory=queryの場合だけ呼び出す。一覧取得失敗・metadata欠落／重複はfail closedとする。
- **Boundary / Tests:** 既知照会の成功と未知／更新／曖昧metadataの副作用前拒否を検証する。既知操作の意味はCORE adapterが管理し、対応serverの実装・version変更時には再検証する。引数のデータ範囲policy、shell操作、実Actor配備証跡は別途必要。

#### Observation command syntax boundary

- **Failure / Cause:** 許可prefixに続くshell構文をbashへ渡すと、追加command、置換、redirect等を実行できる。
- **Invariant / Enforcement:** 観測commandは一つの実行fileとargvへ決定的に解析し、exec.CommandContextで直接実行する。shellを起動しない。単語境界でprogram／subcommandを完全一致検査する。空入力、閉じていないquote、shell制御記号、変数／command置換、redirect、非明示globを拒否する。単一／二重quoteで引数をまとめ、単一quote内部はliteralとして扱う。
- **Boundary / Tests:** harmlessな構文例で旧prefix bypassの拒否、quoted argv、native実行を検証する。このsyntax保証は許可programの全optionが読み取り専用である保証ではない。find等の副作用option、git外部driver、引数データ範囲、canonical test selection、timeoutは別途閉じる。

#### Find observation predicates

- **Failure / Cause:** shellを除去してもfind自身の-exec／-delete等は外部効果を起こせる。program名の許可はoptionの許可ではない。
- **Invariant / Enforcement:** find観測は読み取りpredicateのpositive grammarで検証する。実行、削除、prompt起動、output file、未知predicateを拒否し、引数を取るpredicateの次tokenは値として扱う。値欠落を拒否し、predicate開始後の余分なpath／tokenを許可しない。任意optionをdenylistへ追加して通す方式にはしない。
- **Tests / Boundary:** 副作用predicateと未知optionの拒否、値位置にある同名文字列の許容、欠落値と正規の検索式を検証する。path範囲、OSごとのutility解決、実行時間／output bound、他program固有optionは別途必要。

#### Git observation option boundary

- **Failure / Cause:** Gitの照会subcommandにもoutput file、external diff／textconv、signature検証等の外部実行optionがある。repository configもpager／fsmonitor等を起動し得る。
- **Invariant / Enforcement:** 観測Gitはsubcommandごとの明示的な照会optionのみ許可する。未知option、output指定、外部実行要求、custom formatを拒否する。--以後はpath引数として保持する。実行argvにはno-pager、no-optional-locks、core.fsmonitor=false、core.untrackedCache=false、log.showSignature=false、format.pretty=mediumを付け、diff形式を扱うsubcommandではno-ext-diff／no-textconv、grepではno-textconvを強制する。
- **Tests / Boundary:** 拒否option、option値位置、path区切り、正規照会、環境にexternal diffがあってもbuiltin diffを使うことを検証する。repository／データ範囲、resource上限、他command、canonical test selection、実Actor配備は別途必要。

#### Observation canonical verification

- **Failure / Cause:** CoderLoopのtest_requestからgo test／build／vetを直接起動するとCanonical Test Plan、impact fallback、隔離、receipt照合を迂回する。
- **Invariant / Enforcement:** これらの観測commandはWorkerの既存runTestImpactへ変換する。元Task identityを使い、現在worktreeからResolverが選択する。command引数は範囲を狭める権限を持たないhintであり、直接go実行へ渡さない。正規planのないworkspace、owner失敗、source不一致を成功にしない。
- **Receipt / Coalescing:** 結果は既存ObservationActionResultへTestStatus／TestReceiptを任意fieldとして付ける。連続した検証hintを一つの検証要求へまとめ、各要求に同じreceiptを返す。他のactionを挟んだ場合や別batchでは再利用しない。LLMの「tests passed」をowner証跡として使わない。
- **Tests / Boundary:** 既存Test Impact helperと保存Task ownerでpassed／failed／source不一致／canonical欠落、重複実行なしを確認する。実配備のResolverと全Canonical Step、実Actor利用は別途確認する。

#### Observation action deadline and process ownership

- **Failure / Cause:** 元requestに期限がない場合、観測command／MCPも無期限になる。deadline後の遅延成功や、子processがpipeを保持する場合もある。
- **Invariant / Enforcement:** 非テスト観測は既存Worker CommandTimeoutの正の既定値を共有する。元Task／Run／scopeを保持した派生contextで実行し、より短い親期限を尊重する。action期限切れは遅延成功を拒否し、後続actionを止める。正規検証はTestImpact専用timeoutを維持する。
- **Process / Output:** TestImpactと観測commandは同じWorker process起動・interrupt・猶予・tree killを使う。nil contextをBackgroundへ救済しない。stdout／stderrは各64KiBを上限に保持して超過を明記し、観測の既存2KiB投影は維持する。
- **Boundary / Tests:** deadlineの保持、時間切れ後の結果拒否、後続未実行、既存process-tree試験、診断buffer上限を確認する。MCP backendのctx協調と実機quiescence、D-state等のOS停止不能状態、配備後receiptは別途確認する。

#### Canonical workspace physical path guard

- **Failure / Cause:** lexical相対pathだけの判定は、workspace内symlinkから外部fileへのアクセスを許す。protected fileへのaliasもbasename検査を迂回する。
- **Invariant / Enforcement:** Security SandboxGuardでworkspaceと対象の実pathを比較する。未作成fileは最も近い既存ancestorを解決してから残りを結合する。dangling link、解決不能、明示的な親遡及componentは拒否する。単に..で始まる通常名は親遡及と区別する。sandbox保護名は入力名と解決先の両方へ適用する。
- **Boundary / Tests:** 外部link、未作成leaf、dangling link、内部link、保護file alias、通常の..prefix名を検証する。これは事前判定であり、openまでの競合を防ぐdescriptor-based I/Oではない。観測path抽出・接続、TOCTOU対策、実Actor配備と他OS実行は別途必要。


### Observation find workspace roots

- Failure / Problem: read-only find predicates still accepted absolute outside or symlink-escaped starting points, exposing paths outside the configured Worker workspace.
- Cause: the observation grammar constrained actions but did not bind its filesystem inputs to the owner workspace.
- Lesson / Invariant: the same find grammar must identify starting points separately from literal predicate values. Missing starting points mean the configured workspace. Every explicit starting point must pass the canonical physical SandboxGuard before starting any process; parent traversal and unresolved workspace fail closed. Pattern values are not filesystem roots.
- Enforcement: CORE Worker observation uses its configured workspace and the infrastructure security guard; no model-selected allowlist or alternate policy is introduced. CLI parses arguments; Boundary admits or rejects paths; no runtime LLM decision is required.
- Tests: outside and mixed roots, parent traversal, external symlink, default and internal roots, and absolute-looking literal patterns.
- Remaining: this is preflight, not descriptor-bound process filesystem isolation. Path replacement races, utility identity across OSes, Git and content-reading command/MCP data scopes, final Full and actual Actor/Viewer evidence remain open.


### Observation file-reading operands

- Failure / Problem: cat/head/tail/wc/grep could read arbitrary process-visible files, including grep pattern files, despite read-only command admission.
- Cause: program names were allowed without parsing operand roles or binding paths to the owning Worker's workspace.
- Lesson / Invariant: one positive argument grammar identifies file operands, pattern files, literal patterns and numeric options. Every input file must be a regular file admitted by the canonical physical workspace guard before any process starts. No inherited stdin, implicit input, follow mode, recursive traversal or indirect file-list input is admitted by this process route.
- Enforcement: CORE parses standard bounded read options, including combined short flags and attached values. Unknown flags fail closed. grep -e is literal text; -f is a file subject to the same path check. Relative file names are relative to the configured workspace; parent components remain visible to the guard. CLI performs parsing; Boundary validates files; no runtime LLM authorization is introduced.
- Tests: outside files for all five tools; mixed files; outside grep pattern files; symlink escapes; valid reads and literal patterns; unsupported implicit/recursive/indirect inputs.
- Remaining: regular-file and workspace checks are preflight. Descriptor-bound I/O, races, in-workspace confidential-data policy, OS-equivalent utility identity, MCP and Git data scopes, final-source Full and live Actor/Viewer proof remain open. Recursive content search must acquire a confined implementation before admission is broadened.


### Observation Git repository binding

- Failure / Problem: inherited Git environment could redirect observations to another repository, and a workspace subdirectory implicitly exposed its parent repository.
- Cause: command cwd was treated as proof of Git repository identity.
- Lesson / Invariant: Git observation requires a repository marker at the configured workspace and Git's effective top-level directory must identify that same physical directory. Missing, nested, redirected or unresolved workspace is rejected before the requested observation.
- Enforcement: the shared Worker command runner merges the process environment while removing Git-specific overrides for Git invocations. Global/system Git config is disabled for these read-only calls and terminal prompting is disabled. Local repository config remains in effect; an effective worktree outside the configured workspace is rejected. Canonical test-impact invocations preserve their existing environment and ownership route. CLI resolves repository identity; CORE Boundary compares directory identity; no runtime LLM authorization is added.
- Tests: inherited GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE and injected config, configured child directory, repository-local core.worktree redirect, and normal Git observations.
- Remaining: Git operand/pathspec and object scope, repository metadata trust and races, OS process identity, final Full, installed artifact and actual Actor/Viewer evidence remain separate obligations.


### Git diff operand boundary

- Failure / Problem: Git diff can implicitly compare filesystem paths outside the repository without an explicit --no-index option.
- Cause: repository identity and an option allowlist do not constrain positional operands.
- Invariant / Enforcement: the existing Git argument grammar returns normalized execution arguments and diff operand roles together. Operands after -- are paths. Before --, an operand may be a repository object/revision expression or a file path; the configured repository resolves revision expressions deterministically, while file operands pass the same canonical workspace path guard before diff runs. Option values are not classified as file paths. CLI parses/resolves; CORE Boundary admits file scope; no runtime LLM decision is involved.
- Tests: identical outside files still fail admission both with and without --; in-repository revisions and paths remain usable; option text retains its role.
- Remaining: object confidentiality and repository metadata trust, path replacement races, other Git subcommand data scopes, MCP, OS runtime proof, final Full and actual Actor/Viewer receipts remain open.


### MCP transport response ownership

- Failure / Problem: per-call goroutines concurrently scanned one Serena stdout stream and discarded responses for other request IDs. A canceled call left a scanner reader alive, able to consume later calls' responses. Pre-canceled calls still sent tool requests.
- Cause: receive ownership followed the waiting call instead of the transport generation; concurrent writes also lacked framing serialization.
- Invariant / Enforcement: each scanner generation has at most one reader and a pending request-ID map. Register before sending; serialize writes; dispatch only to the matching pending request; discard late canceled IDs without removing another request. Cancellation is checked before send and before accepting a response. EOF fails all pending requests. Stop closes the transport and resolves waiters; new startup owns a new receiver.
- Classification: CLI serializes frames and dispatches IDs; Boundary rejects missing/expired context and unmatched late output; no runtime LLM identity inference is used.
- Tests: pre-canceled no-send; canceled first request followed by second response before late first response; concurrent reversed response order; EOF and race detector.
- Remaining: transport cancellation does not prove remote execution stopped. Blocking writes, remote cancellation/quiescence, MCP schema and argument data scope, workspace/project binding, final Full and actual Actor/Viewer evidence remain open.


### MCP send context and failed-frame boundary

- Failure / Problem: a request canceled while waiting for the send mutex, or while writing to an unread subprocess pipe, could outlive its deadline indefinitely.
- Cause: mutex acquisition and pipe writes were not context-bound despite receive-side cancellation.
- Invariant / Enforcement: a context-aware serialization gate admits one complete frame writer. Cancellation before admission returns without sending or disturbing the active writer. Cancellation during a write closes that transport generation's streams and fails its pending responses; partial/failed frames are not followed by another frame on the same receiver. Unrelated process environment and transport ownership are unchanged. CLI serializes frames; Boundary binds admission and failed-frame handling to context; no LLM decision is involved.
- Tests: blocked active writer, canceled queued writer, context-driven pipe closure, retired receiver rejects subsequent calls, short-frame handling and race detector.
- Remaining: stream closure proves local transport retirement, not remote tool/process quiescence. Concurrent restart/generation ownership, remote cancellation, MCP argument/workspace scope, final Full and deployed actual Actor evidence remain open.


### MCP pending request and transport generation binding

- Failure / Problem: a request could register with an old receiver, wait for the write gate, and then write its frame to a newly installed stdin. Its response would belong to a different receiver and the old request could affect the new process.
- Cause: receive registration and send stream selection independently sampled mutable client state.
- Invariant / Enforcement: every send carries the exact receiver generation selected before registration. After acquiring the write gate, CORE compares that receiver with the current transport under the stream lock and rejects stale/retired generations before writing. Startup stream installation retires the previous receiver and closes its streams before publishing the replacement. Cancellation acts only on captured streams.
- Classification: CLI publishes one transport snapshot; Boundary binds send and receive ownership to that snapshot; no LLM-generated identity or guessed current request is used.
- Tests: replace streams between registration and send; assert no old frame reaches new stdin; old waiters fail and replacement requests remain usable; race detector.
- Remaining: source transport identity is not deployed process ownership proof. Remote process reaping/quiescence, actual restart requests, MCP schema/workspace/argument policy, Full and actual Actor/Viewer receipts remain open.


### MCP owned subprocess retirement

- Failure / Problem: Stop and failed initialization killed the owned child without waiting for process reaping; Stop ignored a child when started was false. A new startup could overwrite an unresolved process handle.
- Cause: initialized readiness was conflated with process ownership, and Kill was treated as terminal process evidence.
- Invariant / Enforcement: retain the exact exec.Cmd owned by this client through retirement. Close its transport, kill that child, and call Wait exactly once. Wait completion is observed through a retained channel with a bounded five-second stop deadline; a timeout remains unresolved and blocks replacement startup. Initialization failure uses the same retirement path. A successfully reaped direct child is distinct from descendant or remote work quiescence.
- Classification: CLI retires and reaps the owned process; Boundary prevents replacement while terminal evidence is missing; no LLM process-name inference is used.
- Tests: isolated native Go helper child, both initialized and pre-readiness ownership, repeated Stop, and race detector. Fixture results are source lifecycle evidence, not live Serena or Agent E2E.
- Remaining: natural-exit supervision, descendant/remote operation quiescence, real service/listener/config and restart proof, MCP schema/workspace/argument policy, final Full and actual Actor/Viewer evidence remain open.


### MCP known-failed startup state

- Failure / Problem: after EOF or a canceled/failed frame retired the receiver, started remained true and a later Start returned success without a usable transport.
- Cause: an initialization flag was treated as current transport evidence.
- Invariant / Enforcement: idempotent startup success requires the owned child handle, installed streams and a nonterminal receiver. Known-failed or missing transport state retires through the existing owned-process path before attempting canonical startup again. Failure to resolve or initialize that same backend is reported; no substitute backend is selected.
- Classification: CLI inspects recorded lifecycle state; Boundary rejects stale readiness and gates replacement on retirement. No runtime LLM decision is involved.
- Tests: retired receiver, missing receiver and missing process owner cannot return success under started=true; isolated empty executable path prevents external process launch in these fixtures.
- Remaining: this is recorded-state consistency, not a live ping or natural-exit monitor. Live process/readiness/restart evidence, MCP schema and data scope, final Full and actual Actor/Viewer receipts remain required.


### MCP startup catalog connection binding

- Failure / Problem: a catalog observed from one MCP connection retained only the client object; after internal reconnect the same catalog could invoke a different connection without rediscovery or Snapshot regeneration.
- Cause: client object identity was mistaken for connection identity, contrary to the canonical onboarding contract.
- Invariant / Enforcement: startup captures a nonzero connection generation and verifies it remains unchanged through tools/list. Catalog stores that observed generation. Every catalog call supplies it to the client, which checks it while selecting the receiver and then retains that exact receiver through registration/send. Missing or changed generation fails closed. Reconnection alone cannot promote old catalog evidence.
- Classification: CLI records connection generation and discovery; Boundary binds catalog execution to it; no LLM inference of current capabilities is used.
- Tests: catalog stale-generation execution reaches no remote call; startup discovery crossing a reconnect is unavailable; bound client call after stream replacement sends nothing.
- Remaining: stale capability projection must be regenerated for the new connection; schema preservation/validation, workspace and argument data policy, Full and actual Actor/Viewer evidence remain required.


### MCP observed argument definition preservation

- Failure / Problem: tools/list decoded inputSchema but projected only names; Worker metadata replaced the observed contract with an arbitrary-properties object.
- Cause: startup catalog had no place for the observed definition, splitting discovery from the model-visible argument contract.
- Invariant / Enforcement: the same connection-bound discovery returns name, description and inputSchema. The canonical catalog owns deep-copied definitions; Worker metadata references copies of that observed schema. Missing object schemas, invalid JSON definitions and conflicting duplicate names are excluded, without inventing permissive replacement schemas. Identical duplicate observations may collapse deterministically. Remote descriptions/schemas do not establish read-only classification, Actor identity or access permission.
- Classification: CLI decodes/copies JSON definitions; Boundary excludes malformed/ambiguous observations; LLM may use the exposed contract to propose arguments but cannot authorize execution.
- Tests: schema survives transport-to-catalog-to-Worker projection; mutation of input/exported schema cannot modify catalog; conflicting or absent schema excluded.
- Remaining: preservation and top-level shape checks are not full JSON Schema validation or data-scope authorization. Complete argument validation, workspace/project binding, final Full and actual Actor/Viewer receipts remain open.

### MCP input schema execution boundary

Observed `inputSchema` is compiled once inside the CORE catalog. Missing or uncompileable schemas are unavailable. Schema loading may use the observed document and bundled standard dialects only; remote URLs and filesystem references are refused. Local references remain supported. Execution validates a JSON-normalized private copy against the catalog-owned compiled schema and forwards that same copy only on success. Public metadata cannot weaken this boundary. Invalid arguments return `VALIDATION_FAILED` without remote execution or argument disclosure. JSON Schema validation does not grant filesystem scope, Actor identity, or policy authority. Final-source Full regression and actual Actor route evidence remain required.

MCP transport retirement invalidates its current connection generation. Catalog discovery, Worker metadata, model tool definitions, and execution must reject a catalog from any retired or replaced generation. Reconnection requires a new observation; old schemas are never transferred automatically. Historical startup Snapshot text is not live availability evidence; the request-time context provider regenerates it at the Agent prompt boundary.

### Request-time capability context

CORE injects an immutable context provider during runtime assembly. Agent and Coder prompt assembly invokes it with the request context and recipient. It combines copied Agent contracts, current Worker metadata, generation-checked MCP observations and owner data-route contracts; it never edits Persona or shared prompt maps during requests. Tool-list failures produce unavailable capability evidence. Retired MCP definitions must not remain available in prompt snapshots. This awareness grants no execution authority; execution still revalidates the exact generation and policy.

### Tool mediation receipt failure

A configured mediation recorder is part of the execution boundary. V1, V2 and the policy-facing harness wrapper must persist the mediation record before executing the tool. Persistence failure returns a bounded structured error and prevents the effect in every harness mode, including log-only. No-recorder configuration retains its existing explicit behavior. Canonical Event ownership, Task/Run linkage and context-aware durable persistence remain separate acceptance requirements; this gate alone does not prove that lineage.

When recording is configured, recorder initialization failure aborts runtime assembly; it cannot silently become a no-recorder configuration. Explicit disabled configuration remains distinct from initialization failure.

### Tool admission precedes mediation facts

The outermost Chat/Worker runtime Tool runner validates the current Task/Run owner and request scope before mediation, persistence, budget handling or Tool execution. Missing, stale or canceled execution identity must produce no mediation fact. Input repair still precedes the inner input policy check. This ordering reuses the Task owner admission gate; it does not mint a substitute identity or authorize a rejected request.

### Mediation request-context contract

Mediation recorder and Viewer recent-list APIs accept the original request context. Runtime must not replace it with a background context: canonical execution identity, scope, cancellation and deadline belong to the caller. A canceled request must not begin an append after admission or lock acquisition. Filesystem operations already in progress cannot be claimed canceled or rolled back. Canonical Event Store integration must consume this same context; adding it does not alone prove persisted Task/Run lineage.

### Canonical mediation Event ownership

CORE Tool Harness issues canonical EventID and appends `tool_input_mediated` under component `tool_harness` to the existing Canonical Event Store. The request execution identity supplies TaskID, RunID and TraceID; the trusted Tool scope supplies ActorKind/ActorID. Missing TraceID fails closed rather than starting a substitute trace. Bound Action/Attempt may be copied only when present. No causation edge is invented without a supplied canonical cause.

Viewer recent-list reads the same canonical component ordered by EventSeq. Its displayed lineage is projected from envelope fields, not separately writable payload aliases. There is no JSONL dual write or fallback reader. Existing historical JSONL files are preserved as offline legacy evidence: they lack sufficient owner lineage to become canonical events. Their capture/hash and deployed legacy-consumer retirement remain cutover acceptance requirements. `tool_harness.log_path` is retained only as a legacy artifact location for that cutover; it is not a live record destination.

### Tool context budget execution lineage

CORE ContextBudgetRunnerの記録付き実行は、inner Toolを呼ぶ前に、active context、既存のTaskID／RunID／非空TraceID、認証済みToolExecutionScopeを検証する。不足時はTool実行・usage保存・event追加を行わず拒否する。ContextUsageのTaskID／RunIDには同じ実行contextを投影し、警告／超過EventEnvelopeはそのTraceID、TaskID、RunID、scope由来ActorKind／ActorIDを保持する。bound Action／Attemptがあれば引き継ぐ。Agentラベルやmodel、Execution RoleをActorへ変換しない。新しいroot Traceや推測したcausationを作らず、context usage recordのIDは参照payloadのままとしcausationへ転用しない。usage保存とevent追加の複数store間atomicityおよびoffload Artifact lineageは別途検証する未完境界である。

### Task-scoped ToolLoop admission

TaskID／RunIDを持つToolLoopは、modelを呼ぶ前に、既存execution contextのTaskID／RunIDがConfigと一致し、TraceIDが正規かつ非空であることを検証する。Configから欠けたcontextを補完せず、新しいTraceも生成しない。Subagentのowner入口が選択したcontextを保持し、Action／Attemptは同じTask／Runに束縛する。Taskを持たない明示的なunit simulationは従来の非Task経路を使えるが、production Task ownerの代用にはしない。

### ToolLoop logical operation identity

- Failure / Problem: Tool名だけをkeyとしてActionを再利用すると、別条件の検索や別の書込みが同じActionのRetryになり、成功した前回Attemptまで失敗へ書き換わる。
- Invariant / Enforcement: ToolLoopが受け入れた各Tool呼出しは一つの論理操作として新しいActionとfirst AttemptをAction ownerへ要求する。Tool名、引数の一致、ProviderToolCallIDから既存ActionのRetryを推定しない。明示的に同じ論理操作を再試行するowner経路は既存StartAttemptを使い、同じActionIDと新しいAttemptIDを維持する。ToolLoopの同一失敗入力抑止は引き続き実行前に適用する。
- Tests: 同名Toolの異なる呼出しは異なるActionへ束縛され、Task／Runは維持される。Action ownerの明示Retry試験は引き続き同じActionと別Attemptを要求する。Attempt終端記録と実Actor経路の証明は別途必要。

### Runtime Action owner sharing

- Failure / Problem / Cause: ToolとViewerが同じAction保存先へ別々のManager／Storeを生成すると、同一責務を複数instanceが所有し、Storeのinstance内lockを共有できない。
- Lesson / Invariant / Enforcement: runtime assemblyはTool側のActionManagerをViewerの外部操作へ引き渡す。Tool側で未生成の場合だけ、Viewer consumerの有効化に応じて一度生成する。保存先の組立ては既存のnewRuntimeActionManagerに集約し、consumerが別Storeを開き直さない。
- Tests: PolicyRunnerが作ったAction／Attemptを返却された共有Managerから読めることを検証する。これはprocess内のowner共有を保証する単位であり、複数processのwriter fencing、複数recordのatomicity、Attemptの正確な終端化、配備後の実Actor証跡は別途必要。

### Exact Attempt completion

- Failure / Problem / Cause: ActionIDだけでRunning Attemptを検索すると、古い実行の完了が新しいRetryを終端化できる。さらにAction状態の検証前にAttemptを保存すると、不正入力でも一部の状態が変わる。
- Invariant / Enforcement: 完了入力はActionIDと実行したAttemptIDを必須とする。Actionがopenで、そのCurrentAttemptIDと入力AttemptIDが一致し、同じActionに属するactive Attemptであることをownerが検証する。AttemptとActionの遷移候補をともに検証してから保存し、stale／foreign／重複完了を拒否する。共有Manager内の作成・再試行・完了と読取りは同じ排他境界を使い、検査と更新の間に別の遷移を割り込ませない。
- Tests / Boundary: stale Retry結果、別ActionのAttempt、不正状態、重複完了では保存内容が変わらず、正しい組は終端化できることを検証する。process内排他は複数JSONL recordのdurabilityや複数process fencingを保証せず、保存エラーは呼出し側へ返す。実際のTool完了からの接続、障害復旧、配備後の実Actor経路は別途必要。

### Tool outcome terminal recording

ToolLoopは各Tool実行後、次のmodel呼出しの前に、束縛済みのAction／Attemptへ実結果を記録する。結果分類はAction ownerのCompleteToolAttemptが所有する。非nilの成功responseかつerrorなしなら成功を記録し、同時期のcontext取消だけで観測済み成功を取消へ変えない。失敗は返されたtyped error、responseのtimeout code、context状態の順でtimeout／cancel／その他failureへ分類する。明示的な返却原因を周辺contextより優先する。nil responseもfailureである。

取消後も開始済み操作の終端事実を保存するため、元のcontext valuesを保持し取消だけを切り離した5秒上限のcontextを使用する。このcontextは終端保存専用で、Tool再実行や新操作を認可しない。保存するsummaryは固定の結果分類とし、Tool出力本文を複製しない。終端保存に失敗した場合、ToolLoopは元の実行エラーと保存エラーを保持して停止し、後続modelを呼ばない。複数recordの障害原子性、PolicyRunner自身が発行したActionへの接続、実Actor経路は別途検証する。

### Governed Tool completion ownership

PolicyRunnerは自分が発行したAction／Attemptを同じCompleteToolAttemptで終端化する。発行した組は既存のexecution contextへ束縛してinner Toolへ渡し、Task／Run／Traceと認証scopeを保持する。上流が束縛したAttemptは上流が終端化し、PolicyRunnerは二重完了しない。policy拒否、実行失敗、監査記録失敗、終端保存失敗を成功扱いにしない。実行ServiceはGo errorの原因を保持し、記録失敗が同時に起きた場合は両方を返す。responseが得られていればerrorとともに保持し、timeout等の構造化原因を失わない。nil responseかつerrorなしは失敗とし、成功の監査記録を作らない。記録更新に失敗した場合、更新成功を装ったRecordを返さない。これらは通常のpolicy経路内で行い、効果の再実行や認証迂回を追加しない。

### Bound Tool Attempt admission

contextに形式上正しいIDが入っていることだけでは、実行を認可しない。PolicyRunnerは渡されたAction／AttemptをAction ownerへ照会し、保存済みの組が存在し、Actionがopen、AttemptがactiveかつCurrentAttemptIDと一致し、同じTask／Run、KindTool、対象Tool名に属することを監査追加・Tool実行より前に確認する。検査は状態を変更せず、不一致や保存先の障害は拒否として返す。現在Attemptの整合性判定は完了APIと同じowner実装を共有する。この検査は実行開始時点のadmissionであり、効果実行中の別process更新や障害復旧を保証するものではない。

### Current Tool metadata admission

- Failure / Problem / Cause: PolicyRunnerがTool一覧を独立した可変cacheとして保持すると、削除済みToolを受理し続け、動的登録の照会で並行書込みが競合する。
- Lesson / Invariant / Enforcement: Tool一覧の正本はinner Runnerとする。PolicyRunnerは起動時の一覧検証を維持し、実行ごとに同じrequest contextで現在の一覧を照会する。対象Toolが存在しない場合や照会が失敗した場合はAction作成・監査追加・Tool実行より前に拒否し、照会errorの原因を保持する。別cacheやregistryを追加しない。
- Tests / Boundary: 動的登録、登録削除、一覧取得障害、未知Toolの並行照会を検証する。一覧照会はadmission時点の観測であり、inner Runnerの実行時のgeneration検査や認証・policy判定を置換しない。
