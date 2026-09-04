# ThreadID recovery backupのstaging owner競合

## Failure

format-5 recovery package作成時、CORE全体tarが`core_source/staging/movie_catalog`を走査し、
CORE writer停止後も高いI/O pressureとD-stateを発生させたためbackupを中断した。
未検証candidateはcleanupされ、cutoverは開始していない。

## Problem

CORE serviceだけをruntime maskしても、別serviceが所有する再取得可能artifact stagingのwriterは停止しない。
そのdirectoryをCOREのwriter-stopped recovery cohortへ含めると、snapshotの不変性とowner境界を証明できない。

## Cause

backup tarは`.env`、鍵、binary、log、lock、`tmp`を除外していたが、
正本が除外対象とする外部Toolの再取得可能cache／staging途中fileを物理pathへ投影していなかった。

## Lesson

`core_source`配下であることはCORE正本またはbackup対象であることを意味しない。
configured durable pathと、別ownerが書く再取得可能stagingをwriter identityとlifecycleで分離する。

## Invariant

CORE recovery snapshotはCORE停止窓で不変性を証明できるconfigured durable stateだけを含む。
`core_source`直下の`staging`は外部取得serviceの未import artifact領域として除外し、
import後の正本DB、Common Raw ledger／object／quarantineは設定済みpathから引き続き含める。

## Enforcement

`rencrow-storage-backup`の固定tar contractで`${core_name}/staging`を除外する。
任意globやruntime推測ではなく、正本化したtop-level boundaryだけを適用する。

## Tests

`scripts/tests/storage_backup_contract_test.sh`が固定exclusionの存在を検査する。
format-5のquiesce receipt、L1／Archive hash、外部論理snapshot、scratch restore検査は従来どおり必須とする。
