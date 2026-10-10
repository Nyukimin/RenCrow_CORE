# 2026-10-09 Common Raw Data StorageHost race suite timeout — Failure Knowledge

## 文書情報

- 日付: 2026-10-09 (JST)
- 引き継ぎ元: StorageHost の Common Raw Data race 検証で得られた既知証拠
- 契約正本: `docs/02_機能仕様.md` の Common Raw Data、`docs/04_アーキテクチャ概要.md` の記憶ストレージ、`docs/05_設定リファレンス.md` の DB 物理配置と backup
- 検出／根拠 path:
  - `internal/infrastructure/persistence/storagehost/common_raw_group_test.go`
  - `Tmp/test-runtime/_runs/core-human-relay-storagehost-isolated-20261009/`
  - `Tmp/test-runtime/_runs/core-human-relay-commonraw-fixture-fix-20261009/`
  - `Tmp/test-runtime/_runs/core-human-relay-commonraw-receipt-budget-20261009/`
  - `Tmp/test-runtime/_runs/core-commonraw-storagehost-full-independent-20261009/`

## 結論

Focused race 検証では、最大 batch と、保存完了後の response loss を想定する oversized replay の両方が通った。後者は完了した応答を受信した後の応答消失 hook、再起動前の durable `DONE`、再起動後の exact receipt、件数と重複なしを確認している。一方、独立した全 StorageHost race suite は既定 10 分 watchdog の budget を使い切り、満了時には別の UserMemory test が `ACTIVE` のまま残った。全 suite 成功、deadlock 解消、race suite 全体の合格とは扱わない。

当初の oversized test では、完了した応答を受信した後の応答消失 hook が実行されず、保存完了後に応答を失った前提を証明できなかった。`OutcomeUnknown` と journal `BEGUN` は request が開始されたことを示すが、この応答消失条件を証明しない。全 suite の watchdog budget exhaustion は確認済みだが、underlying performance issue または別 defect は未特定である。

## 実測

| 対象 | 結果 |
|---|---|
| author／independent source-only 検証報告 | focused と full native の独立 normal/race 検証 7 invocation、および group vet 6 件が成功。author の full normal も成功。これらは runtime／実 Actor を通した production acceptance の証拠ではない。 |
| 旧 full StorageHost race | Common Raw の大きな 2 case が client default 30 秒で `OutcomeUnknown` に至った。journal `BEGUN` は request 開始を示すが、完了応答受信後の応答消失を示さない。operation ごとに deadline を設定できる契約は `docs/04_アーキテクチャ概要.md` にあるが、該当 test に 30 秒 SLO はなかった。 |
| 旧 oversized assertion | `OutcomeUnknown` と `BEGUN` だけでは、完了応答受信後の応答消失 hook が動作し、保存完了後に応答を失ったことを証明できなかった。 |
| 強い precondition による再実行 | oversized を 30 秒のままにした guard は 34.49 秒で完了応答受信後の応答消失 hook が未実行と検出し、保存完了後の応答消失を証明できない旧 precondition の false positive を確認した。 |
| Fixture 限定修正後の focused race | 最大 batch は 58.57 秒で成功。oversized replay は 203.43 秒で成功し、完了応答受信後の応答消失 hook、再起動前の journal durable `DONE`、再起動後の exact receipt、件数と重複なしを確認。production default と通常 fixture の 30 秒設定は維持。最終 test source SHA-256: `e7b97dd153c34e659875451156ddd19814106276561b5bdfb9aa3aaa48a9bb47`。 |
| 独立 full StorageHost race | 既定 10 分 suite watchdog が 600.063 秒で満了し、suite gate は FAIL、outer は 743.8767452 秒。満了時の active test header は `ACTIVE TestUserMemoryGroupMissingReceiptRemainsNonrepeatableOutcomeUnknown (0s)`。個別 test の assertion failure は確認されていない。診断には L1 schema 573 と SQLite WAL `pwrite` が記録されたが、原因を確定しない。vet は 70.36 秒で成功。source pre/post SHA は同一。 |

## 未解決／未確認

- 全 StorageHost race suite の underlying performance issue または別 defect と、15 分 watchdog での結果は未確認。Recall の再開と CORE source freeze 後に、承認済みの canonical PowerShell／owner temp 経路で `-race -count=1 -timeout=15m -json` を一度実行する予定だが、未実行。expiry 時は実際の失敗 stage を調べ、自動延長しない。
- Runtime route、実 Actor、再起動を通す formal production acceptance は未確認。
- Timeout 時の SQLite 診断は状態の観測であり、deadlock、WAL が根本原因、または全 race suite の合格を示さない。

## Failure Knowledge

### Failure

全 StorageHost race suite は既定 watchdog の budget を使い切った。旧 oversized test は `OutcomeUnknown` と journal `BEGUN` で request 開始までは確認したが、完了応答受信後の応答消失 hook を確認していなかった。

### Problem

client の 30 秒 default と、より長い処理を許容する operation-specific deadline の区別が test fixture に反映されず、response loss 後の durable 成功を検証できなかった。

### Cause

旧 oversized test の false positive は、30 秒の client timeout までに完了応答受信後の応答消失 hook が実行されなかったことを guard が確認した。全 suite の watchdog budget exhaustion は確認済みだが、underlying performance issue または別 defect は未確認であり、診断中の `ACTIVE` header と SQLite WAL `pwrite` だけでは因果を断定できない。

### Lesson

response loss 後の成功を検証するには、完了応答受信後の応答消失 hook と、再起動前に対象 group／operation／payload hash の durable `DONE` があることを確認し、再起動後に exact receipt と件数・重複なしを照合する。処理時間を test で許容する deadline は、その operation に限定する。

### Invariant

成功した oversized replay の証拠には、完了応答受信後の応答消失 hook、journal の durable `DONE`、exact receipt、期待件数、重複なしを含める。client timeout や `OutcomeUnknown` 単独を commit 後の response loss の証拠にしない。全 suite は独立して完了するまで合格扱いしない。

### Enforcement

`internal/infrastructure/persistence/storagehost/common_raw_group_test.go` の fixture 修正は、初回 intake client の test timeout を 120 秒に限定し、oversized replay は完了応答受信後の応答消失 hook と再起動前の durable `DONE` を assertion する。production 設定と通常 fixture の 30 秒 default は維持する。

### Tests

- 強い guard を付けた focused maximum-batch race: PASS, 58.57 秒。
- 強い guard を付けた oversized replay race: PASS, 203.43 秒。送信 hook、durable `DONE`、exact receipt、件数と重複なしを確認。
- 独立 full StorageHost race: suite watchdog budget exhaustion / gate FAIL, 600.063 秒。満了時の active test は `TestUserMemoryGroupMissingReceiptRemainsNonrepeatableOutcomeUnknown (0s)`。個別 test の assertion failure は確認されていない。
- vet: PASS, 70.36 秒。
- 15 分 watchdog の full race suite: 未実行。
