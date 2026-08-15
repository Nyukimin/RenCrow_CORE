---
title: 調査 — ProfilePromotion再試行と記憶基盤修復
date: 2026-08-15 23:30
status: partial_with_blockers
skill: debug-investigate
symptom: ProfilePromotion terminal failed の滞留、Qdrant Recall vector path の停止、storage backup の失敗
frequency: ProfilePromotion backfill lifecycle、Recall request、storage-backup timer実行時
inputs: SQLite／CORE focused test、authenticated live API E2E、read-only audit／DB／Qdrant／systemd／mount evidence、既存調査報告
related: docs/04_アーキテクチャ概要.md, docs/05_設定リファレンス.md, docs/06_Public_API仕様.md, docs/調査/20260815_213804_ChatGPT_L3想起停止.md
---

## 概要

ProfilePromotion の全件診断、evidence-backed rowだけを再queueする明示retry、lease／attempt初期化、監査、冪等性を実装し、live APIで検証した。retry前は`completed=903`、`failed=1,222`、`pending=17,336`、`running=21`で、failedの内訳はretryable 1,191／orphan 31だった。authorized retry後、300秒設定下で14:41:10 UTCに約212秒の成功E2E（1 message／22 candidates）を確認し、GETは`completed=904`、`failed=31`、`pending=18,525`、`running=22`、`retry_wait=0`、retryable 0／missing 31となった。backlog全件完了ではない。

追加のread-only DB調査では、`pending=18,525`のsource内訳が`chatgpt_export=18,390`／`conversation=135`、created year内訳が2024年3,052／2025年9,976／2026年5,497、thread groupが1,997、24件batch換算のminimumが2,273 batches@24だった。従来のsession／thread選択が`created_at ASC`だけだったため、古いChatGPT importが通常会話より先にclaimされ、「新しい候補が遅い」直接原因になっていた。

Qdrantは再確認でもgreen、exact 55 points／全3584次元／Cosineで、`indexed_vectors_count=0`はindex threshold `10000KB`未満の状態であり、データ消失の証拠ではない。一方、COREの`embed_model`は空で正規embedding backendもないため、Recallのvector pathはblockedである。storage backupはinstalled runner／unitをcurrent `core.yaml`へ同期してfail-closed診断を実検証したが、dirty NTFSをforceせず、canonical LUKS mountはroot権限と実mount検証待ちである。

## 調査経緯

### 仮説1: terminal failed 1,222件は根拠eventの消失で全件retry不能である

- **根拠**: ProfilePromotionが最大試行回数後に`failed`へ遷移し、従来はfailed rowを正規APIから戻す経路がなかった。
- **検証結果**: 確認（evidence-backed rowだけを再queueでき、300秒設定下のProfileExtractor成功E2Eも確認）。backlog全件完了ではない。
- **証拠**:
  - live GET（retry前）は`completed=903`、`failed=1,222`、`pending=17,336`、`running=21`、`retry_wait=0`で、`retryable_failed=1,191`、`missing_evidence/orphan=31`だった。
  - 同じstartup後の累積DB pool snapshotは`max=1`、`pool_wait_count=67`、`pool_wait_duration_ms=180391`だった。これはhandle lifetime累積値で、retry単位の待ち時間ではない。
  - authorized first retryは`requeued=1,191`／`missing=31`、second retryは`requeued=0`／`missing=31`だった。read-only DB確認で`source=viewer`、payload `requeued=1,191`／`missing=31`のaudit eventが1件だけ存在した。
  - retry直後のlive GETは`completed=903`、`failed=31`、`pending=18,527`、`running=21`、`retry_wait=0`で、`retryable_failed=0`、orphan 31だった。
  - 最初の再投入batch 21件は2026-08-15 14:35:41 UTCに180秒の`context deadline exceeded`で`retry_wait`へ遷移した。IdleChat requestsは共通leaseにより`context canceled`となり、ProfilePromotionをpreemptしていない。
  - 次のrequestは2026-08-15 14:37:38 UTCに開始し、旧180秒を越えて2026-08-15 14:41:10 UTC（約212秒）に`Memory ProfilePromotion processed messages=1 candidates=22`を記録した。新parserでparse errorは発生しなかった。
  - 成功E2E後のlive GETは`completed=904`、`failed=31`、`pending=18,525`、`running=22`、`retry_wait=0`、`retryable_failed=0`、`missing_evidence/orphan=31`だった。全backlog完了ではなく、次batch 22件は処理中である。
  - `ProfilePromotionDiagnostics`は`GROUP BY state`で全rowを集計し、failed rowをL1 Raw eventへのLEFT JOINでretryable／missing evidenceへ分ける。Viewerのjob detailは`limit`付きの限定ページであり、全件数の代替ではない。
  - retry transactionは、対応eventが存在するfailedだけを`pending`へ戻し、`attempt_count=0`、lease token／期限、`next_attempt_at`を初期化する。`last_error`、`evidence_event_id`、evidence本文・metadata・stateは変更しない。
  - orphan failedは不変で、結果の`missing_evidence_count`にだけ現れる。1件以上再queueした場合は同じtransactionへ`memory.profile_promotion_retry_requested`を追記し、二回目は`requeued_count=0`で監査を増やさない。
