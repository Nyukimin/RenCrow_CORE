# RenCrow 全体ID統一 正本地図

**Document ID:** `RC-IDENTITY-001`

**Version:** `2.0`

**Date:** `2026-08-27`

**Status:** `CANONICAL / IMPLEMENTATION READY`

**Owner:** `RenCrow CORE`

**Supersedes:** `RenCrow_IDENTITY_CANONICAL_MAP_v1.md`

**Target repository path:** `docs/architecture/identity/IDENTITY_CANONICAL.md`

**Implementation baseline:** `Nyukimin/RenCrow_CORE main@aeeaec86c7a519ebb68c38293a223701e71863fd`

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

Migrationで既存Recordへ新IDを付与する場合は、Field pathを含むUUIDv5で決定的に生成する。

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

---

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
