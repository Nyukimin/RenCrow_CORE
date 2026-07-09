# 会話LLM（Conversation-centric LLM）仕様 v1.0

**作成日**: 2026-03-05
**バージョン**: 1.0
**対象**: RenCrow Chat/Worker/Coder 対応
**ステータス**: 正本仕様（会話システム設計の一次参照）
**前提**: Spawn 禁止 / Subagent あり

---

## 目次

- [1. 目的と設計原則](#1-目的と設計原則)
- [2. 用語定義（会話とは何か）](#2-用語定義会話とは何か)
- [3. 全体構造（Chat / Worker / Coder の責務分離）](#3-全体構造chat--worker--coder-の責務分離)
- [4. 1ターンの処理フロー](#4-1ターンの処理フロー)
- [5. 記憶レイヤー仕様](#5-記憶レイヤー仕様)
- [6. 想起パック（Recall Pack）の注入仕様](#6-想起パックrecall-packの注入仕様)
- [7. インタフェース（JSON スキーマ）](#7-インタフェースjson-スキーマ)
- [8. 非機能要件](#8-非機能要件)
- [付録A: 実装状況（2026-03-05 時点）](#付録a-実装状況2026-03-05-時点)

---

## 1. 目的と設計原則

### 1.1 目的

単発応答ではなく「時間軸で育つ対話」を成立させる。
LLM 自体はステートレス（記憶を持たない）として扱い、**会話・記憶・人格・ユーザー状態を外部構造で維持**する。

### 1.2 設計原則（固定）

| # | 原則 | 理由 |
|---|------|------|
| 1 | **会話ログ ≠ 知識。全部を VectorDB に入れない。** | ノイズが意味検索を劣化させる |
| 2 | **記憶には寿命と粒度がある（短期/中期/長期/KB を分ける）。** | 検索コストと関連性のトレードオフ |
| 3 | **1ターンの処理は「入力→想起→判断→生成→記録」で閉じる。** | 非同期処理の複雑性を排除 |
| 4 | **Spawn 禁止（並列プロセス増殖なし）。Subagent は同期呼び出しとしてのみ使う。** | 制御フローの予測可能性を保証 |

---

## 2. 用語定義（会話とは何か）

会話の境界と単位を、実装が迷わないように固定する。

### 2.1 会話の構成単位

| 用語 | 定義 | 粒度 |
|------|------|------|
| **Message** | 発話の最小単位。speaker と本文、メタ情報を持つ。 | 1発話 |
| **Turn** | **ユーザー入力1回**を起点に、想起・判断・生成・記録が完了するまで。 | 1往復 |
| **Thread** | "いま話している話題の塊"。短期記憶の単位。常に**アクティブ Thread を1つ**持つ。目安: 6〜8往復（最大12メッセージ保持）。 | 1話題 |
| **Session** | 割り込み復帰/再起動復元に必要なまとまり。保持は 24h〜7d 想定。 | 1接続期間 |
| **Conversation** | Thread の連なり。Thread が開始/終了し、終了時に要約されて積み上がる。 | 全履歴 |

### 2.2 追跡用 ID 体系

| ID | 用途 | 形式例 |
|----|------|--------|
| `turn_id` | ターンの識別子 | `turn-0007` |
| `thread_id` | 文脈の識別子 | `thread-0042` |
| `session_id` | セッションの識別子 | `sess-abc123` |
| `event_id` | ログ上の処理単位識別（"処理にも ID" 方針） | `evt-91b3...` |

---

## 3. 全体構造（Chat / Worker / Coder の責務分離）

### 3.1 Chat（会話担当・入口）

| 責務 | 詳細 |
|------|------|
| ユーザー I/F | LINE / Discord / Slack / Web |
| 会話の見た目の生成 | 口調・安全・出力整形 |
| 入力正規化 | Worker に渡すための job_id / turn_id 付与 |
| 最終応答の提示 | Thread の見た目上の連続性維持 |

### 3.2 Worker（中核オーケストレータ）

| 責務 | 詳細 |
|------|------|
| Thread 継続/終了判定 | 話題の切替を検出 |
| 想起パック組み立て | 短期→中期→長期→KB の探索順（軽→重） |
| ルーティング | このターンで何をするか（会話だけ/調査/実装/道具作り） |
| ツール実行 | 可能な範囲で直接実行 |
| Coder 依頼 | 依頼生成・結果の検証・統合 |
| 記録 | 要約・メタ・埋め込み・TTL 管理 |

### 3.3 Coder（実装・重作業担当）

| 責務 | 詳細 |
|------|------|
| コード生成 | スクリプト生成、リファクタ、テスト観点 |
| ツール作り | Skill / CLI / スクリプトの作成 |
| 高難度推論 | 外部 LLM（Web）を使う仕様の穴埋め |
| 返却物の形式 | 差分・ファイル・実行手順（機械可読寄り） |

Coder が複数いる場合（DeepSeek / ChatGPT / Claude）は Worker が用途で選ぶ。
例: 大量コード = Coder1、仕様の詰め = Coder2、高品質推論 = Coder3。

---

## 4. 1ターンの処理フロー

Spawn 禁止に最適化した、同期的な直列フロー。

### 4.1 フロー全体図

```
[1] Chat: ユーザー入力受信
      ├── job_id / turn_id を発行
      └── 現在の短期 Thread state（直近メッセージ）を添付

[2] Worker: Thread 判定
      ├── 継続 or 新規開始
      └── ドメイン推定（例: tech / movie / idol / ops）

[3] Worker: 想起（Recall）
      ├── 短期（Thread turns）
      ├── 中期（Redis hot → DuckDB warm）
      ├── 長期（VectorDB user）
      └── 知識ベース（VectorDB kb:domain）

[4] Worker: 行動計画（Plan）
      ├── 会話応答だけで閉じるか
      ├── 調査が必要か（Web / KB）
      ├── 実装が必要か（Coder 依頼）

[5] Worker → Coder（必要時・同期 Subagent）
      └── CodingRequest を投げる（同一ターン内の同期呼び出し）

[6] Worker: 統合・検証
      ├── 返却物の整合チェック（依存、危険操作、漏れ）
      └── 必要なら再質問を作る（ただし確認質問は最小）

[7] Chat: 最終応答生成
      ├── ユーザー向けに整形（会話として自然）

[8] Worker: 記録
      ├── ログ保存（event_id 付き）
      ├── Thread 終了なら要約 → 中期へフラッシュ
      └── 重要要素は長期へ昇格
```

### 4.2 各ステップの責務境界

| ステップ | 担当 | 入力 | 出力 |
|---------|------|------|------|
| 1. 受信 | Chat | LINE メッセージ | job_id + turn_id + thread_state |
| 2. Thread 判定 | Worker | thread_state + 過去 Thread | 継続/新規 + ドメイン |
| 3. 想起 | Worker | ユーザーメッセージ + ドメイン | recall_pack |
| 4. 行動計画 | Worker | recall_pack + メッセージ | action_plan |
| 5. Coder 呼び出し | Worker→Coder | CodingRequest | 差分/ファイル/手順 |
| 6. 統合・検証 | Worker | Coder 返却物 | 検証済み結果 |
| 7. 応答生成 | Chat | 検証済み結果 + Persona | ユーザー向け応答 |
| 8. 記録 | Worker | ターン全体 | ログ + 記憶更新 |

---

## 5. 記憶レイヤー仕様

### 5.1 レイヤー一覧

| レイヤー | 物理ストア | 寿命 | 目的 |
|---------|-----------|------|------|
| **短期記憶 (Short)** | Chat/Worker の State（RAM） | Thread が生きている間 | 直近文脈、ルーティング、クールタイム等 |
| **中期記憶 (Mid)** | Redis（hot, TTL 24h）→ DuckDB（warm, 7d） | 24h〜7d | 再起動復帰、直近会話検索、最近の合意事項 |
| **長期記憶 (Long)** | VectorDB（namespace `user:<uid>`）+ MetaDB | 原則無期限（忘却導線は別仕様） | 要約・嗜好・方針・継続プロジェクトの意味検索 |
| **知識ベース (KB)** | VectorDB（namespace `kb:<domain>`） | 原則無期限 | RAG 用の資料（映画 DB、運用ルール、社内仕様などは別扱い） |
| **Persona State** | KV（JSON） | プロセス生存期間 | 口調・距離・禁則・キャラ設定の現在値 |
| **User Profile** | KV + 長期要約 | 原則無期限 | 嗜好・優先順位・禁則・プロジェクト文脈 |

**重要**: 短期/中期/長期/KB は**共有資産**。キャラごとに分断しない。キャラ差は Persona State で出す。

### 5.2 Thread 開始/終了条件（境界仕様）

#### 開始条件

| # | 条件 | 例 |
|---|------|-----|
| 1 | 初回入力 | セッション開始時 |
| 2 | 明確な新トピック宣言 | 「別件」「ところで」 |
| 3 | ドメインが変わった | tech → movie |
| 4 | 前 Thread が終了している | タイムアウト後の再入力 |

#### 終了条件

| # | 条件 | 閾値 |
|---|------|------|
| 1 | 明示切替語 | 「別件」「次の話」 |
| 2 | 埋め込み類似度が閾値未満 | cos < 0.75 |
| 3 | ターン上限到達 | 12メッセージ |
| 4 | 無入力タイムアウト | 10分 |

#### 終了時処理（固定）

| # | 処理 | 出力 |
|---|------|------|
| 1 | Thread 要約 | 事実/決定事項/未決を分離 |
| 2 | keywords 抽出 | タグ配列 |
| 3 | embedding 生成 | ベクトル |
| 4 | 中期へ格納 | Redis → DuckDB |
| 5 | 長期昇格判定 | 方針/嗜好/継続案件/禁則のみ昇格 |

---

## 6. 想起パック（Recall Pack）の注入仕様

各ターンで必ず `recall_pack` を作って LLM 入力に注入する。形式を固定するとデバッグが楽になる。

### 6.1 recall_pack の構成

| フィールド | 内容 | 上限 | 探索元 |
|-----------|------|------|--------|
| `short_context` | 直近メッセージ | 最大 12 | Thread turns |
| `mid_summaries` | 直近スレッド要約 | 最大 3 | Redis → DuckDB |
| `long_facts` | 長期からの重要点（箇条書き） | 最大 5 | VectorDB user |
| `kb_snippets` | KB からの根拠断片（出典メタ付き） | 最大 5 | VectorDB kb:domain |
| `constraints` | 禁則・優先順位・出力フォーマット規約 | -- | Persona + User Profile |

### 6.2 責務分離

- **Worker** が recall_pack を生成する
- **Chat** は recall_pack の「見せ方」だけを担当する

---

## 7. インタフェース（JSON スキーマ）

実装に落とすための最小 I/F。

### 7.1 Chat → Worker

```json
{
  "job_id": "job-20260305-000123",
  "turn_id": "turn-0007",
  "user_id": "user-ren",
  "channel": "line",
  "user_message": "...",
  "thread_state": {
    "thread_id": "thread-0042",
    "messages_tail": [
      {"speaker": "user", "msg": "...", "ts": "..."},
      {"speaker": "assistant", "msg": "...", "ts": "..."}
    ]
  },
  "constraints": {
    "spawn": false,
    "subagent": true
  }
}
```

### 7.2 Worker → Coder

```json
{
  "job_id": "job-20260305-000123",
  "turn_id": "turn-0007",
  "request_type": "code|spec|refactor|tooling",
  "inputs": {
    "goal": "...",
    "repo_context": "...",
    "constraints": ["powershell", "no_spawn", "event_id_required"]
  },
  "expected_output": [
    "files",
    "diff",
    "run_steps",
    "tests"
  ]
}
```

### 7.3 Worker → Memory (Write)

```json
{
  "event_id": "evt-91b3...",
  "type": "thread_end_summary|long_term_fact|persona_update",
  "payload": {
    "thread_id": "thread-0042",
    "summary": "...",
    "decisions": ["..."],
    "open_questions": ["..."],
    "keywords": ["..."],
    "embedding": "..."
  },
  "ttl": "7d|24h|null"
}
```

---

## 8. 非機能要件

### 8.1 可観測性

全ログに以下の ID を**必ず**付与する:

```
job_id / turn_id / thread_id / session_id / event_id
```

### 8.2 失敗耐性

- 検索や Coder 失敗でも会話は継続する
- 失敗を明示し、推測は分離する

### 8.3 情報流出制御

- 外部 Coder へ渡すコンテキストは Worker が最小化する
- 必要部分だけを CodingRequest に含める

### 8.4 再現性

- Thread 要約と決定事項があれば、再起動後も「同じ方針」に戻れる

---

## 付録A: 実装状況（2026-03-05 時点）

### A.1 実装済み（22/30 = 73%）

| カテゴリ | 実装内容 | ファイル |
|---------|---------|---------|
| ドメイン型 | Message, Thread, ThreadSummary, Session, RecallPack, PersonaState, UserProfile, AgentStatus | `internal/domain/conversation/` |
| I/F 定義 | ConversationEngine, ConversationManager, EmbeddingProvider, ConversationSummarizer | `internal/domain/conversation/` |
| 3層記憶 | RedisStore(短期), DuckDBStore(中期), VectorDBStore(長期) | `internal/infrastructure/persistence/conversation/` |
| 統合層 | RealConversationManager, RealConversationEngine | `internal/infrastructure/persistence/conversation/` |
| LLM連携 | OllamaEmbedder, LLMSummarizer | `internal/infrastructure/llm/ollama/`, `infrastructure/persistence/conversation/` |
| Agent統合 | MioAgent.Chat() 内で BeginTurn/EndTurn | `internal/domain/agent/mio.go` |
| DI | main.go に ConversationEngine 注入 | `cmd/rencrow/main.go` |
| テスト | 統合テスト 9件全通過 | `infrastructure/persistence/conversation/integration_test.go` |

### A.2 未実装ギャップ（8項目）

| # | 項目 | 優先度 | 仕様セクション |
|---|------|--------|--------------|
| 1 | Thread 自動判定（開始/終了） | A | 5.2 |
| 2 | turn_id / event_id | A | 2.2 |
| 3 | UserProfile 自動抽出 | A | 5.1 Persona/User |
| 4 | Worker による想起パック生成（現在は Mio が直接実行） | B | 3.2, 6.2 |
| 5 | KB namespace 分離（`kb:<domain>`） | B | 5.1 |
| 6 | Worker→Coder 構造化 JSON I/F | B | 7.2 |
| 7 | Constraints 実装（禁則・フォーマット） | B | 6.1 |
| 8 | 情報流出制御（Coder へのコンテキスト最小化） | C | 8.3 |

### A.3 設計判断メモ: Worker 委譲について

仕様では Worker が想起パック生成を担当するが、現在の RenCrow では:

- Mio（Chat）が ConversationEngine を直接保持
- BeginTurn/EndTurn は Mio.Chat() 内で呼ばれる
- Worker（Shiro）は会話記憶を持たない

**現実的な妥協案**: 想起パック生成は Mio に残し、Worker への委譲は「Mio が RecallPack を生成した後に Worker/Coder に必要部分だけ渡す」形にする。

### A.4 次の実装優先順位

1. **Thread 自動判定** -- 埋め込み類似度 + タイムアウトで Thread 開始/終了を自動化
2. **turn_id / event_id 追加** -- task.Task に turn_id、ログに event_id を付与
3. **UserProfile 自動抽出** -- EndTurn 時に LLM でプロファイル要素を抽出

---

**最終更新**: 2026-03-05
**バージョン**: 1.0
**メンテナンス**: 実装進捗に応じて付録 A を更新すること
