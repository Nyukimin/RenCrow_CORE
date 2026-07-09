# integration_contracts.md

## 目的

この文書は、RenCrow における Chat / Worker / Coder 間の境界契約を定義する。
対象は以下の 3 種類のやり取りである。

- Chat → Worker
- Chat → Coder
- Worker / Coder → Chat

本仕様の目的は次の 5 つである。

1. 返却値の必須項目を固定すること
2. 成功 / 失敗 / 保留の意味を統一すること
3. 再委譲条件を境界で明示すること
4. 責務越境を防ぐこと
5. EventId を軸に境界越しの追跡を可能にすること

---

## 基本原則

### 1. 境界をまたぐ値は自由文ではなく構造化する

Chat から Worker / Coder へ渡す値、Worker / Coder から Chat へ返す値は、
原則として JSON オブジェクトで表現する。

自然言語の補足は許可するが、契約成立に必要な項目は必ず構造化フィールドに入れる。

### 2. 境界先は最終判断を持たない

Worker と Coder は実行担当であり、最終的な対話方針、保存方針、再委譲方針は Chat が持つ。

Worker は結論候補や根拠を返してよいが、最終応答を確定しない。
Coder は変更や検証結果を返してよいが、最終説明文を確定しない。

### 3. 返却は 1 task 1 result を守る

1 つの task payload に対し、境界越しの result は 1 つの主結果を返す。
副次情報は含めてよいが、複数の無関係な目的を混在させない。

### 4. 失敗は隠さず分類して返す

境界越しの失敗は、少なくとも次の観点で構造化して返す。

- どの段階で失敗したか
- なぜ失敗したか
- 再試行可能か
- 次に戻るべき段階はどこか

### 5. 再委譲は Chat だけが確定する

Worker と Coder は `next_recommendation` を返してよい。
ただし、実際にどこへ戻すか、誰に再委譲するかは Chat が決定する。

---

## 用語

### request payload

Chat が Worker / Coder に渡す入力契約。

### result payload

Worker / Coder が Chat に返す出力契約。

### boundary status

境界越しの処理状態。少なくとも以下を持つ。

- `completed`
- `blocked`
- `failed`

### handoff summary

Chat がそのまま統合に使える短い要約。
結果本文ではなく、境界越しに渡す要約。

### next recommendation

返却側が推奨する次アクション。
確定命令ではなく、Chat への提案にとどめる。

---

## 共通 request contract

すべての request payload は、最低でも次の共通項目を持つ。

```json
{
  "event_id": "EVT-20260412-000201",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000180",
  "issuer": "chat",
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
    "must_include": []
  },
  "risk_level": "low|medium|high",
  "issued_at": "2026-04-12T10:12:34+09:00"
}
```

### 必須項目

- `event_id`
- `root_event_id`
- `issuer`
- `target`
- `command`
- `objective`
- `scope`
- `constraints`
- `expected_output`
- `risk_level`

### target ごとの追加条件

#### target=worker

- `command` は `/investigate` または `/maintain` に限る
- `constraints` には原則 `編集禁止` を含める
- `expected_output.format` は `summary` または `maintenance_report`

#### target=coder

- `command` は `/plan` `/implement` `/verify` に限る
- `constraints` には環境制約と安全制約を含める
- `expected_output.format` は `plan` `change_set` `verification_report`

---

## 共通 result contract

すべての result payload は、最低でも次の共通項目を持つ。

```json
{
  "event_id": "EVT-20260412-000201",
  "root_event_id": "EVT-20260412-000100",
  "actor": "worker|coder",
  "command": "/investigate|/plan|/implement|/verify|/maintain",
  "status": "completed|blocked|failed",
  "handoff_summary": "Chat が統合しやすい短い要約",
  "details": {},
  "risks": [],
  "next_recommendation": "chat|worker|coder.plan|coder.implement|coder.verify|none",
  "retryable": true,
  "related_event_ids": [],
  "completed_at": "2026-04-12T10:13:20+09:00"
}
```

### 必須項目

- `event_id`
- `root_event_id`
- `actor`
- `command`
- `status`
- `handoff_summary`
- `details`
- `retryable`
- `next_recommendation`

### status の意味

#### completed

目的が境界契約の範囲内で達成された状態。
ただし、全体タスク完了を意味しない。
Chat はこの結果を統合して次判断を行う。

#### blocked

必要な条件不足や境界制約により、現在の担当範囲では継続できない状態。
例:

- 参照不足
- 権限不足
- Hook 拒否
- 範囲不足

`blocked` は失敗ではなく、中断である。

#### failed

実行したが目的を達成できなかった状態。
例:

- 実装後も症状再現
- 検証失敗
- 調査結果の矛盾解消不能

---

## Worker result contract

Worker は、少なくとも次の情報を返す。

