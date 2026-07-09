md_content = """# Coder3 仕様（Claude API 専用・PicoClaw用）

作成日: 2026-02-24  
対象: PicoClaw / OpenClaw 運用における「Coder3」  
目的: 高品質コーディング/推論をClaudeで行う。ただし **OAuthトークンやブラウザセッションは使わず**、**APIキー経由**に限定し、PicoClawの「承認フロー」と整合させる。

---

## 0. 前提（決定事項）
- OAuthトークンは使わない（決定）
- PicoClawはユーザー承認を求めるタイプの自動化を行う（決定）
- 自動承認モード（Auto-Approve）を付ける（決定）
- Coder3は **Claude APIキー** による **APIアクセス** を行う
- Coder3は **Chrome 操作の提案（plan）** を生成できる
- Chrome 操作の実行は **承認フロー（job_id）を通した場合のみ** 可能
- 承認方式: 都度承認（`/approve <job_id>`）または 永続承認（期限付き・Scope 制限）

---

## 1. 全体アーキテクチャ：Chat / Worker / Coder の関係

### 1-1. 役割の分離（責務）
- Chat（会話・司令塔）
  - ユーザーとの対話窓口（LINE/Slack等）
  - 依頼の意図整理、ゴール定義、制約の確定
  - ルーティング（WorkerかCoderか）を決める
  - 承認フローを開始・管理する（承認要求メッセージの生成、押下結果の解釈）
  - 最終出力をユーザー向けに整形して返す

- Worker（実行・道具係）
  - 定型処理・分解・集約・変換・検証の実務（ツール実行）
  - Web取得、ファイル編集、ログ整理、差分適用、テスト実行など
  - **ブラウザ操作**（MCP Chrome 経由で Chrome を制御）
  - “作業を進める”が、破壊的操作は承認ゲートに従う
  - 失敗時は原因を構造化し、Chatに「次の打ち手」を返す

- Coder（高難度の設計・実装係）
  - 仕様策定の骨格、複雑な推論、難しいコード生成・レビュー
  - 出力は「提案（plan）」と「適用差分（patch）」を基本とする
  - 実際の適用（書込み/実行）はWorkerが担当（＋承認ゲート）

### 1-2. 不変ルール（統治）
- Chatは“決める”、Workerは“動かす”、Coderは“考えて書く”
- 実行（適用）に入る前に、必ず承認状態を確認する
- Coderは原則「案と差分」まで。直接の破壊的実行はしない

### 1-3. 典型的な処理フロー（標準）
1. ユーザー → Chat：依頼
2. Chat：意図/ゴール/制約を短く確定、`job_id`発行
3. Chat → Worker：現状収集（ファイル/ログ/環境情報）
4. Chat：難度判定し、必要なら Coder3 に投げる
5. Coder3 → Chat：`plan` + `patch` + `risk` + `need_approval`
6. Chat → ユーザー：承認要求（job_id/影響範囲/取り消し可否）
7. ユーザー承認 → Chat：承認状態更新
8. Chat → Worker：適用（patch反映、テスト、結果収集）
9. Worker → Chat：結果（成功/失敗/ログ/次の手）
10. Chat → ユーザー：結果報告（必要なら再承認へ）

---

## 2. Coder3 の位置づけ（Coderファミリー内）
- Coder1 / Coder2 が存在しても、Coder3は明確に「Claude API専用」の枠
- ルーティングはChatが決める（Coder3が自分で仕事を取りに行かない）
- Coder3の強みは「失敗コストが高い局面の成功率を上げる」こと

---

## 3. Coder3 の役割
### 3-1. 担当領域
- 仕様の整理・実装方針・設計判断が必要なコード生成
- 複雑なリファクタ、テスト設計、レビュー指摘（根拠付き）
- 依存関係や既存コードの整合性を維持した修正案の作成

### 3-2. 非担当（直接実行しない）
- ブラウザ UI 操作、ファイル編集、コマンド実行などの**直接実行**
  - これらは **plan（提案）として生成** し、Worker に渡す
  - Worker が **承認済み job_id** を確認してから実行
- OAuth/セッション/クッキーの取得や流用（セキュリティリスク）
- 破壊的操作（削除/上書き）を **承認なし** で実行

---

## 4. 安全ポリシー
### 4-1. 認証
- 認証方式: Claude公式APIに準拠（APIキー）
- 保存: 環境変数 / シークレットストア（平文ファイル保存は不可）

### 4-2. データ取り扱い
- 送信する入力は「必要最小限」
- 機密（鍵/トークン/個人情報/社外秘）は **原則マスク**して送る
- 例外が必要なら「承認画面で明示」し、ユーザーが許可した場合のみ送る

---

## 5. ルーティング（いつCoder3に投げるか）
### 5-1. ルーティング条件（推奨）
- 「生成品質が作業成功率に直結」するタスク
  - 例: 仕様策定の骨格、複雑な推論、曖昧要求の解消、重大バグの原因切り分け
- 1回の失敗コストが大きい（手戻りが重い）タスク

### 5-2. ルーティング禁止（例）
- 低難度の定型作業（Worker/Localで十分）
- 実行・操作系で承認フローが未整備のもの

---

## 6. 承認フロー統合（必須）
Coder3は「案を作る」ことはできるが、**実行（適用）には承認が必要**。

### 6-1. 標準フロー
1. Chatがジョブ作成（`job_id`付与）
2. Coder3が「提案（plan）」と「適用差分（patch）」を生成
3. Chatがユーザーへ承認要求を送る（LINE/Slack等）
4. 承認されればWorkerが適用（書込み/実行）へ進む
5. 結果を通知、ログへ保存

### 6-2. 承認要求メッセージの必須項目
- `job_id`（押下対象を確定するID）
- 操作要約（1〜3行）
- 影響範囲（どのファイル/どの環境に触るか）
- 取り消し可否（ロールバック可/不可、または代替案）
- 参考: コスト見積もり（任意、後述）

---

## 7. 自動承認モード（Auto-Approve）
### 7-1. 目的
承認待ちで詰まる場面を減らしつつ、事故範囲を限定する。

### 7-2. 仕様（最低限）
- Auto-Approve は **範囲（Scope）を持つ**
  - 対象ジョブ種別（例: docs生成のみ）
  - 対象ツール（例: ファイル書込み禁止、差分生成まで）
  - 対象パス（例: `docs/` のみ）
  - 有効期限（例: 30分 / 2時間）
- Auto-Approve の切替は **即時OFF可能**（最優先操作）

### 7-3. 強制的に承認が必要なケース（Auto-Approveでも例外）
- 削除、リネーム、広範囲の上書き
- 機密が含まれる可能性が高い送信
- 外部公開（SNS投稿、公開リポジトリへのpush等）
- コストが閾値超過（後述）

---

## 8. 入出力インタフェース（I/F）
### 8-1. 入力（Coder3へのリクエスト）
- `job_id`: string（必須）
- `task_type`: enum（例: `design` / `codegen` / `review` / `refactor`）
- `context`: string（必要最小限の背景）
- `constraints`: object（言語、環境、禁止事項、出力形式など）
- `artifacts`: 任意（該当ファイル抜粋、差分、ログ断片）

### 8-2. 出力（Coder3からのレスポンス）
- `job_id`: string（必須）
- `plan`: string（手順・判断理由。短く）
- `patch`: string（diff形式推奨。なければ変更案の箇条書き）
- `risk`: object（破壊的変更/互換性/手戻り可能性）
- `cost_hint`: object（概算トークン/上限に近い等のサイン）
- `need_approval`: boolean（通常true。Auto-Approve判定材料にする）

---

## 9. コスト制御
### 9-1. 上限
- 1ジョブあたり `max_tokens` を設定（例: 8k/16k等。運用で調整）
- 連続リトライは回数制限（例: 2回まで）

### 9-2. コスト閾値による停止
- 推定コストが閾値を超える場合、Coder3は「続行可否を承認要求」に切り替える

---

## 10. ログとトレーサビリティ
- すべての入出力に `job_id` を付ける（必須）
- 推奨: 重要処理単位に `event_id` を付与（後で追跡しやすい）
- 保存先はPicoClawの標準ログ/Obsidian等に合わせる（別仕様に従う）

---

## 11. エラー処理
- APIエラー（401/429/5xx）は分類して返す
  - 認証エラー: 即停止（ユーザー通知）
  - Rate limit: バックオフ（最大N回）
  - 生成失敗: 入力不足/矛盾を `plan` に明記し、追加情報を要求（ただし質問は最小限）

---

## 12. 設定（Config）例
```yaml
coder3:
  provider: claude_api
  base_url: "https://api.anthropic.com"
  api_key_env: "ANTHROPIC_API_KEY"
  model: "claude-xxx"   # 運用で固定
  max_tokens: 16000
  retry_max: 2
  timeout_sec: 60

routing:
  # Chatが決める前提の補助ルール（例）
  prefer_coder3_when:
    - "high_failure_cost"
    - "complex_refactor"
    - "ambiguous_spec"
  avoid_coder3_when:
    - "simple_template_work"
    - "no_approval_flow"

approval:
  required_by_default: true
  auto_approve:
    enabled: false
    scope:
      allowed_task_types: ["design", "review"]
      allowed_paths_prefix: ["docs/"]
      deny_operations: ["delete", "rename", "push_public"]
    ttl_minutes: 60
    hard_require_approval:
      - "delete"
      - "rename"
      - "send_sensitive"
      - "push_public"
      - "cost_over_limit"
      
```
---

## 13. MCP Chrome 統合（ブラウザ操作）

### 13-1. 目的
Coder3 が Web サイトの情報収集やブラウザ操作を必要とする場合、MCP Chrome 経由で Chrome を制御する。

### 13-2. アーキテクチャ
```
Ubuntu (PicoClaw)
  │
  ├─ Coder3: Chrome 操作の plan を生成（例: "example.com にアクセスしてタイトルを取得"）
  │           ↓
  ├─ Chat: 承認要求を送信（job_id 付き）
  │           ↓
  └─ 人間: /approve <job_id> または 永続承認（期限付き）
              ↓
Ubuntu (Worker)
  ↓ HTTP
Win11 (mcp-chrome-bridge)
  ↓ Native Messaging
Chrome: 承認された操作のみ実行
```

### 13-3. 承認フローの必須化
- Chrome 操作は **すべて job_id で追跡**
- 承認なしでは実行されない
- 承認要求メッセージには以下を含む:
  - `job_id`
  - 操作内容（URL、クリック対象、入力内容等）
  - リスク（外部送信、個人情報、セッション利用等）
  - `uses_browser: true` フラグ

### 13-4. 利用可能な Chrome 操作
MCP Chrome 経由で以下の操作が可能:
- **chrome_navigate**: 指定 URL に移動
- **chrome_click**: 指定セレクタの要素をクリック
- **chrome_get_text**: 指定セレクタのテキストを取得
- **chrome_screenshot**: ページのスクリーンショットを取得
- **chrome_execute_script**: JavaScript を実行（高リスク・要明示承認）

### 13-5. セキュリティ制約
- ログイン状態の流用は **原則禁止**（例外: 明示的承認が必要）
- 機密情報（パスワード、トークン等）の入力は **高リスク承認**
- 外部送信を伴う操作は **必ず承認要求に明記**
- セッション/クッキーの取得・送信は **禁止**

### 13-6. Auto-Approve 対象外
Chrome 操作は Auto-Approve の対象外（例外なく承認必須）。
理由: ブラウザ操作は予期しない副作用（外部送信、ログイン状態変更等）のリスクが高いため。

---

## 14. 受け入れ条件（Acceptance）

- OAuthトークン/ブラウザセッションを一切使わずに動作する
- 承認フローなしに破壊的変更が適用されない
- **Chrome 操作は job_id で追跡され、承認なしで実行されない**
- **ブラウザ操作の承認要求には `uses_browser: true` フラグが含まれる**
- Auto-ApproveはScopeとTTLで事故範囲が限定され、即OFFできる
- **Chrome 操作は Auto-Approve の対象外**（例外なく承認必須）
- すべての処理が job_id で追跡できる
- Chat/Worker/Coderの責務が混ざらず、実行はWorker、意思決定はChat、設計/生成はCoderで運用できる
