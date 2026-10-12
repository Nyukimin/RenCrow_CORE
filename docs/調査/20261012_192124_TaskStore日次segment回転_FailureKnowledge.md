# TaskStore 日次segment回転（段階2）Failure Knowledge

記録日: 2026-10-12 (JST)。対象: `RenCrow_CORE` のtask store（`<workspace>/tasks/`）。仕様案は
`20261010_205550_TaskStore_ID索引形式_仕様案.md`（実装単位IU-4）。実装commitと仕様との差は仕様案15章のIU-4の行。
本書は11.4の記録のうち、IU-4で確定した不変条件とその強制・試験を残す。IU-5（health、Check Plan、Manifest）の範囲は含まない。

## Failure

履歴量に比例するcommit時間。`jsonlbatch`は追記先fileの先頭からの全体SHA-256を1 transactionで2回計算するため、
約95MBのseg0へ書く限りcommit p50が約2.0〜2.8秒かかり、段階1（索引）で消えたexecの後もwrite tx全体の大半を占めた。

## Problem

task storeは全履歴を一つのfile群に追記していた。索引（段階1）は「どの行を読むか」を履歴から切り離したが、
「どのfileにどれだけ書くか」は履歴の大きさのままだった。

## Cause

追記専用JSONLの書込み先が1組のfileに固定されていた。`jsonlbatch`のprefix hashは追記先fileの全体に比例する。

## Lesson

書込み先を日ごとの小さなsegmentに分け、閉じたsegmentは二度と書かない。履歴は動かさず、全segmentを索引（派生物）が
結ぶ。戻れない操作（凍結）は、(1)戻す道具（連結）が先に試験され、(2)記録（LAYOUT）が原子的で、(3)失敗が「何も変わらない」か
「次の起動が続きから終える」のどちらかになるように作る。

## Invariant / Enforcement / Tests

| ID | Invariant | Enforcement | Tests |
| --- | --- | --- | --- |
| INV-1 | 閉じたsegment（segment 0を含む）のdata fileは書き換え・削除・移動をしない。書込み先は最新のsegment 1つだけ | 書込み経路は`s.batch`（最新segmentのjsonlbatch）だけ。segment 0は0444（WALも）。回転・移行・復元・concat検査は閉じたsegmentのdata fileを開いて書かない | `TestADayChangeStartsANewSegmentAndNeverTouchesClosedSegments`、`TestEnablingRotationFreezesSegmentZeroAndMovesWritesToTheFirstSegment`（size・mtime・SHA-256不変） |
| INV-6 | 書込み先は`max(既存の最後, 今日)`。時計が戻っても過去segmentへ戻らない | `rotationTarget`は現在のsegmentの日より進んだ日にしか新しいsegmentを選ばない | `TestAClockThatRunsBackwardsNeverWritesToAnEarlierSegment` |
| INV-7 | 閉じたsegmentのsizeが記録と違えば起動しない（fail closed）。segment 0の記録の正本は`LAYOUT`、ordinal 1以降はsidecarの表。LAYOUTの版が未知・欠け・切り詰めも起動しない | `openLayout`（全openerの入口）が`verifyFrozenSegmentZero`、復元の`attachSegments`が閉じたsegmentのsizeとdirectory名を照合。reader・verify・concatも同じ入口 | `TestAChangedSealedSegmentOrAnUnknownLayoutIsRefused`（writerとreader） |
| INV-8 | 旧版binaryは凍結したsegment 0を開けず、何も書かない | segment 0とWALを0444にする。`jsonlbatch.New`は既存fileを`O_RDWR`で開くので、旧版はopen時点で失敗する（rootでは効かない。size照合が防御） | `TestAnOlderBinaryCannotOpenAFrozenSegmentZero`、実機予行 |
| INV-9 | 戻れない点は`LAYOUT`のrename。それ以前の失敗はv1のまま継続し、以後の失敗は次のopenが続きから終える | 移行M1〜M5の順序（LAYOUT前に`seg-legacy.tsi`の最新化と最初のsegmentの作成）、`writeLayoutReport`の`installed`、`firstSegment`が空の孤児dirを再利用 | `TestMigrationStoppedAtEveryStepLosesNothingAndCanBeFinished`（全failpoint × 再移行／回転なし） |
| INV-10 | 回転は誰も待たせず、失敗は書込みを止めない。旧segmentで書き続け、失敗は30秒、store多忙は5秒のbackoffで再試行する | `rotateIfDue`は排他lockを**待たずに**`TryLock`を最大250ms試す（Goの排他lock待ちは新しい読取を止め、storage hostのsessionは読取lockを長く持つため）。その間に着いた書込みだけをgateで待たせ（読取は待たせない）、実行中のtxが終わる瞬間を作る。取れなければgateを開けて旧segmentで継続し、5秒後に再試行する。失敗はWARNを出して継続。回転はsegmentを作る前にWALをsettleし索引を同期する。回転が作って切り替えなかった空のsegment dirは次の回転が再利用する（disk上とメモリ上のordinalを一致させる） | `TestAFailedRotationLeavesTheWriterWhereItWas`、`TestALongLivedReadLockDoesNotStopReadsWhileARotationIsDue`、`TestARotationReusesAnEmptyNewerSegmentDirectory`、`TestRotationStoppedAtEveryStepLosesNothing`（日・size弁の両方） |
| INV-11 | `layout concat`は途中で止まっても再実行で終わり、止まった状態はどのopenerも開かない。他の何かが書いたbyteは切り戻さない | LAYOUTのsegment 0サイズから位置を判定し、境界途中の端数は「次のsegmentの先頭と一致する場合だけ」切り戻して再追記（一致しなければ`ErrSealedSegmentChanged`）。完了順は「全種別の追記と全byte照合 → `logs/`をrename → `LAYOUT`をrename」 | `TestConcatStoppedAtEveryStepAndRunAgainEndsTheSame`、`TestConcatReturnsTheStoreToLayoutV1WithEveryLineInOrder`、`TestConcatCutsBackAnUnfinishedAppendButNotForeignBytes` |
| INV-12 | 索引は全segmentを一つに結ぶ派生物で、sidecarが無い・壊れていれば全segmentのログから作り直せる。再構築結果 = 復元結果 | 位置の上位16bitがsegment ordinal。sidecar版2に閉じたsegmentの表。復元は表の照合 → 開いているsegmentの末尾再生 → 後続segmentの全再生 | `TestSegmentedIndexMatchesFullFoldOnRandomOperations`（oracle差分、10〜15 segment、再起動6種）、`TestEverySingleBitFlipAndTruncationOfALayoutV2SidecarIsHarmless`、`TestACrashRightAfterARotationReplaysFromTheOlderSidecar` |

