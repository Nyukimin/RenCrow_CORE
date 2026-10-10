# OPS原文受付の競合とpanic終了保証（2026-10-09）

## 文書情報

- 日付: 2026-10-09 JST
- 引き継ぎ元: RenCrow_Harness完了作業におけるCORE accepted OPS input Stage2
- owner: RenCrow_CORE Conversation owner
- 契約正本: `docs/02_機能仕様.md`、`docs/04_アーキテクチャ概要.md`、`internal/domain/conversation/accepted_ops_input.go`
- 検出／根拠: `Tmp/test-runtime/_runs/ops-acceptance-panic-cleanup-20261009T0226Z/`、`Tmp/test-runtime/_runs/ops-acceptance-stage2-independent-verify-20261009T0236Z/`

## 結論

内部受付境界の2件を修正した。独立SQLite handleでの受付競合は既存poolの接続で読取り前に`BEGIN IMMEDIATE`を行い、同じpayloadは保存済みreceiptへ、異なるpayloadはConflictへ収束させる。開始済みtransactionは正常return、エラー、panicの全経路で終了し、rollbackを確認できない接続をpoolへ戻さない。

これは内部受付・原文読出しの結論である。HTTP intake、Human署名、Task/Runの実行claim、Harnessへの実委譲、配備・運用受入の完了を意味しない。

## 実測

| 証拠 | 結果 |
| --- | --- |
| R3独立handle混在payload競合 | 10/10失敗。各runの3回の試行が初回thread bindingでSQLite code 5 (`SQLITE_BUSY`)、最終Unavailable |
| R4書込開始順序修正 | 同payload・異payloadの独立handle試験各10回成功 |
| R5 panic RED | pre-COMMIT panic後、同じpooled connectionから未commitの受付・manifest・record・state・threadの5種類の行が見えた |
| R6終了処理修正 | panic、同payload、異payloadの試験各10回成功。関連Common Raw試験成功 |
| 別担当による独立検証 | domain 121件、対象L1SQLite 47件、競合・snapshot・panicの4件をrace有効で成功 |

最終Stage2対象source digestは`d4a76282a7e39ceb249742f74fd9a45a73d8997a8657c818960860d595280a63`。対象変更またはSQLite／設定／検証環境の変更時は該当証拠を再評価する。

## Failure Knowledge

### Failure

同一requestへの異payload再送がConflictでなくUnavailableとなった。また、手動transaction開始後のpanicで未終了transactionが接続poolへ戻り得た。

### Problem

受付identity、Common Raw原文、最初のcanonical Threadを一つのtransactionで保存する境界に、独立handle間の書込開始順序と異常終了時の接続状態保証が不足していた。

### Cause

固定依存の既定`BeginTx`はdeferred transactionであり、receipt／thread読取り後の初回writeが競合した。短い同条件再試行ではwinnerのcommit前に試行を消費できた。`sql.Conn`上の文字列`BEGIN IMMEDIATE`へ変更した際、`Conn.Close`だけをdeferしてもtransaction終了は保証されなかった。

### Lesson

一つのstore instanceのmutexで独立handle間の競合を検証した扱いにしない。手動transactionにはdriverの`sql.Tx`と同じ終了保証を明示的に用意する。通常returnだけでなくpanic、cancel、commit失敗、rollback失敗を確認する。

### Invariant

- 読取りより前にwriterを確保し、一つの既存接続で全受付処理を行う。
- 同じowner／request／canonical payloadは保存済みidentityへreplayする。
- 異payloadのみConflictとし、BUSY、IO、その他のSQL障害をConflictに丸めない。
- 原文はCommon Rawだけが正本である。
- 未終了transactionの接続をpoolへ戻さない。
- 署名・外部実行時のraw state認可は、その境界で別途確認する。

### Enforcement

`acceptOPSInputTransaction`は既存poolの`Conn`で`BEGIN IMMEDIATE`し、`transactionFinished`と一つの終了処理を共有する。rollbackはrequestのcancelから独立した5秒contextを使い、確認できない接続を`driver.ErrBadConn`で破棄する。defer順序によりrollback／破棄が接続Closeより先に動く。読出しとretry読出しは同じread snapshotで原文・状態・hashを検証する。

### Tests

`l1_sqlite_ops_acceptance_test.go`の独立handle同payload／異payload、再open replay、状態制限、snapshot、commit失敗、rollback失敗、`TestAcceptOPSInputPanicRollsBackAndReleasesConnection`を維持する。通常Common Raw helperの成功・rollback・storage-host receipt順序の回帰も維持する。

## 未確認

- FKはapplication検証であり、共有DBのFK enforcementを新設したものではない。
- commit失敗試験はcancelされたcontextによる試行とcleanupを検証し、全ディスク障害を網羅しない。
- R1–R4は一部のtemp／cache配置が正規規定外だった。旧証拠はhash付きで正規`_runs`へ保存し、R5/R6と独立検証は正規owner runtime配置で実行した。
- remote owner、実user ingress、Human署名、Shiro実行、配備後の再起動保持はこの記録の受入範囲外である。
