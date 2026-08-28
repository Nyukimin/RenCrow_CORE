---
name: tdd-task-implementation
description: Implement one bounded task with red-first tests, a minimal owner-side patch, green verification, and explicit unresolved boundaries.
version: 1.0.0
---

# TDD Task Implementation

## Purpose

このカードは、確定済みimplementation planを1 unitずつ実装する。Coderはplan/patchを提案し、Workerまたは所有moduleの実行境界だけが編集・test・外部効果を担当する。

## Inputs

- approved bounded implementation plan
- baseline receipt and exact allowed files
- canonical contracts and red test or verification point

## Procedure

1. planとscopeを再確認し、対象外の変更を分離する。
2. 失敗するtestまたは再現確認を先に追加し、期待statusとreceiptを固定する。
3. 最小のowner-side implementationを行う。旧route・重複正本が不要なら同unitで削除する。
4. relevant test、format、lint、buildを順に実行し、失敗は握りつぶさない。
5. diff、実行結果、未確認のruntime/E2E境界を返す。

## Output contract

`implementation_result` は changed_files、red_evidence、diff_summary、test_commands、results、receipt_refs、unverified_boundaries、blockers を持つ。成功testだけでdeployやuser-visible completionを推定しない。

## Stop conditions

- planと実コードの契約が矛盾する。
- allowed files外の変更、権限拡大、direct backend routeが必要になる。
- testがtimeoutした場合は同じstepを延長再実行せず、残留と停止境界を診断する。
- canonical runtime、実Actor、または利用者向け結果が未確認のまま完了を宣言しそうになる。
