---
name: finish-implementation-unit
description: Close one implementation unit only after scope, canonical source, tests, architecture invariants, and required terminal evidence are reconciled.
version: 1.0.0
---

# Finish Implementation Unit

## Purpose

このカードは、実装unitのcloseout判定を行う。実装項目、test、build、healthだけを完了の代替にせず、要求ごとの終端と未確認境界を残す。

## Inputs

- original request, accepted plan, and baseline receipt
- complete diff and canonical specification changes
- TDD, architecture, route, delivery, and user-visible evidence

## Procedure

1. 要求動詞ごとのevidence matrixを埋める。
2. allowed scope、旧経路削除、正本更新、semantic duplication、hard invariantを確認する。
3. CLI、LLM、Boundaryの当初分類と実装後routeを照合する。
4. relevant tests/build/verificationを同一revisionで確認する。
5. all required terminal evidenceが揃う場合だけcloseし、残るものは `unverified`、`blocked`、`deferred` で返す。

## Output contract

`implementation_unit_closeout` は unit_id、request_evidence、diff_scope、canonical_updates、tests、architecture_checks、terminal_evidence、status、remaining_boundaries を含む。

## Stop conditions

- 一つでも必須終端条件が未確認。
- 実装差分と検証済みhashが一致しない。
- 配備・再起動・E2Eが要求されるのに証拠がない。
- 完了のために証拠のない推定、scope拡大、旧経路温存が必要。
