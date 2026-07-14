# RenCrow To-Be 統合実装仕様

- status: canonical
- canonical_path: `docs/02_正本仕様/11_RenCrow_ToBe_統合実装仕様.md`
- source_spec: `docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md`
- last_reviewed: 2026-07-14

## この文書の役割

本書は、`10_RenCrow_ToBe_統合仕様.md` を実装へ移すための正本実装仕様である。個別接続の詳細は `docs/04_構築指標/03`～`06` を参照する。

## 1. 実装 Phase

### Phase 0: 仕様固定

目的:

- 本仕様を docs に追加する。
- 既存新仕様 README へ登録する。
- `91-94` 相当の個別仕様を移植する場合の番号衝突を避ける。

作業:

- `10_RenCrow_ToBe_統合仕様.md` を追加する。
- `docs/README.md` に本仕様を追加する。
- 実装は行わない。

完了条件:

- `git diff --check` が通る。
- docs のみの差分である。

### Phase 1: CORE 機能台帳

目的:

- 実装済み、Facade、Legacy、構想を混同しない。

作業:

- `12_CORE_機能台帳.md` を作る。
- `modules/*`、`internal/features/*`、`internal/domain/*`、`internal/application/*` を台帳化する。
- `advisor`、`knowledge_relation`、`economic_objective` を `concept` として登録する。

完了条件:

- 各 feature に owner package と state がある。
- `docs/refs/10_新仕様/13_実装項目インベントリ.md` と矛盾しない。
- コード変更なし。

### Phase 2: Advisor MVP

目的:

- Codex を Tool 直呼びではなく Advisor として扱う。

作業:

- `internal/domain/advisor` を追加する。
- `AdviceRequest` / `AdviceResult` / `AdvisorProfile` / `AdvisorScore` を定義する。
- `internal/application/advisor` に `AdvisorService` を追加する。
- 既存 `codex.run` を呼ぶ `CodexAdvisorAdapter` を追加する。
- Shiro の Codex work path を AdvisorService 経由へ移す。

禁止:

- `codex.run` の互換削除。
- Codex に直接 git push / external publish を許す。
- `--dangerously-bypass-approvals-and-sandbox` を許す。

テスト:

```bash
go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

完了条件:

- Shiro が Advisor 経由で Codex advice を取得できる。
- 既存 `codex.run` tests が通る。
- AdvisorResult は実行ではなく助言として記録される。

### Phase 3: Agent Profile MVP

目的:

- Agent の Goal / Utility / Autonomy を runtime から読めるようにする。

作業:

- `internal/domain/agentprofile` を追加する。
- 8人格の静的 profile を定義する。
- Shiro に `ask_advisor` の Autonomy permission を付ける。
- Mio は会話、委譲、再質問に限定する。
- Kuro は risk / stop recommendation を優先する。

テスト:

```bash
go test ./internal/domain/agentprofile ./internal/application/agentprofile ./internal/domain/agent
```

完了条件:

- profile が取得できる。
- 既存 Agent 応答を破壊しない。
- AutonomyEnvelope の forbidden action が判定できる。

### Phase 4: Knowledge Relation MVP

目的:

- カテゴリ別 Knowledge を壊さず、1-2 hop の横断想起を可能にする。

作業:

- `internal/domain/knowledgerelation` を追加する。
- L1 SQLite に relation tables を追加する。
- importer 後に entity / topic / project metadata を保存する。
- scoring により `item_relations` を作る。
- RecallPack に `RelationSnippet` を optional 追加する。

テスト:

```bash
go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

完了条件:

- 同じ Entity / Project を持つ item が relation 登録される。
- 1 hop recall が RecallPack に入る。
- 2 hop 上限を超えない。
- VectorDB がなくても動く。

### Phase 5: Economic Objective MVP

目的:

- Revenue を「候補生成、評価、制作、承認、売上、振り返り」の loop として扱う。

作業:

- `Opportunity` / `EconomicTask` / `EconomicReflection` を `internal/domain/revenue` に追加する。
- persistence を追加する。
- Opportunity から Workstream Goal を作れる service を追加する。
- Human approval gate を既存 revenue approval と接続する。

テスト:

