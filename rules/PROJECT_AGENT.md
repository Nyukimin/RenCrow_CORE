# PROJECT_AGENT.md - RenCrow_CORE 実装ルール

**最終更新**: 2026-07-18

## 0. このファイルの役割

このファイルはRenCrow_CORE固有のAI実装ルールです。製品仕様の正本ではありません。

- 製品仕様の唯一の正本: `docs/README.md` と、そこに列挙された9つの仕様書
- 常時必要な作業ルール: `AGENTS.md`
- 共通実装ルール: `rules/common/`
- path固有制約: `rules/rules_path_scoped_constraints.md`
- routing実務: `rules/routing-policy.md`
- Viewer実務: `rules/rules_viewer_ui.md`
- 視覚方針: `DESIGN.md`

このファイルは、正本の契約を安全に実装する方法だけを補足します。製品の責務、機能、設定、API、運用契約をこのファイルで再定義しません。

## 1. 実装開始前の必須確認

### 1.1 module rootとGo module

`/home/nyukimi/RenCrow`を単一repositoryとして扱わず、RenCrow_COREのrootで作業します。

```bash
git status --short --branch
git remote -v
grep '^module\|^go ' go.mod
go build ./...
```

- module pathやGo versionを過去資料から推測しない。
- remoteとmodule pathの不一致を見つけても、影響範囲を確認せず自動変更しない。
- dirty worktreeの既存差分を上書き、破棄、無関係なcommitへ混入しない。

### 1.2 実装前チェックリスト

- [ ] `docs/README.md`から対象領域の現行正本を読んだ
- [ ] 現行正本と実装・test・production wiringの差異を確認した
- [ ] 変更の所有moduleと責務を確認した
- [ ] 対象ファイルだけでなく呼出元、依存先、近接testを読んだ
- [ ] 受入条件と最小の検証方法を実装前に決めた
- [ ] fallback、degraded、unavailable、errorを成功へ丸めていない
- [ ] UI・音声・state変更では、表示、再生、入力、永続化、観測の正本を分けた

## 2. 正本への収束

### 2.1 優先順位

製品仕様が衝突した場合は次の順で判断します。

1. `docs/README.md`と対象の現行正本
2. 現行production wiringとtestによる事実確認
3. `AGENTS.md`と関連rulesの作業制約
4. `DESIGN.md`または`TOOL_CONTRACT.md`の対象別方針

archive branch、Knowledge、削除済みdocs、版付き旧仕様、引き継ぎ資料は現行正本ではありません。現行正本に不足がある場合は、実装事実を確認して `docs/README.md` に列挙された該当文書を更新します。別の正本を作りません。

### 2.2 重複記述を増やさない

- product behavior、API field、config key、module ownership、運用契約は現行正本へ書く。
- rulesには、調査順序、禁止事項、検証条件など作業者の制約だけを書く。
- READMEやcommentへ同じ仕様を複製する場合は、正本へのlinkと用途を明確にする。
- 仕様変更と実装変更を黙って分離しない。

## 3. 責務とmodule境界

COREの所有範囲と外部module境界は `docs/01_システム概要.md` と `docs/04_アーキテクチャ概要.md` に従います。

- 外部moduleの演算本体、model、backend固有設定をCOREへ取り込まない。
- PORTALの外部UIとCOREのDebug Viewerを混同しない。
- CMDはclient facadeであり、COREのruntime状態や仕様を所有しない。
- 横断的に再利用するtoolはRenCrow_Toolsを所有先とする。
- route、provider、store、CLI、background jobを二重登録・二重起動しない。

Chat、Worker、Coder、Advisor、Toolの契約は `docs/02_機能仕様.md` と `docs/03_キャラクター・エージェント仕様.md` に従います。CoderやAdvisorをside effectの最終責任者にしません。

## 4. 実装ルール

### 4.1 Go

