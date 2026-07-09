# Worker 仕様 v0.1

## 1. 位置づけ

Worker は RenCrow における独立コンテキストの調査専用機である。

設計方針は Claude Code 型とする。つまり、主会話を汚さず、別コンテキスト・限定責務・構造化出力で動く。役割は「読む・探す・比べる・要約する・整える」に限定する。

Worker は会話主体ではない。ユーザー向けの最終文は生成しない。

---

## 2. 責務

Worker の責務は以下に限定する。

- コードベース探索
- 既存実装の比較
- 大量ログ読解
- 過去 Event の探索
- 類似障害の検索
- 差分比較
- 調査結果の要約
- skill 候補の抽出
- 履歴索引の整備
- 夜間・定期の整備ジョブ

Worker は「読む主体」であり、「最終判断主体」でも「実装主体」でもない。

---

## 3. 共通基盤との関係

### 3.1 EventId

Worker は Chat から委譲された ChildEventId を受け取り、それに紐づくイベントを生成する。

推奨メタデータは以下とする。

```json
{
  "event_id": "EVT-20260412-000118",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000110",
  "actor": "worker",
  "event_type": "task_start|search|compare|summary|skill_candidate|complete|error",
  "timestamp": "2026-04-12T10:15:00+09:00",
  "task_kind": "investigate|maintenance",
  "status": "started|running|blocked|failed|completed",
  "summary": "短い説明"
}
```

### 3.2 Hook

Worker の Hook は「勝手に広げない」「長く話さない」「根拠を残す」を強制するために使う。

少なくとも以下を持つ。

- TaskStart Hook
- ResultBeforeReturn Hook
- MemoryCandidate Hook

### 3.3 記憶層

Worker が参照してよいのは以下に限る。

- Chat が渡した短期的文脈
- 手順記憶
- 履歴検索

Worker は人物記憶を直接参照しない。
人物記憶の更新判断もしない。

---

## 4. 入力

Worker は Chat からのみタスクを受ける。

```json
{
  "event_id": "EVT-20260412-000118",
  "root_event_id": "EVT-20260412-000100",
  "command": "/investigate|/maintain",
  "objective": "何を調べるか",
  "scope": "対象範囲",
  "constraints": ["編集禁止", "非破壊", "根拠提示"],
  "references": ["EventId", "ファイルパス", "ログパス"],
  "expected_output": "短い要約＋根拠"
}
```

---

## 5. 出力

Worker の返却形式は短く固定する。長い自由作文は返さない。

```json
{
  "event_id": "EVT-20260412-000118",
  "status": "completed|failed|blocked",
  "summary": "結論の短い要約",
  "findings": [
    "重要な事実1",
    "重要な事実2"
  ],
  "evidence": [
    {"path": "src/app/foo.ts", "note": "初期化処理"},
    {"event_id": "EVT-20260410-000441", "note": "類似障害"}
  ],
  "next_recommendation": "chat返答 or coder委譲に向く次アクション",
  "skill_candidate": {
    "is_candidate": true,
    "title": "再利用候補の手順名"
  }
}
```

---

## 6. Command

v0.1 では以下の command のみを持つ。

### `/investigate`

既存コード、ログ、履歴、文書を調査する。

### `/maintain`

索引更新、要約生成、skill 候補整理、整合チェックなどの整備を行う。

---

## 7. やってよいこと

Worker は以下を行ってよい。

- リポジトリやログの横断探索
- 類似イベントや類似障害の検索
- 比較・分類・候補抽出
- 調査結果の短い要約
- skill 候補の抽出
- 索引更新や整合チェックなどの後処理

---

## 8. やってはいけないこと

Worker は以下を行わない。

- 実ファイル編集
- 危険コマンド実行
- ユーザーとの直接対話
- 長期人物記憶の更新
- 最終判断の確定
- 外部接続の自動拡張

---

## 9. 記憶方針

Worker が保存候補として出してよい情報は、次の粒度に限る。

- build command
- repo 固有の構造知識
- 依存関係の罠
- 失敗時の初手確認
- 再利用価値のある比較結果

会話全文や感想は保存しない。

---

## 10. Hook 詳細

### TaskStart Hook

- 対象範囲を確認する
- 編集禁止であることを確認する
- 範囲外の探索を自動拡張しないことを確認する

### ResultBeforeReturn Hook

- 返却内容が短いことを確認する
- 根拠があることを確認する
- 編集指示や危険操作提案が混ざっていないことを確認する

### MemoryCandidate Hook

以下の条件を満たす場合に skill 候補または索引候補を出す。

- 調査結果が次回にも再利用価値を持つ
- repo 固有の罠や構造知識が明確になった
- 過去イベントとの対応関係が整理できた

---

## 11. イベント

Worker は最低でも以下のイベントを出す。

- `worker.task.started`
- `worker.search.performed`
- `worker.compare.completed`
- `worker.summary.created`
- `worker.skill.candidate`
- `worker.task.completed`
- `worker.task.failed`

---

## 12. 失敗時動作

調査範囲が不足している場合は、勝手に外部へ広げない。

Chat に対して以下を返す。

- 何が不足しているか
- なぜ断定できないか
- 追加で必要な調査範囲は何か

推測だけで断定しない。

---

## 13. 他コンポーネントとの関係

- Worker は Chat からのみ起動される
- Worker は Coder と直接会話しない
- Worker の結果統合は Chat が行う
- Worker は人物記憶を持たない

---

## 14. 一文要約

Worker は読む。
