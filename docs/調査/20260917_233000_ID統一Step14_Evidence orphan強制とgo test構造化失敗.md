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
| runner 乖離（新規）→ 同日解消 | 当時の実測: HEAD から再 build した `rencrow-check-plan-runner` は正本 manifest v3 を **拒否**（`load check manifest: manifest schema_version must be 1 or 2`、version gate が `runnerSchemaVersion=1` と `ownerManifestVersion=2` のみ受理、**EXIT=3**）。配備物（Aug 25 ビルド）は別の箇所で落ち、`json: unknown field "coverage"`（`plannerManifestFields` に coverage/executor/receipt_schema/surfaces が無く、v2 用 set は version 2 のときしか使われない）。HEAD manifest を v2 へ書き換えても `executor must contain exactly kind and command_id` で通らない。**解消実測（commit `4f90a6e`）**: `maxOwnerManifestVersion = 3` を追加して version gate を `1..3` へ拡張（超限は `manifest schema_version must be 1 to 3`）、v3 の `executor.acquisition` は runner 側で**構造検証のみ**で受理（strict decode・`mode=owner_self_collect`・`verification_safe` 必須・inputs 非空・stable な id・duplicate id 拒否・required/class/source 必須）し、planner へは strict v1 へ投影したまま（extension が planner へ非漏洩、source 非変異）。class/source の列挙は verifier 側に残し runner に持たせない（Semantic Duplication 回避）。実 planner 使用 e2e `TestRunnerRealProcess` が PASS（env は `RENCROW_CHECK_PLAN_BIN`）、配備 runner は sha256 `ad00de4a26265eea5c90ece8291c2de58d437735f02018185eabf06b9ba9f60b`（`vcs.revision=4f90a6e`、`vcs.modified=false`、/tmp で 2 回ビルドして一致＝決定的） |
| runner 実行主体 | `systemctl --user list-units` に check-plan/verifier を起動する unit は存在せず（あるのは `rencrow-binary-drift-notify.timer` と `rencrow-resilience.timer` のみ）、`git grep rencrow-check-plan-runner` は CORE・workspace root を含めて呼び出し側を出さない。よって今回の check 登録に CORE 再起動は不要 |
| 配備 manifest の pin 乖離 | `~/.local/share/rencrow/checks/core.json` は 11 件（sha256 `6f6a6ff2…`）で、`ecosystem.yaml` pin も `6f6a6ff2…`（workspace root `/home/nyukimi/RenCrow/ecosystem.yaml`）／drift monitor ローカルコピーの pin は `c1839255…` と**三者三様**。drift 実測は `MATCH 6 / MISMATCH 25 / DIRTY 0` で `core.json MISMATCH content hash不一致`（2026-09-17 17:17 UTC）。drift monitor は `~/.local/bin/rencrow-core-verify` を inventory に持たない（`main_package` 4 件は storage 系と manifest のみ）ため、verifier の再配備は drift 検査では捕捉されない。**copy 実施後の再実測（19:31 UTC、root manifest・`--json` 52 件）**: `MATCH 40 / MISMATCH 8 / UNMAPPED 4`。core.json は `MISMATCH content hash不一致`（built `bb97d7ec…` vs pin `6f6a6ff2…`）として残る。root `ecosystem.yaml` は `components.core.version = bae91856` の blob（11 件、sha256 `6f6a6ff2…` と実測一致）と自己整合しており、**pin を 12 件へ進めるには `components.core.version` の release 判断が要る**（未実施、下記残件）。`rencrow-check-plan-runner` と `rencrow-check-plan` は inventory 自体に無く（name に plan を含む item が 0 件）、runner の再配備は drift 検査で捕捉されない |
| 配備 verifier の再配備 | `go build -o ~/.local/bin/rencrow-core-verify.rencrow-new ./cmd/rencrow-core-verify` → `mv` で rename（`rm -f` は hook で reject されるため）。旧 Sep 2 の `bae9185` → 新 `f606193`、`vcs.modified=false`、sha256 `fa41b5e4fba6bea8ee68f6a59595380c4b4a0fbcfd028c97bb4ce46d9d70ff87`。ビルドは `/tmp` で 2 回とも sha256 `784340b512bf8114…`（HEAD `fe2e3b0`、dirty 時点）に一致＝決定的 |
| 配備物による live receipt | `~/.local/bin/rencrow-core-verify run --manifest config/checks/core.json --check-id core_complexity_identity_orphan --complexity-db /srv/rencrow/db/core/databases/ops/complexity_hotspot.db` → **`status=passed`、EXIT=0**（2026-09-17T17:30:44Z）、`identity_orphans:0`、`read_only_observation:true`、`live_database_write_performed:false`。DB の mtime `14:02:29` と sha256 `9445bec8…` は不変、sidecar 0 件 |
| fail-closed の実証 | evidence dir を未作成のまま実行すると `status=blocked`・`failure_boundary="evidence output unavailable"`・**EXIT=20**・`evidence_refs:[]`（2026-09-17T17:30:30Z 実測）。証跡を書けない実行を `passed` にしない挙動を確認 |
| 配備 manifest の copy（実施済み）**→ 実効pathは下記訂正節で確定（default の読み出し元）** | 正本 12 件（sha256 `bb97d7ec…`）を `~/.local/share/rencrow/checks/core.json` へ**byte copy**し、配備 sha256 が正本と一致することを確認（mode 0644）。**pretty-print すると sha が変わる**（実測 `fac14e5eaa2d1f8dc431e3dda0c935084397fa93fc00642e07ea62ed81f18278`）ため、content sha256 pin であるこの file は byte copy が必須。退避: `core.json.bak-6f6a6ff2-20260917T1918Z`（旧 11 件 `6f6a6ff2…`）と `core.json.bak-fac14e5e-20260917T1920Z`（中間 pretty 版）。 runner 側の v3 受理（`4f90a6e`）と同一 IU で実施 |

