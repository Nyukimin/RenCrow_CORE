# Storage backup lifecycle競合の調査・修復

## 1. 調査概要

- 調査日時: 2026-08-23 13:25 JST
- 対象: Ubuntu productionの`rencrow-storage-backup.service`、CORE、Knowledge配置
- 症状: 2026-08-23 00:33 UTCのbackup中にCOREが再起動し、snapshot対象が変更されたためbackupが失敗した。
- 結論: backup runnerはCOREをstopするだけで、backup window中の別経路からのstartを拒否していなかった。またbackup媒体のmount windowがrunner契約に含まれず、Knowledge sourceも2 TB live-data HDDのowner root外を参照していた。

## 2. 仮説と検証

### 仮説1: resilienceがbackup停止直後にCOREを直接再起動した

- 判定: 反証
- 証拠: COREのstartは04:34:00 UTC、resilience処理はstart後の04:34:08 UTCであり、時系列が逆だった。
- 含意: resilience単独を止めてもdeploy、手動restart等の別start経路を防げず、原因境界を解消しない。

### 仮説2: stopだけでは外部startとsnapshot windowを相互排他にできない

- 判定: 確認
- 証拠: 失敗runではCOREがsnapshot中に二度startし、04:44:27 UTCに`hobby_graph`が変更された。runnerにはbackup lockはあったが、そのlockをCORE start経路は参照しなかった。
- 修正: snapshot開始前に`systemctl --user mask --runtime --now rencrow.service`を実行し、成功・失敗・signalの全cleanupでunmaskする。開始前にactiveだった場合だけCOREを再開する。

### 仮説3: Knowledgeが別媒体にあり、2 TB HDD 2基の正本構成と不整合である

- 判定: 確認
- 証拠: live configは`/srv/rencrow/knowledge/RenCrowKnowledge`を参照していた。sourceはdirectoryのみでfileが0件だったため、移送対象dataはなかった。
- 修正: `/srv/rencrow/db/core/knowledge`を作成し、live configの`knowledge_source`を同pathへ切り替えた。backup destinationは`/srv/rencrow/backup`配下だけを許可する。

## 3. 根本原因

backup処理とCORE lifecycleの所有境界が仕様・実装とも不足していた。runnerのlockはbackup同士だけを排他し、snapshot中のCORE startを拒否する機械的gateではなかった。加えて、専用backup HDDのmount lifecycleとKnowledge owner rootがrunnerのpreflightに明示されていなかった。

## 4. 修正内容

- `scripts/rencrow-storage-backup`
  - backup mediumを必要時だけmountし、runnerがmountした場合はcleanupでunmountする。
  - backup destinationが`/srv/rencrow/backup`配下であることをfail closedで検証する。
  - COREをruntime maskしてsnapshot中の外部startを拒否する。
  - cleanupでmask解除、開始前にactiveだったCOREの復帰、staging削除、mount復元を行う。
- `scripts/tests/storage_backup_contract_test.sh`
  - mount/unmountおよびruntime mask/unmask契約のRED/Green回帰を追加した。
- `docs/05_設定リファレンス.md`、`docs/09_運用ログ・panic保存仕様.md`
  - backup windowの所有、CORE lifecycle gate、終了時復元を正本化した。
- production config
  - Knowledge sourceを`/srv/rencrow/db/core/knowledge`へ移した。旧sourceにfileはなかったため、内容の創作・merge・削除はしていない。

## 5. 検証結果

- TDD Red: 旧runnerにmount、unmount、runtime mask、unmaskの4契約がなく、contract testが4件を列挙して失敗した。
- TDD Green: `make test-storage-backup`成功。Common Rawのnegative fixtureが期待どおり拒否され、storage backup/configure contractが成功した。
- production backup: 2026-08-23 04:07:08 UTC開始。04:07:09にCOREをruntime maskして停止し、04:16:14にunmaskして再開した。
- restore verification: 04:21:44にchecksum、gzip展開、restore検査成功。
- promotion/mirror: 04:23:29に`/srv/rencrow/backup/snapshots/core/recent/20260823-130709`を確定し、Knowledge mirror成功。
- service result: exit status 0。終了時にbackup mediumをunmountした。

## 6. バイアスチェック

- 確証バイアス: resilience原因説を採用せず、journalの時系列で反証した。
- アンカリング: 以前のmount失敗へ固定せず、今回の失敗時刻、process、変更fileを再確認した。
- 単一原因化: lifecycle排他、mount ownership、Knowledge配置を別契約として検証した。
- 正常性バイアス: service aliveだけで成功とせず、restore check、promotion、mirror、exit statusを終端証拠にした。

## 7. 残る境界

- source commitとEcoSystem pinは、この調査記録を含むCORE revision確定後に更新する。
- backup HDDは平常時unmountを維持する。旧1 TB/3 TB媒体は製品構成へ戻さない。
