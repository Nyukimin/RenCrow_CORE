---
name: design-to-spec
description: Convert an accepted design decision into a canonical, testable specification while preserving ownership, invariants, and route boundaries.
version: 1.0.0
---

# Design to Specification

## Purpose

このカードは、設計案を実装者が検証可能なspecへ変換する。提案、仕様、policy、state変更を一つへ混ぜず、既存canonical routeの拡張可否を先に確認する。

## Inputs

- accepted problem statement and user-visible outcome
- design alternatives and rejection reasons
- affected module contracts, invariants, and failure knowledge

## Procedure

1. source-of-truthとownerを確定し、重複概念・旧route・例外を探索する。
2. entry、input、decision、output、next ownerを一方向に記述する。
3. schema、policy、state transition、error/status、receiptを定義する。
4. CLI、LLM、Boundaryを分離し、LLMの必須性または品質優位性を記録する。
5. acceptance test、failure knowledge、削除対象の旧経路をspecへ結び付ける。

## Output contract

`spec_change` は目的、scope、canonical_refs、data/schema、route、policy boundary、state machine、errors、acceptance、failure knowledge、deletionsを含む。未確定項目は実装へ渡さない。

## Stop conditions

- canonical ownerを増やさないと実現できない。
- runtime policyや認証境界が定義されていない。
- 代替案の採用理由または旧経路削除条件がない。
- 同じ意味の設定・schema・routeを複製する。
