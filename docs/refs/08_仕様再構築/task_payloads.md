# task_payloads.md

## 目的

この文書は、RenCrow における Chat → Worker / Coder への委譲ペイロードを固定するための仕様である。

対象 command は以下の 5 つ。

- `/investigate`
- `/plan`
- `/implement`
- `/verify`
- `/maintain`

本仕様の目的は、自由文の委譲を避け、入力契約と出力契約を明示することにある。

---

## 基本方針

すべての task payload は JSON オブジェクトとして表現する。

必須原則は以下の通り。

1. すべての payload は `event_id` と `root_event_id` を持つ
2. 委譲先は `target` で明示する
3. command は `command` に固定文字列で入れる
4. 目的は `objective` に短く書く
5. 対象範囲は `scope` に構造化して入れる
6. 制約は `constraints` に列挙する
7. 出力要求は `expected_output` に明記する
8. 参照記憶や参照イベントは `references` に集約する
9. 曖昧な自由作文を禁止する

---

## 共通スキーマ

すべての payload は少なくとも以下の共通項目を持つ。

```json
{
  "event_id": "EVT-20260412-000121",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000118",
  "target": "worker|coder",
  "command": "/investigate|/plan|/implement|/verify|/maintain",
  "objective": "達成すべき目的",
  "scope": {
    "paths": [],
    "modules": [],
    "topics": [],
    "time_range": null
  },
  "constraints": [],
  "references": {
    "event_ids": [],
    "skill_ids": [],
    "files": [],
    "notes": []
  },
  "expected_output": {
    "format": "summary|plan|change_set|verification_report|maintenance_report",
    "max_length": "short|medium|long",
    "must_include": []
  },
  "risk_level": "low|medium|high",
  "issued_at": "2026-04-12T10:12:34+09:00"
}
```

### 共通フィールドの意味

`event_id`  
この委譲自体の一意 ID。

`root_event_id`  
元のユーザー依頼に対応する起点イベント ID。

`parent_event_id`  
直前の親イベント ID。Chat が直接投げる場合は Chat 側 delegation イベントを指す。

`target`  
委譲先。`worker` または `coder` のみ。

`command`  
実行要求の型。固定値のみ許可。

`objective`  
達成目標。1文で短く書く。

`scope`  
対象範囲。パス、モジュール、話題、時間範囲などを構造化して持つ。

`constraints`  
禁止事項、優先事項、環境制約。

`references`  
参照対象の集約。EventId、skill、ファイル、補足メモを含む。

`expected_output`  
返却形式の要求。

`risk_level`  
危険度。Chat が設定する。

---

## command 別仕様

## 1. `/investigate`

### 用途

Worker に対して、調査・探索・比較・要約を依頼する。

### target

- `worker` のみ

### 使う場面

- 既存実装の所在が不明
- 類似障害の再調査が必要
- 大量ログの要点抽出が必要
- 実装前に根拠確認が必要
- Chat 単独では事実確定できない

### 入力例

```json
{
  "event_id": "EVT-20260412-000201",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000180",
  "target": "worker",
  "command": "/investigate",
  "objective": "ログ保存不具合の原因候補を特定する",
  "scope": {
    "paths": ["src/logging/", "tests/logging/", "logs/"],
    "modules": ["logging", "startup"],
    "topics": ["initialization order", "file flush timing"],
    "time_range": null
  },
  "constraints": [
    "編集禁止",
    "推測だけで断定しない",
    "根拠を3件以内で返す"
  ],
  "references": {
    "event_ids": ["EVT-20260410-000441"],
    "skill_ids": ["SKILL-00021"],
    "files": ["docs/logging_notes.md"],
    "notes": ["過去に初期化順逆転で類似障害あり"]
  },
  "expected_output": {
    "format": "summary",
    "max_length": "short",
    "must_include": ["結論", "根拠", "次アクション"]
  },
  "risk_level": "low",
  "issued_at": "2026-04-12T10:12:34+09:00"
}
```

### 出力契約

`/investigate` の返却は以下を満たす。

- 結論は短く 1 つに寄せる
- 根拠は多くても 3 件程度
- 編集指示を直接実行しない
- 次の適切な委譲先を示してよい

### 出力例

```json
{
  "event_id": "EVT-20260412-000201",
  "status": "completed",
  "summary": "原因候補は初期化順の逆転が最有力",
  "findings": [
    "logger 初期化前に flush が呼ばれている",
    "tests/logging/test_boot.py でも同条件で再現可能"
  ],
  "evidence": [
    {"path": "src/logging/bootstrap.py", "note": "flush 呼び出し位置"},
    {"path": "tests/logging/test_boot.py", "note": "再現テスト"}
  ],
  "next_recommendation": "coder へ /plan を委譲",
  "skill_candidate": {
    "is_candidate": true,
    "title": "ログ初期化順逆転の確認手順"
  }
}
```