- **チェックリスト結果**:
  - ☑ 確証バイアス: failed件数だけで消失と断定せず、evidence joinのretryable／orphan内訳、authorized first／second retry、audit、retry後のlive countsを確認した。
  - ☑ 頻度制約: 単発操作ではなく、ChatGPT由来backfillの継続lifecycleで蓄積したterminal stateとして扱った。
  - ☑ ライフサイクル: claim→running→retry_wait／failed→明示retry→pending、cancel時のdefer、lease解放、完了監査、再投入batchのdeadline→retry_wait→300秒設定下のsuccess→次batch runningまで確認した。
  - ☑ 反証: workerが`active`／`running`であることと新しいmemory候補のlatency改善を同一視しない。今回、通常会話が新規claimされず古いimportが先行した状態をsource別に確認し、claim順序修正後に通常会話が先にclaimされたcounterexampleを得た。
  - ☑ 既存知見: candidateを自動confirmedにせず、直接DB書き換えや人の承認待ちartifactを追加しない既存UserMemory契約と整合する。

### 仮説1a: 「新しい候補が遅い」はworker不足ではなくimport優先順である

- **根拠**: retry後のpending backlogが大きく、通常会話が処理されていないように見えた。workerの稼働だけでは、どのsourceがclaimされたかを説明できない。
- **検証結果**: 確認。live DBをread-onlyでsource／year／thread group別に集計し、旧claim順序のRedと修正後のGreenを確認した。
- **証拠**:
  - 確認時の`pending=18,525`は`chatgpt_export=18,390`、`conversation=135`。created yearは2024年3,052、2025年9,976、2026年5,497で、thread groupは1,997、24件batch換算のminimumは2,273 batches@24だった。
  - 修正前のsession／thread選択SQLは`created_at ASC`のみで、古い2024年ChatGPT importが通常会話を先にclaimし得た。これが「新しい候補が遅い」の直接原因であり、QdrantやRecallの消失とは別問題である。
  - TDD Redでは旧orderが古いChatGPT importを先にclaimする失敗を確認した。Greenでは選択SQLの第一orderを`CASE WHEN e.source = 'chatgpt_export' THEN 1 ELSE 0 END ASC`とし、それ以外のsourceを優先したうえで、既存の`created_at ASC`／`evidence_event_id ASC` tie-breakを維持した。
  - session／thread単位のbatch、lease、同一transactionのclaim／状態更新／atomicityは維持し、通常queueが空になればChatGPT importへ戻る。backfill rowの削除や別経路への移送は行わない。
  - root direct validationはl1sqlite test pass（16.360s）、l1 vet、`cmd/rencrow` build、`git diff --check`、`make install`、build／installed SHA-256 `c7458d...`一致、CORE stop/start後readyだった。
  - restart後のlive E2Eは2026-08-15 14:48:19 UTCに、`running`のsourceが`chatgpt_export=8`（再起動前lease残存、lease expiry待ち）／`conversation=1`となり、新binaryが通常会話を新規claimした。続くjournalは14:51:05に`processed messages=1 candidates=0`、14:51:11に`1/0`、14:51:48に`5/5`を記録した。source別stateは`conversation completed=107`→`114`、`pending=134`→`127`、`running=1`で、`chatgpt_export completed=837`／`pending=18,364`／`running=8`（再起動前lease残存）のままだった。通常会話7件が完了へ進んだため、claimだけでなくlatency改善のE2E成功を確認した。全backlog完了ではない。

