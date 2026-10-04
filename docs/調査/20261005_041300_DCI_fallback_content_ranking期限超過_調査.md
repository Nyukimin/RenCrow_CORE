---
title: 調査 — DCI fallback content ranking期限超過
date: 2026-10-05 04:13 JST
status: confirmed
skill: debug-investigate
symptom: Shiro DCI identity受入がMaxSecondsの制限に一致してHTTP 500となる
frequency: provider候補が0件で、fallback corpusがMaxFilesReadを超えるときに再現
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

DCIの既定予算を60秒に拡大する修正は実runtimeへ適用されたが、受入はなお60秒で失敗した。根本原因は、provider/metadata rankが得られないfilesystem fallbackの場合だけ、content ranking対象が `MaxFilesRead`で打ち切られず、`MaxCandidateFiles`までtool経由で直列readする条件分岐である。

## 実測

| 配備revision | DCI予算 | Action | 開始→終了 | 結果 |
|---|---:|---|---|---|
| `9bfb661` | 30秒 | `act_01a107e3-10b6-748c-9a4c-0ee3bf5c3dc0` | 17:08:06.879→17:08:36.880 UTC | `context deadline exceeded` |
| `7a00cff` | 60秒 | `act_01a10853-933e-7c6c-9db0-a20d659f50ee` | 19:11:01.845→19:12:01.845 UTC | `context deadline exceeded` |

`7a00cff` traceは `content ranking stopped: context deadline exceeded` を記録し、evidence/stepは0件だった。またKB candidate providerは `expected 1024, got 3584` のvector次元不一致で利用できず、filesystem fallback条件に入った。

## 調査経緯

### 仮説1: content rankingの直列I/Oがdeadlineを消費する

- **根拠**: 失敗時刻が30秒と60秒の各budgetに一致し、traceがcontent ranking停止を記録した。
- **検証結果**: 確認。
- **反証**: 単一の異常ファイルだけでも同症状は起こりうるが、sourceは複数候補を直列でtool readし、候補一覧が尽きるかcontextが切れるまで継続する。

### 仮説2: KB vector次元不一致が単独の根本原因

- **根拠**: production traceに `expected 1024, got 3584` がある。
- **検証結果**: 棄却。これはfallbackの誘因だが、provider 0件/利用不可でもfilesystem fallbackは有界に完了するべきである。
- **反証**: KB契約を直した場合は速くなる可能性があるが、fallbackのbudget違反は残る。

### 仮説3: fallback候補範囲がbudgetより大きい

- **根拠**: 既定は `MaxCandidateFiles=50`、`MaxFilesRead=10`。`len(sourceRanks) > 0` の場合だけ後者へtruncateする。
- **検証結果**: 確認。
- **反証**: direct filesystem readが十分高速な小corpusでは表面化しないが、productionはtool-mediated readのため毎回再現した。

### チェックリスト結果

- 確証バイアス: provider不一致を即断せず、予算の倍化と実装分岐で反証した。
- ライフサイクル: context開始、timeout、recovery contextでのterminal trace保存まで確認した。対になるresource操作はない。
- 発生頻度: provider候補0件のproduction受入で連続3回、budgetに正確に一致して再現した。
- 既存知見: Step03 DCIのowner trace契約と矛盾せず、既存commentの「実行できるfileだけcontent rank」と条件分岐の矛盾を特定した。

## Failure Knowledge

- **Failure**: DCI identity受入が内部deadlineでHTTP 500。
- **Problem**: 予算を拡大しても、fallback content rankingが全budgetを消費する。
- **Cause**: `MaxFilesRead`によるtruncateがmetadata rankの存在を条件にし、rankのないfallback候補に適用されない。
- **Lesson**: 実行予算は候補の取得元やrankの有無に依存させない。
- **Invariant**: content rankingがreadするfile数は常に `MaxFilesRead` 以下。
- **Enforcement**: ranking対象をrankの有無に関係なくtruncateする。
- **Tests**: provider/metadata rankなしで `MaxCandidateFiles > MaxFilesRead` の失敗再現test。

## 修正案

1. `rankCandidateFilesByContent` へ渡す候補を、metadata rankの有無に関係なく `MaxFilesRead` までに制限する。
2. provider候補0件のfilesystem fallbackを用い、tool read回数が `MaxFilesRead` と一致するTDD testを追加する。
3. KB vector次元不一致は別のowner data契約不具合として分離し、本修正の完了条件に混ぜない。

## 関連ソースファイル

- `internal/application/dci/explorer.go:360` - candidate並び替えとcontent ranking対象の制限。
- `internal/application/dci/explorer.go:705` - content rankingの直列read。
- `internal/application/dci/explorer_test.go:1010` - metadata rankありのみを覆っていた既存budget test。

## 教訓

- 上位timeoutや既定値を広げる前に、各stageの件数budgetがfallbackを含む全pathに適用されるかをtraceとTDDで固定する。

## 未解決 / 未確認

- KB collection `kb_general` のvector次元不一致のowner migrationは未解決。
- fallback budget修正後のproduction Shiro DCI pre/post-restart受入は未確認。