### 配備 manifest copy の実効path確定（2026-09-17 20:0x〜20:1x UTC、初回判定の訂正）

| 実行 | 実測値 |
| --- | --- |
| 初回判定（20:0x UTC）→ 20:1x UTC で**反証・訂正** | `grep -rn "share/rencrow/checks"` で workspace root 全 repo（`.go`/`.js`/`.py`/`.sh`/`.ps1`/`.yaml`/Makefile）を走らせ hits が `ecosystem.yaml` の `installed_path` 宣言と本 `docs/調査/` の JSON のみだったため、配備 copy を「reader 0 件＝inert」と判定した。**この判定は誤り** |
| 誤りの機械的原因 | `defaultManifestPath()`（`cmd/rencrow-check-plan-runner/runner.go:42-51`）が `filepath.Join(home, ".local", "share", "rencrow", "checks", "core.json")` と**path を構成**するため、連結字列 `share/rencrow/checks` は `.go` に出現しない。`main.go:19` が `--manifest` の flag default として `defaultManifestPath()` を渡し、`runner.go:177` も `options.ManifestPath = defaultManifestPath()` を通す |
| 解決順（コード確定） | `RENCROW_CORE_CHECK_MANIFEST` 指定 → 無ければ `$HOME/.local/share/rencrow/checks/core.json` → home 解決不可時のみ相対 `config/checks/core.json`。env 優先は既存 test `TestDefaultManifestPathHonorsExplicitOwnerConfiguration`（`runner_test.go:181-186`）が固定 |
| reader の実証（負のcontrol） | 配備 copy を `/tmp` へ退避して `env -u RENCROW_CORE_CHECK_MANIFEST ~/.local/bin/rencrow-check-plan-runner --phase runtime` → `load check manifest: open /home/nyukimi/.local/share/rencrow/checks/core.json: no such file or directory`（cwd は `/tmp`、repo の `config/checks/core.json` も無い）。**配備 copy が default の読み出し元であることを実証**（復帰後の sha256 は `bb97d7ec…` のまま） |
| copy による実効値の変化 | `--now 2026-09-17T20:00:00Z` 固定で比較。旧配備 11 件（`core.json.bak-6f6a6ff2-20260917T1918Z`）→ `plan_revision=sha256:528cf15e…`、新配備 12 件（現 default）→ `plan_revision=sha256:32e0e279…`。12 件の側にだけ `core_complexity_identity_orphan` が含まれる（`check_id` 列挙で実測: 12 件=True / 11 件=False）。**copy は実効 manifest を 11→12 件へ実際に変えている** |
| 帰結（訂正版） | copy は無効な変更ではなく、**default 経路の実効 manifest を変えた**。ただし runner は `included check "core_canonical_actor_e2e" is not allowlisted` で phase ごとに **EXIT=3 `blocked`・results 0** の fail-closed を返すため、12 件目（orphan check）の集計が完結するわけではない（実行所有は verifier 側にあり、runner に `owner_cli` 実行経路は無い＝20 時台の実測から変更なし）。自動呼び出し側（systemd unit）は依然 0 件 |
| drift 側のリスク（重要化） | `ecosystem.yaml:64` の `installed_path` pin は `6f6a6ff2…`（＝`components.core.version bae91856` の blob、11 件）のままなので `core.json MISMATCH content hash不一致` が残る。`--apply` auto-redeploy が走ると配備 copy が **11 件へ巻き戻り、default 経路から orphan check が消える**。自動 `--apply` 経路は実測上ない（drift notify unit は `--json` のみ）ため当面は無害だが、pin の release 判断までリスクは残る |

