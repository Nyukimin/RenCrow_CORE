# ID統一 Step 14 Evidence orphan強制と `go test ./...` の構造化失敗

- 記録時刻: 2026-09-17 23:30 JST（14:30 UTC）
- 対象: RenCrow_CORE branch `identity/03-dci`、HEAD `40393d1`、Step 14 EvidenceID/MemoryID
- 引き継ぎ元: `docs/調査/20260916_151504_ID統一Step10-20_作業引き継ぎ.md`、`docs/調査/20260907_ID統一Step14_EvidenceMemory_check_plan.json`
- 契約正本: `docs/architecture/identity/IDENTITY_CANONICAL.md#step-14-evidenceidとmemoryid`
- 検出／根拠 path: `internal/infrastructure/persistence/complexity/sqlite_store.go`、`internal/infrastructure/persistence/complexity/sqlite_store_test.go`、`cmd/rencrow/resilience_commands.go:561`、`/srv/rencrow/db/core/databases/ops/complexity_hotspot.db`

## 結論

Step 14 の完了条件①EvidenceID 文字列派生 0、②Evidence orphan 0、③Memory promotion の Task/Run/Event 紐付けはいずれも充足。ただし②は当初**機械強制が成立していなかった**：共通 `save()` が FK の index 列を書かず、live に空欄 8 行が残っていた。意味的な orphan は 0 だったが、`CountComplexityIdentityOrphans` を owner API として追加し、legacy の空 FK を InvalidIndex として検出する test で強制した。live の 8 行は writer-stop 後に backfill して解消。

並行して、この repository では `go test ./...` が**構造的に FAIL**（EXIT=1、239 packages は ok）することを確認した。`Tmp/test-runtime/gomodcache` が `.gitignore` 済み・untracked のため `go list` 解決段階で外れる。resilience の `buildInstallAndRestart()` は incident 時に同じ repo dir で `go test ./...` を使うため、現状の auto-repair は必ず failed する。今回 scope 外とし、報告のみ。

## 実測

| 項目 | 実測値 |
| --- | --- |
| 未コミット差分 | `sqlite_store.go` と `sqlite_store_test.go` の 2 ファイルのみ、`git diff \| sha256sum` = `d7638caf78e31e4324029843a07ac659470b684f162ab4253cfbbb2d7fd5e2cb` |
| 実バグ | live `complexity_hotspot.db` の index 列 8 行が空：`complexity_hotspot.scan_id` 3、`complexity_hotspot_evidence.hotspot_id` 3、`complexity_report_artifact.scan_id` 2。payload（正本）と親行は全て実在＝意味的 orphan は 0 |
| backfill | `rencrow-resilience.timer` → `rencrow.service` 停止（:18790 非 listen 確認）後に `UPDATE <t> SET <fk>=json_extract(payload,'$.<fk>') WHERE COALESCE(<fk},'')=''` で 3+3+2=8 行更新、`pragma integrity_check` ok |
| pre/post sha256 | pre `decebfc7b07d407fb3f335d4c0b1f32a94495fae438bc1ad785ee296b17ef8a3` → post `1bf784a4746f67b9542e65a935523620302cb4f4a695dabe1653310e477fd7e7`（`…/step14-evidence-memory-20260907T035720Z/orphan-backfill-20260917T1350Z/`） |
| orphan 集計 | backfill 直後および CORE 再起動後の両方で `TOTAL_ORPHANS=0`（scan_events 2 / hotspots 4 / evidence 4 / artifacts 3） |
| 配備 binary（初回） | `go build -o /tmp/rencrow-s14 ./cmd/rencrow` → `*.rencrow-new` 経由 rename で `~/.local/bin/rencrow`。旧 `24a41febd173f830…` → 新 `2eb77d49447220949dcb91110a2529a5fc2e90102fc1f730d9d67ce4f574127b`。再起動後も変化なし |
| 再配備（HEAD commit 後） | Step 14 差分を `082ad99` へ commit 後に再ビルドし、同じ手順で再配備。`go version -m` の `vcs.revision=082ad99…`、sha256 `5d010023d107eb0a3b846f52a7ea5f22d2db980391cf235101a70f1388deb584`。`/tmp` で 2 回ビルドして sha256 一致（決定的）。ready 復帰は約 140s、再起動後 `TOTAL_ORPHANS=0` を維持 |
| 正規 runtime route | `POST /viewer/complexity-hotspots/scan`（`scan_id=step14_orphan_route_20260917`）→ HTTP 201。新 evidence `evd_01a0afad-08d8-7996-954a-9bf0e6b9b7ae`（typed `evd_`）、hotspot `…_nested_loop_1`、artifact `art_complexity_step14_orphan_route_20260917`。index 列が payload と一致することを rows 単位で実測 |
| 再起動維持 | `systemctl --user restart rencrow.service` → ready（MainPID 1887514、起動 14:06:07 UTC）→ `GET /viewer/complexity-hotspots` 200（scans 2 / hotspots 4 / evidence 4 / reports 3、step14 route の scan 残存）→ orphan 0 維持 |
| pre-deploy 検査 | gofmt clean、`go vet ./internal/infrastructure/persistence/complexity/` clean、`go test -count=1` で complexity 0.310s / domain 0.009s / application 0.009s / modules/core 11.866s が全て ok（arch test 2 本 pass） |
| `go test ./...` | EXIT=1。`pattern ./...: directory Tmp/test-runtime/gomodcache/github.com/dgryski/go-rendezvous@v0.0.0-20200823014737-9f7001d12a5f outside main module or its selected dependencies`。ok は 239 packages。`git ls-files Tmp` = 0、`.gitignore:75 /Tmp/` |
| resilience 設定 | `rencrow-resilience.timer` `OnUnitActiveSec=2min`、`AUTO_REPAIR=true`、`REPAIR_ROUTE=CODE2`。過去 2 時間の repair 試行 0 件（journal に Starting/Finished のみ、inotify `No space left on device` は散発だが無害） |
| Check Plan（登録前） | `config/checks/core.json`（正本）と `~/.local/share/rencrow/checks/*.json`（配備）の双方で complexity を参照する check 0 件 |

