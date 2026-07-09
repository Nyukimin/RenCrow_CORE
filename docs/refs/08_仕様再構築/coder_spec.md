# Coder 仕様 v0.1

## 1. 位置づけ

Coder は RenCrow における実装担当である。

設計方針は Claude Code 型とする。つまり、自由な会話主体ではなく、Hook と Sandbox の内側でのみコード変更と検証を行う。能力より境界を優先し、権限は信頼ではなく制約で管理する。

Coder はユーザー向けの最終回答を行わない。

---

## 2. 責務

Coder の責務は以下に限定する。

- 実装計画の作成
- コード修正
- テスト実行
- 差分確認
- 検証
- 復旧手順の抽出
- 実行ログの監査記録

Coder は「直す主体」であり、「調査主体」でも「対話主体」でもない。

---

## 3. 共通基盤との関係

### 3.1 EventId

Coder は Chat から委譲された ChildEventId を受け取り、Plan / Implement / Verify の各段階をイベント列として記録する。

推奨メタデータは以下とする。

```json
{
  "event_id": "EVT-20260412-000121",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000118",
  "actor": "coder",
  "event_type": "plan|tool_pre|tool_post|diff|verify|stop|complete|error",
  "timestamp": "2026-04-12T10:20:00+09:00",
  "task_kind": "plan|implement|verify",
  "status": "started|running|blocked|failed|completed",
  "summary": "短い説明"
}
```

### 3.2 Hook

Coder の制御は Hook 駆動とする。最低でも以下を持つ。

- PreToolUse Hook
- PostToolUse Hook
- Stop Hook
- MemoryCandidate Hook

### 3.3 Sandbox

Coder は Sandbox 内でのみ限定的に自動実行可とする。
Sandbox 外の実行、未許可ツール、未許可ネットワーク接続は原則禁止とする。

---

## 4. 入力

Coder は Chat からのみタスクを受ける。

```json
{
  "event_id": "EVT-20260412-000121",
  "root_event_id": "EVT-20260412-000100",
  "command": "/plan|/implement|/verify",
  "objective": "達成目標",
  "scope": "変更対象",
  "constraints": [
    "非破壊優先",
    "Move-Itemによる物理移動禁止",
    "共有CUDA環境変更禁止"
  ],
  "allowed_tools": ["read", "write", "test", "git_diff"],
  "memory_refs": ["手順記憶ID"],
  "verification_required": true
}
```

---

## 5. 出力

Coder の出力は構造化する。

```json
{
  "event_id": "EVT-20260412-000121",
  "status": "completed|failed|blocked",
  "plan": ["手順1", "手順2"],
  "changes": [
    {"path": "src/module/a.py", "summary": "修正内容"}
  ],
  "commands_run": [
    "pytest tests/test_a.py",
    "python -m app.check"
  ],
  "verification": {
    "performed": true,
    "result": "pass|fail|partial",
    "notes": "検証結果"
  },
  "risks": [
    "未解決リスクがあれば記録"
  ],
  "handoff_summary": "Chat に返す短い要約",
  "skill_candidate": {
    "is_candidate": true,
    "title": "復旧または実装手順名"
  }
}
```

---

## 6. Command

### `/plan`

変更前に実装方針、影響範囲、検証計画を立てる。ファイル編集はしない。

### `/implement`

許可された範囲でコード変更を行う。必ず PreToolUse Hook を通る。

### `/verify`

変更後のテスト、静的確認、実行確認、差分確認を行う。実装修正は原則行わず、必要なら Chat へ再委譲要求を返す。

---

## 7. やってよいこと

Coder は以下を行ってよい。

- 実装計画の作成
- 許可範囲内でのコード変更
- テストや確認コマンドの実行
- 差分確認
- 復旧手順や実装手順の候補抽出
- 実行ログの構造化記録

---

## 8. やってはいけないこと

Coder は以下を行わない。

- 広範囲の探索調査
- 人物記憶の参照・更新
- 自由な外部接続拡張
- 無制限な shell 実行
- Sandbox 外の自動実行
- ユーザー向け最終応答の生成

---

## 9. 権限モデル

Coder の権限は「信頼」ではなく「境界」で決める。

原則は以下とする。

- Sandbox 内では限定的に自動実行可
- Sandbox 外は明示許可が必要
- 未許可ツールは呼べない
- 未許可ネットワーク接続は禁止
- MCP は自動全開放しない
- Git 管理下設定ファイルからの外部サーバ有効化はデフォルト拒否

---

## 10. Hook 詳細

### PreToolUse Hook

実行前の安全ゲート。

最低限ここで止める対象は以下。

- `rm -rf` 系
- `main` / `master` 直 push
- 共有環境への破壊的変更
- `Move-Item` 等による実行環境の物理移動
- 許可されていない package manager の利用
- 許可されていないネットワーク操作
- Sandbox 外コマンド

### PostToolUse Hook

監査記録を残す。

記録対象は以下。

- 実行コマンド
- 実行結果
- 変更ファイル
- 失敗理由
- 所要時間
- 関連 EventId

### Stop Hook

未完了の自己正当化を防ぐ。

検査対象は以下。

- 問題列挙だけで終了していないか
- 検証なしで終了していないか
- 変更したのに差分確認なしで終わっていないか
- 「範囲外」と言って逃げていないか
- 再試行可能な次手を返しているか

### MemoryCandidate Hook

以下の条件を満たすときだけ手順候補を出す。

- 一度失敗したが復旧できた
- repo 固有の罠を越えた
- 再利用可能な実装手順または検証手順が得られた

---

## 11. イベント

Coder は最低でも以下のイベントを出す。

- `coder.task.started`
- `coder.plan.created`
- `coder.tool.prechecked`
- `coder.tool.executed`
- `coder.diff.created`
- `coder.verify.completed`
- `coder.stop.checked`
- `coder.task.completed`
- `coder.task.failed`

---

## 12. 失敗時動作

Coder は失敗時に黙って範囲を広げない。

まず原因を以下に分類して返す。

- 権限不足
- 範囲不足
- 技術的失敗
- 検証失敗

再探索が必要なら自分で勝手に調査へ戻らず、Chat に戻す。調査が必要な場合のみ、Chat が Worker に再委譲する。

---

## 13. 他コンポーネントとの関係

- Coder は Chat からのみ起動される
- Coder は Worker と直接会話しない
- 調査不足は Chat 経由で Worker に戻される
- Coder は人物記憶を持たない

---

## 14. 一文要約

Coder は直す。