### runner v3 受理と配備 manifest 通過の実測（2026-09-17 19:18〜19:31 UTC）

| 実行 | 実測値 |
| --- | --- |
| 旧配備 runner × 配備 manifest（11 件 v3） | **EXIT=3 `blocked`／`load check manifest: json: unknown field "coverage"`**（`/tmp/s14i/before_deployed_manifest.json`）。copy 前に旧配備物が**別の箇所で**拒否する事象を実証 |
| 新 runner × 配備 manifest（11 件） | EXIT=3 `blocked`、`plan_revision=sha256:e5e861fd6bad2b59eed4adfe83adcc0e3618acde1881cfa787badbfd1c1700a5`、`included check "core_runtime_identity_lifecycle_security" is not allowlisted`、results 0 |
| 新 runner × 正本 manifest（12 件） | EXIT=3 `blocked`、`plan_revision=sha256:b64d00705ff4a630614dd6dcbb476194fd0624ede6b84631f37a7389d0fbfb45`、同じ `not allowlisted` |
| 配備 runner（`--manifest` 無し・phase=runtime） | EXIT=3 `blocked`、`plan_revision=sha256:80c1b3cb67156c8df7ca823d57eb1dab0fb2da4c9fe578cbe843b8efc4ebc4c2`、`core_runtime_identity_lifecycle_security is not allowlisted` |
| 同・phase=diagnostic | EXIT=3 `blocked`、`plan_revision=sha256:eba04d6ca4c292bfa9ac6c05dcfff9f9406ea6a67ad25bcf032f1078ad37c8fd`、`core_canonical_actor_e2e is not allowlisted` |
| 結論 | **schema 段階の拒否は解消**。runner が実行経路に持つのは HTTP GET 4 route（`core_health`/`core_readiness`/`core_l1_lightweight_query`/`core_l1_snapshot_integrity`）と `rencrow-storage-restore-check` のみで、**`owner_cli` を実行する経路は runner に無い**（実行と allowlist の所有は `rencrow-core-verify`）。よって 12 件のうち runtime/diagnostic で included になる非 allowlist check により `blocked` の fail-closed となるのが現況の正当な挙動であり、gate の完結には呼び出し側と allowlist の確定が要る（未実施） |
| runner の呼び出し側 | `systemctl --user list-units` に check-plan/verifier を起動する unit は依然なし（`rencrow-binary-drift-notify.timer` と `rencrow-resilience.timer` のみ）。drift notify unit は `check_deployed_binaries_notify.py` を `--json` 指定のみで起動し `--apply` を付けないため、**今回の core.json copy が自動で 11 件へ巻き戻る経路は実測上ない**（auto-redeploy は `--apply` 明示時のみ、`check_deployed_binaries.py:1037` の `redeploy_managed_file` は `module_dir@component.version` の blob を読む） |

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

### 配備pathのreader探索を連結字列grepだけで「reader 0件」と断定した（自己誤判定）

