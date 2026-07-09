# 実装仕様: 操作ログ JSON 保持 v1

**作成日**: 2026-03-19
**ステータス**: 実装済み
**対象**: RenCrow Viewer / Chat / Worker / Coder 操作ログ

## 1. 概要

RenCrow の操作ログは、短期ライブ監視と後追い監査を分離して扱う。

現行実装の責務分離:

- `EventHub`
  - 短期ライブ監視
- `EventLogStore`
  - persisted operation log の正本
- `MonitorStore`
  - in-memory の agent/job snapshot
- `ExecutionReport`
  - 完了結果の evidence

本仕様の目的は以下。

- Chat / Worker / Coder の挙動を JSON で後追いできるようにする
- operation history を JSONL で保持する
- TTL GC で古いログを定期削除する
- GC 自体も JSON で監査できるようにする
- docs と Skill の役割分担を固定する

## 2. 永続ログの正本

### 2.1 ファイル

operation log 系の正本は以下。

- 操作ログ: `workspace/orchestrator_event_log.jsonl`
- GC 監査ログ: `workspace/orchestrator_event_gc.jsonl`

関連する別系統:

- execution evidence: `workspace/execution_report.jsonl`

`execution_report.jsonl` は operation log ではない。job 完了結果の evidence として別責務で扱う。

### 2.2 1 行 1 event

`orchestrator_event_log.jsonl` は `orchestrator.OrchestratorEvent` を append-only で保存する。

最低限の追跡項目:

- `timestamp`
- `type`
- `from`
- `to`
- `route`
- `job_id`
- `session_id`
- `channel`
- `chat_id`
- `content`

### 2.3 operation log のデータフロー

現行の流れは以下。

1. orchestrator が `OrchestratorEvent` を emit
2. `idleAwareEventListener.OnEvent()` が受ける
3. `EventLogStore.Append()` が persisted JSONL に追記する
4. `EventHub.OnEvent()` が live SSE に流す
5. `MonitorStore.OnEvent()` が agent/job snapshot を更新する

このため、persisted log は operation history の正本であり、`MonitorStore` は観測用導出状態にすぎない。

## 3. 監視イベント

後追い対象イベントは少なくとも以下。

- `message.received`
- `routing.decision`
- `agent.start`
- `agent.dispatch`
- `agent.note`
- `agent.thinking`
- `agent.response`
- `agent.error`
- `mailbox.sent`
- `mailbox.waiting`
- `mailbox.received`
- `mailbox.error`
- `worker.retry_request`
- `worker.classified_failure`
- `entry.stage`

このイベント群で、`mio -> shiro -> coder -> shiro -> mio` の handoff と失敗地点を追える。

## 4. MonitorStore と外部監視 API

### 4.1 MonitorStore の責務

`MonitorStore` は in-memory の監視集約層であり、以下を持つ。

- 直近 logs
- agent snapshot
- job snapshot
- optional evidence lookup
- optional archived log lookup

`MonitorStore` 自体は persisted operation log の正本ではない。

### 4.2 API

外部確認 API は以下。

- `GET /viewer/status`
- `GET /viewer/agents`
- `GET /viewer/agent/detail?id=...`
- `GET /viewer/jobs`
- `GET /viewer/job/detail?job_id=...`
- `GET /viewer/logs`
- `GET /viewer/audit/summary`

### 4.3 `/viewer/logs`

`/viewer/logs` は `scope=live|persisted` を受ける。

- `live`
  - `MonitorStore.Logs()`
- `persisted`
  - `EventLogStore.Query()`

filter:

- `type`
- `agent`
- `route`
- `job_id`
- `session_id`
- `chat_id`
- `limit`

### 4.4 `/viewer/jobs` と `/viewer/job/detail`

`/viewer/jobs` は `MonitorStore.Jobs()` による live job snapshot を返す。  
`/viewer/job/detail` は live job detail に加え、対応する evidence があれば同時に返す。

このため、job 監視は persisted log の単純な再表示ではなく、event reducer による導出表示である。

