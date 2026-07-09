# record_storage_schema.md

## 目的

この文書は、Mio の「原本保存」系の保存スキーマを定義する。

ここで扱うのは、記憶ではなく記録である。
記録の役割は、想起のために圧縮することではなく、会話・報告・作業の一次情報を失わず残すことにある。

---

## 1. 共通原則

### 1.1 append-only を基本とする

原本保存は、まず積み上げる場所である。
原文や原結果を上書きで整形しない。

### 1.2 EventId で束ねる

会話、委譲、作業、Hook、検証、成果物は EventId を共通キーとして束ねる。

### 1.3 原文と要約を混ぜない

記録は原文または原結果を保持する。
要約は別フィールドまたは別レコードに置く。

### 1.4 記憶化は後段に分離する

原本保存は、そのまま記憶ではない。
Worker が候補を抽出し、Chat が必要部分だけを記憶へ昇格させる。

---

## 2. 記録レイヤ

原本保存では、少なくとも次の5系統を分ける。

1. Conversation Record
2. Task Record
3. Event Record
4. Audit Record
5. Artifact Record

---

## 3. 共通レコードスキーマ

すべての記録レコードは、次の共通フィールドを持つ。

```json
{
  "record_id": "REC-20260412-000311",
  "record_type": "conversation|task|event|audit|artifact",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000118",
  "parent_record_id": null,
  "actor": "user|chat|worker|coder|hook|system",
  "channel": "web|voice|cli|mobile|internal",
  "created_at": "2026-04-12T16:30:00+09:00",
  "status": "active|closed|failed|archived",
  "summary": "原本理解を助ける短い説明"
}
```

### 補足

`record_id`
記録レイヤ内の一意キー。

`root_event_id`
大元の依頼単位。

`event_id`
そのレコードが属する処理単位。

`summary`
検索補助のための短い説明。本文の代替ではない。

---

## 4. Conversation Record スキーマ

### 目的

会話本文、中間報告、完了報告など、人間が読む対話記録を残す。

### 形式

```json
{
  "record_id": "REC-20260412-CONV-0032",
  "record_type": "conversation",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000100",
  "actor": "user|chat",
  "channel": "web",
  "turn_index": 32,
  "message_kind": "user_input|assistant_response|progress_update|completion_report",
  "content_text": "会話本文",
  "attachments": [],
  "created_at": "2026-04-12T16:30:00+09:00",
  "status": "active",
  "summary": "memory に関する構想の相談"
}
```

### 保存対象

- ユーザー入力
- Chat の返答
- 中間報告
- 完了報告

### 保存しないもの

- 内部思考の逐語記録
- モデルの hidden reasoning

---

## 5. Task Record スキーマ

### 目的

委譲や作業単位の入出力を、構造化して残す。

### 形式

```json
{
  "record_id": "REC-20260412-TASK-0018",
  "record_type": "task",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000121",
  "actor": "chat|worker|coder",
  "channel": "internal",
  "task_direction": "request|result",
  "target_actor": "worker|coder|chat",
  "command": "/investigate|/plan|/implement|/verify|/maintain",
  "payload": {
    "objective": "達成目標",
    "scope": "対象範囲",
    "constraints": ["非破壊優先"]
  },
  "created_at": "2026-04-12T16:32:00+09:00",
  "status": "closed",
  "summary": "Coder へ verify を委譲"
}
```

### 補足

- request と result を分けて残す
- payload は当時の形で残す
- 後から schema 変更しても原本は変えない

---

## 6. Event Record スキーマ

### 目的

状態遷移や処理節目を追跡するための基準記録。

### 形式

```json
{
  "record_id": "REC-20260412-EVT-0044",
  "record_type": "event",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000121",
  "actor": "chat|worker|coder|hook",
  "channel": "internal",
  "event_type": "request.received|delegation.created|coder.tool.executed|verify.completed|memory.candidate",
  "event_status": "started|running|failed|completed",
  "event_payload": {
    "note": "pytest 実行完了",
    "result": "pass"
  },
  "created_at": "2026-04-12T16:34:00+09:00",
  "status": "active",
  "summary": "verify.completed"
}
```

