# ActionStore 日次segment回転 Failure Knowledge

記録日: 2026-10-13 (JST)。対象: `RenCrow_CORE` のAction store（`<workspace>/state/actions/`）。設計案は
`20261013_115300_ActionStore全件読み_設計案.md`（実装単位IU-5。実装commitと仕様との差は7章・7.3、実測は7.4）。
task storeの同種の記録は `20261012_192124_TaskStore日次segment回転_FailureKnowledge.md`。本書はIU-5で確定した不変条件と、
その強制・試験を残す。health、Check Plan、Manifest（IU-6）は含まない。

## Failure

履歴量に比例するcommit時間。`jsonlbatch`は追記先fileの先頭からの全体SHA-256を1 transactionで2回計算するため、約80MBの
2 logへ書く限りcommit p50が約1.7〜2.0秒かかった。常駐snapshot（IU-3）でexec（全件読み・全件検証）が消えた後も、write
transaction全体の大半（Tool 1回の作成・終端の2 commitで約3.4〜4秒）を占めた。

## Problem

Action storeは全履歴を1組のlogに追記していた。snapshotは「どの記録を読むか」を履歴から切り離したが、「どのfileにどれだけ
書くか」は履歴の大きさのままだった。

## Cause

追記専用JSONLの書込み先が1組のfileに固定されていた。`jsonlbatch`のprefix hashは追記先fileの全体に比例する。

## Lesson

書込み先を日ごとの小さなsegmentに分け、閉じたsegmentは二度と書かない。履歴は動かさず、全segmentをsnapshot（派生物）が
結ぶ。戻れない操作は、(1)戻す道具（連結）が先に試験され、(2)記録（`LAYOUT`・`SEALED`）が原子的で、(3)失敗が「何も変わらない」か
「次の起動が続きから終える」のどちらかになるように作る。**sidecarのような派生物に閉じたsegmentの記録を預けない**: 記録は
segmentごとの不変fileに、次のsegmentを作る前に書く。そうすると「記録が無い閉じたsegment」は落ちた結果ではなく異常になり、
初回に読んだ値を信じる経路（TOFU）が要らなくなる。

## Invariant / Enforcement / Tests

| ID | Invariant | Enforcement | Tests |
| --- | --- | --- | --- |
| ARC-1 | 閉じたsegment（segment 0を含む）のlogは書き換え・削除・移動をしない。書込み先は最新のsegment 1つだけ | 書込みは`s.batch`（最新segmentのjsonlbatch）だけ。segment 0は凍結時、他は封印時に2 logとWALを0444にする。回転・移行・concat検査は閉じたsegmentのlogを開いて書かない | `TestAClosedSegmentIsNeverWrittenAgain`、`TestFirstOpenWithRotationMovesThePlainStoreToSegments`（size・SHA-256不変） |
| ARC-2 | 書込み先は`max(既存の最後, 今日)`。時計が戻っても過去segmentへ戻らず、同じ日は99 segmentまでで超えても書込みは止まらない | `rotationTarget`は現segmentの日より進んだ日にしか次を選ばない。`observeClock`が逆行の1区間につき有限の行数だけWARN | `TestAClockThatGoesBackNeverReturnsToAnEarlierSegment`、`TestADayHasAtMost99SegmentsAndTheStoreKeepsWriting` |
| ARC-3 | 閉じたsegmentのsizeが記録と違えば、記録が無ければ、`LAYOUT`・`SEALED`の版が未知・欠け・切り詰めなら、起動しない（fail closed）。何も書かない | 記録の正本は、segment 0が`LAYOUT`、他が各segmentの`SEALED`。全openerの入口（`openLayout`）がsegment 0を、`buildSnapState`が閉じたsegmentを**読む前にstatで**、読んだ後に読んだbyte数で照合する。`verify`・`layout show`・`concat`も同じ記録を使う | `TestAChangedSegmentFailsClosedAndNothingIsWritten`（6通り）、`TestAnUnknownOrDamagedLayoutFailsClosedAndNothingIsWritten`（8通り。writer・verify・layout show） |
| ARC-4 | `SEALED`が回転のcommit point。書いた後のsegmentには書かない。書いた後に止まった回転は、次のopenが続きのsegmentを作って終える。書く前の失敗は旧segmentのまま | 順序R1 settle → R2 `SEALED`（tmp＋fsync＋rename＋dir fsync）→ R3 次のsegment作成 → R4 切替。次を作る前に旧segmentのdirを再度fsync。`openWriterBatch`は最新が封印済みなら次を作る（`Rotation`の値に依らない）。`r.incomplete`が書込みを次の作成の試みに向ける | `TestRotationStoppedAtEveryStepLosesNothingAndTheStoreGoesOn`（6点）、`TestARotationThatStoppedAfterTheSealIsCompletedAtTheNextOpen`、`TestSegmentedStoreSurvivesAProcessKilledAtEveryStage`（回転4段＋commit内4段の`os.Exit`） |
| ARC-5 | 別のwriterが封印したsegmentには書かず、その封印を上書きもしない（回転後の2つ目のwriterは受け付けない） | 書込みごと（OS lock下）に`requireOpenSegment`が現segmentの`SEALED`をstatして`ErrSegmentMissing`。`rotateLocked`も最初に同じ検査 | `TestASecondWriterFailsClosedOnceTheFirstHasRotated`、`TestARotationStopsWhenAnotherWriterAlreadySealedTheSegment` |
| ARC-6 | snapshot = 全segmentを順に全件読みした結果。切替は読みと排他で、hookは常に自分のsegmentに適用する | 構築・再構築は全segmentを順に畳み込む。読みは`stale`判定から答えまで`segMu`を読み取りで持ち、切替だけが書き込みで持つ。hookは`onCommitFor(ordinal)`で固定し、違うsegmentの適用は壊れ印を付けて作り直す | snapshotの全試験（`requireSnapshotEqualsLog`が全segmentを読む）、`TestRotationUnderConcurrentReadersAndWriters`（`-race`。回転しても作り直し0回）、`TestAHandleSeesAnotherHandlesWritesToTheNewestSegment` |
| ARC-7 | 旧版binaryは凍結したsegment 0を開けず、何も書かない | segment 0とWALを0444にする。`jsonlbatch.New`は既存fileを`O_RDWR`で開くので、旧版はopen時点で失敗する（root権限では効かない。次の新版のopenがsegment 0のsize照合で止まるのが防御） | `TestAnOlderBinaryCannotOpenASegmentedStoreAndWritesNothing`、本番ホストのコピーでの予行（設計案7.4） |
| ARC-8 | 戻れない点は`LAYOUT`のrename。それ以前の失敗はv1のまま継続し、以後の失敗は次のopenが続きから終える | 移行M1 settle・snapshot一致 → M3 最初のsegment → M4 `LAYOUT` → M5 凍結の順序。`firstSegment`が空の孤児dirを再利用。M4前の失敗は`migrationAborted`でv1継続 | `TestMigrationStoppedAtEveryStepLosesNothingAndCanBeFinished`（全failpoint × 再移行／回転なしで開く） |
| ARC-9 | `layout concat`は元の形式を、「元データ＋以後の追記」とbyteで一致するように戻す。途中で止まっても再実行で終わり、止まった状態はどのopenerも開かない。何も削除しない。稼働中のwriterに追記させない | LAYOUTのsegment 0サイズから位置を判定し、端数は次のsegmentの先頭と一致する場合だけ切り戻す。完了順は「追記と全byte照合 → `logs/`をrename → `LAYOUT`をrename」。最新segmentのOS lockを取って全fileを0444にしてからコピーする（`ErrStoreBusy`）。閉じたsegmentの記録が合わなければ何も変えずに止まる | `TestConcatReturnsTheStoreToThePlainLayoutByteForByte`、`TestConcatStoppedAtEveryStepIsFinishedByRunningItAgain`（13点）、`TestConcatRefusesAChangedSegmentAndChangesNothing`、`TestConcatRefusesWhileTheStoreIsInUse`、`TestAWriterThatSurvivesTheStartOfAConcatCannotAppend` |