完了判定、`mio_reported`、live job と evidence の優先順位は `docs/01_正本仕様/実装仕様.md` の「20. Viewer / Evidence / Job 実装仕様」を正本とする。

### 4.5 `/viewer/audit/summary`

`/viewer/audit/summary` は monitor が保持する集約情報を返す。

少なくとも以下を含む。

- `stored_logs`
- `by_type`
- `by_agent`
- `by_route`

## 5. TTL GC

### 5.1 保持ポリシー

基準は `timestamp` ベース TTL。

現行デフォルト:

- `retention_days: 14`
- `gc_interval_minutes: 60`

### 5.2 削除方式

GC は以下の手順で行う。

1. source file を読む
2. retention 内の行だけ temp file に書く
3. `rename` で原子的に置換する

append-only 運用のまま compaction する方式であり、既存行の直接上書きはしない。

### 5.3 GC 監査ログ

各 GC 実行ごとに `orchestrator_event_gc.jsonl` へ以下を残す。

- `started_at`
- `finished_at`
- `source_path`
- `retention_days`
- `before_count`
- `after_count`
- `deleted_count`
- `decode_error_count`
- `timestamp_error_count`
- `status`
- `error`

`status` は以下。

- `ok`
- `partial_error`
- `error`

`partial_error` は malformed JSON や壊れた `timestamp` を読み飛ばした場合に使う。

## 6. 設定

設定キー:

```yaml
viewer_log:
  enabled: true
  path: "./workspace/orchestrator_event_log.jsonl"
  retention_days: 14
  gc_interval_minutes: 60
```

現行実装のデフォルト:

- `path` 未設定時: `./workspace/orchestrator_event_log.jsonl`
- `retention_days <= 0`: `14`
- `gc_interval_minutes <= 0`: `60`
- `enabled == false` でも、デフォルト適用時に `true` へ補正される

最後の点は現行の挙動であり、設定上 `false` を明示しても無効化できない実装になっている。この文書では実装値を正本として記載する。

## 7. docs と Skill の役割分担

仕様・I/F・保持ポリシーの正本は docs に置く。  
運用時の調査順序と不具合対策は Skill に置く。

- docs
  - [実装仕様_操作ログJSON保持_v1.md](/home/nyukimi/RenCrow_CORE/docs/実装仕様_操作ログJSON保持_v1.md)
- skill
  - [SKILL.md](/home/nyukimi/RenCrow_CORE/workspace/skills/log-ops/SKILL.md)

Skill 側に置く内容:

- canonical files
- canonical APIs
- investigation order
- common failure patterns
- reporting rules

## 8. 運用上の見方

基本の調査順序は以下。

1. `job_id` を見つける
2. `/viewer/logs?scope=persisted` または `orchestrator_event_log.jsonl` で handoff を追う
3. `mailbox.waiting` / `mailbox.received` / `agent.error` を見る
4. ユーザー向けの最終結果は `mio -> user` の `agent.response` で確定する

補助:

- 現在状態を見る: `/viewer/status`, `/viewer/agents`
- job 単位で見る: `/viewer/jobs`, `/viewer/job/detail`
- 完了証跡を見る: `execution_report.jsonl`, `/viewer/evidence/*`

## 9. 既知の制約

- `EventHub` は短期ライブ監視用であり、監査の正本ではない
- agent の `offline` は in-memory 状態に依存し、履歴の欠損ではない
- persisted log は JSONL の逐次蓄積であり、分析基盤や検索 DB ではない
- `execution_report.jsonl` は operation log の代替ではない
- `viewer_log.enabled` は現行実装上、事実上 `true` に補正される

## 10. 検証観点

現行仕様として最低限確認すべきこと:

- event が `orchestrator_event_log.jsonl` に append される
- `/viewer/logs?scope=persisted` が persisted log を返す
- `/viewer/jobs` と `/viewer/agent/detail` が monitor snapshot を返す
- GC が retention 外のみ削除する
- GC が `partial_error` を記録できる
- `execution_report.jsonl` が operation log ではなく evidence として別系統で扱われる
- docs と Skill の調査順序が矛盾しない
