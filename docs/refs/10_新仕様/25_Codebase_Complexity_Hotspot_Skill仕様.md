# Codebase Complexity Hotspot Skill 仕様

## 1. 目的

本仕様は、RenCrow において、コードベース内の複雑性ホットスポットを検出し、安全に改善できる可能性のある箇所をレポートする Skill を定義する。

この Skill を **Codebase Complexity Hotspot Skill** と呼ぶ。

本 Skill の目的は、コードを自動で最適化することではない。

目的は以下である。

```text
- 計算量が大きくなりやすい箇所を見つける
- 繰り返し lookup や重複 scan を見つける
- N+1 パターンを見つける
- render 負荷が高い箇所を見つける
- 安全に改善できる可能性のある箇所を整理する
- 改善前後の複雑性を見積もる
- リスクレベルと必要テストを提示する
```

本 Skill は、デフォルトでは **Report-only mode** で動作する。

つまり、コードを変更せず、調査結果、改善候補、リスク、必要テストを提示する。

## 2. 位置づけ

本仕様は、RenCrow の Skill Governance 配下に属する。

```text
24_Agent_Skill_Governance仕様
  Skill の登録、起動、評価、変更管理を定義する。

25_Codebase_Complexity_Hotspot_Skill仕様
  コードベースの複雑性ホットスポット検出 Skill を定義する。
```

また、以下の仕様とも接続する。

```text
19_DCI_直接コーパス探索仕様
  原文コード、ログ、仕様を横断探索する。

20_Tool_Harness_Contract_Mediation仕様
  tool call を安全に検証、修復する。

21_AI_Native_Engineering_Workflow仕様
  Project Init、worktree、CLI、Skill 運用を定義する。

23_Workstream_Operating_Loop仕様
  Goal、Artifact、Report、Human approval を管理する。
```

## 3. 基本方針

本 Skill は、以下の原則で動作する。

```text
1. デフォルトではコードを変更しない
2. まず複雑性ホットスポットを発見する
3. 改善案は提案に留める
4. 改善には必ずテスト要件を添える
5. ベンチマークなしに高速化を断言しない
6. 挙動互換性を優先する
7. 小さなコードへの過剰最適化を避ける
8. 可読性を大きく損なう最適化は避ける
9. 修正する場合は別 Goal / 別 PR / 別 patch proposal に分離する
```

## 4. 対象

本 Skill が対象とするコードは以下である。

```text
- アプリケーションコード
- API 処理
- DB アクセス処理
- バッチ処理
- データ変換処理
- React / Vue / UI レンダリング処理
- 集計処理
- 検索、フィルタ処理
- ループ、再帰処理
- I/O を含む処理
```

対象外は以下である。

```text
- vendored code
- node_modules
- .venv / venv
- generated files
- minified files
- build artifacts
- lock files
- 外部ライブラリ本体
```

## 5. 起動条件

### 5.1 明示起動

ユーザーが以下のように依頼した場合に起動する。

```text
- 重いところを探して
- 複雑なところを見つけて
- O(n²)っぽいところを探して
- N+1 を探して
- パフォーマンス改善候補を出して
- 安全に最適化できる場所を探して
- render が重そうなところを調べて
```

### 5.2 自動起動候補

以下の場合、Coder は本 Skill の起動を検討する。

```text
- 大量データ処理のコードを変更する前
- UI の描画遅延が疑われる時
- DB アクセスが多い処理を調査する時
- バッチ処理が遅いという報告がある時
- 同じ配列を何度も find / filter している疑いがある時
- レンダリング内で重い計算をしている疑いがある時
```

## 6. 動作モード

### 6.1 Report-only mode

デフォルトモード。

```text
- コードを読む
- パターンを検出する
- ホットスポットを分類する
- 複雑性を見積もる
- 改善案を出す
- リスクと必要テストを出す
- コードは変更しない
```

### 6.2 Proposal mode

ユーザーが明示した場合のみ、改善 patch 案を作成する。

```text
- 対象 hotspot を 1 つに絞る
- 変更方針を説明する
- patch proposal を作る
- テスト案を作る
- Human approval を待つ
```

### 6.3 Apply mode

MVP では原則禁止。

将来的に導入する場合も、以下を満たす必要がある。

```text
- worktree 上で実行
- 対象 hotspot が 1 つ
- 既存テストがある
- 追加テストがある
- diff を人間が確認
- Human approval 済み
```

## 7. 検出パターン

### 7.1 Nested Loop

```text
for の中に for
map の中に filter
filter の中に find
reduce の中に lookup
```

