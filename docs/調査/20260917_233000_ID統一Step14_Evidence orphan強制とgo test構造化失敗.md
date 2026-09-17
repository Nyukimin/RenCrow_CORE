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
| Check Plan | `config/checks/core.json`（正本）と `~/.local/share/rencrow/checks/*.json`（配備）の双方で complexity を参照する check 0 件 |

## Failure / Problem / Cause / Lesson / Invariant / Enforcement / Tests

- Failure: Step 14 の「Evidence orphan 0」を check plan 上の assertion で宣言しても、live DB で FK index 列 8 行が空だった。つまり orphan 集計を機械に任せると 0 にならず、人手の payload 照合でのみ 0 と読めていた。
- Problem: 実体（payload）と索引（index 列）が乖離した行が本番に存在し、`scan_id`/`hotspot_id` の join・欠損検出が実データで機能しない。ID 統一の目的である lineage 追跡が索引レベルで未完成。
- Cause:
  1. 共通 `save()` が index 列を INSERT せず payload だけ書いていた（`indexColumn == ""` の旧 3 列 INSERT を残した呼び出しが hotspot/evidence/artifact に混在）。
  2. 空 FK を数えない orphan query（親行不在だけを見る NOT EXISTS）を使っていたため、空欄が「orphan でない」と誤読された。
  3. 副次事象として、`go test ./...` が `Tmp/test-runtime/gomodcache`（gitignore 済み・untracked）を main module 外とみなして setup failed になる。`buildInstallAndRestart()`（`cmd/rencrow/resilience_commands.go:561`）は incident 時に `RENCROW_RESILIENCE_REPO_DIR` へ `go test ./...` を実行するため、resilience auto-repair は構造上必ず failed する。
- Lesson: orphan／lineage の保証は「payload を見たら整合」では完了ではなく、**索引列を正本から決定論的に書く実装**と**空欄を不正とする集計 API**の両方が要る。集計 API を持たないまま check plan の assertion だけで完了宣言しない。仓库レベルの `./...` は test 失敗ではなく go list 解決の失敗であり、それを成功条件にしている運用コードは潜在不具合。
- Invariant: index 列（`complexity_hotspot.scan_id`、`complexity_hotspot_evidence.hotspot_id`、`complexity_report_artifact.scan_id`）は payload の同名値と一致しなければならない。空・不一致は InvalidIndex、非空で親行不在は Missing として集計される。実 Actor・owner module・認証境界は変更しない（`CountComplexityIdentityOrphans` は store レベルの強制 API として意図的に caller なしで新設）。
- Enforcement: `CountComplexityIdentityOrphans(ctx)` / `complexityOrphanCountQuery`（CTE + LEFT JOIN、InvalidIndex と Missing を分欄）で store API として固定。恒久強制には `config/checks/core.json` への check 登録が別途必要（未実施）。
- Tests: `TestSQLiteStoreWritesIdentityIndexColumns`（save が index 列を書く）、`TestCountComplexityIdentityOrphansReportsEmptyIndexColumns`（legacy の空 FK → `EvidenceInvalidIndex=1`、親欠落 → `EvidenceMissingHotspot=1`）、既存 arch test `TestStep14EvidenceMemoryLegacyFieldsAreBanned` / `TestStep14MigrationSourceIsRemovedAfterCutover`。

## 未確認

- loopback viewer route `/viewer/complexity-hotspots` と `/scan` に認証 middleware が効いているか（grep で `WithAuth` / `RequireAuth` 等の該当なしと読めるが断定しない）。
- cutover receipt の source 紐付け。receipt に記録された source は `8a867f1` のままで、現行 binary は HEAD `082ad99`（`vcs.modified=true` は `docs/調査/` の dirty のみ、`*.go`・`go.mod`・`go.sum`・`config/`・`cmd/`・`internal/` は dirty 0 件を `git status` で確認）。Step 13 と同種の残件。
- resilience auto-repair が実際に incident へ落ちて挙動がどう記録されるか（過去 2 時間は試行 0 件で未観測）。
- `CountComplexityIdentityOrphans` を運用 check へ登録した場合の所要時間と失敗時の consumer（未測定）。
- `s14_full_system_restorecheck`、`s14_development_methodology_evidence_receipt`、`s14_relation_id` は deferred 3 件のまま（変更なし）。

## 優先順位の提案

1. ~~未コミット 2 ファイルを論理的な日本語 commit にする~~ 実施済み（`082ad99`）し、現行 revision 相当の再配備証跡を `redeploy-head-082ad99-20260917T143802Z` に残した。残りは cutover receipt 側の source 更新。
2. `CountComplexityIdentityOrphans` を `config/checks/core.json` の check として登録し、Check Plan 側で gate を持つ。
3. resilience の `go test ./...` を、この仓库で成立する形式（単一 package または `./internal/...` 列挙、もしくは gitignore 済み cache dir の扱いを修正）へ寄せる。現運用で repair が成功した記録が無い点を先に確認する。
4. loopback viewer route の認証 middleware 有無を owner route 定義から確定させる。