### 仮説2: Qdrantのindex0／55 pointsがRecallデータ消失を示している

- **根拠**: collection `rencrow_memory_3584`の`indexed_vectors_count=0`が、検索不能またはvector消失に見える。
- **検証結果**: 棄却（データ消失の所見ではない）。Recall vector pathの利用不能は別途確認した。
- **証拠**:
  - Qdrant再確認はgreen、exact point countは55、全vectorは3584次元、collection設定は3584／Cosine、`indexed_vectors_count=0`だった。
  - Qdrantのindex thresholdは`10000KB`であり、index0はこのthreshold未満の少量collection状態で、point countまたはvector次元の欠落を示さない。index countだけで消失とは判定しない。
  - `/home/nyukimi/.rencrow/config/core.yaml`の`conversation.embed_model`は空で、旧Ollama endpointを使わない設定だった。COREはembedderを生成せず、Recallはvector searchをskipする。
  - RenCrow_LLMの現行aliasにはembedding alias／embedding targetがなく、Gatewayのread-only embedding probeもvectorを返さなかった。正規CORE→Gateway→Runtime embedding E2Eは未成立である。
- **チェックリスト結果**:
  - ☑ 確証バイアス: health、point count、vector dimension、index count、CORE embedder設定、LLM routeを分離して確認した。
  - ☑ 頻度制約: embedder未設定の全Recall requestで同じskip境界になるため、index0の単発現象とは区別した。
  - ☑ ライフサイクル: collection存在→embedding生成→Qdrant searchの各境界を追い、embedding生成前にCOREが停止することを確認した。
  - ☑ 既存知見: healthだけをRecall成功とせず、DB、record、Recall、ログまで検証する既存ルールと一致する。

### 仮説3: storage backupは設定だけを直せば実行可能になる

- **根拠**: installed serviceに`RENCROW_CONFIG`がなく、installed scriptがlegacy `config.yaml`をdefaultにしていた。
- **検証結果**: 部分確認（installed config projectionとfail-closed診断は確認、host storageの復旧は未実施）。
- **証拠**:
  - `make install-storage-backup`でCORE binary、backup runner、unitをinstallし、build／installed SHA-256が一致した。CORE stop/start後はreadyで、installed unitの`RENCROW_CONFIG=.../.rencrow/config/core.yaml` projectionを確認した。runtime config path自体は変更していない。
  - destination更新後のinstalled script/current `core.yaml`で`check`を実行すると、`[NG] required backing mount is inaccessible for /srv/rencrow/backup/snapshots/core`、rc=1となった。COREはactiveのままで、preflightはfail-closedだった。
  - `require_backing_mount`は最初の`timeout stat`失敗をpath付き診断へ変換し、CORE停止やRedis／Qdrant exportより前に終了する。root filesystem、unmounted path、fallback path、force mountは許可しない。
  - canonical `/srv/rencrow/backup`のLUKS serviceを非対話startすると`Interactive authentication required`となり、crypt／mountはinactiveだった。別途、installed systemd serviceの`run`は起動したが、snapshot pathのmount preflightで`[NG] required backing mount is inaccessible for /srv/rencrow/backup/snapshots/core`、rc=1で終了した。CORE stop、export、snapshot、tar、promoteの各stageは開始していない。dirty NTFSのforce、mount／unmountは行っていない。
