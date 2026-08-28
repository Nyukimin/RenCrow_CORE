---
name: task-review
description: Review one implementation task against its request, canonical specification, diff, tests, invariants, and unresolved terminal evidence.
version: 1.0.0
---

# Task Review

## Purpose

このカードは、実装unitが要求と正本を満たすかを敵対的に確認する。reviewは実行許可、権限、merge、deployの代替ではない。

## Inputs

- original request and implementation plan
- baseline and complete diff
- canonical specification, tests, receipts, and failure evidence

## Procedure

1. 要求動詞ごとに受入条件と証拠を対応付ける。
2. source-of-truth、owner、route、policy、state、actor、visible resultを追跡する。
3. diffの漏れ、範囲外変更、旧経路、semantic duplication、authority driftを探す。
4. test結果と実際の終端証拠を分け、未確認を `unverified` とする。
5. confirmed、refuted、uncertain の所見と次の修正方針を返す。

## Output contract

`task_review` は request_matrix、findings、invariant_checks、test_evidence、terminal_evidence、verdict、required_actions を持つ。未確認所見をpassへ丸めない。

## Stop conditions

- complete diffまたは元要求がない。
- canonical specとの競合を解決できない。
- unit testだけで利用者向け終端を証明しようとしている。
- review結果をそのままstate変更・merge・deployへ使おうとしている。