```bash
go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

完了条件:

- Opportunity を登録できる。
- expected_profit を計算できる。
- approval_required action は blocked / pending になる。
- RevenueEvent から Reflection を作れる。

### Phase 6: Viewer / Ops 表示

目的:

- 人間が状態を確認できるようにする。

詳細な後続実装仕様は `docs/04_構築指標/06_ToBe_Ops表示実装仕様.md` を正とする。

追加表示:

- Advisor runs
- Advisor scores
- Agent profiles
- Knowledge relation recall trace
- Opportunities
- Approval queue
- Economic reflections

テスト:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient
```

必要に応じて browser E2E を行う。

完了条件:

- 初期表示は要約。
- 生ログや長文 trace は details に閉じる。
- mobile 幅で崩れない。

### Phase 7: Scheduler / Heartbeat 接続

目的:

- 自律候補生成を安全に動かす。

詳細な後続実装仕様は、対象ごとに以下へ分離する。

```text
Advisor / AgentProfile: docs/04_構築指標/03_Advisor_AgentProfile接続実装仕様.md
Knowledge Relation:     docs/04_構築指標/04_KnowledgeRelation接続実装仕様.md
Economic Objective:     docs/04_構築指標/05_EconomicObjective接続実装仕様.md
Viewer / Ops:           docs/04_構築指標/06_ToBe_Ops表示実装仕様.md
```

作業:

- Heartbeat で Opportunity discovery draft を作る。
- Advisor score 更新を日次で集計する。
- Knowledge relation build を import 時または夜間 batch に限定する。

禁止:

- 勝手な公開。
- 勝手な請求。
- 勝手な外部送信。
- 勝手な memory promotion。

完了条件:

- draft-only で動く。
- approval queue なしに外部副作用が発生しない。

## 2. 実装順の原則

実装順は以下を守る。

```text
Docs / Ledger
  ↓
Domain model
  ↓
Application service
  ↓
Persistence
  ↓
Feature wiring
  ↓
Viewer / CLI
  ↓
Scheduler
  ↓
Autonomous loop
```

理由:

- 最初から自律 loop を動かすと安全境界が曖昧になる。
- DB schema より先に domain model を固定する。
- Viewer は実装確認用であり、正本 state の代替ではない。
- Scheduler は最後に接続する。

## 3. テスト方針

### 3.1 必須テスト

Advisor:

```bash
go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

Knowledge Relation:

```bash
go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

Revenue / Economic:

```bash
go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

Viewer / Client:

```bash
go test ./internal/adapter/viewer ./pkg/rencrowclient
```

最終:

```bash
go test ./...
```

### 3.2 失敗テスト

必ず失敗系を作る。

- Advisor が forbidden action を要求したら拒否される。
- Codex adapter が danger sandbox を要求したら拒否される。
- Relation 3 hop 以上は展開されない。
- VectorDB unavailable でも relation recall は成立する。
- Approval required action は自動実行されない。
- Opportunity expected_profit が負なら high priority にならない。
- 外部公開や請求は approval なしに実行されない。

## 4. Migration policy

### 4.1 既存挙動を壊さない

既存 feature を置き換えるのではなく、上位 facade または optional field として追加する。

例:

- `codex.run` は残す。
- `RecallPack` は既存 field を残し、`RelationSnippets` を optional 追加する。
- `revenue` 既存 Product / RevenueEvent は残し、Opportunity を追加する。
- Agent の既存応答は変えず、profile を読むだけから始める。

### 4.2 Feature flag

runtime 影響があるものは config で opt-in にする。

```yaml
advisor:
  enabled: false
  default_provider: codex

knowledge_relation:
  enabled: false
  max_hops: 2

economic_objective:
  enabled: false
  draft_only: true
```

### 4.3 Backfill

Knowledge relation は既存全データへ即時 backfill しない。

順序:

1. 新規 import 分だけ relation を作る。
2. 小さい sample で backfill dry-run。
3. relation 数、score 分布、Recall trace を確認。
4. full backfill は別 job とする。

## 5. Acceptance criteria

本仕様の To-Be へ到達したと判断する条件は次である。

```text
[CORE]
- 全 feature が機能台帳で state 管理されている。
- Agent / Advisor / Tool が仕様上も実装上も分かれている。