- **チェックリスト結果**:
  - ☑ 確証バイアス: install後のunit projection、scriptの診断、CORE active維持、LUKS認証失敗、NTFS dirtyを分離して確認した。
  - ☑ 頻度制約: scheduled backup windowの実行時に再現するpreflight failureとして扱い、設定修正だけで成功と推測しない。
  - ☑ ライフサイクル: mount preflight→依存確認→CORE停止→export→archive→restore-checkの順を追い、最初のpreflight以降が未実行であることを確認した。
  - ☑ 既存知見: failed backup unitをaggregate healthへ埋めず、exit-code-1の個別問題として報告する既存ルールと一致する。

## 根本原因

- **ProfilePromotion**: failedは最大試行回数でterminal化する正規状態だが、evidence有無を見て再queueするPublic APIが不足していた。さらにpending 18,525件では、session／thread選択が`created_at ASC`だけだったため、古いChatGPT importが通常会話を先にclaimし、「新しい候補が遅い」直接原因になっていた。source優先orderを追加して通常会話を先にclaimする修正を入れ、live retryで1,191件をpendingへ戻した。最初の21件は180秒deadlineでretry_waitとなったが、runtime timeoutを180秒から300秒へ変更して再起動し、約212秒で1 message／22 candidatesの成功E2Eを確認した。backlog全件完了ではない。
- **Qdrant Recall**: Qdrant collectionの保持状態は正常だが、COREの`embed_model`空と正規embedding backend不在により、embedding生成前のvector Recall境界がblockedになっている。Qdrantに別dimensionを混在させる経路やfallbackはない。
- **storage backup**: current canonical destinationの`stat`は、mount inactive／path inaccessibleのためsnapshot path preflightで失敗し、installed systemd `run`はCOREをactiveのままrc=1でfail-closed終了した。これはLUKS unlock失敗とは別の実行境界である。LUKS serviceの非対話startは別途`Interactive authentication required`となり、crypt／mountとroot-owned directory provisioningがblockedである。runはpreflightまでで、CORE stop、export、snapshot、tar、promoteは開始していない。initial dirty NTFSをforceせず、backup成功は未達である。
- **SQLite観測**: 今回の確認範囲では最近の`SQLITE_BUSY`／`locked`エラーは観測されなかった。ただし無発生の証明ではない。追加されたpool statsは`database/sql.DB.Stats()`のhandle生存期間累積値であり、job／request単位、原因別、直近window別のwait観測ではない。

## 修正内容

1. `GET /viewer/memory/profile-promotions`へ、全件exactな`state_counts`、failed／retryable／missing-evidence、限定job details、累積DB pool statsを追加した。`limit`は50既定／200上限で、`job_count`と全件集計を分離した。
2. `POST /viewer/memory/profile-promotions/retry`へ`RenCrow_CMD`と`cmd-control`の2 headerを必須化し、403／405／503／200（storage errorは500）を明示した。evidence-backed failedだけを同一transactionでpendingへ戻し、attempt／leaseを初期化し、error／evidenceを保持し、orphanを不変にした。成功時監査と二回目冪等性を固定した。
3. backup source unitへcurrent CORE configを明示し、scriptのmount preflightをfail-closedかつpath付き診断にした。`make install-storage-backup`でbuild／installed SHA-256一致を確認し、runtime `core.yaml`のdestinationをcanonical LUKS配下へ更新した。installed `run`はpreflight失敗で終了し、CORE停止以降のstageへ進まないことを確認した。
4. runtime backup destinationを`core_snapshot_root=/srv/rencrow/backup/snapshots/core`、`knowledge_mirror=/srv/rencrow/backup/knowledge/current`、`knowledge_versions=/srv/rencrow/backup/knowledge/versions`へ更新した。`knowledge_source`は既存値を保持した。旧NTFS／CIFS destinationからcanonical LUKS destinationへのconfig driftは解消したが、unlock／mount／root-owned directory provisioningは`Interactive authentication required`でblockedである。
5. ProfilePromotionのsession／thread選択SQLで`CASE WHEN e.source = 'chatgpt_export' THEN 1 ELSE 0 END ASC`を第一orderにし、通常conversationなどimport以外を優先した。既存の`created_at`／`evidence_event_id` tie-break、24件batch、lease、claim／状態更新のatomicityは維持し、通常queueが空けばimportへ戻るためbackfillは削除していない。

