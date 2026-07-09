# observability.md

## 目的

この文書は、RenCrow における観測仕様を定義する。
対象は Chat / Worker / Coder / Hook / Scheduler の動作であり、
最終応答だけでなく、途中の判断・実行・拒否・失敗・復旧を追跡可能にすることを目的とする。

Observability の中心は以下の 3 系統で構成する。

- Event stream
- Hook records
- Audit logs

この 3 系統は役割が異なるため、保存先・用途・閲覧者を分ける。

---

## 基本原則

1. 観測対象は「応答」ではなく「処理列」とする。
2. すべての主要処理は EventId で結ぶ。
3. Event は状態遷移を表し、Audit は実行痕跡を表し、Hook record は強制点の判断を表す。
4. 人が読むための要約と、機械が追うための構造化記録を分ける。
5. 観測のために本体処理を壊さない。観測失敗は本体失敗と分離する。
6. 監視は最終責任を持たない。最終判断は常に Chat が持つ。

---

## 観測対象

### 1. Chat

Chat では以下を観測する。

- request 受信
- 文脈読込
- 記憶参照
- ルーティング判断
- 委譲生成
- 結果統合
- 保存候補抽出
- 応答完了

### 2. Worker

Worker では以下を観測する。

- 調査開始
- 検索実行
- 比較実行
- 根拠抽出
- 要約生成
- skill 候補抽出
- 整備ジョブ開始 / 完了 / 失敗

### 3. Coder

Coder では以下を観測する。

- plan 開始 / 完了
- implement 開始 / 完了
- verify 開始 / 完了
- ツール実行前後
- 差分生成
- 検証結果
- 未完了停止の有無

### 4. Hook

Hook では以下を観測する。

- 発火条件
- 対象イベント
- 判定結果
- 拒否理由
- 記録成功 / 失敗

### 5. Scheduler / Maintenance

- ジョブ起動時刻
- 対象スコープ
- 入出力件数
- スキップ理由
- 異常終了理由

---

## データ種別

### Event stream

役割:
状態遷移を追うための中核記録。

用途:
- 実行フロー追跡
- 失敗復旧の分岐追跡
- セッション再構成
- ダッシュボード集計

必須項目:

```json
{
  "event_id": "EVT-20260412-000121",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000118",
  "actor": "chat|worker|coder|hook|scheduler",
  "event_type": "request|analysis|delegation|tool_pre|tool_post|verify|memory_write|stop|error|complete",
  "status": "started|running|blocked|failed|completed",
  "timestamp": "2026-04-12T10:12:34+09:00",
  "summary": "短い説明"
}
```

### Hook record

役割:
強制点における機械的判断の記録。

用途:
- 危険操作拒否の証跡
- Stop 条件の判定根拠
- 許可 / 拒否の再現

必須項目:

```json
{
  "hook_id": "HOOK-20260412-000401",
  "event_id": "EVT-20260412-000121",
  "hook_stage": "delegation_pre|tool_pre|tool_post|stop_pre|memory_pre|complete_post",
  "actor": "hook",
  "target_actor": "chat|worker|coder",
  "result": "pass|deny|warn|error",
  "rule_refs": ["RULE-SAFE-003", "RULE-HOOK-STOP-002"],
  "reason": "拒否または警告の理由",
  "timestamp": "2026-04-12T10:13:10+09:00"
}
```

### Audit log

役割:
人間が後から読むための実行痕跡。

用途:
- コマンド監査
- 変更ファイル確認
- テスト実行確認
- インシデントレビュー

例:

```json
{
  "audit_id": "AUD-20260412-001011",
  "event_id": "EVT-20260412-000121",
  "actor": "coder",
  "category": "command|file_change|test_run|network_attempt",
  "payload": {
    "command": "pytest tests/test_logging.py",
    "exit_code": 0,
    "changed_files": ["src/logging/core.py"],
    "duration_ms": 1880
  },
  "timestamp": "2026-04-12T10:14:02+09:00"
}
```

---

## 保存方針

### 1. 保存先

- Event stream: `events/`
- Hook record: `audit/hooks/`
- Audit log: `audit/commands/`, `audit/files/`, `audit/tests/`
- 集計結果: `runtime/observability/`

### 2. 粒度

Event は細かすぎないこと。
1つの意味のある状態遷移を 1 イベントとする。

Audit は必要なら細かくてよい。
特に Coder の tool 実行は個別に残す。

Hook record は必ず 1 発火 1 記録とする。