[Advisor]
- Shiro が Codex を Advisor として呼べる。
- AdviceResult は助言として保存され、実行責任は Shiro に残る。
- Advisor score が更新できる。

[Knowledge]
- Knowledge DB はカテゴリ別のまま。
- Relation layer で Entity / Topic / Project 横断 recall ができる。
- RecallPack trace に relation reason が残る。

[Economic]
- Opportunity から Workstream / Artifact / Approval / RevenueEvent / Reflection へ接続できる。
- 公開、送信、請求、契約、価格決定は approval なしに実行されない。

[Safety]
- Advisor、Revenue、Knowledge import のすべてで provenance が残る。
- 外部情報と確定知識を混同しない。
- 自律候補生成は draft-only から開始する。
```

## 6. 最初の実装タスク

最初に着手する作業単位は次とする。

### Task 1: CORE 機能台帳 docs

```text
目的:
  現状の modules / features / legacy-body / concept を台帳化する。

対象:
  docs/02_正本仕様/12_CORE_機能台帳.md
  docs/refs/10_新仕様/13_実装項目インベントリ.md

やること:
  - modules/* を contracted として列挙
  - internal/features/* を feature entry として列挙
  - advisor / knowledge_relation / economic_objective を concept として登録
  - 実装済みと Facade の区別を明記

やらないこと:
  - Go code 変更
  - DB migration
  - runtime wiring

確認:
  git diff --check
```

### Task 2: Advisor domain MVP

```text
目的:
  Codex を Tool ではなく Advisor として呼ぶための domain / service を作る。

対象:
  internal/domain/advisor
  internal/application/advisor
  internal/infrastructure/advisor
  internal/domain/agent

やること:
  - AdviceRequest / AdviceResult / AdvisorProfile を定義
  - AdvisorService を実装
  - CodexAdvisorAdapter を実装
  - Shiro の Codex 利用経路を AdvisorService 経由へ変更
  - 既存 codex.run tests を維持

やらないこと:
  - codex.run 削除
  - 実 git push
  - external publish
  - memory promotion

確認:
  go test ./internal/domain/advisor ./internal/application/advisor ./internal/infrastructure/tools ./internal/domain/agent
```

### Task 3: Knowledge Relation MVP

```text
目的:
  Entity / Topic / Project でカテゴリ横断 recall できるようにする。

対象:
  internal/domain/knowledgerelation
  internal/application/knowledgerelation
  internal/infrastructure/persistence/conversation
  internal/domain/conversation

やること:
  - relation schema を追加
  - relation builder を追加
  - 1 hop recall を追加
  - RecallPack に RelationSnippet を optional 追加

やらないこと:
  - Neo4j 導入
  - 3 hop 以上探索
  - 全データ即時 backfill

確認:
  go test ./internal/domain/knowledgerelation ./internal/application/knowledgerelation ./internal/infrastructure/persistence/conversation ./internal/domain/conversation
```

### Task 4: Economic Objective MVP

```text
目的:
  Revenue を Opportunity 起点の安全な収益 loop にする。

対象:
  internal/domain/revenue
  internal/application/revenue
  internal/infrastructure/persistence/revenue
  internal/application/workstream

やること:
  - Opportunity / EconomicTask / EconomicReflection を追加
  - expected_profit 計算
  - approval_required 判定
  - Opportunity から Workstream Goal 作成

やらないこと:
  - 自動公開
  - 自動請求
  - 自動契約
  - 有料APIの無承認利用

確認:
  go test ./internal/domain/revenue ./internal/application/revenue ./internal/infrastructure/persistence/revenue ./internal/application/workstream
```

## 7. 実装時の注意

- `RenCrow_CORE` に未コミット変更がある場合、その file は必ず差分を読んでから触る。
- `picoclaw_multiLLM` は本作業の操作対象にしない。
- 新規横断ツールは `RenCrow_Tools` 側に置く。`RenCrow_CORE/tools/` へ増やさない。
- 仕様だけで runtime を変えない。runtime 接続は別 Phase で行う。
- すべての自律処理は最初 draft-only にする。
- Human approval gate を先に作ってから外部副作用へ接続する。
