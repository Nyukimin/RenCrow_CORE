---
name: plan-conflict-ruling
description: Rule on conflicting implementation plans using canonical ownership, user constraints, invariants, and evidence without silently broadening scope.
version: 1.0.0
---

# Plan Conflict Ruling

## Purpose

このカードは、複数のplanや証拠が衝突したときに、正本と明示制約へ戻って一つの実行可能なrulingを作る。rulingは権限付与でも人間待ちgateでもない。

## Inputs

- original request and explicit constraints
- competing plans, current evidence, and rejection reasons
- canonical specs, ownership, security, policy, and runtime invariants

## Procedure

1. 各planの前提、owner、route、state変更、終端証拠を比較する。
2. ユーザー訂正、canonical spec、実測、test結果の優先順位を適用する。
3. scopeを狭める、既存routeを拡張する、旧案を捨てるなどの選択を記録する。
4. rejected planは言い換えず、前提・分解・route・設計を再考したrevisionへ更新する。
5. ruling、残存不確実性、次のbounded unitを返す。

## Output contract

`plan_ruling` は conflict_id、inputs、precedence、decision、rejected_reasons、new_revision、scope、acceptance_evidence、unresolved_boundaries を持つ。

## Stop conditions

- canonical sourceとユーザーの現行制約が両立せず、解決主体が未定義。
- safety、auth、policy gateを削って成立させる必要がある。
- 同じrejected案の無限再試行になる。
- rulingでscope、owner、終端証拠を確定できない。