### 3. 保持期間

- Event stream: 長期保持
- Hook record: 長期保持
- Audit log: 中期〜長期保持
- 集計キャッシュ: 短期保持

### 4. 失敗時の扱い

観測書き込み失敗は別イベントとして記録する。
観測に失敗しても、本体処理を即失敗扱いにはしない。
ただし、Hook record の記録失敗は安全上重要なため警告を上げる。

---

## ビュー仕様

人が見る観測ビューは最低限 4 つ持つ。

### 1. Session timeline view

目的:
1 セッションの流れを時系列で追う。

表示項目:
- 時刻
- actor
- event_type
- summary
- status
- parent/root 関係

### 2. Task trace view

目的:
1 RootEventId に紐づく Worker/Coder の派生処理を木構造で追う。

表示項目:
- root_event_id
- delegation tree
- hook 判定
- 失敗位置
- 戻り先

### 3. Risk / deny view

目的:
拒否・警告・高リスク処理だけを見る。

表示項目:
- hook_stage
- deny / warn 件数
- rule_refs
- 対象コマンド
- 発生 actor

### 4. Maintenance health view

目的:
Worker の定期整備の健康状態を見る。

表示項目:
- job 名
- 最終実行時刻
- 対象件数
- 成功 / 失敗
- スキップ理由

---

## 主体ごとの閲覧責務

### Chat が見るもの

- Session timeline の要約
- Task trace の要点
- deny / fail の結果
- skill 候補の有無

Chat は詳細ログ全文を毎回読まない。
必要時のみ Worker に再調査を依頼する。

### Worker が見るもの

- 過去 Event stream
- Maintenance health
- 調査対象に関する audit 抜粋

Worker は人物記憶ビューを見ない。

### Coder が見るもの

- plan/implement/verify に関連する audit
- 直近の deny / warn
- 対象リポジトリに関連する復旧履歴

Coder は profile 系メモリを見ない。

### 人間運用者が見るもの

- Risk / deny view
- Task trace view
- Audit log 詳細
- Maintenance health view

---

## 指標

v0.1 で持つべき最小指標は以下。

### フロー指標

- request 完了率
- delegation 発生率
- Worker 経由率
- Coder 経由率
- verify 実行率

### 安全指標

- PreToolUse deny 件数
- Stop hook warn 件数
- Sandbox 外要求件数
- 未許可ネットワーク要求件数

### 品質指標

- verify 成功率
- implement 後再修正率
- Worker 調査の再依頼率
- skill 候補採用率

### 運用指標

- maintenance job 成功率
- index 更新所要時間
- observability 書き込み失敗件数

---

## アラート方針

アラートは本当に必要なものだけに絞る。

高優先度:
- Hook record 記録失敗
- 高リスク deny の連続発生
- verify 失敗の連続発生
- maintenance の連続失敗

中優先度:
- Worker の再調査率上昇
- Coder の Stop warn 増加
- audit 書き込み遅延

低優先度:
- skill 候補未採用の増加
- session timeline の欠損軽微エラー

---

## 禁止事項

- Observability を理由に人物記憶へ自動書き込みしない
- Audit log を Event stream の代わりに使わない
- Hook deny の理由を省略しない
- すべてのログを Chat に毎回読ませない
- 観測ダッシュボードを最終判断装置にしない

---

## 典型フロー例

### 1. 調査のみ

1. Chat が request.received
2. Chat が delegation.created (/investigate)
3. Worker が task.started
4. Worker が search / compare / summary を記録
5. Worker が result を返す
6. Chat が integrated / completed

### 2. 実装あり

1. Chat が request.received
2. Chat が /plan を Coder に委譲
3. Coder が plan.created
4. Chat が /implement を委譲
5. Coder が tool_pre → tool_post を繰り返す
6. Coder が verify.completed
7. Stop hook が completed を許可
8. Chat が統合して返答

### 3. Hook 拒否

1. Coder が tool 実行要求
2. PreToolUse hook が deny
3. Hook record 保存
4. Coder が blocked で戻る
5. Chat が failure_recovery に従って次手を決定

---

## v0.1 の要点

Observability の本体は「全部見ること」ではない。
何が起きたか、どこで止まったか、なぜ拒否されたか、どこへ戻すべきかを追えることが本体である。

この仕様では、

- Event = 状態遷移
- Hook record = 強制判断
- Audit = 実行痕跡

の 3 分離を守る。

この分離が崩れると、後から追えなくなる。
