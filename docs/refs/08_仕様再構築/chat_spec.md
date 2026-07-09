# Chat 仕様 v0.1

## 1. 位置づけ

Chat は RenCrow における唯一の会話主体であり、記憶と判断の中心である。

設計方針は Hermes 型とする。つまり、複数の入口を一つの知的中枢に集約し、短期記憶・人物記憶・手順記憶・履歴検索を束ね、必要なときだけ Worker または Coder に委譲する。

Chat は 3者の中で最上位に位置する。Worker と Coder は Chat から委譲されたタスクのみを処理する。

---

## 2. 責務

Chat の責務は次の5つに限定する。

1. ユーザー入力の理解
2. 会話文脈の維持
3. 記憶の参照と更新判断
4. Worker / Coder への委譲判断
5. 最終応答の生成

Chat は「考える主体」であり、「読む専門家」でも「直す専門家」でもない。

---

## 3. 共通基盤との関係

### 3.1 EventId

すべてのユーザー依頼に対して RootEventId を発行する。
Chat が Worker または Coder に委譲する場合は ChildEventId を発行し、派生関係を保持する。

推奨メタデータは以下とする。

```json
{
  "event_id": "EVT-20260412-000123",
  "root_event_id": "EVT-20260412-000100",
  "parent_event_id": "EVT-20260412-000118",
  "actor": "chat",
  "event_type": "request|delegation|analysis|memory_read|memory_update_decision|complete|error",
  "timestamp": "2026-04-12T10:12:34+09:00",
  "task_kind": "chat",
  "status": "started|running|blocked|failed|completed",
  "summary": "短い説明"
}
```

### 3.2 Hook

Chat は Worker / Coder ほど多くの Hook を持たないが、少なくとも以下の節目で内部チェックを行う。

- 委譲前
- 記憶更新判断前
- 最終応答前

### 3.3 記憶層

Chat は以下の4層を参照できる。

- 短期記憶: 現在セッション専用
- 人物記憶: ユーザーの好み、禁止事項、環境制約
- 手順記憶: 再利用可能な作業手順
- 履歴検索: 過去イベント、実行ログ、成果物情報

更新権限は分ける。

- 短期記憶: Chat が直接更新
- 人物記憶: Chat のみ更新判断可
- 手順記憶: Chat が保存可否判断し、保存実行は Worker
- 履歴検索: Chat は検索起点のみ。整備は Worker

---

## 4. 入力

Chat は複数の入口を一つの形式に正規化して扱う。

```json
{
  "root_event_id": "EVT-20260412-000100",
  "channel": "voice|cli|web|mobile|message",
  "user_text": "ユーザー入力本文",
  "context_refs": ["直前会話ID", "関連EventId"],
  "session_state": {
    "current_topic": "現在の話題",
    "risk_level": "low|medium|high"
  }
}
```

---

## 5. 出力

Chat の出力は次の2種類である。

### 5.1 会話応答

ユーザーに返す自然言語の最終応答。

### 5.2 委譲タスク

Worker または Coder に渡す構造化タスク。

```json
{
  "event_id": "EVT-20260412-000118",
  "root_event_id": "EVT-20260412-000100",
  "target": "worker|coder",
  "command": "/investigate|/plan|/implement|/verify|/maintain",
  "objective": "達成すべき目的",
  "scope": "対象範囲",
  "constraints": [
    "非破壊優先",
    "共有環境変更禁止"
  ],
  "expected_output": "返してほしい形式",
  "relevant_memory_refs": ["手順記憶ID", "EventId"]
}
```

---

## 6. やってよいこと

Chat は以下を行ってよい。

- 音声入力、CLI入力、メッセージ入力など複数入口の正規化
- 現在会話に必要な短期記憶の保持
- 人物記憶、手順記憶、履歴検索の参照
- 自分で返すか、Worker / Coder に渡すかの判定
- Worker / Coder から返ってきた結果の統合
- 最終応答の生成
- 人物記憶更新の可否判断
- 手順記憶化の可否判断

---

## 7. やってはいけないこと

Chat は以下を原則として行わない。

- 大量ログの読み込み
- 広範囲コード探索
- 実ファイル編集
- 危険コマンド実行
- 長時間の索引更新やバックグラウンド整備

これらは Worker または Coder に委譲する。

---

## 8. 判断フロー

Chat は以下の順で判断する。

1. 依頼の意図を解釈する
2. 危険度を判定する
3. 記憶参照の必要性を判定する
4. 自分で答えられるかを判定する
5. 調査が必要なら Worker に `/investigate` を委譲する
6. コード変更や検証が必要なら Coder に委譲する
7. 戻り結果を統合してユーザーへ返す
8. 必要に応じて人物記憶または手順記憶の更新可否を判断する

---

## 9. イベント

Chat は最低でも以下のイベントを出す。

- `request.received`
- `memory.read`
- `delegation.created`
- `result.integrated`
- `memory.update_decided`
- `response.completed`

---

## 10. 失敗時動作

Chat が判断不能な場合は、まず Worker に調査委譲する。

Chat が複数案で迷う場合は、候補を内部比較したうえで最小リスク案を採用する。

Chat は丸投げしない。委譲時には必ず目的、範囲、制約を構造化して渡す。

---

## 11. 他コンポーネントとの関係

- Chat は Worker / Coder に依存する
- Worker と Coder は Chat に依存する
- Worker と Coder は互いに直接会話しない
- 必要な連携は Chat を経由する

---

## 12. 一文要約

Chat は考える。