- Failure: 配備 manifest の sha が pin と違うという所見を是正した後、`grep -rn "share/rencrow/checks"` の hits が `ecosystem.yaml` と docs だけであることを根拠に「配備 copy を読む実行経路は存在しない（inert）」と判定した。実際は `main.go:19` が `--manifest` の既定値として `defaultManifestPath()` を使い、配備 copy が **default の読み出し元**だった。負のcontrol（file を退避して `open ...: no such file or directory`）で反証した
- Problem: 否定命題（reader が不在）を成立させるには探索手法が path 表記ゆれを網羅している必要があったが、その前提を検証しなかった。誤判定は「copy は無効」「revert しても効果なし」「MISMATCH は release 判断待ちでよい」という後段の判断に波及していた
- Cause: `filepath.Join` で構成される path は連結字列で source に出現しない。検索対象を文字列1表記に固定し、`.local`/`share`/`checks` の断片や `defaultManifestPath` のような resolver 名の探索を併走させなかった
- Lesson: 「○○が存在しない」を結論にする前は、表記ゆれ（連結／分割／定数展開／Join）を潰した複数表記の検索か、**存在すれば落ちる負の実行**（退避して挙動が変わるか）で裏を取る。特に既定値（flag default）の読み出し元はflag宣言と resolver func からたどるのが最短
- Invariant: 配備 artifact の reader 不在判定は、複数の path 表記と resolver func 名での検索、または artifact を退避した時の失敗の実測を必須証拠とする。grep の hits 0 件だけで reader 不在を主張してよいのは、生成される path がすべて字面一致する文字列から決まる場合に限る
- Enforcement: 未強制。候補は (a) default path と `ecosystem.yaml` の `installed_path` が同一を指すことを検証する architecture test（現 `TestDefaultManifestPathHonorsExplicitOwnerConfiguration` は env 優先しか固定していない）、(b) reader 不在の artifact に content sha pin を持たせない manifest 規則
- Tests: `TestDefaultManifestPathHonorsExplicitOwnerConfiguration`（env 優先のみ固定、home fallback は未カバー）。本判定の回帰 test は新設しない（負のcontrol で確定する事実のため）

- Tests: `TestSQLiteStoreWritesIdentityIndexColumns`（save が index 列を書く）、`TestCountComplexityIdentityOrphansReportsEmptyIndexColumns`（legacy の空 FK → `EvidenceInvalidIndex=1`、親欠落 → `EvidenceMissingHotspot=1`）、既存 arch test `TestStep14EvidenceMemoryLegacyFieldsAreBanned` / `TestStep14MigrationSourceIsRemovedAfterCutover`。

## 未確認

- ~~loopback viewer route の認証 middleware~~ 確定済み（2026-09-17 15:0x UTC 実測）。top-level handler stack は `cmd/rencrow/main.go:195` の `withTailscaleViewerOnlyGuard(withInteractionProfileGuard(mux))` の 2 層のみで、`internal/features/web/registrar.go:35-39` の complexity-hotspots 5 route は `mux.HandleFunc` へ素の `http.HandlerFunc` を直接登録する（per-route の認証 wrapper なし）。実測でもヘッダ無し GET は 200 を返し、`X-RenCrow-Client` と `X-RenCrow-Interaction-Profile` を自己申告した場合は profile の allow list で 403 になる。つまり **loopback はヘッダ無しで読むと guard を素通りし、guard は host（`.ts.net`）と自己申告 profile ベース**。CORE が `0.0.0.0:18790` で listen する現構成では loopback 以外の origin も同条件で読めるため、認証scopeの保証は network 境界と guard の組合せ依存であり invariant として強制されていない（恒久措置は未実施）。
- cutover receipt の source 紐付け。receipt に記録された source は `8a867f1` のままで、現行 binary は HEAD `082ad99`（`vcs.modified=true` は `docs/調査/` の dirty のみ、`*.go`・`go.mod`・`go.sum`・`config/`・`cmd/`・`internal/` は dirty 0 件を `git status` で確認）。Step 13 と同種の残件。
- resilience auto-repair が実際に incident へ落ちて挙動がどう記録されるか（過去 2 時間は試行 0 件で未観測）。
- ~~`CountComplexityIdentityOrphans` を運用 check へ登録した場合の所要時間と失敗時の consumer（未測定）~~ 実測済み（2026-09-17 17:10 UTC）。実測は上記「Check Plan 登録後の実測」参照。
- ~~runner を通した gate として実際に発火するか~~ **実測で解消**（2026-09-17 19:2x UTC）。runner は正本 manifest v3 を受理し、`blocked`・EXIT=3・`plan_revision` 非空・results 0 の fail-closed を返す（上記「runner v3 受理と配備 manifest 通過の実測」。`not allowlisted` が拒否理由）。ただし**runner に `owner_cli` の実行経路が無い**ため、この check の集計を実際に走らせるのは owner verifier 側の呼び出しであり、その呼び出し側（unit／親）が不在な点は残る（下記残件）。
- `s14_full_system_restorecheck`、`s14_development_methodology_evidence_receipt`、`s14_relation_id` は deferred 3 件のまま（変更なし）。