## 検証結果

- focused `go test` 6 packages（`./internal/infrastructure/persistence/conversation/l1sqlite`、`./internal/infrastructure/persistence/conversation`、`./internal/adapter/viewer`、`./internal/features/memory`、`./internal/application/memorypromotion`、`./cmd/rencrow`） — pass。`memorypromotion` direct rerunは0.194s。
- Git管理Go directoryを`go list -e`で列挙した全buildable packageに対する`go vet` 243件 — pass
- 同じ列挙に対する`go build` 237件 — pass
- `make install` buildおよび`make install-storage-backup` — pass。CORE binaryとbackup runner／unitのbuild／installed SHA-256一致、CORE stop/start後ready、unitの`RENCROW_CONFIG=.../.rencrow/config/core.yaml` projectionを確認した。
- `bash scripts/tests/storage_backup_contract_test.sh` — pass
- `bash -n scripts/rencrow-storage-backup` — pass
- `git diff --check` — pass
- retry testで、evidence-backed rowのpending／attempt 0／last_error保持、orphan不変、raw evidence不変、監査1件、二回目`requeued=0`を確認した。
- diagnostics testで、5 stateの全件集計、failed 2／retryable 1／missing evidence 1、limited details、pool statsを確認した。
- source-priority TDDで、Redは旧`created_at ASC`が古いChatGPT importを先にclaimする失敗、Greenは通常conversationを先にclaimしてcomplete後にChatGPT backfillをclaimする挙動を確認した。root direct validationはl1sqlite test pass（16.360s）、l1 vet、`cmd/rencrow` build、`git diff --check`、`make install`、build／installed SHA-256 `c7458d...`一致、CORE stop/start後readyだった。
- source-priority修正後のlive E2E（2026-08-15 14:48:19 UTC）は`running`が`chatgpt_export=8`（再起動前lease残存、expiry待ち）／`conversation=1`で、新binaryが通常conversationを新規claimした。journalは14:51:05 `processed messages=1 candidates=0`、14:51:11 `1/0`、14:51:48 `5/5`。source別stateは`conversation completed=107`→`114`、`pending=134`→`127`、`running=1`、`chatgpt_export completed=837`／`pending=18,364`／`running=8`だった。通常会話7件が完了へ進み、latency改善E2E成功を確認したが、全backlog完了ではない。
- live GET／POST retry E2E — pass。POSTのmissing／spoofed／other profileは全て403、authorized firstは`requeued=1,191`／`missing=31`、secondは`requeued=0`／`missing=31`、retry直後は`completed=903`／`failed=31`／`pending=18,527`／`running=21`／`retry_wait=0`、retryable 0だった。read-only audit確認はsource viewer、payload 1件だった。
- runtime `core.yaml`のProfilePromotion timeoutを180秒から300秒へ変更し、CORE stop/start後readyを確認した。14:37:38 UTC開始のrequestが14:41:10 UTC（約212秒）に`processed messages=1 candidates=22`で成功し、新parserのparse errorもなかった。この変更はrepository差分ではなくruntime config変更である。
- 成功E2E後のGETは`completed=904`／`failed=31`／`pending=18,525`／`running=22`／`retry_wait=0`、retryable 0／missing 31だった。次batch 22件は処理中で、全backlog完了ではない。
- installed backup serviceの`run` — fail-closed pass。current `core.yaml`とcanonical destinationを使用し、`[NG] required backing mount is inaccessible for /srv/rencrow/backup/snapshots/core`、rc=1、CORE active維持を確認した。runはmount preflightで終了し、CORE stop／export／snapshot／tar／promoteは開始していない。LUKS非対話startは別途`Interactive authentication required`でcrypt／mount inactive、root-owned directory provisioningもblocked、backup成功は未達である。
- 標準`go vet ./...`は既存`Tmp/test-runtime/gopath`を誤走査したため採用せず、cacheを削除せずGit管理Go directoryを`go list -e`で列挙して検証した。