### Check Plan 登録後の実測（2026-09-17 17:05〜17:17 UTC）

| 項目 | 実測値 |
| --- | --- |
| manifest 登録 | `config/checks/core.json` を 11 件 → **12 件**へ更新。`check_id=core_complexity_identity_orphan`、`guarantee_id=complexity_identity_orphan_zero`、`phase=diagnostic`、`cost=low`、`safety_gate=false`、`coverage=["durability"]`、`consumer=Step14 identity completion decision`、`failure_action=blocked`、`executor={kind:owner_cli, command_id:core-complexity-identity-orphan, acquisition:{mode:owner_self_collect, verification_safe:false, inputs:[complexity_hotspot_db / external_prerequisite / owner_external_artifact]}}`。正本 sha256 `bb97d7ec1ce6de8f6f6e62b6414853ee115dfdc27d68c76c5a11d432d34f80ed` |
| 実行側 | 新 file `cmd/rencrow-core-verify/complexity_orphan_checks.go`（`runComplexityIdentityOrphan`）＋ `verifier.go` へ `--complexity-db`（alias `--complexity-database`）と allowlist/verifier 配線を追加。store 側は `complexity.OpenSQLiteStoreReadOnly()` を新設し、Lstat で symlink・非 regular を reject → `mode=ro&_time_format=sqlite&_pragma=busy_timeout=5000` → `PRAGMA query_only` が 1 であることを確認 → Ping。`NewSQLiteStore` は `MkdirAll` と migrate を走らせるので使わない |
| owner verifier live receipt | `go build -o /tmp/s14b_verify ./cmd/rencrow-core-verify` → `run --manifest config/checks/core.json --check-id core_complexity_identity_orphan --complexity-db /srv/rencrow/db/core/databases/ops/complexity_hotspot.db` → **`status=passed`、EXIT=0**、evidence `identity_orphans:0`（scan_events 2 / hotspots 4 / evidence 4 / artifacts 3、missing・invalid index 全項目 0）、`read_only_observation:true`、`live_database_write_performed:false`、`db_name:complexity_hotspot.db`（path 非露出） |
| 所要時間 | 3 回実測して **real 18ms / 18ms / 18ms**（EXIT=0 一律）。前回の別ビルド実測 15ms と同オーダーで、`cost=low` 宣言と整合 |
| read-only 検証 | 実行前後で DB の mtime `2026-09-17 14:02:29` と sha256 `9445bec8d433fd79a659e29263d919f6d139b9b078cacc06491a27401d508782` が不変、`-wal`/`-shm` sidecar 0 件。live 書込みを行わないことを byte 単位で確認 |
| owner tests | `TestVerifierComplexityOrphanManifestUsesOnlyFixedOwnerInput` ほか新 test 群と `TestVerifierAllowlistCoversCurrentManifestCommands`（12 件 assert へ更新）が PASS。`go test -count=1` で rencrow-core-verify 2.108s / complexity 0.333s / rencrow-check-plan-runner 0.018s、`go test -count=1 ./modules/core/` 11.111s（arch test 含む）、`gofmt -l cmd internal config` clean、`go vet` clean |
| runner 乖離（新規） | HEAD から再 build した `rencrow-check-plan-runner` は正本 manifest v3 を **拒否**：`load check manifest: manifest schema_version must be 1 or 2`（`runner.go:448` の gate は `runnerSchemaVersion=1` と `ownerManifestVersion=2` のみ受理）で **EXIT=3**。配備物（Aug 25 ビルド）は別の箇所で落ち、`json: unknown field "coverage"`（`runner.go:491` の `plannerManifestFields` に coverage/executor/receipt_schema/surfaces が無く v2 用 set は version 2 のときしか使われない）。HEAD manifest を v2 へ書き換えても `executor must contain exactly kind and command_id` で通らない。**つまり Check Plan runner を通した gate は現状 `blocked` であり、この check の機械強制は owner verifier 実行と owner tests に留まる** |
| runner 実行主体 | `systemctl --user list-units` に check-plan/verifier を起動する unit は存在せず（あるのは `rencrow-binary-drift-notify.timer` と `rencrow-resilience.timer` のみ）、`git grep rencrow-check-plan-runner` は CORE・workspace root を含めて呼び出し側を出さない。よって今回の check 登録に CORE 再起動は不要 |
| 配備 manifest の pin 乖離 | `~/.local/share/rencrow/checks/core.json` は 11 件（sha256 `6f6a6ff2…`）で、`ecosystem.yaml` pin も `6f6a6ff2…`（workspace root `/home/nyukimi/RenCrow/ecosystem.yaml`）／drift monitor ローカルコピーの pin は `c1839255…` と**三者三様**。drift 実測は `MATCH 6 / MISMATCH 25 / DIRTY 0` で `core.json MISMATCH content hash不一致`（2026-09-17 17:17 UTC）。drift monitor は `~/.local/bin/rencrow-core-verify` を inventory に持たない（`main_package` 4 件は storage 系と manifest のみ）ため、verifier の再配備は drift 検査では捕捉されない |
| 配備 verifier の再配備 | `go build -o ~/.local/bin/rencrow-core-verify.rencrow-new ./cmd/rencrow-core-verify` → `mv` で rename（`rm -f` は hook で reject されるため）。旧 Sep 2 の `bae9185` → 新 `f606193`、`vcs.modified=false`、sha256 `fa41b5e4fba6bea8ee68f6a59595380c4b4a0fbcfd028c97bb4ce46d9d70ff87`。ビルドは `/tmp` で 2 回とも sha256 `784340b512bf8114…`（HEAD `fe2e3b0`、dirty 時点）に一致＝決定的 |
| 配備物による live receipt | `~/.local/bin/rencrow-core-verify run --manifest config/checks/core.json --check-id core_complexity_identity_orphan --complexity-db /srv/rencrow/db/core/databases/ops/complexity_hotspot.db` → **`status=passed`、EXIT=0**（2026-09-17T17:30:44Z）、`identity_orphans:0`、`read_only_observation:true`、`live_database_write_performed:false`。DB の mtime `14:02:29` と sha256 `9445bec8…` は不変、sidecar 0 件 |
| fail-closed の実証 | evidence dir を未作成のまま実行すると `status=blocked`・`failure_boundary="evidence output unavailable"`・**EXIT=20**・`evidence_refs:[]`（2026-09-17T17:30:30Z 実測）。証跡を書けない実行を `passed` にしない挙動を確認 |
| 配備 manifest の copy は未実施 | `~/.local/share/rencrow/checks/core.json` は HEAD 時点の正本と同一（11 件、sha256 `6f6a6ff2…`）で、今回の 12 件目（正本 sha256 `bb97d7ec…`）は未反映。**copy を見送った理由**は runner が正本 manifest v3 を schema 段階で拒否するため copy でも gate は発火せず、workspace root の `ecosystem.yaml` pin（`6f6a6ff2…`）と新しい乖離を作るだけになるため。copy は runner の v3 受理とセットで実施する |