---

## 2. `/plan`

### 用途

Coder に対して、変更前の計画立案を依頼する。

### target

- `coder` のみ

### 使う場面

- 変更方針の整理が必要
- 影響範囲確認が必要
- 高リスクのため段階を分けたい
- いきなり実装させたくない

### 入力例

```json
{
  "event_id": "EVT-20260412-000221",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000201",
  "target": "coder",
  "command": "/plan",
  "objective": "ログ保存不具合の修正方針を立てる",
  "scope": {
    "paths": ["src/logging/", "tests/logging/"],
    "modules": ["logging"],
    "topics": ["initialization order"],
    "time_range": null
  },
  "constraints": [
    "ファイル編集禁止",
    "非破壊優先",
    "検証計画を含める"
  ],
  "references": {
    "event_ids": ["EVT-20260412-000201"],
    "skill_ids": [],
    "files": [],
    "notes": ["原因候補は初期化順の逆転が最有力"]
  },
  "expected_output": {
    "format": "plan",
    "max_length": "medium",
    "must_include": ["修正方針", "影響範囲", "検証方法"]
  },
  "risk_level": "medium",
  "issued_at": "2026-04-12T10:15:00+09:00"
}
```

### 出力契約

- 実装せず、計画のみ返す
- 影響範囲を明示する
- 検証方法を必ず含める
- 未確定事項があれば分離して書く

### 出力例

```json
{
  "event_id": "EVT-20260412-000221",
  "status": "completed",
  "plan": [
    "bootstrap 内の logger 初期化順を flush より前へ移動する",
    "既存テストに再現ケースを追加する",
    "pytest tests/logging を実行して回帰確認する"
  ],
  "impact_scope": [
    "src/logging/bootstrap.py",
    "tests/logging/test_boot.py"
  ],
  "open_questions": [],
  "handoff_summary": "変更は logging モジュール内に限定できる見込み"
}
```

---

## 3. `/implement`

### 用途

Coder に対して、許可範囲内で実装変更を依頼する。

### target

- `coder` のみ

### 使う場面

- `/plan` で妥当性が確認できた
- 低〜中リスクで変更範囲が明確
- 変更対象と禁止事項が明示できている

### 入力例

```json
{
  "event_id": "EVT-20260412-000241",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000221",
  "target": "coder",
  "command": "/implement",
  "objective": "logging の初期化順を修正する",
  "scope": {
    "paths": ["src/logging/bootstrap.py", "tests/logging/test_boot.py"],
    "modules": ["logging"],
    "topics": ["initialization order"],
    "time_range": null
  },
  "constraints": [
    "Move-Item による物理移動禁止",
    "共有環境変更禁止",
    "許可パス外の編集禁止",
    "危険コマンド禁止"
  ],
  "references": {
    "event_ids": ["EVT-20260412-000221"],
    "skill_ids": [],
    "files": [],
    "notes": ["plan 済み"]
  },
  "expected_output": {
    "format": "change_set",
    "max_length": "medium",
    "must_include": ["変更ファイル", "実行コマンド", "未解決リスク"]
  },
  "risk_level": "medium",
  "issued_at": "2026-04-12T10:18:00+09:00"
}
```

### 出力契約

- 変更ファイルを必ず列挙する
- 実行コマンドを記録する
- 検証前なら「未検証」と明記する
- Hook により危険操作が止められている前提

### 出力例

```json
{
  "event_id": "EVT-20260412-000241",
  "status": "completed",
  "changes": [
    {"path": "src/logging/bootstrap.py", "summary": "初期化順を修正"},
    {"path": "tests/logging/test_boot.py", "summary": "再現テストを追加"}
  ],
  "commands_run": [
    "python -m compileall src/logging"
  ],
  "verification": {
    "performed": false,
    "result": "partial",
    "notes": "構文確認のみ。回帰テスト未実施"
  },
  "risks": [],
  "handoff_summary": "変更は完了。次は /verify が必要"
}
```

---

## 4. `/verify`

### 用途

Coder に対して、変更後の検証を依頼する。

### target

- `coder` のみ

### 使う場面

- 実装変更後の確認
- テスト、静的確認、差分確認が必要
- 完了前に回帰確認したい

### 入力例

```json
{
  "event_id": "EVT-20260412-000261",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000241",
  "target": "coder",
  "command": "/verify",
  "objective": "logging 修正の回帰確認を行う",
  "scope": {
    "paths": ["src/logging/bootstrap.py", "tests/logging/test_boot.py"],
    "modules": ["logging"],
    "topics": ["test", "diff", "regression"],
    "time_range": null
  },
  "constraints": [
    "実装変更は原則しない",
    "失敗時は原因分類して返す"
  ],
  "references": {
    "event_ids": ["EVT-20260412-000241"],
    "skill_ids": [],
    "files": [],
    "notes": []
  },
  "expected_output": {
    "format": "verification_report",
    "max_length": "medium",
    "must_include": ["実施内容", "結果", "残リスク"]
  },
  "risk_level": "low",
  "issued_at": "2026-04-12T10:21:00+09:00"
}
```