想定される問題:

```text
O(n²)
O(n*m)
大量データ時の遅延
```

### 7.2 Repeated Lookup

同じ配列、オブジェクト、DB 結果に対して、繰り返し検索を行う。

```text
items.find(...)
items.filter(...)
users.find(...)
records.some(...)
```

改善候補:

```text
Map / Set による index 化
事前 grouping
事前 normalization
```

### 7.3 Repeated Scan

同じデータ集合を複数回走査する。

```text
items.filter(...).map(...)
items.filter(...).reduce(...)
items.map(...).filter(...).map(...)
```

改善候補:

```text
1 回のループへ統合
中間結果の再利用
generator / lazy evaluation
```

ただし、可読性が落ちる場合は慎重に扱う。

### 7.4 N+1 Pattern

ループ内で DB / API / ファイルアクセスを行う。

```text
for item in items:
  db.query(...)

items.map(async item => await fetch(...))
```

改善候補:

```text
batch query
join
prefetch
bulk API
cache
```

### 7.5 Render Hotspot

UI レンダリング内で重い処理を行う。

```text
React component 内で重い filter / sort / map
render ごとの new Date / regex / large calculation
props 変更ごとの全件再計算
大きなリストの全描画
```

改善候補:

```text
memoization
useMemo
useCallback
virtualized list
selector cache
component split
```

### 7.6 Repeated Serialization

```text
JSON.parse / JSON.stringify の繰り返し
deep clone の多用
structuredClone のループ内利用
```

改善候補:

```text
parse once
cache
shallow update
差分更新
```

### 7.7 Expensive Regex / String Processing

```text
大きな文字列に対する複数 regex
ループ内 regex compile
全件文字列 split / join
```

改善候補:

```text
regex 事前 compile
1 回の scan に統合
文字列処理範囲を限定
```

### 7.8 Recursive / Graph Traversal Risk

```text
再帰探索
依存関係グラフ探索
木構造走査
```

確認すること:

```text
- cycle 対策があるか
- visited set があるか
- depth limit があるか
- 同じ node を何度も処理していないか
```

## 8. 複雑性見積もり

本 Skill は、厳密な証明ではなく、実装上の目安として複雑性を見積もる。

### 8.1 表記

```text
O(1)
O(log n)
O(n)
O(n log n)
O(n*m)
O(n²)
O(k*n)
unknown
```

### 8.2 before / after 見積もり

改善案には、可能な範囲で before / after を付ける。

```text
Before:
  O(n²)

After:
  O(n)

Method:
  配列 find を Map lookup へ変更
```

### 8.3 注意

複雑性見積もりは推定であり、性能改善の保証ではない。

実際の性能改善には以下が必要である。

```text
- representative data
- benchmark
- profiling
- regression test
```

## 9. リスク分類

### 9.1 Low Risk

```text
- 純粋計算の重複削減
- Map / Set による単純 lookup 化
- 事前に明らかな定数値を外へ出す
- 既存テストで挙動が十分覆われている
```

### 9.2 Medium Risk

```text
- 処理順序に意味がある可能性
- 重複 key の扱いが変わる可能性
- undefined / null の扱いが変わる可能性
- UI render 結果に影響する可能性
- 非同期処理順序が変わる可能性
```

### 9.3 High Risk

```text
- DB query の構造変更
- API 呼び出し順序の変更
- キャッシュ導入
- 並列化
- 状態管理変更
- 認証、課金、決済、削除処理
- エラーハンドリングの挙動変更
```

High Risk の改善は、Report-only に留め、別途 Goal Contract を作る。

## 10. 必要テスト

改善提案には、必ず必要テストを添える。

### 10.1 共通テスト

```text
- 空配列
- 1 件のみ
- 重複データ
- null / undefined
- 順序が重要なデータ
- 大量データ
- エラーケース
```

### 10.2 N+1 改善テスト

```text
- 取得件数が同じ
- 欠損データの扱い
- 権限条件
- join / batch 後の順序
- DB query count
```

### 10.3 UI 改善テスト

```text
- 表示結果が同じ
- フィルタ結果が同じ
- ソート順が同じ
- key が安定している
- 再レンダリング回数
- スマホ表示
```

### 10.4 キャッシュ導入テスト

```text
- キャッシュ無効化
- 入力変更時の再計算
- stale data
- memory growth
- concurrency
```

## 11. 出力形式

### 11.1 Complexity Hotspot Report

