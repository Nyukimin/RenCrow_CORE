# 統合実装仕様: Heartbeat・合議制

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../02_実装仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: HeartbeatとDeliberation Mode

## 12. Heartbeat システム（2026-03-01追加）

### 12.1 基本原則
すべてのエージェントは自律的に Heartbeat を発行し、動作状態を Worker へ報告する。

### 12.2 Heartbeat データ構造
```json
{
  "agent_id": "order3",
  "alias": "Gin",
  "timestamp": "2026-03-01T12:00:00Z",
  "status": "idle|busy|error",
  "current_job_id": "job_20260301_001",
  "memory_usage_mb": 5.2,
  "last_task_duration_ms": 1234,
  "error_message": ""
}
```

### 12.3 Worker の集約処理
Worker（Shiro）は：
1. すべてのエージェントから Heartbeat を収集
2. 自身の Heartbeat も含める
3. 統合状態レポートを生成
4. Chat へ報告（必要時のみ）

### 12.4 Heartbeat イベント
- `heartbeat.received`: エージェントから Heartbeat を受信
- `heartbeat.timeout`: エージェントが一定時間応答なし
- `heartbeat.error`: エージェントがエラー状態を報告
- `heartbeat.aggregated`: Worker が統合レポートを生成

### 12.5 設定
```json
{
  "heartbeat": {
    "enabled": true,
    "interval_sec": 30,
    "timeout_sec": 60,
    "report_to_chat": false  // エラー時のみ報告
  }
}
```

---

## 13. 合議制（Deliberation Mode）（2026-03-01追加）

### 15.1 概要
必要時に複数の Order（Coder）が独立して提案を生成し、Worker が集約・比較して Chat が最終決定する。

**目的**:
- 複数 LLM の提案を比較して品質を向上
- Spawn 多用を避けつつ協調動作を実現

### 15.2 動作フロー
```
1. Chat が合議制を有効化（明示コマンド `/deliberate` または自動判定）
   ↓
2. Worker が複数 Order へ並列リクエスト
   - Order1 (Aka / DeepSeek): 提案 A を生成
   - Order2 (Ao / OpenAI): 提案 B を生成
   - Order3 (Gin / Claude): 提案 C を生成
   ↓
3. Worker が提案を集約・比較
   - 類似点・相違点を分析
   - リスク・コストを比較
   ↓
4. Chat が最終決定
   - ユーザーへ選択肢を提示（必要時）
   - 自動選択（低リスク時）
```

### 13.3 合議制を適用すべきケース
- **失敗コストが高いタスク**: 削除、広範囲のリファクタリング
- **曖昧な仕様**: 解釈が分かれる可能性がある
- **複雑な設計決定**: アーキテクチャ変更、API 設計

### 13.4 合議制を避けるべきケース
- **単純なタスク**: タイポ修正、ドキュメント生成
- **コスト制約**: 予算・時間が限られている
- **承認フローなしのタスク**: 自動実行が前提

### 13.5 設定
```json
{
  "deliberation": {
    "enabled": false,  // デフォルト無効
    "auto_trigger": {
      "enabled": false,
      "conditions": [
        "risk=high",
        "ambiguous_spec",
        "architectural_change"
      ]
    },
    "max_parallel_orders": 3,
    "timeout_sec": 120
  }
}
```

### 13.6 ログイベント
- `deliberation.started`: 合議制開始
- `deliberation.proposal_received`: Order からの提案受信
- `deliberation.comparison`: 提案の比較結果
- `deliberation.decision`: 最終決定

---
