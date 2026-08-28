---
name: rencrow-development-loop
description: Orchestrate the RenCrow development lifecycle from intake through specification, bounded TDD implementation, review, delivery verification, and evidence-based closeout.
version: 1.0.0
---

# RenCrow Development Loop

## Purpose

このカードは、RenCrowの開発unitを一方向の証拠鎖で進める上位methodologyである。各工程のownerを保ち、Skill、LLM、model、providerはactor・権限・正本にならない。

## Inputs

- user goal, requested outcome, and explicit constraints
- target repository/module and current baseline
- canonical specifications, backlog state, and required operational evidence

## Procedure

1. `development-intake` で目的、owner、正本、終端を固定する。
2. 必要なら `backlog-maturation-revalidation` と `design-to-spec` で現行契約を確定する。
3. `implementation-planning` と `worktree-baseline-setup` でbounded unitを作る。
4. `tdd-task-implementation` でred-first実装し、`systematic-debugging` を失敗時だけ使う。
5. `task-review`、`branch-review`、`plan-conflict-ruling` で差分と競合を判定する。
6. `delivery-verification` と `finish-implementation-unit` で要求ごとの終端証拠を閉じる。

## Output contract

`development_loop_receipt` は goal、unit_id、phase_records、owner_chain、classification、diff/test receipts、delivery evidence、final_status、unverified_boundaries を含む。各phaseのreceiptを省略して総合passを作らない。

## Stop conditions

- canonical owner、scope、policy、route、actor、終端のいずれかが未確定。
- rejected案を同じ前提で繰り返す。
- safety/auth/policy gateを迂回する代替経路が必要。
- 必須phaseのreceiptがなく、完了statusを確定できない。
