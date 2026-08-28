---
name: backlog-maturation-revalidation
description: Revalidate a backlog item against current specifications, implementation evidence, dependencies, and a concrete acceptance route before work begins.
version: 1.0.0
---

# Backlog Maturation and Revalidation

## Purpose

このカードは、古いbacklogの文面をそのまま実装要求へ昇格させず、現行正本と現在状態へ再接続する。Skillはbacklog stateや権限を変更しない。

## Inputs

- backlog item, origin, and current status
- current canonical specifications and related items
- source, test, runtime, and deployment evidence

## Procedure

1. itemの目的、利用主体、期待効果、対象moduleを抽出する。
2. 現行spec、実装、test、route、設定、直近diffを照合する。
3. obsolete、duplicate、conflict、dependency、missing-evidence を分類する。
4. acceptance criteriaを実際の利用経路とreceiptへ落とし込む。
5. status、owner、scope、blocked reason、revalidation evidenceを提案する。

## Output contract

`revalidation` は item_id、canonical_refs、current_state、conflicts、dependencies、acceptance_criteria、recommended_status、evidence_refs を持つ。更新はbacklog ownerの検証済みcommandだけで行う。

## Stop conditions

- 現行正本とbacklogのどちらを採るか決められない。
- acceptanceがunit testだけで、利用者向け終端を含まない。
- duplicateまたは旧routeを残したまま進める必要がある。
- status変更権限または対象scopeが未確認。