```markdown
# Complexity Hotspot Report

## Summary

- Scan scope:
- Files scanned:
- Hotspots found:
- High risk:
- Medium risk:
- Low risk:

## Top Recommendations

1. 対象:
   期待効果:
   リスク:
   次アクション:

2. 対象:
   期待効果:
   リスク:
   次アクション:

## Hotspots

### 1. {file}:{line_start}-{line_end}

Type:
Current pattern:
Estimated complexity:
Possible improvement:
Estimated after:
Risk:
Why this matters:
Required tests:
Suggested next action:
Evidence:

## Non-goals

今回あえて対象外にしたもの。

## Limitations

調査できなかった範囲、不確実な点。
```

## 12. Evidence

各 Hotspot には Evidence を付ける。

```json
{
  "file_path": "src/example.ts",
  "line_start": 120,
  "line_end": 168,
  "pattern": "nested_find_in_loop",
  "snippet": "orders.map(order => users.find(...))",
  "estimated_complexity": "O(n*m)",
  "confidence": 0.78
}
```

Evidence は DCI の Evidence Pack と互換性を持たせる。

## 13. Scoring

Hotspot の優先度は以下で算出する。

```text
priority_score =
  0.30 * estimated_complexity_risk
+ 0.20 * execution_likelihood
+ 0.20 * data_size_likelihood
+ 0.15 * improvement_safety
+ 0.10 * testability
+ 0.05 * user_relevance
```

### 13.1 estimated_complexity_risk

```text
O(n²): high
O(n*m): high
O(n log n): medium
O(n): low
unknown: medium
```

### 13.2 execution_likelihood

```text
- hot path
- render path
- request path
- batch path
- rarely used admin path
```

### 13.3 improvement_safety

```text
- pure refactor: high
- cache: medium
- DB query rewrite: low
- concurrency change: low
```

## 14. Human Decision Gate

本 Skill の Report を受けても、自動修正はしない。

以下は必ず人間判断とする。

```text
- どの hotspot を修正するか
- performance 改善を優先するか
- 可読性低下を許容するか
- cache 導入を許可するか
- DB query 変更を許可するか
- benchmark を作るか
- PR を作るか
```

## 15. 禁止事項

本 Skill では以下を禁止する。

```text
- 自動で最適化 patch を適用する
- ベンチなしに高速化を断言する
- テストなしに安全と判断する
- 可読性を大きく壊す最適化を推奨する
- 挙動変更を「最適化」として隠す
- 複数 hotspot を 1 つの PR で同時修正する
- 小規模コードを過剰最適化する
- 本番 DB や外部 API に負荷をかける調査を行う
```

## 16. Tool 利用

本 Skill が使用してよいツールは以下である。

```text
readFile
listDir
rg
fd
grep
git diff
git status
test read-only
static analysis script
```

条件付きで許可するもの:

```text
benchmark script
test runner
type checker
lint
```

禁止するもの:

```text
writeFile
applyPatch
shell destructive command
npm install
pip install
DB migration
external API load test
```

Proposal mode では、writeFile / applyPatch は禁止ではないが、別 Goal と Human approval を必要とする。

## 17. Skill 構成

```text
skills/core/codebase-complexity-hotspot/
  SKILL.md
  checklist.md
  report_template.md
  patterns.md
  risk_matrix.md
  evals/
    nested-loop.md
    repeated-lookup.md
    n-plus-one.md
    react-render.md
    cache-risk.md
```

## 18. SKILL.md 案

```markdown
# Codebase Complexity Hotspot

## Purpose

コードベースを調査し、計算量、繰り返し処理、N+1、render 負荷などの複雑性ホットスポットを発見し、Report-only で改善候補を提示する。

## When to Use

- ユーザーが重い箇所を探したいと言った
- パフォーマンス改善候補を調べたい
- O(n²) や N+1 の疑いがある
- 大量データ処理や render 遅延が疑われる

## When Not to Use

- 具体的なバグ修正が目的
- 小さな文言修正
- UI デザイン調整
- すでに対象 hotspot が決まっている
- 本番負荷試験が必要

## Procedure

1. Project Init Pack を確認する
2. Scan scope を決める
3. 対象外ディレクトリを除外する
4. ループ、lookup、DB/API 呼び出し、render path を探索する
5. hotspot 候補を Evidence 付きで抽出する
6. 複雑性を見積もる
7. 改善案を出す
8. リスクを分類する
9. 必要テストを提示する
10. Report を出す

## Output

Complexity Hotspot Report

## Safety

- デフォルトではコードを変更しない
- 高リスク改善は提案のみ
- 修正する場合は別 Goal を作る
```

## 19. DB 設計