```json
{
  "event_id": "EVT-20260412-000201",
  "root_event_id": "EVT-20260412-000100",
  "actor": "worker",
  "command": "/investigate",
  "status": "completed",
  "handoff_summary": "初期化順逆転が最有力",
  "details": {
    "summary": "ログ保存不具合は logger 初期化前の flush 実行が最有力",
    "findings": [
      "bootstrap.py で flush が先行",
      "test_boot.py で同条件再現"
    ],
    "evidence": [
      {"path": "src/logging/bootstrap.py", "note": "flush 呼び出し位置"},
      {"path": "tests/logging/test_boot.py", "note": "再現テスト"}
    ],
    "candidate_set": [],
    "skill_candidate": {
      "is_candidate": true,
      "title": "ログ初期化順逆転の確認手順"
    }
  },
  "risks": [],
  "next_recommendation": "coder.plan",
  "retryable": false,
  "related_event_ids": ["EVT-20260410-000441"],
  "completed_at": "2026-04-12T10:13:20+09:00"
}
```

### Worker の必須 detail 項目

- `summary`
- `findings`
- `evidence`

### Worker が返してよい追加項目

- `candidate_set`
- `skill_candidate`
- `maintenance_stats`
- `index_updates`

### Worker が返してはいけないもの

- 直接のコード変更命令
- 最終応答文
- 人物記憶更新指示の確定
- 実装済みを装う表現

### Worker の失敗返却ルール

Worker が `blocked` または `failed` を返す場合、`details` に少なくとも以下を含める。

```json
{
  "failure_class": "investigation|scope|access|conflict",
  "reason": "短い理由",
  "missing_inputs": [],
  "retry_preconditions": []
}
```

---

## Coder result contract

Coder は、少なくとも次の情報を返す。

```json
{
  "event_id": "EVT-20260412-000221",
  "root_event_id": "EVT-20260412-000100",
  "actor": "coder",
  "command": "/implement",
  "status": "completed",
  "handoff_summary": "bootstrap.py の flush 順序を修正し、関連テストは通過",
  "details": {
    "plan": [
      "flush の呼び出し順を修正",
      "再現テストで確認"
    ],
    "changes": [
      {"path": "src/logging/bootstrap.py", "summary": "flush 呼び出し順を変更"}
    ],
    "commands_run": [
      "pytest tests/logging/test_boot.py"
    ],
    "verification": {
      "performed": true,
      "result": "pass",
      "notes": "再現テスト成功"
    },
    "skill_candidate": {
      "is_candidate": true,
      "title": "初期化順逆転修正の検証手順"
    }
  },
  "risks": [
    "統合テストは未実施"
  ],
  "next_recommendation": "chat",
  "retryable": false,
  "related_event_ids": ["EVT-20260412-000201"],
  "completed_at": "2026-04-12T10:14:31+09:00"
}
```

### Coder の必須 detail 項目

#### `/plan`

- `plan_steps`
- `impact_scope`
- `verification_plan`

#### `/implement`

- `plan`
- `changes`
- `commands_run`
- `verification` または `verification_pending_reason`

#### `/verify`

- `verification`
- `commands_run`
- `diff_checked`
- `unresolved_items`

### Coder が返してはいけないもの

- ユーザー向け最終説明文の確定
- 未実行なのに実行済みと見せる表現
- Hook 拒否を隠した成功扱い
- 調査不足を実装失敗に偽装すること

### Coder の失敗返却ルール

Coder が `blocked` または `failed` を返す場合、`details` に少なくとも以下を含める。

```json
{
  "failure_class": "hook_denied|implementation|verification|scope|permission",
  "reason": "短い理由",
  "failed_stage": "plan|implement|verify",
  "retry_preconditions": [],
  "safe_next_step": "再試行条件または Chat へ戻す理由"
}
```

---

## command 別境界契約

## 1. `/investigate`

### issuer / target

- issuer: `chat`
- target: `worker`

### request の最小条件

- `objective` が 1 つに絞られていること
- `scope` が探索対象を示していること
- `constraints` に編集禁止があること

### result の最小条件

- `handoff_summary`
- `details.summary`
- `details.findings`
- `details.evidence`
- `next_recommendation`

### Chat 側の受理条件

Chat は、根拠がなく結論だけがある result を受理しない。
必要なら `blocked` として再調査へ戻す。

---

## 2. `/plan`

### issuer / target

- issuer: `chat`
- target: `coder`

### request の最小条件

- `objective` が変更対象に対応していること
- `scope.paths` または `scope.modules` が空でないこと
- `constraints` に安全制約が入っていること

### result の最小条件

- `details.plan_steps`
- `details.impact_scope`
- `details.verification_plan`
- `risks`

### Chat 側の受理条件

Chat は、実装計画に検証計画がない場合は `/implement` に進めない。
高リスク時は、`risks` が空でも過信しない。

---

## 3. `/implement`

### issuer / target