## 設計上の判断（仕様との差の理由）

- **閉じたsegmentのWALがpending/tornなら修復せず起動しない**（仕様5.3末尾は「標準のRecoverを1回適用」）。回転は閉じる
  segmentのWALを先にsettleするので、閉じたsegmentの未settleは落ちた結果ではない。rollbackはfileを切り詰めるため、
  外部が書いたbyteを消す前に止める。最新のsegmentの回復は従来どおり起動時のRecoverが行う。

- sidecarは仕様4.3のsegmentごとの不変sidecarではなく、**累積1ファイル**（版2）にした。Task鍵表と`prev_ordinal`は
  索引の構造全体の変更になり、IU-2の符号化・復元・構造検査を拡張する方が欠陥の混入が少ない。代償は、checkpointが
  全索引の書き直しのままであること、sidecarが壊れると全segmentを再構築すること。
- seg-legacy.tsi（版1）は移行時に最新化して以後触らない。`layout concat`で戻した後の旧版が、全再構築なしに出発点にできる。
- 記録の所有者を一つにした: segment 0のsizeの正本は`LAYOUT`（sidecarの表は照合のみ）。

## 残るリスク・未確認

- ordinal 1以降の閉じたsegmentのsize記録はsidecar（派生物）にしかない。sidecarが無い起動では、それらは検査できない
  （ログから再構築するだけ）。
- segment回転後・checkpoint前に落ちた場合、閉じたsegmentはsidecar上では「開いている」ままで、その末尾は再生で取り込まれる
  （その区間のfork検知は無い）。
- Windowsでの0444（読取専用属性）とrename置換、macOS既定のfile descriptor上限は`go vet`・cross buildまでで、実機未検証。
- 段階2のstoreを`rotation.enabled=false`で開くと、最新のsegmentに書き続けて大きくなり、commit時間が再び伸びる。
  起動時にWARNを出す（挙動は止めない）。
- 99を超える同日のsize弁切替は仕様の「fail closed」ではなく、WARNを出して現在のsegmentで書き続ける（可用性優先）。
- Phase B（詳細の窓・全IDの鍵mapのみの常駐）はIU-6。年間のメモリ増加と、sidecar全体の書き直しの増大はその段で扱う。