### 19.1 complexity_scan_event

```sql
CREATE TABLE IF NOT EXISTS complexity_scan_event (
  scan_id TEXT PRIMARY KEY,
  workstream_id TEXT,
  repo TEXT NOT NULL,
  scan_scope TEXT,
  mode TEXT NOT NULL,
  files_scanned INTEGER,
  hotspots_found INTEGER,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT
);
```

### 19.2 complexity_hotspot

```sql
CREATE TABLE IF NOT EXISTS complexity_hotspot (
  hotspot_id TEXT PRIMARY KEY,
  scan_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  line_start INTEGER,
  line_end INTEGER,
  hotspot_type TEXT NOT NULL,
  estimated_complexity TEXT,
  estimated_after TEXT,
  risk_level TEXT,
  priority_score REAL,
  confidence REAL,
  summary TEXT,
  suggested_improvement TEXT,
  required_tests TEXT,
  created_at TEXT NOT NULL
);
```

### 19.3 complexity_hotspot_evidence

```sql
CREATE TABLE IF NOT EXISTS complexity_hotspot_evidence (
  evidence_id TEXT PRIMARY KEY,
  hotspot_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  line_start INTEGER,
  line_end INTEGER,
  snippet TEXT,
  reason TEXT,
  created_at TEXT NOT NULL
);
```

## 20. 設定ファイル案

```yaml
complexity_hotspot:
  enabled: true
  default_mode: "report_only"

  scan:
    exclude_dirs:
      - node_modules
      - .venv
      - venv
      - dist
      - build
      - coverage
      - .git
    exclude_patterns:
      - "*.min.js"
      - "*.lock"
      - "package-lock.json"
      - "pnpm-lock.yaml"
      - "yarn.lock"

  detection:
    nested_loop: true
    repeated_lookup: true
    repeated_scan: true
    n_plus_one: true
    render_hotspot: true
    repeated_serialization: true
    expensive_regex: true
    recursion_risk: true

  output:
    max_hotspots: 20
    include_low_risk: true
    include_required_tests: true
    include_before_after_estimate: true

  safety:
    auto_apply: false
    require_human_approval_for_patch: true
    require_tests_for_patch: true
    one_hotspot_per_pr: true
```

## 21. EventId

```text
complexity_scan_started
complexity_scan_completed
complexity_hotspot_found
complexity_report_created
complexity_patch_proposal_requested
complexity_patch_proposal_created
complexity_patch_approved
complexity_patch_rejected
```

## 22. MVP 実装順

### 22.1 Phase 1: Report-only Skill

- `SKILL.md` 作成
- report_template 作成
- scan scope 定義
- rg / fd ベースの探索
- 手動複雑性見積もり

### 22.2 Phase 2: Pattern Detection

- nested loop 検出
- repeated lookup 検出
- N+1 候補検出
- render hotspot 候補検出

### 22.3 Phase 3: Risk / Test Matrix

- risk matrix 作成
- required tests 自動提案
- high / medium / low 分類

### 22.4 Phase 4: Evidence / DB

- hotspot DB 保存
- Evidence 保存
- scan event 保存

### 22.5 Phase 5: Proposal Mode

- 選択された hotspot だけ patch proposal
- Goal Contract 生成
- Human approval 必須
- test 追加提案
- sandbox metadata がある場合は Sandbox Promotion Request を作成
- diff / test / rollback / approval が不足する場合は Promotion Gate で approve しない

## 22.6 実装状況

MVP として、Report-only の静的検出基盤は実装済み。

実装済み:

- `internal/domain/complexity` に `ScanEvent` / `Hotspot` / `HotspotEvidence` と validation を追加。
- `internal/application/complexity` に report-only analyzer を追加。
- nested loop、repeated lookup、N+1 候補、render hotspot 候補の簡易検出。
- hotspot に `estimated_complexity`、`estimated_after`、`risk_level`、`priority_score`、`confidence`、`required_tests` を付与。
- `internal/infrastructure/persistence/complexity` に JSONL store と SQLite store を追加。
- `complexity_hotspot.*` config を追加し、`storage` / `sqlite_path` runtime 切替、`default_mode=report_only` と `auto_apply=false` を validation で強制。
- `/viewer/complexity-hotspots` と `/viewer/complexity-hotspots/scan` を追加。
- Viewer Ops に `Complexity Hotspots` summary を追加。
- `skills/core/codebase-complexity-hotspot/` に `skill_manifest.yaml`、`SKILL.md`、checklist、report template、patterns、risk matrix、evals を配置。
- `/viewer/complexity-hotspots/scan` 実行時に Skill Bootstrap へ `complexity_hotspot_scan` と `core.codebase-complexity-hotspot` を記録する。
- `candidate_patterns` による候補ファイル事前抽出を scan API / analyzer で利用できる。
- `/viewer/complexity-hotspots/scan` の結果から Markdown の `complexity_hotspot_report` artifact を生成し、JSONL / SQLite に保存する。
- `workstream_id` がある scan report は Workstream Artifact に `pending_review` として登録する。
- `auto_candidate_patterns=true` の scan API で、直近 DCI trace / rg command 由来の語を `candidate_patterns` へ自動マージできる。
- `/viewer/complexity-hotspots/proposals` で選択 hotspot を Workstream Goal Contract と `pending_review` Artifact へ接続し、patch は未適用のまま Human approval 待ちにできる。
- `/viewer/complexity-hotspots/proposals` で選択 hotspot の `complexity_patch_proposal` Markdown artifact を生成し、必要テスト・risk・Human approval 必須条件を表示できる。
- `/viewer/complexity-hotspots/proposals` で選択 hotspot の `complexity_coder_diff_request` Markdown artifact を生成し、Coder が具体 diff を作るための境界、必要テスト、Promotion Gate 条件を review-only で残せる。
- risk が `high` の hotspot は、通常 proposal とは別に `High-risk complexity review` Goal と `complexity_high_risk_review_request` Artifact へ分岐し、別 Goal / PR / migration review checklist 前提にできる。
- `/viewer/complexity-hotspots/proposals` に `sandbox_id` と diff / test / rollback / approval metadata が渡された場合、Sandbox Promotion Request と Gate Log を作成し、`sandbox_promotion` / `sandbox_decision` / `sandbox_gate_log` を返す。
- 具体 diff、test result、rollback plan、human approval が揃わない場合、Sandbox Promotion Gate は `approve` せず `needs_review` / `needs_more_tests` として残す。
- `complexity_patch_proposal` と `complexity_coder_diff_request` Markdown artifact に External PR Review Checklist と Migration / High-risk Review Checklist を追加し、外部 PR 自動作成禁止、実在問題確認、既存 issue / PR 確認、1 hotspot = 1 PR / 1 intent、完全 diff / test / rollback / risk 説明 / Human review 必須を明記できる。
- `/viewer/complexity-hotspots/concrete-diffs` で Coder runtime から渡された unified diff を対象 hotspot file scoped として検証し、`complexity_concrete_diff_proposal` Markdown artifact と Workstream `pending_review` Artifact に保存できる。
- concrete diff API に `sandbox_id` と diff / test / rollback / approval metadata が渡された場合、Sandbox Promotion Request と Gate Log を作成する。証跡不足なら `approve` せず `needs_review` / `needs_more_tests` として残す。

未実装 / 残作業:

- Coder model が `complexity_coder_diff_request` を受けて自律的に具体 diff を生成し、`/viewer/complexity-hotspots/concrete-diffs` へ投入する orchestration。

## 23. 成功指標

```text
complexity_scan_count
hotspot_detection_count
accepted_hotspot_count
false_positive_rate
proposal_to_patch_rate
post_patch_test_pass_rate
performance_regression_count
behavior_regression_count
```

重要指標:

```text
- レポートから実際に改善対象として採用された率
- 修正後にテストが通った率
- 挙動変更事故ゼロ
- 自動 patch 適用ゼロ
```

## 24. 設計上の結論

Codebase Complexity Hotspot Skill は、最適化を自動実行する Skill ではない。

これは、Coder が安全に性能改善候補を見つけるための調査 Skill である。

RenCrow では、性能改善を以下の順で扱う。

```text
1. hotspot を発見する
2. Evidence を出す
3. 複雑性を見積もる
4. 改善案を出す
5. リスクと必要テストを出す
6. 人間が対象を選ぶ
7. 別 Goal で patch proposal を作る
8. テスト後に適用する
```

この流れにより、RenCrow は「速そうだから直す」のではなく、「安全に改善できる候補を選んで直す」開発支援ができる。

## 25. まとめ

本仕様は、RenCrow の Coder / Worker における複雑性ホットスポット検出 Skill を定義する。

本 Skill のカテゴリは以下である。

```text
Skill
Coder
Performance Review
Complexity Analysis
Report-only
Human Decision Gate
```

最終原則は以下である。

```text
性能改善は、推測で実行しない。
まず発見し、証拠を出し、リスクを分類し、必要テストを定義する。
修正はその後に行う。
```