### 補足

- Event は状態遷移を追うための記録
- 会話本文や詳細ログはここへ混ぜない

---

## 7. Audit Record スキーマ

### 目的

実行痕跡、Hook 判定、コマンド結果など、検証可能性を支える記録。

### 形式

```json
{
  "record_id": "REC-20260412-AUD-0012",
  "record_type": "audit",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000121",
  "actor": "coder|hook",
  "channel": "internal",
  "audit_kind": "tool_precheck|tool_post|hook_decision|verification_log",
  "command_text": "pytest tests/test_memory.py",
  "result_text": "2 passed",
  "affected_paths": ["tests/test_memory.py"],
  "decision": "allowed|blocked|warned|passed|failed",
  "details": {
    "hook_name": "PreToolUse",
    "reason": "sandbox 内で安全"
  },
  "created_at": "2026-04-12T16:35:00+09:00",
  "status": "closed",
  "summary": "PreToolUse で許可後、pytest 実行"
}
```

### 補足

- 監査は Event とは別層で持つ
- 実行痕跡を人間がたどれることを優先する

---

## 8. Artifact Record スキーマ

### 目的

生成された成果物や派生物の由来を残す。

### 形式

```json
{
  "record_id": "REC-20260412-ART-0005",
  "record_type": "artifact",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000121",
  "actor": "chat|worker|coder",
  "channel": "internal",
  "artifact_id": "ART-20260412-0005",
  "artifact_kind": "deliverable|draft|verification|derived|temp",
  "path": "artifacts/reports/memory_architecture.md",
  "mime_type": "text/markdown",
  "version": "v1",
  "derived_from": ["REC-20260412-TASK-0018"],
  "created_at": "2026-04-12T16:36:00+09:00",
  "status": "active",
  "summary": "memory_architecture.md の成果物記録"
}
```

### 補足

- 実ファイルは artifacts 側にある
- Record 側では由来と参照を持つ

---

## 9. 保存しないもの

原本保存でも、次は原則保存しない。

- モデル内部の生推論
- hidden chain of thought
- 再生成可能な巨大キャッシュ
- 無意味な細粒度ノイズトレース
- 一時 embedding 本体

残すのは、外に現れた行為と結果である。

---

## 10. 保持期間の考え方

### 長期保持

- Conversation Record
- Task request / result
- Event Record
- 重要 Audit Record
- Artifact Record

### 中期保持

- 詳細コマンドログ
- 詳細検証ログ
- 補助監査情報

### 短期保持

- temp artifact の詳細
- 補助トレース
- 再生成可能な作業補助記録

---

## 11. 記録から記憶への受け渡し

原本保存はそのまま想起に使わない。
受け渡しは次の流れにする。

1. 会話・報告・作業を Record に保存する
2. Worker が日次・週次で候補抽出する
3. Chat が保存価値を判断する
4. 必要断片だけを Memory へ昇格する

### 候補抽出のためのメタレコード

```json
{
  "record_id": "REC-20260412-CAND-0003",
  "record_type": "event",
  "root_event_id": "EVT-20260412-000100",
  "event_id": "EVT-20260412-000121",
  "actor": "worker",
  "channel": "internal",
  "event_type": "memory.candidate.extracted",
  "event_status": "completed",
  "event_payload": {
    "candidate_type": "work_optimization",
    "candidate_title": "非破壊手段優先",
    "source_record_ids": ["REC-20260412-CONV-0032", "REC-20260412-AUD-0012"]
  },
  "created_at": "2026-04-12T23:59:00+09:00",
  "status": "active",
  "summary": "memory 候補を抽出"
}
```

---

## 12. 最小ディレクトリ対応

このスキーマを保存先に対応させるなら、最小ではこう切れる。

- `records/conversations/`
- `records/tasks/`
- `records/events/`
- `records/audit/`
- `records/artifacts/`

本文と索引は分けてよい。

---

## 13. この文書の一文要約

原本保存は、Mio が普段思い出すための場所ではなく、
会話、報告、作業の一次情報を EventId で束ねて残し、
必要なときに再調査・再抽出・由来確認ができるようにするための保存スキーマである。