## Failure / Problem / Cause / Lesson / Invariant / Enforcement / Tests

- Failure: Step 14 の「Evidence orphan 0」を check plan 上の assertion で宣言しても、live DB で FK index 列 8 行が空だった。つまり orphan 集計を機械に任せると 0 にならず、人手の payload 照合でのみ 0 と読めていた。
- Problem: 実体（payload）と索引（index 列）が乖離した行が本番に存在し、`scan_id`/`hotspot_id` の join・欠損検出が実データで機能しない。ID 統一の目的である lineage 追跡が索引レベルで未完成。
- Cause:
  1. 共通 `save()` が index 列を INSERT せず payload だけ書いていた（`indexColumn == ""` の旧 3 列 INSERT を残した呼び出しが hotspot/evidence/artifact に混在）。
  2. 空 FK を数えない orphan query（親行不在だけを見る NOT EXISTS）を使っていたため、空欄が「orphan でない」と誤読された。
  3. 副次事象として、`go test ./...` が `Tmp/test-runtime/gomodcache`（gitignore 済み・untracked）を main module 外とみなして setup failed になる。`buildInstallAndRestart()`（`cmd/rencrow/resilience_commands.go:561`）は incident 時に `RENCROW_RESILIENCE_REPO_DIR` へ `go test ./...` を実行するため、resilience auto-repair は構造上必ず failed する。