## 残課題

- retry requestの認可、evidence-backed 1,191件の再queue、二回目冪等性、retry後のlive counts、timeout 300秒設定下の約212秒成功E2Eは確認済みである。全backlogは未完了で、次batch 22件が処理中である。
- source-priority修正後も全backlogは未完了である。14:48:19 UTCのsource別snapshotでは通常conversation 1件がrunning、ChatGPT import 8件は再起動前lease残存のexpiry待ちだったが、その後通常会話7件が完了した。worker activeだけを新しいmemory latency改善の証拠とは扱わず、完了journalとstate遷移を根拠にする。通常queueが空になった後はbackfillを継続する。
- 31件のorphanはraw evidenceがないためretry対象外であり、evidenceを復元できる正本手順なしにDBを直接書き換えない。
- Qdrant Recallは、RenCrow_LLM Runtimeを経由する専用embedding alias／backend、3584次元一致、CORE側dimension guardを用意してからlive E2Eを行う。別経路やfallbackは追加しない。
- backup source／runner／unitはinstall済みで、旧NTFS／CIFS destinationからcanonical LUKS destinationへのconfig driftも解消した。runはmount preflightでfail-closed終了し、CORE停止以降のstageは未開始である。残るblockerはLUKS unlock／mountとroot-owned directory provisioningの`Interactive authentication required`であり、dirty NTFSのforceなし、canonical destinationでのbackup成功は未達である。
- pool waitは累積DB handle値のため、ProfilePromotion request／batch単位のlatency、busy原因、window差分は観測できない。必要なら別の明示的observability契約が必要だが、今回のscopeでは追加しない。
- SQLiteに最近のBUSY／lockedがないことは、将来のlock競合や履歴の不存在を保証しない。`/health`やfocused testだけでmemory／Recall／backupのlive完了とは判定しない。

## 関連ソースファイル

- `internal/adapter/viewer/memory_profile_promotion_handler.go:21-94` - GET diagnosticsとPOST retryのHTTP contract、header、status
- `internal/domain/memory/profile_promotion.go:13-54` - job、diagnostics、pool stats、retry resultのJSON contract
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_profile_promotion.go:323-438` - evidence付き同一transaction retry、全件集計、累積`sql.DB.Stats()`
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_profile_promotion.go:78-85` - import以外を優先するsession／thread claim orderと既存tie-break
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_profile_promotion_test.go:129-270` - orphan不変、raw保全、audit、idempotency、diagnostics test
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_profile_promotion_test.go:274-323` - 通常conversation先行claim、complete後のChatGPT backfill claim
- `cmd/rencrow/interaction_profile_guard.go:128-165` - `RenCrow_CMD`／`cmd-control`のallowlist
- `scripts/rencrow-storage-backup:11,110-132` - current config default、fail-closed mount preflight
- `systemd/user/rencrow-storage-backup.service:6-11` - `RENCROW_CONFIG`の明示的service environment
- `/home/nyukimi/.rencrow/config/core.yaml` - installed CORE conversation／Qdrant／backup設定（read-only確認）

## 教訓（将来の調査への知見）

- 全件集計と限定detailを同じAPIで返す場合、`job_count`を全件数と誤読させず、exact state countersを別fieldで固定する。
- retryはterminal stateを一括解除せず、根拠の存在を同一transactionで検証し、orphan不変・error保持・監査・二回目冪等性を受入条件にする。
- Qdrantのhealth、point count、index count、embedding availability、Recall実行結果は別の証拠であり、どれか一つを他の成功へ昇格しない。
- backupのsource、installed unit／script、mount state、実行境界を分けて記録し、source修正をinstall済み・backup成功・host修復済みと混同しない。
- SQLite pool statsは便利な累積指標だが、直近エラー、job因果、request latencyの証明ではない。`SQLITE_BUSY`／`locked`不在も限定された観測結果として報告する。
