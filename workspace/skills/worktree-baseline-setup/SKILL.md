---
name: worktree-baseline-setup
description: Establish a read-only baseline for the owning worktree, including repository identity, dirty state, branch, toolchain, and reproducible test entrypoints.
version: 1.0.0
---

# Worktree Baseline Setup

## Purpose

このカードは、作業開始前のworktree状態をreceipt化する。既存のdirty変更を保護し、指定されていないbranch、stash、reset、install、restartを行わない。

## Inputs

- exact worktree and repository root
- allowed task scope and local repository rules
- expected build/test entrypoint

## Procedure

1. `git rev-parse --show-toplevel` と現在branch・HEADを確認する。
2. `git status --short --branch` で既存差分とuntrackedを記録する。
3. AGENTS、README、docs、module-local rulesのread orderを確認する。
4. toolchain、config source、test command、temporary output boundaryを確認する。
5. baseline hashと観測時刻を固定し、変更禁止境界を明示する。

## Output contract

`baseline_receipt` は repository、worktree、branch、head、dirty_paths、rules_refs、toolchain、test_entrypoint、temp_root、observed_at を持つ。setupが必要な場合は別の明示scopeへ返す。

## Stop conditions

- worktree rootまたはowner repositoryが期待と異なる。
- dirty stateを保護できない。
- branch変更、stash、reset、installが必要になる。
- test出力がrepo内の許可されたTemp境界に収まらない。
