---
title: 調査 — DCI fallback content ranking期限超過
date: 2026-10-05 04:13 JST
status: corrected
skill: debug-investigate
symptom: Shiro DCI identity受入がMaxSecondsの制限に一致してHTTP 500となる
frequency: provider候補が0件でfallback corpusがMaxFilesReadを超える旧条件、およびmetadata ranking readがwriter connectionを待つ条件
inputs: production DCI owner trace、pre-restart receipt、関連source/test
related: docs/調査/20261003_storage_host_separation_受入記録.md
---

# DCI fallback content ranking期限超過 調査記録

## 文書情報

- 日付: 2026-10-05 04:13 JST
- 引き継ぎ元: `9bfb661`配備後のShiro DCI identity受入失敗
- 契約正本: `docs/architecture/identity/IDENTITY_CANONICAL.md`
- 検出: `Tmp/test-runtime/deploy-7a00cff/evidence/pre-restart/core-core-dci-identity-pre-restart-20261004T191026.000000000Z.json`
- 根拠: `/srv/rencrow/db/core/databases/ops/dci.db` の `dci_search_trace`

## 結論

DCIの既定予算を60秒に拡大した後、`7d9c283` のfallback read-budget修正で、metadata rankがないfilesystem fallbackも `MaxFilesRead` に制限されることは確認できた。しかし `7d9c283` 配備後の実受入 Action `act_01a10877-3a86-77d4-b44a-56a7fb696e16` はなお60秒で終了し、step/evidenceは0件だった。したがってread-budget defectはconfirmedだが、受入timeoutの十分原因ではなかった。

残存する直接原因は、content ranking直前の `L1SourceMetadataRanker.RankDCICandidateFiles` → `L1SQLiteStore.ListSourceRegistryEntries` が、max conn 1のwriter `s.db`をqueryしていたことである。継続writeまたはwriter connection占有時にmetadata readが待たされ、search budgetを消費した。query-only `readDB`へ変更し、短いcontextでwriter connection保持中もregistry readが成功する回帰testを追加した。provider vector mismatchはfallbackの誘因として残る別問題であり、本修正の十分原因とは扱わない。

## 実測

| 配備revision | DCI予算 | Action | 開始→終了 | 結果 |
|---|---:|---|---|---|
| `9bfb661` | 30秒 | `act_01a107e3-10b6-748c-9a4c-0ee3bf5c3dc0` | 17:08:06.879→17:08:36.880 UTC | `context deadline exceeded` |
| `7a00cff` | 60秒 | `act_01a10853-933e-7c6c-9db0-a20d659f50ee` | 19:11:01.845→19:12:01.845 UTC | `context deadline exceeded` |
| `7d9c283` | 60秒 | `act_01a10877-3a86-77d4-b44a-56a7fb696e16` | 受入開始→60秒後 | `context deadline exceeded`; step/evidence=0 |

`7a00cff` traceは `content ranking stopped: context deadline exceeded` を記録し、evidence/stepは0件だった。またKB candidate providerは `expected 1024, got 3584` のvector次元不一致で利用できず、filesystem fallback条件に入った。`7d9c283` のproduction証拠も provider vector mismatch と `content ranking stopped` を記録したが、file_read Actionはaction storeに生成されていなかった。これはcontent rankingのfile read回数超過だけではなく、metadata ranking readがcontent ranking開始前にdeadlineを消費したことと整合する。

## 調査経緯

### 仮説1: content rankingの直列I/Oがdeadlineを消費する

- **根拠**: 失敗時刻が30秒と60秒の各budgetに一致し、traceがcontent ranking停止を記録した。
- **検証結果**: read-budget defectとして確認。ただし `7d9c283` で候補を `MaxFilesRead` に制限した後も受入timeoutが残り、十分原因ではない。
- **反証**: `7d9c283` productionでは file_read Actionが生成されず、content file read開始前のmetadata read待ちが残った。

### 仮説2: KB vector次元不一致が単独の根本原因

- **根拠**: production traceに `expected 1024, got 3584` がある。
- **検証結果**: 棄却。これはfallbackの誘因だが、provider 0件/利用不可でもfilesystem fallbackは有界に完了するべきである。
- **反証**: KB契約を直した場合は速くなる可能性があるが、fallbackのbudget違反は残る。

### 仮説3: fallback候補範囲がbudgetより大きい

