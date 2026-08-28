---
name: implementation-planning
description: Produce a bounded implementation plan from a canonical specification, including file scope, dependency order, TDD checks, and terminal evidence.
version: 1.0.0
---

# Implementation Planning

## Purpose

このカードは、確定したspecを実装可能なbounded execution unitへ分解する。計画は権限や完了宣言ではなく、Workerが実行可能な入力契約である。

## Inputs

- canonical specification and acceptance criteria
- owning repository/module and current source state
- existing tests, route, configuration, and failure evidence

## Procedure

1. 変更対象ファイルと直接依存を確認し、owner外の作業を分離する。
2. CLI、LLM、Boundaryの工程、入力、出力、失敗status、receiptを表にする。
3. Red test、実装、Green test、refactor、integration evidenceの順に並べる。
4. allowed files、forbidden files、invariants、削除対象、rollback観点を固定する。
5. 一度に検証できる終端証拠と、未確認境界を明示する。

## Output contract

`implementation_plan` は purpose、owner、exact_files、observed_behavior、ordered_steps、contracts、tests、validation_commands、forbidden_changes、blockers を含む。

## Stop conditions

- source of truth、owner、または依存順が未確定。
- 同じfileを別unitが同時に編集する。
- testまたは代替検証が受入条件に対応しない。
- planが実装・deploy・restartのscopeを暗黙に拡張する。