- issuer: `chat`
- target: `coder`

### request の最小条件

- 直前に `/plan` 完了結果があることが望ましい
- `allowed_tools` または同等の実行制約が別仕様で確定していること
- `constraints` に破壊禁止と共有環境制約が入っていること

### result の最小条件

- `details.changes`
- `details.commands_run`
- `handoff_summary`
- `verification` または `verification_pending_reason`

### Chat 側の受理条件

Chat は、変更ファイル情報なしの `/implement` result を受理しない。
`verification_pending_reason` がある場合は、必ず `/verify` へ段階を分ける。

---

## 4. `/verify`

### issuer / target

- issuer: `chat`
- target: `coder`

### request の最小条件

- 検証対象の変更または event 参照があること
- `objective` が検証目的であること

### result の最小条件

- `details.verification`
- `details.commands_run`
- `details.diff_checked`
- `details.unresolved_items`

### Chat 側の受理条件

Chat は、`verification.performed=false` の結果を成功完了として扱わない。
高リスク時は `diff_checked=true` を必須にする。

---

## 5. `/maintain`

### issuer / target

- issuer: `chat` または `scheduler`
- target: `worker`

### request の最小条件

- 目的が整備作業に限定されていること
- 自動統合や自動昇格を含まないこと

### result の最小条件

- `handoff_summary`
- `details.maintenance_stats`
- `details.outputs`
- `details.skipped_items`

### Chat 側の受理条件

Chat は、整備結果を人物記憶や成果物へ自動昇格しない。
必要なら別イベントで昇格判断を行う。

---

## 再委譲条件

## Worker → Chat に返す再委譲条件

Worker は自分で再委譲しない。
次のいずれかに当てはまる場合、`next_recommendation` を返して Chat に判断を戻す。

- 根拠がそろい、実装が必要になった
- 範囲不足で追加参照が必要
- 根拠が衝突し、再探索の方針決定が必要
- 保存候補の価値判断が必要

## Coder → Chat に返す再委譲条件

Coder は次のいずれかに当てはまる場合、Chat に戻す。

- Hook 拒否で進めない
- 調査不足で実装前提が崩れた
- 実装は終わったが検証条件が追加で必要
- 失敗原因が設計側か実装側か判別不能

## Chat の再委譲原則

Chat は、返却された `next_recommendation` を盲信しない。
以下を再判定する。

- いま必要なのは調査か実装か検証か
- 高リスクかどうか
- 記憶更新や成果物昇格を伴うか
- 前回と同じ失敗を繰り返していないか

---

## エラーコード方針

result payload では、必要に応じて `error_code` を返してよい。
推奨コードは以下。

- `WRK_SCOPE_MISSING`
- `WRK_EVIDENCE_CONFLICT`
- `WRK_ACCESS_DENIED`
- `CDR_HOOK_DENIED`
- `CDR_PERMISSION_BLOCKED`
- `CDR_IMPLEMENT_FAILED`
- `CDR_VERIFY_FAILED`
- `INT_RESULT_INVALID`

### 使い方

- `status=blocked|failed` のときに付与してよい
- 人間向け説明は `reason` に書く
- 機械分岐は `error_code` を使う

---

## 境界での禁止事項

### Chat の禁止事項

- Worker/Coder に自由文だけを投げること
- 制約なしで `/implement` を出すこと
- 調査結果なしに高リスク実装へ入ること

### Worker の禁止事項

- 最終応答の確定
- 実装の実行
- 人物記憶の直接更新
- 調査不足のまま断定

### Coder の禁止事項

- 調査不足を無視した実装開始
- Hook 拒否の迂回
- 未検証のまま成功扱い
- Sandbox 外への自動拡張

---

## 受理拒否ルール

Chat は、境界契約を満たさない result を受理してはならない。
受理拒否の例は以下。

- 必須項目不足
- `status` 不正
- `handoff_summary` 空欄
- `details` が command 契約に合っていない
- 実行していないのに `completed` として返している
- 根拠なし結論のみ

受理拒否時は、別 EventId で `integration.failure` を記録し、
不足項目または不整合項目を明示して再委譲する。

---

## 最小統合チェックリスト

Chat が境界結果を統合する前に、最低限以下を確認する。

1. `event_id` と `root_event_id` が一致しているか
2. `actor` と `command` の組み合わせが正しいか
3. `status` が `completed|blocked|failed` のいずれかか
4. `handoff_summary` が空でないか
5. command 別の必須 detail 項目があるか
6. 失敗時に `reason` と `retry_preconditions` があるか
7. `next_recommendation` が責務越境していないか
8. 高リスク時に検証情報が十分か

---

## 一文で言うと

境界契約は、
「何を頼んだか」「どこまでできたか」「なぜ止まったか」「次にどこへ戻すか」を、
Chat / Worker / Coder の間で揺らさず受け渡すための約束である。