### 出力契約

- 何を検証したかを列挙する
- pass / fail / partial を明示する
- fail 時は原因分類を返す
- 必要なら再委譲提案を返してよい

### 出力例

```json
{
  "event_id": "EVT-20260412-000261",
  "status": "completed",
  "verification": {
    "performed": true,
    "result": "pass",
    "checks": [
      "pytest tests/logging/test_boot.py",
      "git diff -- src/logging/bootstrap.py tests/logging/test_boot.py"
    ],
    "notes": "再現ケースを含めてテスト通過"
  },
  "risks": [],
  "handoff_summary": "実装と検証が完了。Chat で統合可能"
}
```

### fail 時の原因分類

`/verify` が失敗した場合、以下のいずれかに分類する。

- `permission_insufficient`
- `scope_insufficient`
- `technical_failure`
- `verification_failure`
- `environment_unavailable`

例:

```json
{
  "event_id": "EVT-20260412-000261",
  "status": "failed",
  "failure_type": "verification_failure",
  "summary": "既存テスト 1 件が失敗し、回帰を確認できなかった",
  "next_recommendation": "Chat へ戻し、追加調査または再実装を依頼"
}
```

---

## 5. `/maintain`

### 用途

Worker に対して、整備系の後処理や定期ジョブを依頼する。

### target

- `worker` のみ

### 使う場面

- Event 要約の作成
- skill 候補の整理
- 索引更新
- 整合チェック
- 定期メンテナンス

### 入力例

```json
{
  "event_id": "EVT-20260412-000281",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000261",
  "target": "worker",
  "command": "/maintain",
  "objective": "今回の修正イベントを要約し、skill 候補を整理する",
  "scope": {
    "paths": [],
    "modules": ["logging"],
    "topics": ["event summary", "skill extraction"],
    "time_range": null
  },
  "constraints": [
    "実ファイル編集禁止",
    "人物記憶更新禁止",
    "既存 skill 上書き禁止"
  ],
  "references": {
    "event_ids": [
      "EVT-20260412-000201",
      "EVT-20260412-000221",
      "EVT-20260412-000241",
      "EVT-20260412-000261"
    ],
    "skill_ids": [],
    "files": [],
    "notes": []
  },
  "expected_output": {
    "format": "maintenance_report",
    "max_length": "short",
    "must_include": ["要約", "生成物", "候補件数"]
  },
  "risk_level": "low",
  "issued_at": "2026-04-12T10:25:00+09:00"
}
```

### 出力契約

- 実施した整備内容を短く返す
- 生成または更新した索引・要約・候補を示す
- 破壊的変更を行わない

### 出力例

```json
{
  "event_id": "EVT-20260412-000281",
  "status": "completed",
  "summary": "logging 修正イベントの要約を保存し、skill 候補を1件抽出した",
  "artifacts": [
    "events/2026/04/12/EVT-20260412-000100.summary.json",
    "memory/skills/candidates/logging_init_order.md"
  ],
  "counts": {
    "events_summarized": 4,
    "skill_candidates": 1
  }
}
```

---

## Chat 側ルール

Chat は command を選ぶとき、以下を守る。

1. 調査が必要なら、先に `/investigate`
2. 高リスク実装は、必ず `/plan` を経由
3. `/implement` の後は原則 `/verify`
4. 完了後の整理は必要に応じて `/maintain`
5. 調査と実装を1回の payload に混ぜない
6. 1 payload 1 objective を守る

---

## 最小バリデーション規則

すべての payload に対して以下を検査する。

- `event_id` が存在する
- `root_event_id` が存在する
- `target` と `command` の組み合わせが正しい
- `objective` が空でない
- `constraints` が配列である
- `expected_output.format` が許可値に含まれる
- `risk_level` が `low|medium|high` のいずれかである

追加規則:

- `/investigate` は `target=worker` のみ
- `/maintain` は `target=worker` のみ
- `/plan` `/implement` `/verify` は `target=coder` のみ

---

## 実装メモ

v0.1 では、`scope` と `references` を厳密に作りすぎず、空配列を許容する。  
ただし、`objective` と `constraints` と `expected_output` は省略しない。

将来拡張で追加しやすい項目は以下。

- `approval_policy`
- `sandbox_profile`
- `timeout_sec`
- `retry_policy`
- `priority`
- `scheduler_context`

---

## 一文での要約

Chat は自由文で丸投げしない。  
Worker/Coder には、目的・範囲・制約・返却形式を固定した JSON で委譲する。
