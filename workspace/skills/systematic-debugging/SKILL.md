---
name: systematic-debugging
description: Investigate a symptom with evidence-led hypotheses, discriminating checks, and a bounded remediation proposal without guessing or bypassing canonical routes.
version: 1.0.0
---

# Systematic Debugging

## Purpose

このカードは、観測事象から原因を仮説駆動で絞る。診断と修正・restart・deployを分離し、正規runtime経路を短縮して成功に見せない。

## Inputs

- user observation, symptom, time window, and reproduction
- source, config, logs, process/socket, and test evidence
- canonical route and expected contract

## Procedure

1. symptom、known-good、timeline、scope、actual actorを固定する。
2. owner、source、artifact、config、process、route、storageの各境界を確認する。
3. 仮説ごとに支持証拠、反証証拠、識別test、予想結果を記録する。
4. 同じ仮説の試行は2回までにし、失敗時は前提または観測経路を再確認する。
5. 原因、最小修正方針、再現test、未確認境界を返す。

## Output contract

`debug_report` は symptom、known_good、timeline、hypotheses、checks、root_cause、remediation_scope、regression_test、unverified_boundaries を含む。証拠のない断定や代替routeの結果をcanonical成功としない。

## Stop conditions

- ユーザー観測と分析が矛盾し、再観測できない。
- process owner、config、route、実Actorが特定できない。
- direct backend、fake server、別modelでの回避が必要になる。
- 修正scopeまたは破壊的操作の許可がない。
