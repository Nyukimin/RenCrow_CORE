# event_schema.md

## 目的

この文書は、RenCrow における EventId ベースの共通イベントスキーマを定義する。  
目的は、Chat / Worker / Coder / Hook のすべての主要処理を、最終応答ではなく「追跡可能なイベント列」として扱えるようにすることにある。

本スキーマは v0.1 とし、まずは以下を重視する。

- 誰が処理したかを追えること
- どの依頼から派生したかを追えること
- 何が起きたかを時系列で追えること
- Hook による停止・失敗・監査を記録できること
- 履歴検索と手順記憶化の入力に使えること

---

## 1. 設計原則

### 1.1 Event は最終結果ではなく過程を表す

1つの依頼に対して、受信、委譲、ツール実行、検証、失敗、完了を別イベントとして記録する。  
「最後にどう返したか」だけでなく、「途中で何をして、どこで止まり、何を見たか」が追えることを優先する。

### 1.2 Event は親子関係を持つ

ユーザー依頼を起点とする `root_event_id` を持ち、委譲や内部処理は `parent_event_id` を通じて派生関係を表す。

### 1.3 Event は人向け要約と機械向け属性を両方持つ

監査やデバッグのための短い要約と、機械処理のための型付き属性を分けて保持する。

### 1.4 Event は actor ごとの責務を崩さない

- Chat は判断・統合・応答に関するイベントを出す
- Worker は調査・比較・要約・整備に関するイベントを出す
- Coder は計画・変更・検証・失敗に関するイベントを出す
- Hook は強制点・ブロック・監査補助に関するイベントを出す

---

## 2. EventId 仕様

### 2.1 EventId の役割

EventId は、RenCrow 内のすべての主要処理単位を一意に識別する ID である。

### 2.2 基本ルール

- 1つのユーザー依頼に対して `root_event_id` を1つ発行する
- すべての派生イベントは `root_event_id` を継承する
- 派生元がある場合は `parent_event_id` を設定する
- 同じ actor の連続処理でも、節目ごとに別 Event とする
- Hook は独立イベントとして扱ってよい

### 2.3 推奨形式

形式は固定ではないが、少なくとも時系列ソートしやすく、一意性が保証されること。

推奨例:

`EVT-20260412-000123`

または内部的には UUID を使い、表示用に短縮 ID を併記してもよい。

---

## 3. 共通イベントスキーマ

すべてのイベントは最低限、以下の共通フィールドを持つ。

```json
{
  "event_id": "EVT-20260412-000123",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000118",
  "actor": "chat|worker|coder|hook",
  "event_type": "request|delegation|tool_pre|tool_post|analysis|memory_write|verify|stop|error|complete",
  "timestamp": "2026-04-12T10:12:34+09:00",
  "task_kind": "chat|investigate|plan|implement|verify|maintenance",
  "status": "started|running|blocked|failed|completed",
  "summary": "短い説明"
}
```

### 3.1 必須フィールド

#### event_id
そのイベント自身の一意 ID。

#### root_event_id
最初のユーザー依頼に対応するルート ID。  
同一依頼系列を束ねる主キーとして使う。

#### actor
イベントを発行した主体。

- `chat`
- `worker`
- `coder`
- `hook`

#### event_type
イベントの種類。後述の分類に従う。

#### timestamp
タイムゾーン付き ISO 8601 形式を推奨する。

#### status
イベント時点での状態。

- `started`
- `running`
- `blocked`
- `failed`
- `completed`

#### summary
人間が一覧で見たときに意味がわかる短文。

### 3.2 条件付き必須フィールド

#### parent_event_id
派生元がある場合に必須。  
ルートイベントでは省略可。

#### task_kind
以下のような処理種別を入れる。

- `chat`
- `investigate`
- `plan`
- `implement`
- `verify`
- `maintenance`

#### error_code / error_detail
`status=failed` または `status=blocked` の場合に付与する。

#### tool_name / command
tool 実行系イベントの場合に付与する。

---

## 4. イベント種別

### 4.1 request
依頼受信イベント。

主に Chat が発行する。

用途:
- ユーザー入力受信
- Scheduler 起点タスク受信

### 4.2 delegation
委譲イベント。

Chat が Worker または Coder にタスクを渡す際に発行する。

### 4.3 analysis
調査・推論・要約イベント。

主に Worker または Chat が発行する。

### 4.4 tool_pre
ツール実行前イベント。

主に Coder または Hook が発行する。  
PreToolUse 判定の記録に使う。

### 4.5 tool_post
ツール実行後イベント。

実行コマンド、結果、変更ファイル、所要時間などを記録する。

### 4.6 verify
検証イベント。

テスト、静的確認、差分確認、起動確認などを表す。

### 4.7 memory_write
記憶保存イベント。

手順記憶や索引更新、履歴要約保存に使う。  
人物記憶は Chat の判断イベントと分離してもよい。

### 4.8 stop
停止直前の完了性確認イベント。

特に Coder では、未完了の自己正当化防止に使う。

### 4.9 error
失敗イベント。

