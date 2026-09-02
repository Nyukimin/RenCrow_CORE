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

---

### Step 06: TurnIDとMessageID

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
  同じbounded contextで並行取得し、登録順に結果とlimitationを統合して決定性を保つ。
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
