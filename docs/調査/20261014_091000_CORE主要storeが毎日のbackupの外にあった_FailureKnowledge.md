# CORE主要storeが毎日のbackupの外にあった Failure Knowledge

記録日: 2026-10-14 (JST)。対象: `RenCrow_CORE` の毎日のbackup（`scripts/rencrow-storage-backup`、`scripts/rencrow-storage-restore-check`、
`rencrow-storage-backup.service`、毎日03:00 JST）と、`workspace_dir`配下のTask store（`<workspace_dir>/tasks/`）・Action store
（`<workspace_dir>/state/actions/`）。仕様は `docs/05_設定リファレンス.md`「Task storeとAction storeのbackup」。
storeの構造は `20261012_192124_TaskStore日次segment回転_FailureKnowledge.md`、`20261013_230400_ActionStore日次segment回転_FailureKnowledge.md`。
本書は本番へ配備する前の記録で、配備後の確認は「残るリスク・未確認」に分けて書く。

## Failure

CORE（会話、Task、Run、Action）の実行履歴の正本であるTask storeとAction storeが、毎日のbackupの外にあった。2つで約270MB（Task store
約190MB、Action store約78MB）の履歴で、障害時に失えば復元できない。backupは毎日成功し、`restore-check`も通っていたため、欠けていることは
どの検査にも現れなかった。

## Problem

backupの範囲が「`backup.core_source`（`/srv/rencrow/db`側）配下」という設定上の境界で決まり、「COREが失ってはならない永続storeの全部」では
なかった。`workspace_dir`配下のstoreは別の媒体（HOME側）にあり、durable store Manifestにも、restore checkの検査対象にも無かった。
欠けていることは各設計メモとRunbookに「対象外」と書かれていた（読めば分かるが、検査はそれを強制しない）。

## Cause

1. storeを足した側（Task store、Action store）が、自分のbackup範囲を決める所有者を持たなかった。backup範囲は設定のpathで決まるので、
   設定の外に置かれたstoreは、足しても範囲に入らない。
2. 「backupが成功した」と「必要なものが入っている」を区別する検査が無かった。restore checkは入っているものの健全性しか見ない。
3. storeの場所を知る関数（`defaultTaskStorePath`、`defaultActionStorePath`）がCLIとruntimeにだけあり、backupはそれを使えなかった。

## Lesson

backupの範囲は、storeの所有者が場所を決める関数から導き、その場所がcohortに入っていること、入っているものを所有者のCLIが隔離したコピーで
検証したことを、publishの条件にする。新しいstoreを足したときに、範囲から漏れたことが成功として通る道を残さない。

## Invariant / Enforcement / Tests