権限不足、範囲不足、技術的失敗、検証失敗などを分類して記録する。

### 4.10 complete
完了イベント。

その処理単位が閉じたことを明示する。

---

## 5. actor 別の推奨イベント

### 5.1 Chat

Chat は少なくとも以下を発行する。

- `request.received`
- `memory.read`
- `delegation.created`
- `result.integrated`
- `memory.update_decided`
- `response.completed`

推奨例:

```json
{
  "event_id": "EVT-20260412-000100",
  "root_event_id": "EVT-20260412-000100",
  "actor": "chat",
  "event_type": "request",
  "timestamp": "2026-04-12T09:30:10+09:00",
  "task_kind": "chat",
  "status": "started",
  "summary": "ユーザー依頼を受信: Worker/Coder 仕様の作成"
}
```

### 5.2 Worker

Worker は少なくとも以下を発行する。

- `worker.task.started`
- `worker.search.performed`
- `worker.compare.completed`
- `worker.summary.created`
- `worker.skill.candidate`
- `worker.task.completed`
- `worker.task.failed`

### 5.3 Coder

Coder は少なくとも以下を発行する。

- `coder.task.started`
- `coder.plan.created`
- `coder.tool.prechecked`
- `coder.tool.executed`
- `coder.diff.created`
- `coder.verify.completed`
- `coder.stop.checked`
- `coder.task.completed`
- `coder.task.failed`

### 5.4 Hook

Hook は少なくとも以下を発行する。

- `hook.delegation.checked`
- `hook.tool.pre.blocked`
- `hook.tool.post.logged`
- `hook.stop.checked`
- `hook.memory.candidate.detected`

---

## 6. 拡張フィールド

共通スキーマに加えて、用途別に以下の拡張フィールドを持てる。

### 6.1 command 系

```json
{
  "tool_name": "bash",
  "command": "pytest tests/test_a.py",
  "sandboxed": true,
  "duration_ms": 1820,
  "exit_code": 0
}
```

### 6.2 file change 系

```json
{
  "changed_files": [
    "src/module/a.py",
    "tests/test_a.py"
  ],
  "diff_summary": "初期化順とテスト前提を修正"
}
```

### 6.3 error 系

```json
{
  "error_code": "VERIFY_FAILED",
  "error_category": "verification|permission|scope|technical",
  "error_detail": "テストは実行できたが期待値が一致しなかった"
}
```

### 6.4 evidence 系

```json
{
  "evidence": [
    {
      "kind": "file",
      "ref": "src/app/foo.ts",
      "note": "エントリポイント初期化順"
    },
    {
      "kind": "event",
      "ref": "EVT-20260410-000441",
      "note": "類似障害の復旧記録"
    }
  ]
}
```

---

## 7. 典型フロー例

### 7.1 調査だけで完結する場合

1. Chat が request を受信する
2. Chat が delegation を発行して Worker に `/investigate`
3. Worker が analysis を複数発行する
4. Worker が complete を返す
5. Chat が result.integrated を発行する
6. Chat が response.completed を発行する

### 7.2 実装を伴う場合

1. Chat が request を受信
2. Chat が Worker に調査委譲
3. Worker が調査結果を返す
4. Chat が Coder に `/plan`
5. Coder が plan.created
6. Coder が tool_pre / tool_post / verify を発行
7. Coder が stop.checked
8. Coder が task.completed
9. Chat が結果統合して応答完了

---

## 8. 保存単位

### 8.1 生イベント

Hook や実行の完全な履歴。  
監査・事故解析・再現用に残す。

### 8.2 要約イベント

履歴検索や類似障害検索のための圧縮版。  
必要なら非同期で Worker が生成する。

例:

```json
{
  "event_id": "EVT-20260412-000121",
  "actor": "coder",
  "task_kind": "implement",
  "objective": "ログ保存の不具合修正",
  "scope": ["src/logging/", "tests/logging/"],
  "result": "completed",
  "key_findings": [
    "初期化順が逆だった"
  ],
  "commands_run": [
    "pytest tests/logging"
  ],
  "artifacts": [
    "logs/run-20260412.json"
  ],
  "related_skill_ids": ["SKILL-00021"]
}
```

---

## 9. バリデーションルール

最低限、以下を満たすこと。

- `event_id` は一意である
- ルート以外は `root_event_id` を持つ
- `actor` は許可値のみ
- `status` は許可値のみ
- `event_type` は許可値のみ
- `failed` / `blocked` の場合は理由フィールドがある
- `tool_post` には command または tool_name がある
- `verify` には result または note がある

---

## 10. v0.1 の運用ルール

最初から複雑なイベント分類をしすぎない。  
まずは以下が追えればよい。

- どの依頼から始まったか
- 誰が処理したか
- 委譲されたか
- 危険操作が止められたか
- 何を実行したか
- 検証したか
- 完了したか
- 後で再利用できるか

---

## 11. 一文で言うと

Event は、RenCrow の「何が起きたか」を、あとからたどれる形にするための最小単位である。  
会話ログではなく、処理の因果列として持つ。