- **根拠**: 既定は `MaxCandidateFiles=50`、`MaxFilesRead=10`。`len(sourceRanks) > 0` の場合だけ後者へtruncateする。
- **検証結果**: 確認。
- **反証**: direct filesystem readが十分高速な小corpusでは表面化しない。`7d9c283` でこの欠陥を修正したが、production timeoutは残った。

### 仮説4: metadata ranking readがwriter connection待ちでdeadlineを消費する

- **根拠**: source flowはcontent ranking直前に `RankDCICandidateFiles` から
  `ListSourceRegistryEntries` を呼ぶ。後者はquery-only `readDB`ではなくmax conn 1の`s.db`を使っていた。
  `7d9c283` productionではfile_read Actionがaction storeに存在せず、read-budget修正後も60秒で停止した。
- **検証結果**: 確認。既存registry entry保存後に `store.db` の唯一のconnectionを保持し、100ms contextで
  `ListSourceRegistryEntries` を呼ぶ回帰testは修正前に `context deadline exceeded` となり、`s.readDB`変更後にGREENとなった。
- **反証**: provider vector dimension mismatchは同時に存在するが、metadata readのwriter starvationを説明しない別要因である。

### チェックリスト結果

- 確証バイアス: provider不一致を即断せず、予算の倍化と実装分岐で反証した。
- ライフサイクル: context開始、timeout、recovery contextでのterminal trace保存まで確認した。対になるresource操作はない。
- 発生頻度: provider候補0件のproduction受入で連続3回、budgetに正確に一致して再現した。`7d9c283` 配備後も60秒で再現し、file_read Actionは0件だった。
- 既存知見: Step03 DCIのowner trace契約と矛盾せず、既存commentの「実行できるfileだけcontent rank」と条件分岐の矛盾を特定した。

## Failure Knowledge

- **Failure**: DCI identity受入が内部deadlineでHTTP 500。
- **Problem**: 予算を拡大しても、fallback content rankingのread-budget defect修正後にmetadata ranking readがwriter connection待ちとなり、受入timeoutが残った。
- **Cause**: `ListSourceRegistryEntries` がquery-only `readDB`ではなくmax conn 1の`s.db`を使い、継続write／writer connection占有時に短いsearch contextが期限切れになった。provider vector mismatchはfallbackの誘因であり別問題。
- **Lesson**: 候補取得元やrankの有無に依存しないread budgetに加え、metadata ranking readはquery-only poolへ分離してwriter starvationを避ける。
- **Invariant**: content rankingがreadするfile数は常に `MaxFilesRead` 以下。metadata rankingのregistry readはwriter poolを待たない。
- **Enforcement**: fallback候補を `MaxFilesRead` へtruncateし、`ListSourceRegistryEntries` は `s.readDB.QueryContext` を使う。
- **Tests**: `TestExplorerFallbackContentRankingRespectsMaxFilesRead` と、writer connection保持中の
  `TestL1SQLiteStoreListSourceRegistryEntriesUsesReadPoolWhenWriterConnectionHeld`（修正前RED、修正後GREEN）。

## 修正案

1. `ListSourceRegistryEntries` のqueryをwriter `s.db`からquery-only `s.readDB`へ変更する。
2. 既存のfallback read-budget修正と `TestExplorerFallbackContentRankingRespectsMaxFilesRead` は維持し、metadata readのwriter starvation回帰testを追加する。
3. KB vector次元不一致は別のowner data契約不具合として分離し、本修正の完了条件に混ぜない。

## 関連ソースファイル

- `internal/application/dci/explorer.go:360` - candidate並び替えとcontent ranking対象の制限。
- `internal/application/dci/explorer.go:705` - content rankingの直列read。
- `internal/application/dci/explorer_test.go:1010` - metadata rankありのみを覆っていた既存budget test。
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_source_registry.go` の `ListSourceRegistryEntries` - metadata registry readのquery-only pool切替。
- `internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_connection_test.go` の `TestL1SQLiteStoreListSourceRegistryEntriesUsesReadPoolWhenWriterConnectionHeld` - writer connection保持中のread pool回帰test。

## 教訓

- 上位timeoutや既定値を広げる前に、各stageの件数budgetがfallbackを含む全pathに適用されるか、metadata readがwriter poolを待たないかをtraceとTDDで固定する。

## 未解決 / 未確認

- KB collection `kb_general` のvector次元不一致のowner migrationは未解決。
- `s.readDB`修正のproduction Shiro DCI pre/post-restart受入は未確認（本作業では配備・再起動・runtime/data変更を行わない）。