| ID | Invariant | Enforcement | Tests |
| --- | --- | --- | --- |
| BKP-1 | `workspace_dir`配下のTask storeとAction storeは、毎日のcohortの一部として保存される | runnerは`backup-values`が24値でなければCOREを止める前に`[NG]`で終わる。storeが無い・`workspace_dir`の外・symlinkのときも停止前に終わる | `TestBackupConfigValues*`、`storage_backup_stores_contract_test.sh`（静的契約、`archive_workspace_stores`の実行） |
| BKP-2 | storeの場所はconfigから、runtimeとCLIが使う関数で得る。scriptに場所の文字列を持たない | `backupConfigValues`が`defaultTaskStorePath`・`defaultActionStorePath`を使う。scriptはpathの文字列を持たず、相対位置は`workspace_dir`との差から導く | `TestBackupConfigValuesPlaceBothStoresUnderWorkspaceDir`（storeが`workspace_dir`の配下で互いに別）、`TestBackupConfigValuesLeaveStoresEmptyWithoutWorkspaceDir` |
| BKP-3 | cohortに入れたstoreは、隔離したコピーで所有者のCLIが検証した場合だけpublishされる | restore checkは展開先を`workspace_dir`にしたscratch configとscratchのHOMEで`rencrow tasks verify`・`actions verify`を実行する。runnerは`[OK] workspace stores verified`の行が無ければpublishしない | 破損したTask store・Action storeのcohort、archiveがSHA256SUMSに無いcohort、binaryが無い・失敗する・応答しないcohortの拒否 |
| BKP-4 | 停止窓の中のどんな失敗でも、COREの再開とreadiness確認は必ず行われる | store archiveは`trap cleanup EXIT INT TERM`の設置後、CORE再開の前に置く。失敗はnon-zeroで終わり、既存のcleanupが`core_was_active`を見て再開する | 静的な順序検査（trap < store archive < 再開、停止前の検査 < mask） |
| BKP-5 | 書き手の排除はCOREの停止だけに頼らない | Task storeは`.writer.lock`のOS lockをarchive中ずっと保持する（書込みCLI`tasks create`、`reap-queued --apply`、`rebuild-index`、`layout concat --apply`はCORE稼働中だけ拒否されるため、停止窓の中で開ける）。**Action storeはwriter lockが無く未強制**（COREの停止だけ） | 別processがlockを保持している間はarchiveを拒否し、手放した後は成功する試験（lockの真のOS実装がある環境） |
| BKP-6 | 派生物の索引は含めず、writerの世代counterを持つ`.writer.lock`は含める | archiveは`tasks/index`だけを除外し、`.writer.lock`は除外しない。静的契約が`.writer.lock`の除外を拒否する | archiveの内容検査（`.writer.lock`・WAL・`LAYOUT`が有り、`tasks/index`が無い。読取専用modeが保たれる） |
| BKP-7 | 古いcheckerと新しいrunnerの組合せで、storeの検査が黙って抜けない | runnerはcheckerの出力に`[OK] workspace stores verified`が無ければ`[NG]`にする | 静的契約（runnerが出力を検査する）、旧cohort（キー無し）は`[INFO] no workspace stores`で検査を飛ばし、成功を主張しない試験 |

## 設計上の判断

- **別archive**: `rencrow-storage-restore-check`以外に`rencrow-core-migration-package`が`rencrow-state.tar.gz`の1 archive cohortを前提にするため、
  `core-export`は変えず、stores archiveは通常のbackupのcohortにだけ足す。`SHA256SUMS`には両方を載せ、manifestの`workspace_stores_archive`が
  指すarchiveが`SHA256SUMS`に載っていることをrestore checkが確かめる（`sha256sum -c`は載っているものしか見ないため）。
- **`format_version=4`のまま**: format 5は過去に退役し、契約試験が拒否を固定している。`rencrow-core-migration-package`と`rencrow-core-verify`も
  format 4を前提にする。キーを足すだけで、キーの無い旧cohortはlegacyとして通す。
- **`index/`は含めない**: 派生物であり、含めるとcheckerが派生物の不一致（`differs_from_log`）でbackup全体を落とし得る。含めない場合、
  `rencrow tasks verify`は`sidecar=missing`で通る（ログだけで索引を構築して独立畳み込みと照合する）。Runbookの「`index/`は無くても起動時に作り直す」と整合する。
- **strict（部分publishしない）**: cohortは同一停止窓の整合点を表す。storeだけ欠けたcohortをpublishすると、manifestの無いlegacyと区別できず、
  daily・weekly・monthlyへ`cp -a`で増殖する。失敗は`rencrow-storage-backup.service`のfailedとして見え、前の世代は消えない（pruneはpublishの後）。
- **`--check-runs`を付けない**: Task storeの保持期限が実装されてTaskがcoldへ退避されると、Actionが指すRunが正当にTask storeから無くなり得る。
  誤検知でbackupが毎日失敗し続けるより、store単体の整合（`verify`）に限る。