## 設計上の判断（task storeとの差の理由）

- **閉じたsegmentの記録はsidecarではなく`SEALED`**（segmentごとの不変file）。本storeはD7でsidecarを作らず、`LAYOUT`を回転のたびに
  書き直す案は「版標識」を可変にするため採らなかった。task storeの「sidecarが無い起動では検査できない」という弱点が無い。
- **`SEALED`を次のsegmentより先に書く**。task storeは作ってから切り替えるので、回転の途中で止まると閉じたsegmentの記録が
  無いことがあり得る。本storeは「記録が無い閉じたsegment」を起動できない異常にして、初回に読んだ値を信じる経路を作らなかった。
  代償は、`SEALED`の後に次のsegmentを作れない間は書込みがエラーになること（空き容量・I/O障害では追記も失敗するので実質同じ条件）。
- **回転を`s.mu`保持下で行う**。Action storeのwriterは`s.mu`で直列で、読みは`s.mu`を取らない。判定と実行が一つの臨界区域になり、
  gateも`TryLock`も要らない。`segMu`はブロッキングで取る（`TryLock`の待ちは、読みが常に重なる負荷で取れなかった）。
- 閉じたsegmentの2 logとWALも0444にする（task storeはsegment 0だけ）。安い防御で、`SEALED`の照合が本体。

## 残るリスク・未確認

- **閉じたsegmentが丸ごと消えたこと**（途中のdirectoryの削除、最新のdirectoryの削除）は検出できない。segmentの名前の連鎖
  （各`SEALED`が前のsegmentの名前を持つ等）を記録していない。最新のdirectoryが消えると、その前の封印済みsegmentに次が作られる。
- snapshotの作り直しは全segmentを読み直す（本番コピーで約2.4秒、履歴に比例）。dirtyや壊れ印が常態化すると、書込みと読みが
  その間止まる。閉じたsegmentの畳み込みを不変のbaseにして現segmentだけ読み直す最適化は未実装（IU-6以降の候補）。
- 同じstoreを複数processが書く構成は、回転後の2つ目のwriterを受け付けない（`ErrSegmentMissing`）。読取専用の`rencrow actions verify`は
  自分で構築するので影響しないが、稼働中のstoreに使うと最新segmentのOS lockを検査の間取る。
- `rotation.enabled=false`に戻した回転済みstoreは最新のsegmentへ書き続け、commit時間が再び伸びる（起動時にWARN）。
- root権限で動く場合、0444は効かない。size照合が防御。Windows（読取専用属性、rename）とmacOSのfile descriptor上限は
  `go vet`とcross buildまでで、実機未検証。
- 旧版の`rencrow actions verify`は回転後のstoreのsegment 0しか読まず`ok`と報告する。
- 本番での`run`による実起動のM1〜M5は未予行（`NewJSONLStoreWithOptions`をharnessで本番コピーに対して実行した範囲）。
