---
name: launch-readiness-qa-loop
description: Use before launch, release, major Viewer/app changes, or broad quality audits to inventory all app features, derive user stories from code, track status in one canonical board or spreadsheet, test every story, document errors, fix UX/logistical defects, and retest.
---

# Launch Readiness QA Loop

## Purpose

ローンチ前、大きな公開前、または広範囲の品質確認時に、RenCrow の機能をユーザー視点で一周し、期待動作、テスト状態、エラー、修正、再テストを単一の正準トラッキング表に集約する。

この Skill は、通常の小さな修正や単一バグ調査ではなく、「完成したつもりのアプリが実際に使えるか」を確認するための QA loop である。

## When to Use

- ローンチ前、公開前、demo 前、release / deploy / main promotion 前。
- Viewer、Scheduler、Idle Job、Movie Catalog、Knowledge Memory、STT / TTS、Backlog などの大きな変更後。
- 仕様と実装のズレ、壊れた導線、未確認機能が広範囲にありそうな時。
- ユーザーが「全機能を見て」「ローンチ前に確認」「ユーザーストーリーでテスト」「正準スプレッドシートで追跡」と依頼した時。

## When Not to Use

- 小さな 1 件のバグ修正。
- live 障害対応中。
- 通常会話や通常 push のたび。
- 仕様がまだ探索段階で、期待動作を確定できない時。
- 実ブラウザや API で確認できないまま、見たふりの QA になりそうな時。

## Owner Roles

```text
Human / Owner:
  起動判断、優先度、許容 UX、修正範囲、ローンチ可否を決める。

Analyzer / Research Agent:
  コード、route、API、Viewer、CLI、config から機能一覧を作る。

QA / UI Agent:
  ユーザーストーリーを実ブラウザ、API、CLI、ログで検証する。

Coder / Worker:
  発見された実装エラー、導線不足、UX エラーを修正する。

Doc / Ops Agent:
  正準トラッキング表、artifact、evidence、再テスト結果を維持する。
```

## Canonical Tracker

結果は必ず単一の正準 tracker に集約する。

初期実装では Google Sheets ではなく、repo 内 artifact または Viewer backlog / Workstream board を使ってよい。

推奨カラム:

```text
story_id
area
feature
user_story
expected_behavior
entry_point
test_steps
evidence
status
severity
error_type
error_summary
fix_commit
retest_status
owner
notes
```

`status` は次に限定する。

```text
not_started
inventory_done
test_passed
test_failed
blocked
fixed_pending_retest
retest_passed
deferred
not_applicable
```

`error_type` は次を基本とする。

```text
implementation
logistical
ux
docs
data
performance
security
unknown
```

## Procedure

### 1. Scope を決める

対象を明示する。

```text
full_app:
  ローンチ前の全機能確認。

affected_area:
  今回変更した機能群だけ確認。

monthly_maintenance:
  stale な機能、壊れた導線、未確認 API を軽く確認。
```

RenCrow の umbrella root ではなく、対象 module を先に確定する。

### 2. 機能を棚卸しする

コードと runtime entry point から機能を拾う。

確認対象:

```text
- Viewer routes / tabs / buttons
- API handlers
- CLI commands
- scheduler / idle jobs
- background jobs
- config flags
- persistence / DB / JSONL stores
- STT / TTS / LLM integration points
- docs にあるが UI から到達できない機能
```

コードに基づく期待動作を作る。推測で機能を増やさない。

### 3. ユーザーストーリー化する

各機能をユーザー視点の story に変換する。

形式:

```text
As a <user or operator>,
I can <action>,
so that <outcome>.
```

RenCrow 内部用には日本語でよい。

例:

```text
運用者は Viewer から backlog の未完了件数を確認できる。
運用者は Scheduler の job run log を確認できる。
ユーザーは Movie Catalog の取得状況を Viewer で確認できる。
```

### 4. テスト手順を書く

各 story に対して、実行可能な確認手順を定義する。

最低限:

```text
- entry point
- precondition
- action
- expected visible result
- expected log / API / DB result
- failure evidence の保存先
```

Viewer は DOM 存在だけで完了扱いしない。必要に応じて `viewer-live-verification` skill を使う。

### 5. 一周テストする

全 story を 1 つずつ確認する。

記録するもの:

```text
- pass / fail / blocked
- 実行日時
- 実行環境
- screenshot / DOM / API response / log / command output
- 再現手順
- 期待動作との差分
```

失敗を見つけても、棚卸し中に大きな修正へ脱線しない。まず tracker に残す。

### 6. エラーを分類する

```text
implementation:
  API failure、保存されない、例外、状態不整合。

logistical:
  導線がない、起動順が不明、設定が分からない、状態の所在が不明。

ux:
  押せない、読めない、重なる、長文で崩れる、結果が見えない。

docs:
  実装と docs がずれている。

data:
  DB / JSONL / Source Registry / cache の不整合。
```

severity は `critical`、`high`、`medium`、`low` に限定する。

### 7. 修正 loop に切り替える

修正は優先度順に行う。

```text
critical:
  ローンチ不可。先に直す。

high:
  主要導線が壊れている。原則ローンチ前に直す。

medium:
  回避策があるが体験を落とす。ローンチ判断で扱う。

low:
  後続 backlog でよい。
```

修正時は、1 issue / 1 area ごとに scope を絞る。無関係なリファクタを混ぜない。

### 8. 再テストする

修正後は該当 story だけでなく、関連 story も再テストする。

`fixed_pending_retest` のまま完了扱いしない。

ローンチ判定に使える状態は次のみ。

```text
test_passed
retest_passed
deferred
not_applicable
```

`deferred` は理由と owner を必ず書く。

## Output

最終報告は次を含める。

```text
- scope
- tracker path or URL
- total stories
- passed
- failed
- blocked
- deferred
- critical / high remaining count
- fixed commits
- retest status
- launch recommendation
```

launch recommendation は次に限定する。

```text
go
go_with_known_issues
no_go
needs_owner_decision
```

## Safety

- テストしていない story を pass にしない。
- 「代表機能が動いた」だけで全体完了にしない。
- screenshot / log / API response などの evidence なしに UI 完了を主張しない。
- live service の再ビルドや再起動が必要な場合は、対象 skill / runbook に従う。
- 外部送信、公開、課金、正式 DB promotion は Human approval なしに行わない。
- tracker を複数に分散させない。必ず単一の正準表へ戻す。
