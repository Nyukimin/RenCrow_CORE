---
name: branch-review
description: Compare a branch or commit range with its base, review ownership and integration impact, and produce evidence without changing branch state.
version: 1.0.0
---

# Branch Review

## Purpose

このカードは、指定されたbranch/commit rangeの変更をbaseとの差分として確認する。branch作成、checkout、rebase、merge、pushは行わない。

## Inputs

- exact repository, base ref, and target ref or commit range
- request, canonical contract, and release/integration constraints
- test and receipt evidence for the range

## Procedure

1. repository root、base/targetのSHA、merge-base、dirty stateを記録する。
2. complete diff、changed files、commit order、generated artifactsを確認する。
3. owner boundary、cross-module contract、旧route、conflict、unverified testを判定する。
4. integration order、required checks、rollback/evidence boundaryを記録する。
5. review verdictを `ready`、`changes_required`、`blocked` のいずれかで返す。

## Output contract

`branch_review` は repository、base_ref、target_ref、range_diff、ownership、integration_risks、checks、verdict、evidence_refs を含む。

## Stop conditions

- baseまたはtarget refが曖昧、欠落、異なるrepositoryにある。
- dirty stateを保護したまま比較できない。
- release claimに対して必要なtest/build/deploy evidenceがない。
- branch reviewのためにbranch stateを変更する必要がある。
