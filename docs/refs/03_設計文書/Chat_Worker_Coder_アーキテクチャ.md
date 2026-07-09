# Chat / Worker / Coder アーキテクチャ

**最終更新**: 2026-03-02
**バージョン**: v3.0 Clean Architecture

---

## 📋 目次

- [概要](#概要)
- [基本原則](#基本原則)
- [役割と責務](#役割と責務)
  - [Mio（Chat）：全体オーケストレーター](#miochat全体オーケストレーター)
  - [Shiro（Worker）：Coderオーケストレーター](#shiroworkercoderオーケストレーター)
  - [Coder（Aka/Ao/Gin）：実装担当](#coderakaaogin実装担当)
- [コミュニケーションフロー](#コミュニケーションフロー)
- [分散実行と通信フォーマット](#分散実行と通信フォーマット)
- [共通基盤](#共通基盤)
- [コネクタ層](#コネクタ層)
- [実装マップ](#実装マップ)

---

## 概要

RenCrowは、**Chat（Mio）**、**Worker（Shiro）**、**Coder（Aka/Ao/Gin）** の役割を持つエージェントで構成されています。

### 役割一覧

| 役割 | 愛称 | LLM | 責務 |
|------|------|-----|------|
| **Chat** | Mio（澪） | Ollama (Chat) | 全体オーケストレーター、ユーザー窓口 |
| **Worker** | Shiro（白） | Ollama (Worker) | Coderオーケストレーター、実行担当 |
| **Coder1** | AO（青） | DeepSeek | 仕様設計、アーキテクチャ検討 |
| **Coder2** | Aka（赤） | OpenAI | 実装、コード生成 |
| **Coder3** | Kin（金） | Anthropic Claude | 高品質コーディング、推論 |
| **Coder4** | Gin（銀） | Gemini | 高速プロトタイピング、実験 |

---

## 基本原則

### 1. すべてのAgentは日本語で会話する

**重要**: Agent間の通信は、**JSONフォーマットのパラメータとして日本語メッセージを送受信**します。プログラムAPIではなく、日本語による自然言語の対話が中心です。

**正確な表現**:
- ❌ 「プログラムAPI（関数呼び出し）のみ」
- ❌ 「構造化データ（JSON）のみ」
- ✅ **「JSONの中に日本語メッセージを含める」**

```
ユーザー: 「hello.goを作成して」
  ↓（日本語）
Mio: 「承知しました。Workerに依頼します」
  ↓（日本語）
Worker: 「了解しました。Ginに依頼します」
  ↓（日本語）
Worker → Gin: 「hello.goを作成してください」
  ↓（日本語）
Gin → Worker: 「こちらのplanとpatchを確認してください」
  ↓（日本語）
Worker: 「実行しました。完了です」
  ↓（日本語）
Mio → ユーザー: 「hello.goを作成しました」
```

**重要なポイント**:
- Worker は Mio に「どのCoderを使うか」を確認しない
- Worker が**自律的に判断**してCoderを選択
- Mio は Coder に関する指示を出さない（Workerに委譲するのみ）

### 2. すべてのやり取りはモニタリング可能

- **ユーザー**: すべての会話をモニタリング可能
- **Mio**: すべての業務の進捗を理解（全体把握）
- **透明性**: どのAgentが何をしているか、常に可視

### 3. 階層的な指揮命令系統

```
ユーザー
  ↓ 会話
Mio（全体オーケストレーター）
  ↓ 指示
Worker（Coderオーケストレーター）
  ↓ 指示・会話
Coder1/2/3
```

**重要な原則**:
- **可視性（モニタリング）**: Mio はすべてを見ている（Coder の作業も含む）
- **指揮命令（指示）**: Mio → Worker → Coder（直接の飛び越しはない）

---

## エージェントの基本構造

すべてのAgentは、以下の共通構造を持ちます：

```
Agent {
  ├─ Personality（個性）
  │   ├─ 愛称（Mio/Shiro/Aka/Ao/Gin）
  │   ├─ 性格・口調
  │   └─ 判断の傾向
  │
  ├─ LLM Provider（思考エンジン）
  │   ├─ Ollama（Mio/Shiro）
  │   ├─ Claude（Gin）
  │   ├─ DeepSeek（Aka）
  │   └─ OpenAI（Ao）
  │
  ├─ Communication（会話能力）
  │   ├─ 日本語で会話
  │   ├─ 他のAgentと対話
  │   └─ 会話履歴の保持
  │
  ├─ Tools（道具・能力）
  │   ├─ ToolRunner（Worker/Coder）
  │   ├─ MCPClient（Worker/Coder）
  │   └─ 役割固有のツール
  │
  ├─ Memory（記憶）
  │   ├─ Session（会話履歴）
  │   ├─ Context（コンテキスト）
  │   └─ ユーザー情報（Mioのみ）
  │
  └─ Responsibility（責務）
      └─ 役割固有の責務
}
```

### 個性（Personality）

**すべてのAgentは個性を持ちます**。個性は会話スタイル、判断の傾向、協働の仕方に影響します。

| Agent | 愛称 | 個性の特徴 | 会話スタイル |
|-------|------|-----------|------------|
| **Mio** | 澪 | ユーザーに寄り添う、優しい、理解者 | 丁寧で親しみやすい、共感的 |
| **Shiro** | 白 | 実務的、効率的、オーケストレーター | 簡潔で明確、結果重視 |
| **Aka** | 赤 | 設計志向、アーキテクト、全体構造を重視 | 論理的、構造化された説明 |
| **Ao** | 青 | 実装志向、エンジニア、実用性重視 | 実践的、具体的 |
| **Gin** | 銀 | 高品質志向、職人、細部へのこだわり | 丁寧、慎重、精密 |

**個性の影響範囲**:
- **会話スタイル**: 同じ内容でも、各Agentが異なる表現で伝える
- **判断の傾向**: 同じタスクでも、アプローチや優先順位が異なる
- **協働の仕方**: 他のAgentとの会話でも、個性が現れる

---

## 役割と責務

### Mio（Chat）：全体オーケストレーター

**愛称**: Mio（澪）
**LLM**: Ollama (Chat)
**温度設定**: 0.7（会話の自然さ重視）

#### 責務

1. **ユーザーの理解**（最重要責務）
   - **ユーザーを知り、寄り添う**
   - ユーザーの意図・文脈を理解
   - データ収集の指示（Workerに依頼）
   - 個別のニーズに合わせた応答
   - 参照: 仕様.md「Chat：ユーザー応答の最終文を生成する唯一の窓口役割」

2. **ユーザーとの唯一の窓口**
   - ユーザーからの入力を受け取る
   - 最終的な回答を生成・返信
   - エラー通知、進捗報告

3. **全体のオーケストレーション**
   - すべての業務の進捗を把握（モニタリング）
   - Worker の作業を監視
   - Coder 達（Aka/Ao/Gin）の作業も把握
   - すべての会話を理解

4. **自分でできる作業の処理**
   - **CHAT（会話・対話）**: Mio が直接処理
   - 説明、相談、雑談、要約、レビュー等

5. **委譲判断（Workerへのみ）**
   - 自分でできない作業 → Worker に委譲
   - **Coder には直接指示を出さない**
   - Worker を通じて Coder に指示

#### Mio が知っていること

- ✅ Worker の存在と作業内容
- ✅ Coder 達（Aka/Ao/Gin）の存在
- ✅ Coder 達が何をしているか
- ✅ Worker ↔ Coder の会話内容
- ✅ すべての業務の進捗状態

#### Mio が指示を出す相手

- ✅ **Worker のみ**
- ❌ Coder には直接指示しない

#### インターフェース

```go
// Mio の主要メソッド
func (m *MioAgent) Chat(ctx context.Context, t task.Task) (string, error)
func (m *MioAgent) DelegateToWorker(ctx context.Context, message string) (string, error)
```

#### 委譲判断フロー

```
ユーザー入力
  ↓
Mio: これは自分でできる会話？
  ├─ YES（CHAT）→ Mio.Chat() で直接応答
  └─ NO → Worker に委譲
         「Workerさん、これをお願いします」
```

#### 設定

```yaml
ollama:
  chat_model: "Chat"  # Mio用
```

---

### Shiro（Worker）：Coderオーケストレーター

**愛称**: Shiro（白）
**LLM**: Ollama (Worker)
**温度設定**: 0.3（確実性重視）

#### 責務

1. **ルーティング分類器としての判定**（核心的責務）
   - タスクのカテゴリ分類（PLAN/ANALYZE/OPS/RESEARCH/CODE等）
   - どのカテゴリか判定し、適切な処理へ
   - 参照: 仕様.md「Router：入力をカテゴリに分岐させ、ワーカー呼び出しを制御するシステム部」

2. **データ蓄積・データ分析・検証**（核心的責務）
   - **情報検索 & 蓄積**
   - データの構造化・集計・傾向分析（ANALYZE）
   - 調査・出典確認・比較・最新情報（RESEARCH）
   - 参照: 仕様.md「Worker：分類器・データ蓄積・データ分析・検証などを担う非コーディング役割」

3. **Coder 達のオーケストレーター（自分自身も含む）**
   - Mio からの委譲を受ける
   - どの Coder を使うか決定
   - Coder1（AO）、Coder2（Aka）、Coder3（Kin）、Coder4（Gin）を統括
   - Coder 達との会話・調整
   - **自分自身もコーディング能力を持つ**

4. **ツール実行**
   - ファイル操作（読み込み、書き込み、削除等）
   - シェルコマンド実行
   - MCP操作

5. **Patch適用（即時実行）**
   - Coder が生成した patch を即座に実行
   - 7種のファイル操作対応（create/update/delete/append/mkdir/rename/copy）
   - Git auto-commit（オプション）

#### Worker が指示を受ける相手

- ✅ **Mio のみ**

#### Worker が指示を出す相手

- ✅ **Coder1（AO）**
- ✅ **Coder2（Aka）**
- ✅ **Coder3（Kin）**
- ✅ **Coder4（Gin）**

#### Worker と Coder の会話例

```
Worker → Gin: 「hello.goを作成してください」
  ↓
Gin → Worker: 「承知しました。以下のplanとpatchを確認してください」
  ↓
Worker: 「確認しました。実行します」
  ↓
Worker: （patch を実行）
  ↓
Worker → Gin: 「実行完了しました。結果を報告します」
  ↓
Worker → Mio: 「タスクが完了しました」
```

#### インターフェース

```go
// Worker の主要メソッド
func (s *ShiroAgent) Execute(ctx context.Context, t task.Task) (string, error)
func (s *ShiroAgent) Classify(ctx context.Context, t task.Task) (routing.Decision, error)
func (s *ShiroAgent) DelegateToCoder(ctx context.Context, coderName string, message string) (string, error)
func (s *ShiroAgent) ExecutePatch(ctx context.Context, patch string) (*ExecutionResult, error)
```

#### 設定

```yaml
ollama:
  worker_model: "Worker"  # Shiro用

worker:
  auto_commit: false
  command_timeout: 300
  workspace: "."
  protected_patterns:
    - ".env*"
    - "*credentials*"
  action_on_protected: "error"
```

---

### Coder（Aka/Ao/Gin）：実装担当

**愛称**: Aka（赤）/ Ao（青）/ Gin（銀）
**LLM**: DeepSeek / OpenAI / Claude

#### 責務

1. **Proposal生成**
   - plan（実装計画）
   - patch（差分、JSON or Markdown）
   - risk（リスク評価）
   - costHint（工数見積もり）

2. **コード設計・実装**
   - アーキテクチャ検討（Aka）
   - 標準的な実装（Ao）
   - 高品質コーディング（Gin）

3. **Worker との会話**
   - Worker からの指示を受ける
   - 日本語で会話しながら作業
   - 結果を Worker に報告

#### Coder が指示を受ける相手

- ✅ **Worker のみ**
- ❌ Mio から直接指示は受けない

#### Coder が報告する相手

- ✅ **Worker のみ**

#### Coder別の特性

| Coder | 愛称 | LLM | 得意分野 | Temperature |
|-------|------|-----|----------|-------------|
| **Coder1** | AO（青） | DeepSeek | 仕様設計、アーキテクチャ検討 | 0.5 |
| **Coder2** | Aka（赤） | OpenAI | 実装、標準的なコード生成 | 0.4 |
| **Coder3** | Kin（金） | Claude | 高品質コーディング、複雑な推論 | 0.3 |
| **Coder4** | Gin（銀） | Gemini | 高速プロトタイピング、実験 | 0.3 |

#### Proposal フォーマット

```markdown
## Plan
- ステップ1: ファイル構造の確認
- ステップ2: 関数の実装
- ステップ3: テストの追加

## Patch
```json
[
  {
    "action": "create",
    "path": "pkg/test/hello.go",
    "content": "package test\n\nfunc Hello() string {\n\treturn \"Hello World\"\n}"
  }
]
```

## Risk
- 既存の関数との名前衝突の可能性（低）

## CostHint
- 見積もり: 5分
```

#### インターフェース

```go
// Coder の主要メソッド
func (c *CoderAgent) GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error)
func (c *CoderAgent) DiscussWithWorker(ctx context.Context, message string) (string, error)
```

#### 設定

```yaml
claude:
  model: "claude-sonnet-4-20250514"  # Gin用

deepseek:
  model: "deepseek-chat"  # Aka用

openai:
  model: "gpt-4o-mini"  # Ao用
```

---

## コミュニケーションフロー

### 1. 全体フロー（日本語会話ベース）

```
ユーザー: 「hello.goを作成して」
  ↓ 日本語
Mio: 「承知しました。これはコーディングなので、Workerに依頼します」
  ↓ 日本語（Mio → Worker への委譲）
Worker: 「了解しました。Ginに依頼します」（Workerが自律的に判断）
  ↓ 日本語（Worker → Gin への指示）
Gin: 「以下のplanとpatchを提案します...」
  ↓ 日本語（Gin → Worker への報告）
Worker: 「確認しました。patchを実行します」
  ↓ ファイル操作実行
Worker: 「実行完了しました」
  ↓ 日本語（Worker → Mio への報告）
Mio: 「完了しました」
  ↓ 日本語（Mio → ユーザーへの返信）
ユーザー: （結果を確認）
```

**重要**: Worker は Mio に確認を求めず、自分の責任で Coder を選択します。

### 2. モニタリングの可視性

すべての会話は以下の関係者に可視：

| 会話 | ユーザー | Mio | Worker | Coder |
|------|---------|-----|--------|-------|
| ユーザー ↔ Mio | ✅ | ✅ | ✅ | ✅ |
| Mio ↔ Worker | ✅ | ✅ | ✅ | ✅ |
| Worker ↔ Coder | ✅ | ✅ | ✅ | ✅ |

**Mio は全体オーケストレーターとして、すべての会話を把握します。**

### 3. 指揮命令系統

```
ユーザー
  ↓ 指示
Mio（全体把握、Workerにのみ指示）
  ↓ 委譲
Worker（Coderオーケストレーター）
  ↓ 指示
Coder1/2/3
```

**重要**:
- Mio は Coder を知っているが、直接指示は出さない
- すべての指示は Worker を経由

### 4. Worker即時実行フロー

```
ユーザー: 「/code3 hello.goを作成」
  ↓
Mio: 「Workerさん、CODE3で処理してください」
  ↓
Worker: 「Ginさん、hello.goを作成してください」
  ↓
Gin: Proposal生成（plan/patch/risk）
  ↓
Worker: patch実行（WorkerExecutionService）
  ├─ ファイル作成
  ├─ Git auto-commit（オプション）
  └─ 実行結果生成
  ↓
Worker → Mio: 「完了しました」
  ↓
Mio → ユーザー: 「hello.goを作成しました」
```

---

## 分散実行と通信フォーマット

### 1. 物理的な分散実行が可能

**重要な設計原則**: Mio以外のAgent（Shiro、Aka、Ao、Gin）は、**物理的に別のCPU・別のマシンで動作可能**です。

```
[CPU1/マシン1] Mio（メインプロセス）
  ↓ SSH経由でJSON送信
[CPU2/マシン2] Worker（別プロセス）
  ↓ SSH経由でJSON送信
[CPU3/マシン3] Gin（別プロセス/別マシン）
  ↓ SSH経由でJSON送信
[CPU4/マシン4] Aka（別プロセス/別マシン）
```

**不変の原則**:
- ✅ 分散実行しても、各Agentの**機能（責務）は変わらない**
- ✅ 通信は**日本語テキスト（JSONパラメータ）**のみ
- ✅ **交互に会話**（同期的なリクエスト-レスポンス）

---

### 2. 通信プロトコル: SSH

Agent間の通信は**SSH経由**で行われます。

**利点**:
- ✅ **既存インフラ活用**: SSH鍵認証、既存の運用ノウハウ
- ✅ **セキュア**: 通信の暗号化、認証
- ✅ **枯れた技術**: 広く使われている、信頼性が高い
- ✅ **ファイアウォール越え**: SSH経由で容易

**実装イメージ**:
```bash
# Worker（CPU2）への接続
ssh user@worker-host "picoclaw-agent worker"

# Gin（CPU3）への接続
ssh user@coder-host "picoclaw-agent coder --type=gin"
```

---

### 3. 通信フォーマット: JSON + 日本語メッセージ

**基本フォーマット**:
```json
{
  "from": "Worker",
  "to": "Coder3",
  "session_id": "session_abc123",
  "job_id": "job_20260302_001",
  "message": "hello.goを作成してください",
  "context": {
    "user_request": "hello.goを作成して",
    "history": [
      {"role": "user", "content": "..."},
      {"role": "assistant", "content": "..."}
    ]
  },
  "timestamp": "2026-03-02T15:30:00Z"
}
```

**重要なポイント**:
- ✅ **構造化データ**: パース容易、ログ保存容易
- ✅ **日本語メッセージ**: `message` フィールドに自然言語
- ✅ **メタデータ**: `session_id`, `job_id`, `context` 等
- ✅ **拡張性**: 新しいフィールド追加が容易

---

### 4. Proposal受け渡しの例

**Gin（Coder3）→ Worker への Proposal 送信**:
```json
{
  "from": "Coder3",
  "to": "Worker",
  "session_id": "session_abc123",
  "job_id": "job_20260302_001",
  "message": "以下のplanとpatchを確認してください",
  "proposal": {
    "plan": "ステップ1: pkg/test/ディレクトリの確認\nステップ2: hello.go作成\nステップ3: テスト追加",
    "patch": "[{\"action\": \"create\", \"path\": \"pkg/test/hello.go\", \"content\": \"package test\\n\\nfunc Hello() string {\\n\\treturn \\\"Hello World\\\"\\n}\"}]",
    "risk": "既存ファイルとの衝突の可能性（低）",
    "costHint": "見積もり: 5分"
  },
  "timestamp": "2026-03-02T15:30:05Z"
}
```

**Worker → Mio への実行結果報告**:
```json
{
  "from": "Worker",
  "to": "Mio",
  "session_id": "session_abc123",
  "job_id": "job_20260302_001",
  "message": "hello.goの作成が完了しました",
  "execution_result": {
    "status": "success",
    "created_files": ["pkg/test/hello.go"],
    "git_commit": "abc123def456",
    "elapsed_time": "3.2s"
  },
  "timestamp": "2026-03-02T15:30:10Z"
}
```

---

### 5. Session/Memoryの分散管理

**各Agentは独立したMemoryを持ちます**:

```
Gin（独立Memory）
  - 自分が生成したProposal
  - Workerとの会話履歴
  ↓ JSON（message + proposal）
Worker（独立Memory）
  - Ginとの会話履歴
  - 実行結果
  - Mioとの会話履歴
  ↓ JSON（message + execution_result）
Mio（独立Memory）
  - すべての会話ログ（階層的に受信）
  - ユーザーとの会話履歴
  - Worker、Coderの進捗状況
  ↓ 日本語メッセージ
ユーザー
```

**会話ログの階層的伝播**:
- ✅ **Coder → Worker**: Coderの会話ログがWorkerに伝播
- ✅ **Worker → Mio**: Worker、Coderの会話ログがMioに伝播
- ✅ **Mioが全体把握**: すべての会話ログを受け取り、モニタリング

**利点**:
- ✅ **疎結合**: 各Agentが独立して動作
- ✅ **スケーラビリティ**: メモリ使用量を分散
- ✅ **透明性**: すべての会話がMioに集約される

---

### 6. 分散実行の利点

| 項目 | 説明 |
|------|------|
| **負荷分散** | LLM呼び出し（重い処理）を複数CPUに分散 |
| **並列実行** | 複数Coderが同時に動作可能 |
| **スケーラビリティ** | マシンを追加してAgent数を増やせる |
| **独立性** | 各Agentが独立したプロセス/サービス |
| **保守性** | Agent単位でのアップデート・再起動が可能 |
| **セキュリティ** | SSH認証、ネットワーク分離 |

---

### 7. 現在の実装 vs 将来の拡張

**現在（v3.0）**:
- すべてのAgentが同一プロセス内で動作
- `internal/domain/agent/` で実装
- ローカル関数呼び出し

**将来の拡張**:
- Agent間通信を SSH + JSON に移行
- 通信層の抽象化（ローカル ↔ リモート切り替え可能）
- マイクロサービス的なアーキテクチャ

**設計の先見性**:
- ✅ 抽象インターフェース（`llm.LLMProvider`）
- ✅ 疎結合な責務分離
- ✅ 日本語ベースの通信
- → 将来の分散実行に容易に移行可能

---

## 共通基盤

### 1. 会話インターフェース

すべてのAgentが持つ共通の会話能力：

```go
type Agent interface {
    // 日本語で会話
    Speak(ctx context.Context, message string) (string, error)

    // 会話履歴の取得
    GetConversationHistory() []Message
}
```

### 2. Task定義

```go
type Task interface {
    JobID() string           // タスク識別子
    SessionID() string       // セッション識別子
    UserMessage() string     // ユーザーメッセージ
    History() []Message      // 会話履歴
}
```

### 3. Session管理

```go
type Session interface {
    ID() string
    AddMessage(role, content string)
    Messages() []Message
    LastUpdate() time.Time
}
```

すべての会話はSessionに記録され、モニタリング可能。

---

## コネクタ層

### LLMプロバイダー

| プロバイダー | 用途 | 特徴 |
|-------------|------|------|
| **Ollama** | Mio/Shiro | ローカル実行、常駐化 |
| **Claude** | Gin | 高品質コーディング |
| **DeepSeek** | Aka | 仕様設計 |
| **OpenAI** | Ao | 標準実装 |

### ツールシステム

Worker が使用するツール：
- `file_read` - ファイル読み込み
- `file_write` - ファイル書き込み
- `file_delete` - ファイル削除
- `shell_exec` - シェルコマンド実行
- `mcp_call` - MCP操作

### MCP統合

Worker と Coder が使用：
- MCPサーバーとの統合
- ツール呼び出し
- コンテキスト収集

---

## 実装マップ

### ディレクトリ構成

```
internal/
├── domain/                          # Domain層
│   ├── agent/                       # エージェント
│   │   ├── mio.go                   # Mio（全体オーケストレーター）
│   │   ├── shiro.go                 # Shiro（Coderオーケストレーター）
│   │   ├── coder.go                 # Coder基底
│   │   └── interfaces.go            # 会話インターフェース
│   ├── llm/                         # LLM抽象化
│   ├── routing/                     # ルーティング
│   ├── proposal/                    # Proposal定義
│   ├── patch/                       # Patch定義
│   ├── task/                        # Task定義
│   └── session/                     # Session定義
│
├── application/                     # Application層
│   ├── orchestrator/                # メッセージオーケストレーター
│   │   └── message_orchestrator.go # Mio/Worker統合
│   └── service/                     # サービス
│       └── worker_execution_service.go  # Worker即時実行
│
├── infrastructure/                  # Infrastructure層
│   ├── llm/                         # LLMプロバイダー実装
│   │   ├── ollama/                  # Mio/Shiro用
│   │   ├── claude/                  # Gin用
│   │   ├── deepseek/                # Aka用
│   │   └── openai/                  # Ao用
│   ├── tools/                       # ツール実装
│   ├── mcp/                         # MCP統合
│   └── routing/                     # ルーティング実装
│
└── adapter/                         # Adapter層
    ├── config/                      # 設定管理
    └── line/                        # LINE統合
```

### 依存関係グラフ

```
MessageOrchestrator
  ↓
Mio（全体オーケストレーター）
  ├─ OllamaProvider (Chat)
  └─ DelegateToWorker()
      ↓
  Worker（Coderオーケストレーター）
      ├─ OllamaProvider (Worker)
      ├─ ToolRunner
      ├─ MCPClient
      └─ DelegateToCoder()
          ↓
      Coder1/2/3
          ├─ DeepSeekProvider (Aka)
          ├─ OpenAIProvider (Ao)
          └─ ClaudeProvider (Gin)
```

---

## まとめ

### 設計原則

1. **日本語による会話**: すべてのAgent間通信は日本語の自然言語
2. **階層的指揮命令**: Mio → Worker → Coder（飛び越しなし）
3. **全体可視性**: Mio はすべてをモニタリング（Coder の作業も含む）
4. **責務の分離**: Mio（全体）、Worker（Coder統括）、Coder（実装）

### 役割まとめ

| 役割 | モニタリング範囲 | 指示を出す相手 |
|------|----------------|---------------|
| **Mio** | すべて（ユーザー、Worker、Coder全て） | Worker のみ |
| **Worker** | Coder 達 | Coder1/2/3 |
| **Coder** | 自分の作業のみ | なし（報告のみ） |

### 重要な原則

- ✅ Mio は Coder を**知っている**（モニタリング）
- ✅ Mio は Coder に**指示を出さない**（Worker経由）
- ✅ Worker は Coder 達の**オーケストレーター**
- ✅ すべての会話は**日本語**
- ✅ すべての会話は**モニタリング可能**

---

**関連ドキュメント**:
- [実装仕様（正本）](01_正本仕様/実装仕様.md) - 詳細な実装仕様
- [仕様.md](仕様.md) - 要件定義
- [LLM運用/](../LLM運用/) - LLM固有の運用ルール