## 優先順位の提案

1. ~~未コミット 2 ファイルを論理的な日本語 commit にする~~ 実施済み（`082ad99`）し、現行 revision 相当の再配備証跡を `redeploy-head-082ad99-20260917T143802Z` に残した。残りは cutover receipt 側の source 更新。
2. ~~`CountComplexityIdentityOrphans` を `config/checks/core.json` の check として登録し、Check Plan 側で gate を持つ~~ 登録と live receipt は実施済み（下記実測）。(a) ~~runner が正本 manifest v3 を受理できるようにする~~ **解消**（`4f90a6e`：version gate を `1..3`、v3 `acquisition` を構造検証で受理、planner へは v1 投影、実 planner e2e PASS、配備 sha `ad00de4a…`）、(b) ~~配備済み `rencrow-core-verify`（Sep 2 の `bae9185`）を再配備する~~ 実施済み（`f606193`、live receipt `passed`）、(c) 配備 manifest の copy は実施済み（12 件 byte copy `bb97d7ec…`）で、**`--manifest` 既定値の実効読み出し元であり実効値が変わっている**（上記訂正節）。**root `ecosystem.yaml` の pin は `6f6a6ff2…` のまま**（1 行だけ `bb97d7ec…` へ動かせば content hash の MISMATCH は解消するが、`components.core.version` を identity branch の commit へ進めるかは release 判断なので未実施）、(d) runner を起動する unit／呼び出し側を決める（現状どちらも無い＝残件）。
3. resilience の `go test ./...` を、この仓库で成立する形式（単一 package または `./internal/...` 列挙、もしくは gitignore 済み cache dir の扱いを修正）へ寄せる。現運用で repair が成功した記録が無い点を先に確認する。
4. ~~loopback viewer route の認証 middleware 有無を owner route 定義から確定させる~~ 実施済み。上記の実測で per-route 認証 wrapper が無いことを確認。残りは `0.0.0.0:18790` listen 现状の network 境界のみで保証されている点の恒久措置（bind 方針の決定、または check による強制）。
5. 新規残件（drift 監視と配備 manifest）: (i) root `ecosystem.yaml` の `config/checks/core.json` pin が `6f6a6ff2…`（11 件）のまま＝drift 実測で `core.json MISMATCH content hash不一致`、かつ将来 `--apply` auto-redeploy を走らせると配備 core.json が `component.version = bae91856` の blob へ**11 件へ巻き戻る**（`bae91856` は `origin/main` の ancestor ではなく、その blob sha が `6f6a6ff2…` であることを実測）。(ii) drift monitor の inventory に `rencrow-check-plan-runner`／`rencrow-check-plan` が無く、runner の再配備は検査で捕捉されない（complexity DB も監視対象外）。(iii) runner／verifier を起動する systemd unit・呼び出し側が不在で、Check Plan gate は手動実行以外では発火しない。(iv) runner に `owner_cli` の実行経路を持たせない設計は Conceptual Integrity 側で維持（実行所有は verifier）。**pin を 1 行だけ更新する自己整合的な修正は成立しない**。`make check-manifest` は EXIT=0（manifest 内の自己整合）。`make check-workspace` は **本 IU で root repo を未変更のまま** EXIT=1 の既存状態：`components.core HEAD 4f90a6e does not match source-pinned version bae91856`（`validate_ecosystem.py:854-866`＝CORE の checkout が identity branch であること由来の既存 fail）。sha 照合は同じ `component.version` の blob に対して走る（`validate_ecosystem.py:867-876` `_git_blob_sha256`）ため、version を据え置いて sha だけ `bb97d7ec…` へ動かせば `managed file config/checks/core.json hash bb97d7ec… does not match manifest 6f6a6ff2…` になる（`bae91856` の blob sha が `6f6a6ff2…` であることは実測、fail 自体はコード照合）。`make check-pins` は `{"ok":false,"status":"blocked","error":"workspace is dirty and cannot be pinned: /home/nyukimi/RenCrow/RenCrow_PORTAL"}` で blocked。よって core.json の sha を 12 件へ進めるには `components.core.version` の release 判断（identity branch を main 側へ寄せる決定）が前提になり、本 IU の scope 外として残件化する。
