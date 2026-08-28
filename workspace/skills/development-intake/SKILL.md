---
name: development-intake
description: Turn a development request into a bounded intake with an owning repository, canonical source, constraints, and terminal evidence.
version: 1.0.0
---

# Development Intake

## Purpose

このカードは、実装を始める前に依頼の目的と終端条件を固定する。Skill本文は判断材料を整理するだけで、権限や実行主体を付与しない。

## Inputs

- user goal and requested verb
- target repository or module, if known
- observed behavior, constraints, and required evidence

## Procedure

1. 依頼を運用成果と要求動詞に分解する。
2. owning repository/module、canonical specification、current runtime route を確認する。
3. source、policy、state、route、利用主体、visible result、receipt の終端を記録する。
4. CLI、LLM、Boundary の工程分類を行い、LLM採用理由が必要なら明記する。
5. 許可範囲、対象外、失敗時status、検証commandを固定する。

## Output contract

`intake` は goal、owner、canonical_refs、allowed_scope、forbidden_scope、classification、acceptance_evidence、open_questions を含む。未確認の終端は `unverified` として残し、実装開始条件にしない。

## Stop conditions

- ownerまたはcanonical sourceが決まらない。
- 要求の終端証拠が定義できない。
- authority requirementをruntime policyで確認できない。
- 既存routeを迂回する提案になっている。