- **事前検査は停止前**: 24値、storeの存在、`workspace_dir`の配下、symlinkでないこと。失敗はCOREを止める前に終える。
- **tarはportableなoptionだけ**: archive処理は関数`archive_workspace_stores`に分け、契約試験が関数本体を抜き出してmacOS・Linux・Git Bashで実際に実行する
  （既存のtarの`--acls --xattrs`は、storeには意味が無く、Git Bashのtarで使えないので使わない）。

## 残るリスク・未確認

- **本番配備は未実施**。配備には新しい`rencrow`（`backup-values`が24値）が要る。古いbinaryでは、COREを止める前に`[NG]`で終わる（backupは失敗するが、COREは動き続ける）。
  配備後の最初の成功backupで、cohortに`workspace-stores.tar.gz`が有り、`SHA256SUMS`・manifestがそれを指し、journalに`[OK] workspace stores verified`が出たことを確かめる。
- **Action storeの排他は未強制**: 書込みCLI（`actions layout concat --apply`）が停止窓の中で開けても、runnerは検出できない。writer lockを持たないため、最新segmentの
  `.jsonlbatch.lock`を取るにはLAYOUTの解釈が要る。発生は、停止窓の中に利用者が書込みCLIを実行する場合に限られる。
- **`resilience`の自己修復**（`buildInstallAndRestart`）が停止窓に当たると、`~/.local/bin/rencrow`の入れ替えは通り、再起動だけがmaskで失敗する。低頻度なので記録だけ。
- **stagingの容量検査は無い**: cohortは`~/.cache`のstagingに作られ、restore checkが同じ場所に展開する。今回の追加は小さい（archive約26MB、展開約270MB）が、
  既存の不足検査の欠如は残る。
- **verifyの所要時間は本番で未測定**: `tasks verify`は全segmentの再畳み込み、`actions verify`は全segmentの全件読みを行う。本番のコピーでの実測は配備前の手順に含める。
- **Windowsでのstoreの動作は未確認**: Go側のstoreの`go test`はlinux・darwinだけ（Canonical Test Plan）。契約試験はWindowsでも走るが、Windows固有のlock・読取専用属性の挙動の保証は主張しない。
- 範囲外: `~/.rencrow/config`、credentials、`workspace/logs`などworkspace_dir配下の他のdurable state、Manifest登録、`core-export`、remote profile（storage host）のstore。

## アドバイザ相談（Fable、2026-10-14）

設計を確定する前に、読み取り専用のアドバイザへ設計案と7つの問いを渡した。助言は承認ではなく、採否は起案者が決めた。

| 問い | 助言の要旨 | 採否 |
| --- | --- | --- |
| 停止窓の前提 | timerはstoreを開かない。ただしCOREが止まっている間だけ通る書込みCLIがあり、停止窓はそこに当たる。Task storeの`.writer.lock`を`flock -n`で保持してarchiveする | 採用（BKP-5）。Action storeは未強制として記録した |
| strictかisolateか | strictでよい。cohortの意味は整合点で、半分のpublishはlegacyと区別できなくなる | 採用 |
| 別archive | 賛成。ただし`sync`の対象に新archiveを加えること。checkerの出力検査はrunner側 | 採用（publish時の`sync`をcohortの全fileに） |
| `--check-runs` | 外す判断に賛成。verifyは期限で包む | 採用 |
| 新binary要求 | 恒久的な別binaryは、storeの場所の知識が2つのbuildに分かれるので避ける。通常の配備を正にし、dropinは配備日の1回限り。`rencrow-core-verify`がrestore checkを環境変数なしで呼ぶので、既定binaryも新しい必要がある | 採用（配備手順に反映）。ただし古い`rencrow`でも`tasks verify`・`actions verify`自体は動くので、`backup-values`だけ新しい橋渡しの手順も併記する |
| 見落とし | symlink、容量、停止時間（gzipが窓内）、HOMEとcwd | symlinkの拒否とcwd・HOMEの隔離は採用。容量検査は既存に無いので記録だけ。gzipは本番の実データで約4秒と測り、窓内で受容 |
