---
name: rencrow-remediation-loop
description: "RenCrowの不具合を、1件ずつTDD修正、正規E2E、Push、再build、再起動、deploy、修正箇所の配備後テストまで閉じ、全件解消後にだけフルチェックする。ユーザーが修正から配備まで反復して最後に全体検証するよう求めた時に使う。"
---

# RenCrow Remediation Loop

## Outcome

未達一覧を単なるsource修正で消さず、各不具合について次の終端経路を1件ずつ完成させる。

```text
canonical source -> owner -> failing test -> fix -> relevant tests
-> canonical E2E -> commit/push -> pinned artifact -> rebuild/restart/deploy
-> active runtime identity -> same defect test -> receipt
```

全対象がこの経路を通過した後だけ、`rencrow-full-system-verification`を使って固定Planのフルチェックを行う。

## Activation and authorization

このSkillは、ユーザーが不具合修正に加えてPush、再build、再起動、deployまで明示的に求めた時に使う。
read-only監査には使わず、`rencrow-full-system-verification`を直接使う。

依頼に含まれないrepository、host、service、外部公開、credential、data migrationへscopeを広げない。
明示されたループは対象修正のcommit、push、owner標準deploy、必要なservice restartを許可するが、branch作成、
force push、履歴改変、任意process停止、database削除、代替topology作成は許可しない。owner、target、rollback境界が
確定しない破壊的操作は実行せず、その不具合を`blocked`として止める。

## Classification gate

実装前に各工程を記録する。

- `CLI`: 再現、test、build、Git、artifact identity、service/listener、health、E2E receipt取得。
- `LLM`: 原因仮説、意味判断、複数の妥当な修正案からの選択だけ。採用理由を残す。
- `Boundary`: owner、認証、policy、schema、state変更、push、deploy、restart、rollback、receipt検証。

決定可能な検査や配備をLLMへ委ねず、LLMの説明をtestやreceiptの代用にしない。

## Preflight

1. workspace rootの`AGENTS.md`と対象moduleの`AGENTS.md`、正本仕様、active deployment contractを読む。
2. catalogと各childを別Git repositoryとして扱い、対象ごとにowner、正本、upstream、branch、dirty差分を記録する。
3. frozen defect ledgerを作る。各行は最低限`defect_id`、症状、owner、canonical route、再現command、
   expected、actual、severity、依存、terminal evidence、statusを持つ。
4. ASSISTANTのようなrequired-but-unimplemented componentはCoverage Policyの構造化除外に従い、実装済み不具合と混ぜない。
5. WIPは常に1件。依存順を除き、次の不具合へ移る前に現在行を配備後receiptまで閉じる。

## Per-defect state machine

各不具合で次を順番どおり実行する。途中の成功を後段の代用にしない。

### 1. Reproduce and RED

- 正規routeで症状を再現し、fresh evidenceを保存する。
- 最小のbehavioral testまたはarchitecture testを追加し、修正前に期待理由で失敗する`RED`を確認する。
- test doubleだけで本番E2Eを代用しない。REDを作れない場合は理由と別の機械的再現contractを明示する。

### 2. Implement and local GREEN

- 既存ownerとcanonical routeを拡張し、新しい短縮routeや独立正本を作らない。
- 最小修正後、追加test、ownerのrelevant suite、lint/vet/buildを実行する。
- timeout時は時間だけ延長せず、残留process、cache、network、child processを診断する。
- Hard Invariant、Semantic Duplication、旧route残存、Failure Knowledgeを確認する。

### 3. Pre-deploy canonical E2E

- 本番と同じ認証、policy、owner module、runtime route、実ActorでE2Eを行う。
- source側だけでは実行不能なら、build artifactを隔離した一時環境でcontractを確認し、production成功とは呼ばない。
- E2Eが失敗したら原因を再分類し、同じ不具合のREDへ戻る。次の行へ進まない。

### 4. Commit and Push

- 対象pathだけstageし、ユーザーの無関係なdirty差分を含めない。
- affected repositoryごとにtest済みdiffをcommitし、通常pushする。force pushしない。
- cross-module変更はowner dependency順でpushし、catalog pinを実際のfull commit SHAへ更新してcatalog検証後にpushする。
- `local HEAD == upstream`とCIまたはremote receiptを確認する。Push失敗をdeployで迂回しない。

### 5. Rebuild, restart, and deploy

- pushed/pinned revisionからowner標準CLIでbuildし、artifact stamp/hashをsource revisionと照合する。
- 固定portとruntime identityを維持する。listener競合ではownerが一致する旧generationだけを標準service managerで扱う。
- 対象serviceだけを依存順に再起動・deployする。`active`だけで成功としない。
- installed artifact、active config、service PID/cgroup、listener destination、readinessを一つのidentity chainで確認する。
- restart loop、readiness failure、identity mismatch時は新しい試行を重ねず原因診断へ戻る。owner deploy contractに自動rollbackが
  定義されている場合だけその結果を検証し、未定義の手動rollbackは新しい許可なしに行わない。

### 6. Post-deploy defect test

- 最初の再現commandと追加した修正部分testを、配備済みartifactと正規routeに対して再実行する。
- 必要なuser-visible、browser、media、Agent、DB、backup結果まで確認し、fresh receipt/traceを保存する。
- source revision、pushed revision、installed artifact、service identity、実request receiptが一致した時だけ行を`fixed`にする。
- 失敗、blocked、unverifiedなら同じ行を開いたまま原因を更新し、REDからループする。

## Loop control

- 同じ失敗条件を無変更で再実行しない。各revisionは前回Evidenceから前提、分解、route、または実装を変更する。
- 新しい不具合を発見したらledgerへ追加するが、現在のWIPを中断する必要がある依存・安全問題でなければ順番を保つ。
- required terminal evidenceが一つでも欠ける行を`fixed`にしない。
- 全行が`fixed`または正本policyに基づく`not_applicable`になるまで最終フルチェックへ進まない。
- credentialや外部hostなど新しい権限が必要なら、該当行と正確な境界を`blocked`として報告し、他を成功扱いして終了しない。

## Final full check

全行を閉じた後、新しい評価時刻で `$rencrow-full-system-verification` を読み、同Skillのread-only手順を完全実行する。
修正中に得た部分receiptを最終結果へ手動転記せず、Coverage Policy、全owner manifest、全required phaseからPlanを再composeする。

最終条件は次のすべてである。

- aggregate trackerが全included checkを持つ。
- `failed=0`、`blocked=0`、`deferred=0`、`unverified=0`。
- 許可される除外は正本Coverage Policyに構造化されたrequired-but-unimplemented componentだけで、理由と再参加条件を報告する。
- source、remote、catalog pin、installed artifact、runtime route、実Actor/user-visible result、receiptが整合する。

フルチェックで新しい未達が出たら、その行をledgerへ追加してper-defect loopへ戻る。フルチェックを部分checkで置換しない。

## Report

各iterationでdefect ID、RED、changed files、tests、E2E、commit/push SHA、artifact identity、service PID、readiness、
post-deploy receipt、残る境界を短く更新する。最終報告は全iteration一覧とaggregate revision、status counts、除外、
`all_clear`を含める。未確認が残る場合は「完了」「問題なし」と報告しない。
