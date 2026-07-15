# 統合実装仕様: I/O・Worker契約

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../02_実装仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: 現行input / output、Worker、承認境界

## 4. I/O契約

### 4.1 現行入力境界

単一の`InputMessage`へ全経路を統合せず、境界ごとの型を使う。

| 境界 | 現行型 | 主なfield |
| --- | --- | --- |
| channel / Viewerからorchestrator | `ProcessMessageRequest` | `session_id`, `channel`, `chat_id`, `user_message`, `to`, `attachments` |
| domain task | `task.Task` | `job_id`, `user_message`, `channel`, `chat_id`, `attachments`, `recipient`, `forced_route`, `route` |
| module Chat | `modules/chat.Input` | `session_id`, `channel`, `user_id`, `to`, `text`, `audio` |

### 4.2 RoutingDecision

現行正本型は`internal/domain/routing.Decision`とする。

- `route`
- `confidence`
- `reason`
- `evidence[]`: `source`, `matched`, `route`, `confidence`, `reason`

`local_only`等のpolicy flagはroute decisionへ暗黙に追加せず、対象policyまたはtask / config境界で明示する。

### 4.3 WorkerInput

`WorkerInput`という統合型は現行code、producer、consumer、validator、contract testに存在しないため、現行正本契約として採用しない。

Worker agentは`task.Task`を受け、公開worker moduleは`worker.Action`を受ける。単一の`WorkerInput`を新設する場合は、2箇所以上のconsumer、互換性、validation、error contract、testを先に示す。

### 4.4 Worker出力

全経路共通の`WorkerOutput`は置かず、境界ごとの出力を使う。

| 境界 | 現行出力 |
| --- | --- |
| Shiro agent | `Execute(ctx, task.Task) (string, error)` |
| worker module | `worker.Result{job_id,status,output,error,started_at,finished_at,metadata}` |
| message orchestration | `ProcessMessageResponse{response,route,confidence,job_id,verification}` |

`error`、`failed`、`denied`、`canceled`を成功textへ埋め込んで握りつぶさない。

---

## 5. ワーカー仕様
- 公開worker moduleの実行結果は`worker.Result`で構造化する
- agent interfaceがstringを返す経路でもerrorを別戻り値で伝播する
- Coderのproposal / patchとWorkerの実行結果を同一型・同一責務にしない
- external side effect、破壊的操作、公開、課金はpolicyとapproval gateを通す

### 5.1 Coder3 の出力仕様（2026-02-24追加）
Coder3 は「提案と差分」を生成し、実行は Worker に委譲する：
- 必須フィールド:
  - `job_id`: ジョブ識別子
  - `plan`: 手順・判断理由（簡潔に）
  - `patch`: diff 形式の変更案（または変更内容の箇条書き）
  - `risk`: 破壊的変更/互換性/手戻り可能性
  - `need_approval`: 承認要否（通常 true）
- 任意フィールド:
  - `cost_hint`: 概算トークン数や上限接近の警告

詳細: 元 `docs.zip` 内 `05_LLM運用プロンプト設計/Coder3_Claude_API仕様.md`。Coder3 / 承認フロー詳細を触る時だけ一時復帰する。

---

## 6. 承認フロー仕様（2026-02-24追加）

### 6.1 基本原則
- Coder（Coder1/Coder2/Coder3）は「提案（plan）」と「差分（patch）」を生成
- **実際の適用（書込み/実行）は Worker が担当**
- 破壊的操作には**承認が必須**

### 6.2 標準フロー
1. Chat がジョブ作成（`job_id` 付与）
2. Coder が提案・差分を生成
3. Chat がユーザーへ承認要求（LINE/Slack 等）
4. 承認後、Worker が適用実行
5. 結果を通知、ログ保存

### 6.3 承認要求メッセージの必須項目
- `job_id`: 押下対象を確定する ID
- 操作要約（1〜3行）
- 影響範囲（ファイル/環境）
- 取り消し可否（ロールバック可/不可）
- コスト見積もり（任意）

### 6.4 Auto-Approve モード
Auto-Approve は **Scope（範囲）と TTL（有効期限）を持つ**：
- 対象ジョブ種別（例: docs 生成のみ）
- 対象ツール（例: ファイル書込み制限）
- 対象パス（例: `docs/` のみ）
- 有効期限（例: 30分）
- **即時 OFF 可能**（最優先操作）

**強制承認が必要なケース**（Auto-Approve でも例外）:
- 削除、リネーム、広範囲の上書き
- 機密情報を含む可能性が高い送信
- 外部公開（SNS 投稿、public リポジトリへの push）
- コストが閾値超過

詳細: 元 `docs.zip` 内 `05_LLM運用プロンプト設計/Coder3_Claude_API仕様.md` 7章。Coder3 / 承認フロー詳細を触る時だけ一時復帰する。

---