- Lesson: orphan／lineage の保証は「payload を見たら整合」では完了ではなく、**索引列を正本から決定論的に書く実装**と**空欄を不正とする集計 API**の両方が要る。集計 API を持たないまま check plan の assertion だけで完了宣言しない。仓库レベルの `./...` は test 失敗ではなく go list 解決の失敗であり、それを成功条件にしている運用コードは潜在不具合。
- Invariant: index 列（`complexity_hotspot.scan_id`、`complexity_hotspot_evidence.hotspot_id`、`complexity_report_artifact.scan_id`）は payload の同名値と一致しなければならない。空・不一致は InvalidIndex、非空で親行不在は Missing として集計される。実 Actor・owner module・認証境界は変更しない（`CountComplexityIdentityOrphans` は store レベルの強制 API として意図的に caller なしで新設）。
- Enforcement: `CountComplexityIdentityOrphans(ctx)` / `complexityOrphanCountQuery`（CTE + LEFT JOIN、InvalidIndex と Missing を分欄）で store API として固定。恒久強制となる `config/checks/core.json` への check 登録も実施済み（2026-09-17 17:10 UTC）。`check_id=core_complexity_identity_orphan` / `guarantee_id=complexity_identity_orphan_zero` / `phase=diagnostic` / `executor.kind=owner_cli` / `command_id=core-complexity-identity-orphan` として宣言し、実行側は `cmd/rencrow-core-verify/complexity_orphan_checks.go` の `runComplexityIdentityOrphan` が `--complexity-db` で明示された DB だけを `PRAGMA query_only` で開いて集計する。live DB の自動探索と既定 path fallback は持たせず、owner input 欠落は `passed` ではなく `blocked`。
- Tests: `TestSQLiteStoreWritesIdentityIndexColumns`（save が index 列を書く）、`TestCountComplexityIdentityOrphansReportsEmptyIndexColumns`（legacy の空 FK → `EvidenceInvalidIndex=1`、親欠落 → `EvidenceMissingHotspot=1`）、既存 arch test `TestStep14EvidenceMemoryLegacyFieldsAreBanned` / `TestStep14MigrationSourceIsRemovedAfterCutover`。