- errorを握りつぶさず、必要な文脈を付けて返す。
- panicはprocess継続に使わず、異常証拠と外部supervisorによる復旧を前提にする。
- goroutineを無制限に起動しない。
- `context.Context`、停止条件、channel close、timerの寿命を明示する。
- package、file、functionは既存の責務境界に合わせる。
- 新しい抽象化より、安定した既存contractの再利用を優先する。
- 公開contractを変える場合は互換性とcontract testを先に固定する。

### 4.2 state、ID、cache

- stateの主たる真実、owner、寿命、再構築元を明示する。
- 新しいIDを追加する前に既存IDで表現できないか確認する。
- cache、queue、pendingを整合性設計の代替にしない。
- 表示、TTS再生、STT入力、口パク、logを同じstateで代用しない。

詳細は `rules/common/rules_state_management.md` に従います。

### 4.3 Safe Build Mode / Tool Build Mode

| mode | 対象 | 制約 |
| --- | --- | --- |
| Safe Build Mode | 既存code、DB、config、service、運用、Memory、正式dataに触る作業 | 小さい差分、事前test、rollback可能性、実runtime確認を優先 |
| Tool Build Mode | 本体から独立した新規tool、検証CLI、実験 | RenCrow_Tools、`experiments/`、`sandbox/`の適切な所有先へ隔離 |

判断に迷う場合、または正式data・本番runtimeへ接続する場合はSafe Build Modeとして扱います。

## 5. Viewer、STT、TTS

- Viewer変更ではタブ、API、state、DOM、CSSの対象責務を限定する。
- 既存表示や操作を「整理」の名目で削除しない。
- desktopとnarrow/mobileを実ブラウザで確認する。
- 表示本文をTTS chunkから再構成しない。
- STT録音状態と認識結果の投入先を分離する。
- 音声生成、音声取得、browser再生、口パクを別の成功条件として確認する。

製品契約は現行正本、視覚方針は `DESIGN.md`、実装制約は `rules/rules_viewer_ui.md`、観測方法は `rules/common/rules_observation_verification.md` に従います。

## 6. 高リスク操作

次は高リスクとして、ユーザーの依頼範囲と影響を確認します。

- dependencyの追加・更新
- file削除、schema変更、data migration
- CI、build、deploy、systemd設定変更
- 大規模横断refactor
- secret、権限、network boundary変更
- production data、Memory、Knowledge、Source Registryへのwrite
- service restart、外部公開、送信、課金

secretをsource、config sample、test、docs、logへ埋め込みません。破壊的操作、外部side effect、approvalの契約は `docs/07_安全・承認・データ方針.md` に従います。

## 7. serviceとhealth

再起動、panic、hang、self-repair、log retentionの契約は `docs/09_運用ログ・panic保存仕様.md` を正本とします。

手動で再ビルド・再起動する場合の基本順序:

1. `systemctl --user stop rencrow.service`
2. 残存する`rencrow` processがないことを確認
3. `:18790`のlistenが消えたことを確認
4. `/health/live`が応答しないことを確認
5. build・install
6. service start
7. `/health/live`、`/health`、必要なfeature endpointを分けて確認

外部LLM、STT、TTSの停止をCORE自身のliveness failureと混同しません。

## 8. testと完了条件

基本確認:

```bash
go test ./...
go build ./...
```

対象に応じて `go vet ./...`、lint、contract test、integration test、Playwright、実runtime E2Eを追加します。

- testの期待値を実装へ合わせて黙って弱めない。
- unit test通過だけでViewer、service、stream、STT/TTS、外部連携を完了扱いしない。
- docsだけの変更では、index、relative link、allowlist、旧正本参照ガードを確認する。
- 未実施、失敗、flaky、環境不足は未確認として報告する。
- commit前に無関係な差分、secret、生成物が混ざっていないことを確認する。

## 9. 更新ルール

- 製品仕様の変更は `docs/README.md` に列挙された該当正本へ反映する。
- このファイルは、実装者の作業制約が変わった場合だけ更新する。
- 一般化できる内容は `rules/common/`、path固有内容は `rules/rules_path_scoped_constraints.md`、反復手順はskillへ置く。
- 同じ製品仕様をこのファイルへコピーして第二の正本にしない。