## 未確認

- ~~loopback viewer route の認証 middleware~~ 確定済み（2026-09-17 15:0x UTC 実測）。top-level handler stack は `cmd/rencrow/main.go:195` の `withTailscaleViewerOnlyGuard(withInteractionProfileGuard(mux))` の 2 層のみで、`internal/features/web/registrar.go:35-39` の complexity-hotspots 5 route は `mux.HandleFunc` へ素の `http.HandlerFunc` を直接登録する（per-route の認証 wrapper なし）。実測でもヘッダ無し GET は 200 を返し、`X-RenCrow-Client` と `X-RenCrow-Interaction-Profile` を自己申告した場合は profile の allow list で 403 になる。つまり **loopback はヘッダ無しで読むと guard を素通りし、guard は host（`.ts.net`）と自己申告 profile ベース**。CORE が `0.0.0.0:18790` で listen する現構成では loopback 以外の origin も同条件で読めるため、認証scopeの保証は network 境界と guard の組合せ依存であり invariant として強制されていない（恒久措置は未実施）。
- cutover receipt の source 紐付け。receipt に記録された source は `8a867f1` のままで、現行 binary は HEAD `082ad99`（`vcs.modified=true` は `docs/調査/` の dirty のみ、`*.go`・`go.mod`・`go.sum`・`config/`・`cmd/`・`internal/` は dirty 0 件を `git status` で確認）。Step 13 と同種の残件。
- resilience auto-repair が実際に incident へ落ちて挙動がどう記録されるか（過去 2 時間は試行 0 件で未観測）。
- ~~`CountComplexityIdentityOrphans` を運用 check へ登録した場合の所要時間と失敗時の consumer（未測定）~~ 実測済み（2026-09-17 17:10 UTC）。実測は上記「Check Plan 登録後の実測」参照。残る未確認は **runner を通した gate として実際に発火するか**で、現行 runner は正本 manifest v3 を schema 段階で拒否するため runner 経由では `blocked`（上記 runner 乖離の行）。check-plan/verifier を起動する unit も呼び出し側も無いため、発火経路そのものが未運用。
- `s14_full_system_restorecheck`、`s14_development_methodology_evidence_receipt`、`s14_relation_id` は deferred 3 件のまま（変更なし）。

## 優先順位の提案

1. ~~未コミット 2 ファイルを論理的な日本語 commit にする~~ 実施済み（`082ad99`）し、現行 revision 相当の再配備証跡を `redeploy-head-082ad99-20260917T143802Z` に残した。残りは cutover receipt 側の source 更新。
2. ~~`CountComplexityIdentityOrphans` を `config/checks/core.json` の check として登録し、Check Plan 側で gate を持つ~~ 登録と live receipt は実施済み（下記実測）。残件は実行側で、(a) runner が正本 manifest v3 を受理できるようにする（`runner.go:448` の version gate と `runner.go:491` の `plannerManifestFields`）、(b) ~~配備済み `rencrow-core-verify`（Sep 2 の `bae9185`）を再配備する~~ 実施済み（`f606193`、live receipt `passed`）、(c) 配備 manifest `~/.local/share/rencrow/checks/core.json` と ecosystem.yaml の pin を正本 12 件へ揃える（runner の v3 受理とセット、現状は copy 見送り）、(d) runner を起動する unit／呼び出し側を決める（現状どちらも無い）。
3. resilience の `go test ./...` を、この仓库で成立する形式（単一 package または `./internal/...` 列挙、もしくは gitignore 済み cache dir の扱いを修正）へ寄せる。現運用で repair が成功した記録が無い点を先に確認する。
4. ~~loopback viewer route の認証 middleware 有無を owner route 定義から確定させる~~ 実施済み。上記の実測で per-route 認証 wrapper が無いことを確認。残りは `0.0.0.0:18790` listen 现状の network 境界のみで保証されている点の恒久措置（bind 方針の決定、または check による強制）。
